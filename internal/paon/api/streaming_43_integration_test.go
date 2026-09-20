//go:build integration

package api

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	paondb "github.com/mstdn-plusminus-io/paon/internal/paon/db"
	"github.com/mstdn-plusminus-io/paon/internal/paon/migrate"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

func TestStreaming43NotificationEventsScopesAndKillBoundariesAgainstPostgresRedisAndHTTP(t *testing.T) {
	baseDatabaseURL := strings.TrimSpace(os.Getenv("PAON_TEST_DATABASE_URL"))
	redisURL := strings.TrimSpace(os.Getenv("PAON_TEST_REDIS_URL"))
	if baseDatabaseURL == "" || redisURL == "" {
		t.Fatal("PAON_TEST_DATABASE_URL and PAON_TEST_REDIS_URL are required for integration tests")
	}
	databaseURL, database := streaming43IsolatedDatabase(t, baseDatabaseURL)
	if applied, err := migrate.Run(t.Context(), database); err != nil || !applied {
		t.Fatalf("migrate isolated streaming database = %v, %v", applied, err)
	}
	cfg := config.Config{
		RailsEnv:             "test",
		RailsLogLevel:        "unknown",
		Title:                "Paon",
		LocalDomain:          "example.test",
		WebDomain:            "example.test",
		Scheme:               "http",
		SecretKeyBase:        "streaming-43-integration-secret",
		DatabaseURL:          databaseURL,
		DatabaseMaxOpenConns: 12,
		DatabaseMaxIdleConns: 4,
		RedisURL:             redisURL,
		RedisNamespace:       "paon:integration:streaming43:" + randomHex(8) + ":",
	}
	server, err := NewServer(cfg, database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := server.Close(); err != nil {
			t.Errorf("close Paon server: %v", err)
		}
	})
	t.Cleanup(func() { deleteNotificationIntegrationRedisNamespace(server) })
	httpServer := httptest.NewServer(server.echo)
	t.Cleanup(httpServer.Close)

	application := streaming43CreateOAuthApplication(t, database)

	t.Run("read notifications scope receives notification and merged over SSE and WebSocket then revoke kills both", func(t *testing.T) {
		now := time.Now().UTC().Add(-time.Hour)
		account := createNotificationIntegrationAccount(t, database, "stream-owner", "", now)
		user := createNotificationIntegrationUser(t, database, account.ID, "stream-owner@example.test")
		allowed := streaming43CreateAccessToken(t, database, user.ID, application.ID, "stream-allowed-token", "read:notifications")
		denied := streaming43CreateAccessToken(t, database, user.ID, application.ID, "stream-denied-token", "read:statuses")

		streaming43ExpectSSERejected(t, httpServer.URL, denied.Token, http.StatusUnauthorized)
		deniedWS, status := streaming43OpenWebSocket(t, httpServer.URL, denied.Token)
		if status != http.StatusSwitchingProtocols {
			t.Fatalf("insufficient-scope WebSocket handshake status = %d, want 101 with an in-band scope error", status)
		}
		frame := streaming43ReadWebSocketFrame(t, deniedWS, 3*time.Second)
		var scopeError map[string]string
		if err := json.Unmarshal(frame.Payload, &scopeError); err != nil {
			t.Fatalf("decode WebSocket scope error: %v, payload=%s", err, frame.Payload)
		}
		if scopeError["error"] != "Access token does not cover required scopes" {
			t.Fatalf("WebSocket scope error = %#v", scopeError)
		}
		deniedWS.Close()

		sse := streaming43OpenSSE(t, httpServer.URL, allowed.Token)
		defer sse.Close()
		websocket, status := streaming43OpenWebSocket(t, httpServer.URL, allowed.Token)
		if status != http.StatusSwitchingProtocols {
			t.Fatalf("WebSocket handshake status = %d, want 101", status)
		}
		defer websocket.Close()
		websocketEvents, websocketDone := streaming43ObserveWebSocket(websocket)
		streaming43WaitForSubscriptions(t, server, account.ID, allowed.ID, 2)

		sender := createNotificationIntegrationAccount(t, database, "stream-sender", "remote.example", now.Add(-24*time.Hour))
		notification, err := createNotificationIntegrationRow(server, account.ID, sender.ID, 9_001, "Follow", "follow", now)
		if err != nil || notification == nil {
			t.Fatalf("create stream notification = %#v, %v", notification, err)
		}
		publishContext, cancelPublish := context.WithTimeout(t.Context(), time.Second)
		server.publishNotificationIDWithContext(publishContext, notification.ID)
		cancelPublish()
		server.publishNotificationsMerged(account.ID)

		sseNotification := streaming43WaitForEvent(t, sse.Events, "notification")
		streaming43RequireNotificationID(t, sseNotification.Payload, notification.ID, "SSE")
		sseMerged := streaming43WaitForEvent(t, sse.Events, "notifications_merged")
		if sseMerged.Payload != "1" {
			t.Fatalf("SSE notifications_merged payload = %q, want 1", sseMerged.Payload)
		}
		websocketNotification := streaming43WaitForEvent(t, websocketEvents, "notification")
		streaming43RequireNotificationID(t, websocketNotification.Payload, notification.ID, "WebSocket")
		websocketMerged := streaming43WaitForEvent(t, websocketEvents, "notifications_merged")
		if websocketMerged.Payload != "1" {
			t.Fatalf("WebSocket notifications_merged payload = %q, want 1", websocketMerged.Payload)
		}

		streaming43RevokeToken(t, httpServer.URL, application, allowed.Token)
		streaming43WaitForDisconnect(t, sse.Done, "SSE after token revoke")
		streaming43WaitForDisconnect(t, websocketDone, "WebSocket after token revoke")
		streaming43ExpectSSERejected(t, httpServer.URL, allowed.Token, http.StatusUnauthorized)
		streaming43ExpectWebSocketRejected(t, httpServer.URL, allowed.Token, http.StatusUnauthorized)
	})

	for _, state := range []string{"disabled", "suspended"} {
		state := state
		t.Run(state+" account system kill closes SSE and WebSocket and blocks reconnect", func(t *testing.T) {
			now := time.Now().UTC().Add(-time.Hour)
			account := createNotificationIntegrationAccount(t, database, "stream-"+state, "", now)
			user := createNotificationIntegrationUser(t, database, account.ID, "stream-"+state+"@example.test")
			token := streaming43CreateAccessToken(t, database, user.ID, application.ID, "stream-"+state+"-token", "read:notifications")

			sse := streaming43OpenSSE(t, httpServer.URL, token.Token)
			defer sse.Close()
			websocket, status := streaming43OpenWebSocket(t, httpServer.URL, token.Token)
			if status != http.StatusSwitchingProtocols {
				t.Fatalf("WebSocket handshake status = %d, want 101", status)
			}
			defer websocket.Close()
			_, websocketDone := streaming43ObserveWebSocket(websocket)
			streaming43WaitForSubscriptions(t, server, account.ID, token.ID, 2)

			switch state {
			case "disabled":
				if err := database.Model(&models.User{}).Where("id = ?", user.ID).Update("disabled", true).Error; err != nil {
					t.Fatal(err)
				}
			case "suspended":
				if err := database.Model(&models.Account{}).Where("id = ?", account.ID).Update("suspended_at", time.Now().UTC()).Error; err != nil {
					t.Fatal(err)
				}
			}
			server.publishStreamingKill(account.ID, nil)
			streaming43WaitForDisconnect(t, sse.Done, "SSE after "+state+" system kill")
			streaming43WaitForDisconnect(t, websocketDone, "WebSocket after "+state+" system kill")
			streaming43ExpectSSERejected(t, httpServer.URL, token.Token, http.StatusUnauthorized)
			streaming43ExpectWebSocketRejected(t, httpServer.URL, token.Token, http.StatusUnauthorized)
		})
	}
}

func streaming43IsolatedDatabase(t *testing.T, baseDatabaseURL string) (string, *gorm.DB) {
	t.Helper()
	parsed, err := url.Parse(baseDatabaseURL)
	if err != nil {
		t.Fatalf("parse PAON_TEST_DATABASE_URL: %v", err)
	}
	admin, err := paondb.Open(config.Config{DatabaseURL: baseDatabaseURL, RailsLogLevel: "unknown", DatabaseMaxOpenConns: 2, DatabaseMaxIdleConns: 1})
	if err != nil {
		t.Fatalf("open PostgreSQL database used to create isolated database: %v", err)
	}
	adminSQL, err := admin.DB()
	if err != nil {
		t.Fatal(err)
	}
	databaseName := "paon_streaming43_" + randomHex(8)
	if err := admin.Exec(`CREATE DATABASE "` + databaseName + `"`).Error; err != nil {
		_ = adminSQL.Close()
		t.Fatalf("create isolated PostgreSQL database %s: %v", databaseName, err)
	}
	parsed.Path = "/" + databaseName
	databaseURL := parsed.String()
	database, err := paondb.Open(config.Config{DatabaseURL: databaseURL, RailsLogLevel: "unknown", DatabaseMaxOpenConns: 12, DatabaseMaxIdleConns: 4})
	if err != nil {
		_ = admin.Exec(`DROP DATABASE "` + databaseName + `" WITH (FORCE)`).Error
		_ = adminSQL.Close()
		t.Fatalf("open isolated PostgreSQL database %s: %v", databaseName, err)
	}
	databaseSQL, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = databaseSQL.Close()
		if err := admin.Exec(`DROP DATABASE "` + databaseName + `" WITH (FORCE)`).Error; err != nil {
			t.Errorf("drop isolated PostgreSQL database %s: %v", databaseName, err)
		}
		_ = adminSQL.Close()
	})
	return databaseURL, database
}

func streaming43CreateOAuthApplication(t *testing.T, database *gorm.DB) models.OAuthApplication {
	t.Helper()
	now := time.Now().UTC()
	application := models.OAuthApplication{
		Name: "Streaming integration", UID: "streaming-client-" + randomHex(6), Secret: "streaming-secret-" + randomHex(12),
		RedirectURI: "urn:ietf:wg:oauth:2.0:oob", Scopes: "read:notifications", Confidential: true,
		CreatedAt: sql.NullTime{Time: now, Valid: true}, UpdatedAt: sql.NullTime{Time: now, Valid: true},
	}
	if err := database.Create(&application).Error; err != nil {
		t.Fatal(err)
	}
	return application
}

func streaming43CreateAccessToken(t *testing.T, database *gorm.DB, userID int64, applicationID int64, token string, scopes string) models.OAuthAccessToken {
	t.Helper()
	accessToken := models.OAuthAccessToken{
		Token: token, Scopes: models.NullSafeString(scopes), CreatedAt: time.Now().UTC(),
		ApplicationID: sql.NullInt64{Int64: applicationID, Valid: true}, ResourceOwnerID: sql.NullInt64{Int64: userID, Valid: true},
	}
	if err := database.Create(&accessToken).Error; err != nil {
		t.Fatal(err)
	}
	return accessToken
}

type streaming43Event struct {
	Event   string
	Payload string
}

type streaming43SSEClient struct {
	Body   io.ReadCloser
	Cancel context.CancelFunc
	Events <-chan streaming43Event
	Done   <-chan error
}

func streaming43OpenSSE(t *testing.T, serverURL string, token string) *streaming43SSEClient {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, serverURL+"/api/v1/streaming/user/notification", nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "text/event-stream")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		cancel()
		t.Fatalf("open SSE: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		_ = response.Body.Close()
		cancel()
		t.Fatalf("open SSE status = %d, body=%s", response.StatusCode, body)
	}
	events := make(chan streaming43Event, 8)
	done := make(chan error, 1)
	go func() {
		defer close(events)
		scanner := bufio.NewScanner(response.Body)
		var event streaming43Event
		for scanner.Scan() {
			line := scanner.Text()
			switch {
			case strings.HasPrefix(line, "event: "):
				event.Event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				event.Payload = strings.TrimPrefix(line, "data: ")
			case line == "" && event.Event != "":
				events <- event
				event = streaming43Event{}
			}
		}
		done <- scanner.Err()
		close(done)
	}()
	return &streaming43SSEClient{Body: response.Body, Cancel: cancel, Events: events, Done: done}
}

func (client *streaming43SSEClient) Close() {
	if client == nil {
		return
	}
	client.Cancel()
	_ = client.Body.Close()
}

type streaming43WebSocketClient struct {
	conn   net.Conn
	reader *bufio.Reader
}

func streaming43OpenWebSocket(t *testing.T, serverURL string, token string) (*streaming43WebSocketClient, int) {
	t.Helper()
	parsed, err := url.Parse(serverURL)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.DialTimeout("tcp", parsed.Host, 3*time.Second)
	if err != nil {
		t.Fatalf("dial WebSocket: %v", err)
	}
	request, err := http.NewRequest(http.MethodGet, serverURL+"/api/v1/streaming/user/notification", nil)
	if err != nil {
		_ = conn.Close()
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Connection", "Upgrade")
	request.Header.Set("Upgrade", "websocket")
	request.Header.Set("Sec-WebSocket-Version", "13")
	request.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	if err := request.Write(conn); err != nil {
		_ = conn.Close()
		t.Fatalf("write WebSocket handshake: %v", err)
	}
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, request)
	if err != nil {
		_ = conn.Close()
		t.Fatalf("read WebSocket handshake: %v", err)
	}
	status := response.StatusCode
	if status != http.StatusSwitchingProtocols {
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		_ = conn.Close()
		return nil, status
	}
	return &streaming43WebSocketClient{conn: conn, reader: reader}, status
}

func (client *streaming43WebSocketClient) Close() {
	if client != nil && client.conn != nil {
		_ = client.conn.Close()
	}
}

func streaming43ReadWebSocketFrame(t *testing.T, client *streaming43WebSocketClient, timeout time.Duration) websocketFrame {
	t.Helper()
	if err := client.conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		t.Fatal(err)
	}
	frame, err := readWebSocketFrame(client.reader)
	if err != nil {
		t.Fatalf("read WebSocket frame: %v", err)
	}
	_ = client.conn.SetReadDeadline(time.Time{})
	return frame
}

func streaming43ObserveWebSocket(client *streaming43WebSocketClient) (<-chan streaming43Event, <-chan error) {
	events := make(chan streaming43Event, 8)
	done := make(chan error, 1)
	go func() {
		defer close(events)
		for {
			frame, err := readWebSocketFrame(client.reader)
			if err != nil {
				done <- err
				close(done)
				return
			}
			if frame.Opcode == wsOpcodeClose {
				done <- nil
				close(done)
				return
			}
			if frame.Opcode != wsOpcodeText {
				continue
			}
			var payload struct {
				Event   string `json:"event"`
				Payload string `json:"payload"`
			}
			if err := json.Unmarshal(frame.Payload, &payload); err != nil || payload.Event == "" {
				continue
			}
			events <- streaming43Event{Event: payload.Event, Payload: payload.Payload}
		}
	}()
	return events, done
}

func streaming43WaitForSubscriptions(t *testing.T, server *Server, accountID int64, accessTokenID int64, want int64) {
	t.Helper()
	prefix := redisConfig(server.cfg).prefix
	channels := []string{
		prefix + "timeline:" + fmt.Sprint(accountID) + ":notifications",
		prefix + "timeline:access_token:" + fmt.Sprint(accessTokenID),
		prefix + "timeline:system:" + fmt.Sprint(accountID),
	}
	deadline := time.Now().Add(4 * time.Second)
	for {
		ready := true
		for _, channel := range channels {
			value, err := server.redisCommand(t.Context(), "PUBSUB", "NUMSUB", channel)
			if err != nil || notificationIntegrationSubscriptionCount(value) < want {
				ready = false
				break
			}
		}
		if ready {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("streaming Redis subscriptions were not ready for channels %#v", channels)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func streaming43WaitForEvent(t *testing.T, events <-chan streaming43Event, want string) streaming43Event {
	t.Helper()
	timer := time.NewTimer(4 * time.Second)
	defer timer.Stop()
	for {
		select {
		case event, ok := <-events:
			if !ok {
				t.Fatalf("stream closed before %s event", want)
			}
			if event.Event == want {
				return event
			}
		case <-timer.C:
			t.Fatalf("timed out waiting for %s event", want)
		}
	}
}

func streaming43RequireNotificationID(t *testing.T, payload string, notificationID int64, transport string) {
	t.Helper()
	var notification struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(payload), &notification); err != nil {
		t.Fatalf("decode %s notification payload: %v, payload=%s", transport, err, payload)
	}
	if notification.ID != fmt.Sprint(notificationID) {
		t.Fatalf("%s notification id = %q, want %d", transport, notification.ID, notificationID)
	}
}

func streaming43RevokeToken(t *testing.T, serverURL string, application models.OAuthApplication, token string) {
	t.Helper()
	form := url.Values{"client_id": {application.UID}, "client_secret": {application.Secret}, "token": {token}}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, serverURL+"/oauth/revoke", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("revoke token: %v", err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("revoke token status = %d, body=%s", response.StatusCode, body)
	}
}

func streaming43WaitForDisconnect(t *testing.T, done <-chan error, label string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(4 * time.Second):
		t.Fatalf("%s did not disconnect", label)
	}
}

func streaming43ExpectSSERejected(t *testing.T, serverURL string, token string, want int) {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, serverURL+"/api/v1/streaming/user/notification", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("request rejected SSE: %v", err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, response.Body)
	if response.StatusCode != want {
		t.Fatalf("rejected SSE status = %d, want %d", response.StatusCode, want)
	}
}

func streaming43ExpectWebSocketRejected(t *testing.T, serverURL string, token string, want int) {
	t.Helper()
	client, status := streaming43OpenWebSocket(t, serverURL, token)
	if client != nil {
		client.Close()
	}
	if status != want {
		t.Fatalf("rejected WebSocket handshake status = %d, want %d", status, want)
	}
}
