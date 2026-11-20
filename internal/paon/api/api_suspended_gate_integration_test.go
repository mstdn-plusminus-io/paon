//go:build integration

package api

import (
	"context"
	"crypto/sha1"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	paondb "github.com/mstdn-plusminus-io/paon/internal/paon/db"
	"github.com/mstdn-plusminus-io/paon/internal/paon/migrate"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

// TestAPIRejectsSuspendedUsersLikeRailsIntegration covers Rails
// Api::BaseController#require_not_suspended! on the real middleware stack.
func TestAPIRejectsSuspendedUsersLikeRailsIntegration(t *testing.T) {
	databaseURL := os.Getenv("PAON_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("PAON_TEST_DATABASE_URL is required for integration tests")
	}
	cfg := config.Config{
		DatabaseURL:          databaseURL,
		DatabaseMaxOpenConns: 5,
		DatabaseMaxIdleConns: 2,
		Scheme:               "https",
		LocalDomain:          "example.com",
		WebDomain:            "example.com",
		SecretKeyBase:        "suspended-gate-integration-secret",
	}
	database, err := paondb.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := database.Exec(`DROP SCHEMA public CASCADE; CREATE SCHEMA public`).Error; err != nil {
		t.Fatal(err)
	}
	if applied, err := migrate.Run(context.Background(), database); err != nil || !applied {
		t.Fatalf("migrate = %v, %v", applied, err)
	}

	now := time.Now().UTC()
	create := func(value any) {
		t.Helper()
		if err := database.Create(value).Error; err != nil {
			t.Fatal(err)
		}
	}
	staffRole := models.UserRole{Name: "Moderator", Permissions: rolePermissionManageUsers | rolePermissionManageReports, Position: 10, CreatedAt: now, UpdatedAt: now}
	create(&staffRole)

	suspendedAt := sql.NullTime{Time: now.Add(-time.Hour), Valid: true}
	localSuspension := sql.NullInt64{Int64: 0, Valid: true}
	accounts := map[string]models.Account{}
	users := map[string]models.User{}
	tokens := map[string]string{}
	for _, fixture := range []struct {
		username  string
		suspended bool
		disabled  bool
		confirmed bool
		approved  bool
		staff     bool
	}{
		{username: "alice", confirmed: true, approved: true},
		{username: "follower", confirmed: true, approved: true},
		{username: "suspended_follower", suspended: true, confirmed: true, approved: true},
		{username: "disabled_follower", disabled: true, confirmed: true, approved: true},
		{username: "disabled_suspended", suspended: true, disabled: true, confirmed: true, approved: true},
		{username: "unconfirmed", approved: true},
		{username: "pending", confirmed: true},
		{username: "staff", confirmed: true, approved: true, staff: true},
		{username: "suspended_staff", suspended: true, confirmed: true, approved: true, staff: true},
	} {
		account := models.Account{Username: fixture.username, CreatedAt: now, UpdatedAt: now}
		if fixture.suspended {
			account.SuspendedAt = suspendedAt
			account.SuspensionOrigin = localSuspension
		}
		create(&account)
		user := models.User{AccountID: account.ID, Email: fixture.username + "@example.com", EncryptedPassword: "x", Approved: fixture.approved, Disabled: fixture.disabled, CreatedAt: now, UpdatedAt: now}
		if fixture.confirmed {
			user.ConfirmedAt = sql.NullTime{Time: now, Valid: true}
		}
		scopes := "read write follow"
		if fixture.staff {
			user.RoleID = sql.NullInt64{Int64: staffRole.ID, Valid: true}
			scopes += " admin:read admin:write"
		}
		create(&user)
		tokens[fixture.username] = "suspended-gate-" + fixture.username + "-" + randomHex(8)
		accessToken := models.OAuthAccessToken{Token: tokens[fixture.username], ResourceOwnerID: sql.NullInt64{Int64: user.ID, Valid: true}, Scopes: models.NullSafeString(scopes), CreatedAt: now}
		create(&accessToken)
		if fixture.staff {
			// Staff tokens double as browser sessions, which the admin React
			// components use for the admin read API.
			activation := sessionActivationForRequest(user.ID, accessToken.ID, "192.0.2.1", "integration", now)
			create(&activation)
		}
		accounts[fixture.username] = account
		users[fixture.username] = user
	}
	alice := accounts["alice"]
	for _, follower := range []string{"follower", "suspended_follower", "disabled_follower", "disabled_suspended"} {
		create(&models.Follow{AccountID: accounts[follower].ID, TargetAccountID: alice.ID, CreatedAt: now, UpdatedAt: now})
	}
	publicStatus := models.Status{Text: "public", AccountID: alice.ID, Visibility: 0, CreatedAt: now, UpdatedAt: now}
	create(&publicStatus)
	privateStatus := models.Status{Text: "followers only", AccountID: alice.ID, Visibility: 2, CreatedAt: now, UpdatedAt: now}
	create(&privateStatus)
	railsSession := models.SessionActivation{SessionID: "suspended-gate-session-" + randomHex(8), UserID: users["suspended_follower"].ID, CreatedAt: now, UpdatedAt: now}
	create(&railsSession)

	server, err := NewServer(cfg, database)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	id := func(value int64) string { return strconv.FormatInt(value, 10) }
	publicPath := "/api/v1/statuses/" + id(publicStatus.ID)
	privatePath := "/api/v1/statuses/" + id(privateStatus.ID)
	oEmbedPath := "/api/oembed?url=" + url.QueryEscape(cfg.BaseURL()+"/@alice/"+id(publicStatus.ID))
	type credential struct{ bearer, cookieName, cookieValue, wsProtocol string }
	bearer := func(username string) credential { return credential{bearer: tokens[username]} }
	sessionCookie := func(username string) credential {
		return credential{cookieName: sessionCookieName, cookieValue: tokens[username]}
	}
	request := func(method, target string, auth credential) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, target, nil)
		req.Header.Set("Accept", "application/json")
		if auth.bearer != "" {
			req.Header.Set("Authorization", "Bearer "+auth.bearer)
		}
		if auth.cookieName != "" {
			req.AddCookie(&http.Cookie{Name: auth.cookieName, Value: auth.cookieValue})
		}
		if auth.wsProtocol != "" {
			req.Header.Set("Sec-WebSocket-Protocol", auth.wsProtocol)
		}
		rec := httptest.NewRecorder()
		server.echo.ServeHTTP(rec, req)
		return rec
	}
	expectStatus := func(name string, rec *httptest.ResponseRecorder, status int) {
		t.Helper()
		if rec.Code != status {
			t.Fatalf("%s: status = %d, body = %s; want %d", name, rec.Code, rec.Body.String(), status)
		}
	}
	expectError := func(name string, rec *httptest.ResponseRecorder, status int, message string) {
		t.Helper()
		expectStatus(name, rec, status)
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s: body = %s: %v", name, rec.Body.String(), err)
		}
		if body["error"] != message {
			t.Fatalf("%s: body = %s, want error %q", name, rec.Body.String(), message)
		}
	}
	const suspendedMessage = "Your login is currently disabled"

	t.Run("suspended user tokens and sessions are rejected on every API", func(t *testing.T) {
		for _, tt := range []struct {
			name   string
			method string
			target string
			auth   credential
		}{
			{"followers-only status", http.MethodGet, privatePath, bearer("suspended_follower")},
			{"public status", http.MethodGet, publicPath, bearer("suspended_follower")},
			{"status context", http.MethodGet, privatePath + "/context", bearer("suspended_follower")},
			{"account statuses", http.MethodGet, "/api/v1/accounts/" + id(alice.ID) + "/statuses", bearer("suspended_follower")},
			{"verify_credentials", http.MethodGet, "/api/v1/accounts/verify_credentials", bearer("suspended_follower")},
			{"instance", http.MethodGet, "/api/v1/instance", bearer("suspended_follower")},
			{"oembed", http.MethodGet, oEmbedPath, bearer("suspended_follower")},
			{"paon session cookie", http.MethodGet, privatePath, sessionCookie("suspended_follower")},
			{"disabled and suspended", http.MethodGet, publicPath, bearer("disabled_suspended")},
		} {
			rec := request(tt.method, tt.target, tt.auth)
			expectError(tt.name, rec, http.StatusForbidden, suspendedMessage)
			if vary := strings.Join(rec.Header().Values("Vary"), ","); !strings.Contains(vary, "Authorization") {
				t.Fatalf("%s: Vary = %q, want Authorization", tt.name, vary)
			}
		}

		var tokensBefore int64
		if err := database.Model(&models.OAuthAccessToken{}).Where("resource_owner_id = ?", users["suspended_follower"].ID).Count(&tokensBefore).Error; err != nil {
			t.Fatal(err)
		}
		railsCookie := railsSignedCookieFixture(t, railsSession.SessionID, cfg.SecretKeyBase, railsSessionCookiePurpose, time.Now().Add(time.Hour), sha1.New)
		rec := request(http.MethodGet, privatePath, credential{cookieName: railsSessionIDCookieName, cookieValue: railsCookie})
		expectError("legacy Rails session", rec, http.StatusForbidden, suspendedMessage)
		var tokensAfter int64
		if err := database.Model(&models.OAuthAccessToken{}).Where("resource_owner_id = ?", users["suspended_follower"].ID).Count(&tokensAfter).Error; err != nil {
			t.Fatal(err)
		}
		if tokensAfter != tokensBefore {
			t.Fatalf("legacy Rails session: access tokens = %d, want %d (the gate must not issue tokens)", tokensAfter, tokensBefore)
		}
	})

	t.Run("streaming keeps the Node streaming server response", func(t *testing.T) {
		expectError("SSE user stream", request(http.MethodGet, "/api/v1/streaming/user", bearer("suspended_follower")), http.StatusUnauthorized, "Error: Invalid access token")
	})

	t.Run("suspended staff cannot use the admin API", func(t *testing.T) {
		target := "/api/v1/admin/accounts/" + id(accounts["follower"].ID)
		expectError("admin account by token", request(http.MethodGet, target, bearer("suspended_staff")), http.StatusForbidden, suspendedMessage)
		expectError("admin account by cookie", request(http.MethodGet, target, sessionCookie("suspended_staff")), http.StatusForbidden, suspendedMessage)
		expectError("admin accounts index", request(http.MethodGet, "/api/v1/admin/accounts", bearer("suspended_staff")), http.StatusForbidden, suspendedMessage)
		// The admin browser session works for active staff, so the next case
		// exercises the same branch.
		expectStatus("active staff admin account by session cookie", request(http.MethodGet, target, sessionCookie("staff")), http.StatusOK)
		// A Sec-WebSocket-Protocol token next to the session cookie moved the
		// /api gate onto that token while the admin browser branch still used the
		// suspended staff's session cookie.
		withProtocol := sessionCookie("suspended_staff")
		withProtocol.wsProtocol = "unrelated-" + randomHex(4)
		if rec := request(http.MethodGet, target, withProtocol); rec.Code != http.StatusUnauthorized && rec.Code != http.StatusForbidden {
			t.Fatalf("admin account by cookie with websocket protocol: status = %d, body = %s; want 401 or 403", rec.Code, rec.Body.String())
		}
		expectError("self unsuspend", request(http.MethodPost, "/api/v1/admin/accounts/"+id(accounts["suspended_staff"].ID)+"/unsuspend", bearer("suspended_staff")), http.StatusForbidden, suspendedMessage)
		var reloaded models.Account
		if err := database.First(&reloaded, accounts["suspended_staff"].ID).Error; err != nil {
			t.Fatal(err)
		}
		if !reloaded.SuspendedAt.Valid {
			t.Fatal("suspended staff lifted their own suspension")
		}

		// requireAdminWriteWithPermissions rejects suspended staff on its own too.
		for _, username := range []string{"suspended_staff", "staff"} {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/"+id(accounts[username].ID)+"/unsuspend", nil)
			req.Header.Set("Authorization", "Bearer "+tokens[username])
			c := echo.NewContext(req, httptest.NewRecorder(), echo.New())
			user, err := server.requireAdminWrite(c, "admin:write:accounts")
			if username == "staff" {
				if err != nil || user == nil || user.ID != users["staff"].ID {
					t.Fatalf("active staff admin write: user = %+v, err = %v", user, err)
				}
				continue
			}
			var apiErr apiHTTPError
			if !errors.As(err, &apiErr) || apiErr.status != http.StatusForbidden || apiErr.message != suspendedMessage {
				t.Fatalf("suspended staff admin write: user = %+v, err = %#v", user, err)
			}
		}
	})

	t.Run("active users and anonymous requests are unchanged", func(t *testing.T) {
		expectStatus("follower private status", request(http.MethodGet, privatePath, bearer("follower")), http.StatusOK)
		expectStatus("follower verify_credentials", request(http.MethodGet, "/api/v1/accounts/verify_credentials", bearer("follower")), http.StatusOK)
		expectStatus("anonymous public status", request(http.MethodGet, publicPath, credential{}), http.StatusOK)
		// Mastodon 4.6 Api::V1::StatusesController#set_status answers not_found,
		// whose body is Rack's "Not Found".
		expectError("anonymous private status", request(http.MethodGet, privatePath, credential{}), http.StatusNotFound, "Not Found")
		expectStatus("anonymous oembed", request(http.MethodGet, oEmbedPath, credential{}), http.StatusOK)
		expectStatus("staff admin account", request(http.MethodGet, "/api/v1/admin/accounts/"+id(accounts["follower"].ID), bearer("staff")), http.StatusOK)
	})

	t.Run("disabled, unconfirmed, and pending users are left to the handlers like Rails", func(t *testing.T) {
		for _, username := range []string{"disabled_follower", "unconfirmed", "pending"} {
			expectStatus(username+" public status", request(http.MethodGet, publicPath, bearer(username)), http.StatusOK)
		}
		expectError("disabled verify_credentials", request(http.MethodGet, "/api/v1/accounts/verify_credentials", bearer("disabled_follower")), http.StatusForbidden, "Your login is currently disabled")
		expectError("unconfirmed verify_credentials", request(http.MethodGet, "/api/v1/accounts/verify_credentials", bearer("unconfirmed")), http.StatusForbidden, "Your login is missing a confirmed e-mail address")
		expectError("pending verify_credentials", request(http.MethodGet, "/api/v1/accounts/verify_credentials", bearer("pending")), http.StatusForbidden, "Your login is currently pending approval")
	})
}
