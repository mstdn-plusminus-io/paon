package api

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/web"
)

func TestAPIWebCSRFEarlyAuthErrorKeepsRailsAuthorizationVary(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPut, "/api/web/settings", nil)
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Vary"); got != "Authorization" {
		t.Fatalf("Vary = %q, want Authorization", got)
	}
	if !strings.Contains(rec.Body.String(), "The access token is invalid") {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestAPIWebCSRFTokenValidatesHeaderAndRailsFormParam(t *testing.T) {
	expected := web.CSRFTokenForSession("access-token")

	e := echo.New()
	req := httptest.NewRequest("PUT", "/api/web/settings", nil)
	req.Header.Set("X-CSRF-Token", expected)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)
	if !apiWebCSRFTokenValid(c, expected) {
		t.Fatal("X-CSRF-Token header should validate")
	}

	req = httptest.NewRequest("PUT", "/api/web/settings", nil)
	req.Header.Set("X-XSRF-Token", expected)
	rec = httptest.NewRecorder()
	c = echo.NewContext(req, rec, e)
	if !apiWebCSRFTokenValid(c, expected) {
		t.Fatal("X-XSRF-Token header should validate")
	}

	form := url.Values{"authenticity_token": []string{expected}}
	req = httptest.NewRequest("PUT", "/api/web/settings", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	c = echo.NewContext(req, rec, e)
	if !apiWebCSRFTokenValid(c, expected) {
		t.Fatal("authenticity_token form param should validate")
	}

	req = httptest.NewRequest("PUT", "/api/web/settings", nil)
	req.Header.Set("X-CSRF-Token", "wrong")
	rec = httptest.NewRecorder()
	c = echo.NewContext(req, rec, e)
	if apiWebCSRFTokenValid(c, expected) {
		t.Fatal("wrong CSRF token should be rejected")
	}
}
