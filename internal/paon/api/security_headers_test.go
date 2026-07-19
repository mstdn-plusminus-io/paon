package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
)

func TestRailsDefaultSecurityHeadersAreApplied(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)

	headers := rec.Header()
	for key, want := range map[string]string{
		"Server":                 "Mastodon",
		"X-Frame-Options":        "DENY",
		"X-Content-Type-Options": "nosniff",
		"X-XSS-Protection":       "0",
		"Referrer-Policy":        "same-origin",
	} {
		if got := headers.Get(key); got != want {
			t.Fatalf("%s = %q, want %q; headers=%#v", key, got, want, headers)
		}
	}
}

func TestContentSecurityPolicyUsesRailsCSPMediaHostBeforeStorageHost(t *testing.T) {
	policy := railsContentSecurityPolicy(config.Config{
		RailsEnv:            "production",
		Scheme:              "https",
		WebDomain:           "example.com",
		CDNHost:             "https://cdn.example.test/",
		StorageHost:         "https://storage.example.test/bucket",
		CSPMediaHost:        "https://cloudfront.example.test",
		StreamingAPIBaseURL: "wss://stream.example.test/",
	})
	want := "connect-src 'self' data: blob: https://cdn.example.test/ https://cloudfront.example.test wss://stream.example.test/"
	if !strings.Contains(policy, want) {
		t.Fatalf("CSP should prefer Rails media_host over Paperclip storage host: %q", policy)
	}
	if strings.Contains(policy, "storage.example.test") {
		t.Fatalf("CSP leaked Paperclip storage host despite CSPMediaHost: %q", policy)
	}
}

func TestContentSecurityPolicyIncludesRailsDevelopmentShakapackerHost(t *testing.T) {
	policy := railsContentSecurityPolicy(config.Config{
		RailsEnv:                   "development",
		Scheme:                     "http",
		WebDomain:                  "example.test",
		ShakapackerDevServerPublic: "assets.example.test:3035",
	})
	for _, want := range []string{"connect-src 'self' data: blob:", "ws://assets.example.test:3035", "http://assets.example.test:3035"} {
		if !strings.Contains(policy, want) {
			t.Fatalf("development CSP missing %q: %q", want, policy)
		}
	}
	if strings.Contains(policy, "'wasm-unsafe-eval'") {
		t.Fatalf("development CSP must match Rails script-src without wasm-unsafe-eval: %q", policy)
	}

	httpsPolicy := railsContentSecurityPolicy(config.Config{
		RailsEnv:                   "development",
		Scheme:                     "https",
		WebDomain:                  "example.test",
		ShakapackerDevServerPublic: "assets.example.test:3035",
		ShakapackerDevServerHTTPS:  true,
	})
	if !strings.Contains(httpsPolicy, "wss://assets.example.test:3035") || !strings.Contains(httpsPolicy, "https://assets.example.test:3035") {
		t.Fatalf("development HTTPS CSP missing secure Shakapacker URLs: %q", httpsPolicy)
	}

	blankHostPolicy := railsContentSecurityPolicy(config.Config{
		RailsEnv:                   "development",
		Scheme:                     "http",
		WebDomain:                  "example.test",
		ShakapackerDevServerPublic: "",
	})
	if !strings.Contains(blankHostPolicy, "ws://") || !strings.Contains(blankHostPolicy, "http://") {
		t.Fatalf("development CSP should preserve blank Shakapacker host like Rails: %q", blankHostPolicy)
	}
	if strings.Contains(blankHostPolicy, "localhost:3035") {
		t.Fatalf("development CSP should not fall back blank Shakapacker host to localhost: %q", blankHostPolicy)
	}

	productionPolicy := railsContentSecurityPolicy(config.Config{
		RailsEnv:                   "production",
		Scheme:                     "https",
		WebDomain:                  "example.test",
		ShakapackerDevServerPublic: "assets.example.test:3035",
	})
	if strings.Contains(productionPolicy, "assets.example.test:3035") || !strings.Contains(productionPolicy, "'wasm-unsafe-eval'") {
		t.Fatalf("production CSP should omit dev server and keep wasm-unsafe-eval: %q", productionPolicy)
	}

	t.Setenv("RAILS_ENV", "")
	t.Setenv("PAON_ENV", "development")
	blankEnvPolicy := railsContentSecurityPolicy(config.Config{
		Scheme:                     "https",
		WebDomain:                  "example.test",
		ShakapackerDevServerPublic: "assets.example.test:3035",
	})
	if strings.Contains(blankEnvPolicy, "assets.example.test:3035") || !strings.Contains(blankEnvPolicy, "'wasm-unsafe-eval'") {
		t.Fatalf("blank RAILS_ENV should not be treated as development CSP: %q", blankEnvPolicy)
	}
}

func TestContentSecurityPolicyIncludesRailsOneClickSSOFormActionHost(t *testing.T) {
	policy := railsContentSecurityPolicy(config.Config{
		RailsEnv:         "production",
		Scheme:           "https",
		WebDomain:        "example.com",
		SSOFormActionURL: "https://idp.example.test/saml/sso",
	})
	if !strings.Contains(policy, "form-action 'self' https://idp.example.test/saml/sso") {
		t.Fatalf("CSP missing SSO form-action URL: %q", policy)
	}
}

func TestContentSecurityPolicyHeaderIsApplied(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", Scheme: "https", LocalDomain: "example.com", WebDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)

	if got := rec.Header().Get("Content-Security-Policy"); !strings.Contains(got, "default-src 'none'") || !strings.Contains(got, "script-src") {
		t.Fatalf("Content-Security-Policy = %q", got)
	}
}

func TestSecurityHeadersAllowRailsCompatibleHandlerOverrides(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/remote_interaction_helper", nil)
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)

	if got := rec.Header().Get("X-Frame-Options"); got != "SAMEORIGIN" {
		t.Fatalf("X-Frame-Options = %q", got)
	}
	if got := rec.Header().Get("Referrer-Policy"); got != "no-referrer" {
		t.Fatalf("Referrer-Policy = %q", got)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q", got)
	}
	if got := rec.Header().Get("Content-Security-Policy"); !strings.Contains(got, "frame-ancestors 'self'") || !strings.Contains(got, "form-action 'none'") || strings.Contains(got, "font-src") {
		t.Fatalf("remote interaction helper Content-Security-Policy = %q", got)
	}
}
