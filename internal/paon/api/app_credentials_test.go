package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
)

func TestVerifyAppCredentialsRequiresToken(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest("GET", "/api/v1/apps/verify_credentials", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	s := &Server{}
	err := s.verifyAppCredentials(c)
	if err == nil {
		t.Fatal("expected auth error")
	}
	handleAPIError(c, err)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "The access token is invalid") {
		t.Fatalf("body = %s", rec.Body.String())
	}
	if got := rec.Header().Get("Vary"); got != "Authorization" {
		t.Fatalf("Vary = %q, want Authorization", got)
	}
}
