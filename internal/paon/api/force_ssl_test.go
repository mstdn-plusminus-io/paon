package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
)

func TestForceSSLMiddlewareTrustsForwardedHTTPS(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com", WebDomain: "example.com", Scheme: "https", ForceSSL: true}, nil)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "http://example.com/about", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)
	if rec.Code == http.StatusMovedPermanently {
		t.Fatalf("forwarded HTTPS request was redirected: Location=%q", rec.Header().Get("Location"))
	}
	if got := rec.Header().Get("Strict-Transport-Security"); got != "max-age=63072000; includeSubDomains" {
		t.Fatalf("Strict-Transport-Security = %q", got)
	}
}
