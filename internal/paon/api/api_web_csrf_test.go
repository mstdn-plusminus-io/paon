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
	server := newBrowserSecurityTestServer()
	expected := web.CSRFTokenForSession("access-token")

	e := echo.New()
	req := httptest.NewRequest("PUT", "/api/web/settings", nil)
	req.Header.Set("X-CSRF-Token", expected)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)
	if !server.apiWebCSRFTokenValid(c, "access-token") {
		t.Fatal("X-CSRF-Token header should validate")
	}

	req = httptest.NewRequest("PUT", "/api/web/settings", nil)
	req.Header.Set("X-XSRF-Token", expected)
	rec = httptest.NewRecorder()
	c = echo.NewContext(req, rec, e)
	if !server.apiWebCSRFTokenValid(c, "access-token") {
		t.Fatal("X-XSRF-Token header should validate")
	}

	form := url.Values{"authenticity_token": []string{expected}}
	req = httptest.NewRequest("PUT", "/api/web/settings", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	c = echo.NewContext(req, rec, e)
	if !server.apiWebCSRFTokenValid(c, "access-token") {
		t.Fatal("authenticity_token form param should validate")
	}

	req = httptest.NewRequest("PUT", "/api/web/settings", nil)
	req.Header.Set("X-CSRF-Token", "wrong")
	rec = httptest.NewRecorder()
	c = echo.NewContext(req, rec, e)
	if server.apiWebCSRFTokenValid(c, "access-token") {
		t.Fatal("wrong CSRF token should be rejected")
	}
}

func TestAPIWebCSRFAcceptsBrowserSessionTokenInjectedIntoReactShell(t *testing.T) {
	server := newBrowserSecurityTestServer()
	e := echo.New()
	e.Use(server.browserSecurityMiddleware)
	e.GET("/deck/getting-started", func(c *echo.Context) error {
		return c.HTML(http.StatusOK, `<!doctype html><html><head><meta name="csrf-token" content="renderer-token"></head><body><div id="mastodon"></div></body></html>`)
	})
	e.PUT("/api/web/settings", func(c *echo.Context) error {
		if !server.apiWebCSRFTokenValid(c, "access-token") {
			return c.JSON(http.StatusUnprocessableEntity, map[string]string{"error": railsCSRFErrorMessage})
		}
		return c.JSON(http.StatusOK, map[string]any{})
	})

	getRequest := httptest.NewRequest(http.MethodGet, "/deck/getting-started", nil)
	getRequest.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "access-token"})
	getRecorder := httptest.NewRecorder()
	e.ServeHTTP(getRecorder, getRequest)
	if getRecorder.Code != http.StatusOK {
		t.Fatalf("GET status = %d body=%s", getRecorder.Code, getRecorder.Body.String())
	}
	browserCookie := browserSessionCookieFromRecorder(t, getRecorder)
	state, err := server.openBrowserSession(browserCookie.Value)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(getRecorder.Body.String(), "renderer-token") {
		t.Fatalf("React shell retained renderer CSRF token: %s", getRecorder.Body.String())
	}
	if !strings.Contains(getRecorder.Body.String(), `name="csrf-token" content="`+state.CSRFToken+`"`) {
		t.Fatalf("React shell is missing browser CSRF token: %s", getRecorder.Body.String())
	}

	putRequest := httptest.NewRequest(http.MethodPut, "/api/web/settings", strings.NewReader(`{"data":{"onboarded":false}}`))
	putRequest.Header.Set("Content-Type", "application/json")
	putRequest.Header.Set("X-CSRF-Token", state.CSRFToken)
	putRequest.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "access-token"})
	putRequest.AddCookie(browserCookie)
	putRecorder := httptest.NewRecorder()
	e.ServeHTTP(putRecorder, putRequest)
	if putRecorder.Code != http.StatusOK {
		t.Fatalf("PUT status = %d body=%s", putRecorder.Code, putRecorder.Body.String())
	}

	mismatchedRequest := httptest.NewRequest(http.MethodPut, "/api/web/settings", strings.NewReader(`{"data":{}}`))
	mismatchedRequest.Header.Set("Content-Type", "application/json")
	mismatchedRequest.Header.Set("X-CSRF-Token", state.CSRFToken)
	mismatchedRequest.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "different-access-token"})
	mismatchedRequest.AddCookie(browserCookie)
	mismatchedRecorder := httptest.NewRecorder()
	e.ServeHTTP(mismatchedRecorder, mismatchedRequest)
	if mismatchedRecorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("mismatched browser session status = %d body=%s", mismatchedRecorder.Code, mismatchedRecorder.Body.String())
	}
}
