package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestOAuthIntrospectionErrorMatchesDoorkeeperHeadersAndBody(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/oauth/introspect", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())
	if err := writeOAuthIntrospectionError(c, http.StatusUnauthorized, "invalid_token", "The access token is invalid", "unauthorized"); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q", got)
	}
	if got := rec.Header().Get("WWW-Authenticate"); got != `Bearer realm="Doorkeeper", error="invalid_token", error_description="The access token is invalid"` {
		t.Fatalf("WWW-Authenticate = %q", got)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["error"] != "invalid_token" || body["state"] != "unauthorized" {
		t.Fatalf("body = %#v", body)
	}
}

func TestOAuthIntrospectionTokenActiveMatchesDoorkeeperAccessibility(t *testing.T) {
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	active := models.OAuthAccessToken{CreatedAt: now.Add(-time.Hour)}
	if !oauthIntrospectionTokenActive(active, now) {
		t.Fatal("non-expiring token is inactive")
	}
	revoked := active
	revoked.RevokedAt = sql.NullTime{Time: now.Add(-time.Minute), Valid: true}
	if oauthIntrospectionTokenActive(revoked, now) {
		t.Fatal("revoked token is active")
	}
	expired := active
	expired.ExpiresIn = sql.NullInt64{Int64: 60, Valid: true}
	if oauthIntrospectionTokenActive(expired, now) {
		t.Fatal("expired token is active")
	}
}

func TestOAuthNativeAuthorizationCodeRequiresWebUser(t *testing.T) {
	server := &Server{}
	req := httptest.NewRequest(http.MethodGet, "/oauth/authorize/native?code=probe-code", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())
	if err := server.oauthNativeAuthorizationCode(c); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusFound || !strings.HasPrefix(rec.Header().Get("Location"), "/auth/sign_in") {
		t.Fatalf("status = %d Location = %q", rec.Code, rec.Header().Get("Location"))
	}
}
