//go:build integration

package api

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	paondb "github.com/mstdn-plusminus-io/paon/internal/paon/db"
	"github.com/mstdn-plusminus-io/paon/internal/paon/migrate"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestWebRoutesAuthenticateOnlySessionBoundCookiesIntegration(t *testing.T) {
	databaseURL := os.Getenv("PAON_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("PAON_TEST_DATABASE_URL is required for integration tests")
	}
	cfg := config.Config{
		DatabaseURL:          databaseURL,
		DatabaseMaxOpenConns: 5,
		DatabaseMaxIdleConns: 2,
		Scheme:               "https",
		LocalDomain:          "paon.example",
		WebDomain:            "paon.example",
		SecretKeyBase:        "web-session-auth-integration-secret",
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
	role := models.UserRole{Name: "Owner", Position: 1000, Permissions: rolePermissionAdministrator, CreatedAt: now, UpdatedAt: now}
	create(&role)
	newUser := func(username string) models.User {
		t.Helper()
		account := models.Account{Username: username, CreatedAt: now, UpdatedAt: now}
		create(&account)
		user := models.User{AccountID: account.ID, Email: username + "@paon.example", EncryptedPassword: "x", Approved: true, ConfirmedAt: sql.NullTime{Time: now, Valid: true}, RoleID: sql.NullInt64{Int64: role.ID, Valid: true}, CreatedAt: now, UpdatedAt: now}
		create(&user)
		return user
	}
	alice := newUser("alice")
	bob := newUser("bob")

	app := models.OAuthApplication{Name: "Reader", UID: "reader-" + randomHex(8), Secret: randomHex(16), RedirectURI: "https://reader.example/callback", Scopes: "read write follow push admin:read admin:write", CreatedAt: sql.NullTime{Time: now, Valid: true}, UpdatedAt: sql.NullTime{Time: now, Valid: true}, Confidential: true}
	create(&app)
	newToken := func(prefix string, scopes string, applicationID int64) models.OAuthAccessToken {
		t.Helper()
		token := models.OAuthAccessToken{Token: prefix + "-" + randomHex(8), Scopes: models.NullSafeString(scopes), ResourceOwnerID: sql.NullInt64{Int64: alice.ID, Valid: true}, ApplicationID: sql.NullInt64{Int64: applicationID, Valid: applicationID != 0}, CreatedAt: now}
		create(&token)
		return token
	}
	// A read-only token held by a third-party application.
	appToken := newToken("web-session-app", "read", app.ID)
	adminToken := newToken("web-session-admin", "admin:read", app.ID)
	// A password-grant token without an application is not a browser session either.
	passwordToken := newToken("web-session-password", "read write follow push", 0)
	// A session activation of another user must not bind alice's token.
	foreignActivation := sessionActivationForRequest(bob.ID, passwordToken.ID, "192.0.2.2", "integration", now)
	create(&foreignActivation)
	sessionToken := newToken("web-session-browser", "read write follow push", 0)
	activation := sessionActivationForRequest(alice.ID, sessionToken.ID, "192.0.2.1", "integration", now)
	create(&activation)

	server := &Server{cfg: cfg, db: database}
	bearer := func(token string) func(*http.Request) {
		return func(req *http.Request) { req.Header.Set(echo.HeaderAuthorization, "Bearer "+token) }
	}
	cookie := func(name string, value string) func(*http.Request) {
		return func(req *http.Request) { req.AddCookie(&http.Cookie{Name: name, Value: value}) }
	}
	request := func(method, route, target string, handler echo.HandlerFunc, form url.Values, configure ...func(*http.Request)) *httptest.ResponseRecorder {
		t.Helper()
		body := ""
		if form != nil {
			body = form.Encode()
		}
		req := httptest.NewRequest(method, target, strings.NewReader(body))
		if form != nil {
			req.Header.Set(echo.HeaderContentType, "application/x-www-form-urlencoded")
		}
		for _, apply := range configure {
			apply(req)
		}
		return serveWebSessionRoute(method, route, handler, req)
	}
	grantCount := func() int64 {
		t.Helper()
		var count int64
		if err := database.Model(&models.OAuthAccessGrant{}).Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		return count
	}
	authorizeForm := func(extra url.Values) url.Values {
		form := url.Values{
			"response_type": {"code"},
			"client_id":     {app.UID},
			"redirect_uri":  {app.RedirectURI},
			"scope":         {"read write follow push admin:read admin:write"},
		}
		for key, values := range extra {
			form[key] = values
		}
		return form
	}
	assertSignInRedirect := func(t *testing.T, name string, rec *httptest.ResponseRecorder) {
		t.Helper()
		if rec.Code != http.StatusFound || !strings.HasPrefix(rec.Header().Get("Location"), "/auth/sign_in") {
			t.Fatalf("%s: status = %d location = %q body = %s, want a redirect to sign in", name, rec.Code, rec.Header().Get("Location"), rec.Body.String())
		}
	}

	t.Run("web routes ignore OAuth tokens and unbound session cookies", func(t *testing.T) {
		unauthenticated := map[string][]func(*http.Request){
			"bearer header":                     {bearer(appToken.Token)},
			"admin bearer header":               {bearer(adminToken.Token)},
			"application token in cookie":       {cookie(sessionCookieName, appToken.Token)},
			"password token in cookie":          {cookie(sessionCookieName, passwordToken.Token)},
			"unknown token in cookie":           {cookie(sessionCookieName, "missing-"+randomHex(8))},
			"bearer header beside dummy cookie": {bearer(appToken.Token), cookie(railsSessionIDCookieName, "dummy")},
		}
		for name, configure := range unauthenticated {
			assertSignInRedirect(t, "POST /oauth/authorize with "+name, request(http.MethodPost, "/oauth/authorize", "/oauth/authorize", server.oauthAuthorizeDecision, authorizeForm(nil), configure...))
			assertSignInRedirect(t, "GET /settings/applications with "+name, request(http.MethodGet, "/settings/applications", "/settings/applications", server.settingsApplicationsPage, nil, configure...))
			if count := grantCount(); count != 0 {
				t.Fatalf("%s: POST /oauth/authorize issued %d authorization codes", name, count)
			}
		}
		assertSignInRedirect(t, "POST /oauth/authorize with access_token form", request(http.MethodPost, "/oauth/authorize", "/oauth/authorize", server.oauthAuthorizeDecision, authorizeForm(url.Values{"access_token": {appToken.Token}})))
		assertSignInRedirect(t, "GET /settings/applications with access_token query", request(http.MethodGet, "/settings/applications", "/settings/applications?access_token="+appToken.Token, server.settingsApplicationsPage, nil))
		assertSignInRedirect(t, "GET /settings/applications with bearer_token query", request(http.MethodGet, "/settings/applications", "/settings/applications?bearer_token="+appToken.Token, server.settingsApplicationsPage, nil))
		if count := grantCount(); count != 0 {
			t.Fatalf("access_token parameter issued %d authorization codes", count)
		}
	})

	t.Run("session-bound cookie keeps working", func(t *testing.T) {
		rec := request(http.MethodGet, "/settings/applications", "/settings/applications", server.settingsApplicationsPage, nil, cookie(sessionCookieName, sessionToken.Token))
		if rec.Code != http.StatusOK {
			t.Fatalf("settings applications with a session cookie: status = %d body = %s", rec.Code, rec.Body.String())
		}
		rec = request(http.MethodPost, "/oauth/authorize", "/oauth/authorize", server.oauthAuthorizeDecision, authorizeForm(nil), cookie(sessionCookieName, sessionToken.Token))
		if rec.Code != http.StatusFound || !strings.HasPrefix(rec.Header().Get("Location"), app.RedirectURI+"?code=") {
			t.Fatalf("authorize with a session cookie: status = %d location = %q body = %s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
		}
		if count := grantCount(); count != 1 {
			t.Fatalf("authorize with a session cookie issued %d authorization codes, want 1", count)
		}
		user, token, err := server.currentWebSessionUser(echo.NewContext(func() *http.Request {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts", nil)
			req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessionToken.Token})
			return req
		}(), httptest.NewRecorder(), echo.New()), false)
		if err != nil || user == nil || user.ID != alice.ID || token != sessionToken.Token {
			t.Fatalf("currentWebSessionUser = %+v, %q, %v", user, token, err)
		}
	})

	t.Run("admin read API checks scopes whenever a token is present", func(t *testing.T) {
		tests := []struct {
			name      string
			target    string
			configure []func(*http.Request)
			status    int
		}{
			{name: "session cookie", target: "/api/v1/admin/domain_blocks", configure: []func(*http.Request){cookie(sessionCookieName, sessionToken.Token)}, status: http.StatusOK},
			{name: "session cookie with access_token", target: "/api/v1/admin/domain_blocks?access_token=" + appToken.Token, configure: []func(*http.Request){cookie(sessionCookieName, sessionToken.Token)}, status: http.StatusForbidden},
			{name: "dummy cookie with access_token", target: "/api/v1/admin/domain_blocks?access_token=" + appToken.Token, configure: []func(*http.Request){cookie(sessionCookieName, "dummy")}, status: http.StatusForbidden},
			{name: "application token in cookie", target: "/api/v1/admin/domain_blocks", configure: []func(*http.Request){cookie(sessionCookieName, appToken.Token)}, status: http.StatusUnauthorized},
			{name: "read bearer token", target: "/api/v1/admin/domain_blocks", configure: []func(*http.Request){bearer(appToken.Token), cookie(sessionCookieName, sessionToken.Token)}, status: http.StatusForbidden},
			{name: "admin:read bearer token", target: "/api/v1/admin/domain_blocks", configure: []func(*http.Request){bearer(adminToken.Token)}, status: http.StatusOK},
		}
		for _, test := range tests {
			rec := request(http.MethodGet, "/api/v1/admin/domain_blocks", test.target, server.adminDomainBlocks, nil, test.configure...)
			if rec.Code != test.status {
				t.Fatalf("%s: status = %d body = %s, want %d", test.name, rec.Code, rec.Body.String(), test.status)
			}
		}
		rec := request(http.MethodGet, "/api/v1/admin/trends/tags", "/api/v1/admin/trends/tags?access_token="+appToken.Token, server.adminTrendTags, nil, cookie(sessionCookieName, sessionToken.Token))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("trend tags with session cookie and access_token: status = %d body = %s, want 403", rec.Code, rec.Body.String())
		}
		rec = request(http.MethodGet, "/api/v1/admin/trends/tags", "/api/v1/admin/trends/tags", server.adminTrendTags, nil, cookie(sessionCookieName, appToken.Token))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("trend tags with an application token cookie: status = %d body = %s, want 401", rec.Code, rec.Body.String())
		}
	})

	t.Run("REST API keeps accepting OAuth tokens", func(t *testing.T) {
		for name, configure := range map[string][]func(*http.Request){
			"bearer header":  {bearer(appToken.Token)},
			"session cookie": {cookie(sessionCookieName, sessionToken.Token)},
		} {
			rec := request(http.MethodGet, "/api/v1/accounts/verify_credentials", "/api/v1/accounts/verify_credentials", server.verifyCredentials, nil, configure...)
			if rec.Code != http.StatusOK {
				t.Fatalf("verify_credentials via %s: status = %d body = %s", name, rec.Code, rec.Body.String())
			}
		}
		rec := request(http.MethodGet, "/api/v1/accounts/verify_credentials", "/api/v1/accounts/verify_credentials?access_token="+appToken.Token, server.verifyCredentials, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("verify_credentials via access_token: status = %d body = %s", rec.Code, rec.Body.String())
		}
	})
}
