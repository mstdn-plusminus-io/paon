package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
)

func TestRailsAPICORSPreflightRunsBeforeHostAndForceSSL(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com", WebDomain: "example.com", Scheme: "https", ForceSSL: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		host string
	}{
		{name: "http would normally force ssl", host: "example.com"},
		{name: "host would normally be forbidden", host: "blocked.example"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodOptions, "http://"+tt.host+"/api/v1/statuses", nil)
			req.Host = tt.host
			req.Header.Set("Origin", "https://client.example")
			req.Header.Set("Access-Control-Request-Method", "POST")
			rec := httptest.NewRecorder()
			s.echo.ServeHTTP(rec, req)

			if rec.Code != http.StatusNoContent {
				t.Fatalf("status = %d headers = %#v body = %s", rec.Code, rec.Header(), rec.Body.String())
			}
			if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
				t.Fatalf("Access-Control-Allow-Origin = %q", got)
			}
			if location := rec.Header().Get("Location"); location != "" {
				t.Fatalf("preflight must not be redirected before CORS, Location = %q", location)
			}
		})
	}
}

func TestRailsCORSHeadersRequireOrigin(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instance", nil)
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)

	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("CORS header should be absent without Origin like rack-cors: %#v", rec.Header())
	}
}

func TestRailsAPICORSHeadersApplyToAPIResponses(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instance", nil)
	req.Header.Set("Origin", "https://client.example")
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)

	if rec.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("missing API CORS header: %#v", rec.Header())
	}
	if rec.Header().Get("Access-Control-Expose-Headers") == "" {
		t.Fatalf("missing API exposed headers: %#v", rec.Header())
	}
}

func TestRailsPublicCORSHeadersMatchConfiguredPublicResources(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/.well-known/webfinger", "/@alice", "/users/alice"} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.Header.Set("Origin", "https://client.example")
			rec := httptest.NewRecorder()
			s.echo.ServeHTTP(rec, req)
			if rec.Header().Get("Access-Control-Allow-Origin") != "*" {
				t.Fatalf("missing public CORS header for %s: %#v", path, rec.Header())
			}
			if got := rec.Header().Get("Access-Control-Allow-Methods"); got != "GET" {
				t.Fatalf("Access-Control-Allow-Methods = %q", got)
			}
		})
	}
}

func TestRailsPublicCORSPreflightMatchesConfiguredPublicResources(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/.well-known/webfinger", "/@alice", "/users/alice"} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodOptions, path, nil)
			req.Header.Set("Origin", "https://client.example")
			req.Header.Set("Access-Control-Request-Method", "GET")
			req.Header.Set("Access-Control-Request-Headers", "Authorization")
			rec := httptest.NewRecorder()
			s.echo.ServeHTTP(rec, req)

			if rec.Code != http.StatusNoContent {
				t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
				t.Fatalf("Access-Control-Allow-Origin = %q", got)
			}
			if got := rec.Header().Get("Access-Control-Allow-Methods"); got != "GET" {
				t.Fatalf("Access-Control-Allow-Methods = %q", got)
			}
			if got := rec.Header().Get("Access-Control-Allow-Headers"); got != "Authorization" {
				t.Fatalf("Access-Control-Allow-Headers = %q", got)
			}
		})
	}
}

func TestRailsOAuthTokenCORSPreflight(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodOptions, "/oauth/token", nil)
	req.Header.Set("Origin", "https://client.example")
	req.Header.Set("Access-Control-Request-Method", "POST")
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "*" || rec.Header().Get("Access-Control-Allow-Methods") != "POST" {
		t.Fatalf("oauth CORS headers = %#v", rec.Header())
	}
}
