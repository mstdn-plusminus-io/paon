package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestStreamingHealthMatchesNodeServer(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest("GET", "/api/v1/streaming/health", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	s := &Server{}
	if err := s.streamingHealth(c); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK || rec.Body.String() != "OK" {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
}

func TestStreamingChannelAcceptsPathAndQueryForms(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest("GET", "/api/v1/streaming/public/local", nil)
	c := echo.NewContext(req, httptest.NewRecorder(), e)
	if got := streamingChannel(c); got != "public:local" {
		t.Fatalf("channel = %q", got)
	}

	req = httptest.NewRequest("GET", "/api/v1/streaming?stream=user", nil)
	c = echo.NewContext(req, httptest.NewRecorder(), e)
	if got := streamingChannel(c); got != "user" {
		t.Fatalf("channel = %q", got)
	}

	req = httptest.NewRequest("GET", "/api/v1/streaming/public/local?only_media=true", nil)
	c = echo.NewContext(req, httptest.NewRecorder(), e)
	if got := streamingChannel(c); got != "public:local:media" {
		t.Fatalf("channel = %q", got)
	}
}

func TestStreamingSSEFailureBoundaries(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/streaming/unknown", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)
	err := (&Server{}).streaming(c)
	var apiErr apiHTTPError
	if !errors.As(err, &apiErr) || apiErr.status != http.StatusBadRequest || apiErr.message != "Error: Unknown channel requested" {
		t.Fatalf("unknown stream error = %#v", err)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("streaming CORS origin = %q", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Headers"); got != "Authorization, Accept, Cache-Control, X-Disconnect-After" {
		t.Fatalf("streaming CORS headers = %q", got)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/streaming?stream=public", nil)
	c = echo.NewContext(req, httptest.NewRecorder(), e)
	if got := streamingSSEChannel(c); got != "" {
		t.Fatalf("query-only SSE channel = %q, want unknown like Node HTTP router", got)
	}
	for _, channel := range []string{"user", "user:notification", "public", "public:local", "public:remote", "public:media", "public:local:media", "public:remote:media", "direct", "hashtag", "hashtag:local", "list"} {
		if !streamingKnownChannel(channel) {
			t.Fatalf("known Node stream %q was rejected", channel)
		}
	}
	if streamingKnownChannel("unknown") {
		t.Fatal("unknown Node stream was accepted")
	}

	goSrc, err := os.ReadFile("streaming.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`return apiError(c, http.StatusBadRequest, "Error: Unknown channel requested")`,
		`if err != nil || len(channelIDs) == 0`,
		`return apiError(c, http.StatusNotFound, "Not found")`,
	} {
		if !strings.Contains(string(goSrc), want) {
			t.Fatalf("Go streaming failure boundary missing %q", want)
		}
	}
}

func TestStreamingChannelIDsMatchMastodonRedisChannels(t *testing.T) {
	e := echo.New()
	account := &models.Account{ID: 42}
	s := &Server{}

	req := httptest.NewRequest("GET", "/api/v1/streaming?stream=user", nil)
	c := echo.NewContext(req, httptest.NewRecorder(), e)
	ids, err := s.streamingChannelIDs(c, "user", account)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"timeline:42"}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("ids = %#v", ids)
	}

	req = httptest.NewRequest("GET", "/api/v1/streaming/hashtag?tag=%23Go-Lang", nil)
	c = echo.NewContext(req, httptest.NewRecorder(), e)
	ids, err = s.streamingChannelIDs(c, "hashtag", nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"timeline:hashtag:golang"}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("ids = %#v", ids)
	}

	req = httptest.NewRequest("GET", "/api/v1/streaming/list?list=7", nil)
	c = echo.NewContext(req, httptest.NewRecorder(), e)
	ids, err = s.streamingChannelIDs(c, "list", account)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"timeline:list:7"}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("ids = %#v", ids)
	}
}

func TestStreamingDisconnectAfter(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest("GET", "/api/v1/streaming/public", nil)
	req.Header.Set("X-Disconnect-After", "1.5")
	c := echo.NewContext(req, httptest.NewRecorder(), e)
	if got := streamingDisconnectAfter(c); got != 1500*time.Millisecond {
		t.Fatalf("header disconnect = %s", got)
	}

	req = httptest.NewRequest("GET", "/api/v1/streaming/public?x-disconnect-after=2", nil)
	c = echo.NewContext(req, httptest.NewRecorder(), e)
	if got := streamingDisconnectAfter(c); got != 2*time.Second {
		t.Fatalf("query disconnect = %s", got)
	}

	req = httptest.NewRequest("GET", "/api/v1/streaming/public?x-disconnect-after=bad", nil)
	c = echo.NewContext(req, httptest.NewRecorder(), e)
	if got := streamingDisconnectAfter(c); got != 0 {
		t.Fatalf("invalid disconnect = %s", got)
	}
}

func TestStreamingSessionScopeChecks(t *testing.T) {
	if !streamingSessionCanRead(streamingSession{Scopes: "read"}, "user:notification") {
		t.Fatal("read scope should cover notifications")
	}
	if !streamingSessionCanRead(streamingSession{Scopes: "read:notifications"}, "user:notification") {
		t.Fatal("read:notifications should cover notification stream")
	}
	if streamingSessionCanRead(streamingSession{Scopes: "read:statuses"}, "user:notification") {
		t.Fatal("read:statuses should not cover notification stream")
	}
	if !streamingSessionCanRead(streamingSession{Scopes: "read:statuses"}, "public") {
		t.Fatal("read:statuses should cover public stream")
	}
	if streamingSessionCanRead(streamingSession{Scopes: "read:notifications"}, "public") {
		t.Fatal("read:notifications should not cover public stream")
	}
}

func TestStreamingSSEAccessChecksMatchNodeServer(t *testing.T) {
	account := &models.Account{ID: 42}
	if err := streamingSSEAccessError(streamingSession{}, errors.New("missing token"), "public"); err == nil || err.Error() != "Error: Missing access token" {
		t.Fatalf("missing token error = %v", err)
	}
	if err := streamingSSEAccessError(streamingSession{}, errors.New("invalid token"), "public"); err == nil || err.Error() != "Error: Invalid access token" {
		t.Fatalf("invalid token error = %v", err)
	}
	if err := streamingSSEAccessError(streamingSession{Account: account, Scopes: "read:notifications"}, nil, "public"); err == nil || err.Error() != "Error: Access token does not cover required scopes" {
		t.Fatalf("insufficient public scope error = %v", err)
	}
	if err := streamingSSEAccessError(streamingSession{Account: account, Scopes: "read:statuses"}, nil, "public"); err != nil {
		t.Fatalf("read:statuses should allow public SSE: %v", err)
	}
	if err := streamingSSEAccessError(streamingSession{Account: account, Scopes: "read:notifications"}, nil, "user:notification"); err != nil {
		t.Fatalf("read:notifications should allow notification SSE: %v", err)
	}

	src, err := os.ReadFile("streaming.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`if err := streamingSSEAccessError(session, accountErr, channel); err != nil`,
		`return apiError(c, http.StatusUnauthorized, err.Error())`,
		`!streamingSessionCanRead(session, channel)`,
	} {
		if !strings.Contains(string(src), want) {
			t.Fatalf("streaming SSE auth guard missing %q", want)
		}
	}
}

func TestWebSocketBinaryAndDisconnectBehavior(t *testing.T) {
	goSrc, err := os.ReadFile("streaming_websocket.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`case wsOpcodeBinary:`,
		`ws.WriteCloseWithCode(1003, websocketBinaryMessageCloseReason)`,
		`if after := streamingWebSocketDisconnectAfter(c); after > 0`,
		`time.AfterFunc(after`,
	} {
		if !strings.Contains(string(goSrc), want) {
			t.Fatalf("Go WebSocket contract missing %q", want)
		}
	}
}

func TestWebSocketDisconnectAfterUsesNodeQueryOnly(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/streaming?x-disconnect-after=1.5", nil)
	req.Header.Set("X-Disconnect-After", "9")
	c := echo.NewContext(req, httptest.NewRecorder(), e)
	if got := streamingWebSocketDisconnectAfter(c); got != 1500*time.Millisecond {
		t.Fatalf("WebSocket disconnect delay = %s", got)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/streaming", nil)
	req.Header.Set("X-Disconnect-After", "9")
	c = echo.NewContext(req, httptest.NewRecorder(), e)
	if got := streamingWebSocketDisconnectAfter(c); got != 0 {
		t.Fatalf("WebSocket header-only disconnect delay = %s, want disabled like Node", got)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/streaming?X-Disconnect-After=9", nil)
	c = echo.NewContext(req, httptest.NewRecorder(), e)
	if got := streamingWebSocketDisconnectAfter(c); got != 0 {
		t.Fatalf("WebSocket case-mismatched query delay = %s, want disabled like Node", got)
	}
}

func TestSSEEventUsesMastodonEventAndDataShape(t *testing.T) {
	raw := json.RawMessage(`{"id":"100"}`)
	got := string(sseEvent(redisMessage{Event: "update", Payload: raw}))
	if got != "event: update\ndata: {\"id\":\"100\"}\n\n" {
		t.Fatalf("event = %q", got)
	}

	raw = json.RawMessage(`"100"`)
	got = string(sseEvent(redisMessage{Event: "delete", Payload: raw}))
	if got != "event: delete\ndata: 100\n\n" {
		t.Fatalf("event = %q", got)
	}
}

func TestWebSocketCommandParsingAndEventShape(t *testing.T) {
	command, err := parseWebSocketCommand([]byte(`{"type":"subscribe","stream":["hashtag"],"tag":"Go"}`))
	if err != nil {
		t.Fatal(err)
	}
	if command.Type != "subscribe" || firstWebSocketParam(command.Stream) != "hashtag" {
		t.Fatalf("command = %#v", command)
	}
	params := webSocketCommandParams(command)
	if params.Get("tag") != "Go" {
		t.Fatalf("params = %#v", params)
	}

	event := websocketEvent([]string{"hashtag", "Go"}, redisMessage{Event: "update", Payload: json.RawMessage(`{"id":"100"}`)})
	var payload map[string]any
	if err := json.Unmarshal(event, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["event"] != "update" || payload["payload"] != `{"id":"100"}` {
		t.Fatalf("payload = %#v", payload)
	}
	if stream := payload["stream"].([]any); len(stream) != 2 || stream[0] != "hashtag" || stream[1] != "Go" {
		t.Fatalf("stream = %#v", payload["stream"])
	}
}

func TestWebSocketKeepAlive(t *testing.T) {
	goSrc, err := os.ReadFile("streaming_websocket.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`const websocketKeepAliveInterval = 30 * time.Second`,
		`keepAlive := startWebSocketKeepAlive(ctx, cancel, ws, websocketKeepAliveInterval)`,
		`case wsOpcodePong:`,
		`keepAlive.markAlive()`,
		`if !keepAlive.alive.Swap(false)`,
		`ws.WritePing(nil)`,
	} {
		if !strings.Contains(string(goSrc), want) {
			t.Fatalf("streaming_websocket.go missing Rails keepalive parity fragment %q", want)
		}
	}
}

func TestWebSocketKeepAliveSendsPingAndClosesDeadConnection(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	ws := &websocketConn{conn: server, reader: bufio.NewReader(server)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = startWebSocketKeepAlive(ctx, cancel, ws, time.Millisecond)

	frame, err := readWebSocketFrame(bufio.NewReader(client))
	if err != nil {
		t.Fatal(err)
	}
	if frame.Opcode != wsOpcodePing || len(frame.Payload) != 0 {
		t.Fatalf("keepalive frame = %#v", frame)
	}

	if err := client.SetReadDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if _, err := bufio.NewReader(client).ReadByte(); err == nil {
		t.Fatal("dead websocket client was not closed after missing pong")
	}
}

func TestWebSocketCommandParsingMatchesExistingReactClientShape(t *testing.T) {
	command, err := parseWebSocketCommand([]byte(`{"type":"subscribe","stream":"list","list":"7"}`))
	if err != nil {
		t.Fatal(err)
	}
	if command.Type != "subscribe" || firstWebSocketParam(command.Stream) != "list" {
		t.Fatalf("command = %#v", command)
	}
	params := webSocketCommandParams(command)
	if params.Get("list") != "7" {
		t.Fatalf("params = %#v", params)
	}
	if stream := websocketStreamName("list", params); !reflect.DeepEqual(stream, []string{"list", "7"}) {
		t.Fatalf("stream name = %#v", stream)
	}

	event := websocketEvent([]string{"list", "7"}, redisMessage{Event: "update", Payload: json.RawMessage(`{"id":"100"}`)})
	var payload map[string]any
	if err := json.Unmarshal(event, &payload); err != nil {
		t.Fatal(err)
	}
	stream := payload["stream"].([]any)
	if len(stream) != 2 || stream[0] != "list" || stream[1] != "7" {
		t.Fatalf("stream payload = %#v", payload["stream"])
	}
}

func TestWebSocketUnsubscribeMatchesExistingReactClientShape(t *testing.T) {
	command, err := parseWebSocketCommand([]byte(`{"type":"unsubscribe","stream":"hashtag:local","tag":"Go"}`))
	if err != nil {
		t.Fatal(err)
	}
	params := webSocketCommandParams(command)
	if command.Type != "unsubscribe" || firstWebSocketParam(command.Stream) != "hashtag:local" || params.Get("tag") != "Go" {
		t.Fatalf("command = %#v, params = %#v", command, params)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	subCtx, subCancel := context.WithCancel(ctx)
	subscriptions := map[string]websocketSubscription{
		"timeline:hashtag:go:local": {cancel: subCancel},
	}
	if err := (&Server{}).unsubscribeWebSocketStream(streamingSession{Scopes: "read"}, subscriptions, "hashtag:local", params); err != nil {
		t.Fatal(err)
	}
	if _, ok := subscriptions["timeline:hashtag:go:local"]; ok {
		t.Fatalf("subscription was not removed: %#v", subscriptions)
	}
	select {
	case <-subCtx.Done():
	default:
		t.Fatal("subscription context was not cancelled")
	}
}

func TestWebSocketUnsubscribeRejectsUnknownStream(t *testing.T) {
	err := (&Server{}).unsubscribeWebSocketStream(streamingSession{Scopes: "read"}, map[string]websocketSubscription{}, "", nil)
	if err == nil || err.Error() != "Unknown stream type" {
		t.Fatalf("err = %v", err)
	}
}

func TestNormalizeWebSocketChannelHonorsOnlyMedia(t *testing.T) {
	params := url.Values{"only_media": []string{"true"}}
	if got := normalizeWebSocketChannel("public/local", params); got != "public:local:media" {
		t.Fatalf("channel = %q", got)
	}
}
