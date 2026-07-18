package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
)

func TestAPIAuthEarlyErrorsKeepRailsAuthorizationVary(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/preferences", nil)
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
