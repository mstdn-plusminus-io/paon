package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
)

func TestHostAuthorizationIncludesRailsDevelopmentDefaults(t *testing.T) {
	s, err := NewServer(config.Config{
		RailsEnv:              "development",
		Title:                 "Paon",
		LocalDomain:           "dev.instance.example",
		WebDomain:             "dev.instance.example",
		RailsDevelopmentHosts: []string{"workstation.lan"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	for _, host := range []string{
		"localhost:3000",
		"app.localhost:3000",
		"example.test:3000",
		"sub.example.test:3000",
		"127.0.0.1:3000",
		"[::1]:3000",
		"workstation.lan:3000",
		"dev.instance.example:3000",
	} {
		t.Run(host, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "http://"+host+"/about", nil)
			rec := httptest.NewRecorder()
			s.echo.ServeHTTP(rec, req)
			if rec.Code == http.StatusForbidden {
				t.Fatalf("development host %q was forbidden", host)
			}
		})
	}

	req := httptest.NewRequest(http.MethodGet, "http://unconfigured.example/about", nil)
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("unconfigured development DNS host status = %d, want 403", rec.Code)
	}
}

func TestHostAuthorizationIsDisabledInRailsTestEnvironment(t *testing.T) {
	s, err := NewServer(config.Config{RailsEnv: "test", Title: "Paon", LocalDomain: "local.example", WebDomain: "local.example"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "http://any-host.example/about", nil)
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)
	if rec.Code == http.StatusForbidden {
		t.Fatal("Rails test environment unexpectedly enabled host authorization")
	}
}

func TestHostAuthorizationMatchesRailsPortAndForwardedHostRules(t *testing.T) {
	allowed := railsAllowedHosts(config.Config{
		RailsEnv:         "production",
		LocalDomain:      "local.example:3000",
		WebDomain:        "web.example",
		ForceSSL:         true,
		AlternateDomains: []string{".internal.example"},
	})
	for host, want := range map[string]bool{
		"local.example:3000":        true,
		"local.example:4000":        false,
		"local.example":             false,
		"web.example":               true,
		"web.example:8443":          true,
		"internal.example:8443":     true,
		"node.internal.example:443": true,
	} {
		if got := railsHostAllowed(host, allowed); got != want {
			t.Fatalf("railsHostAllowed(%q) = %v, want %v", host, got, want)
		}
	}

	s, err := NewServer(config.Config{RailsEnv: "production", Title: "Paon", LocalDomain: "local.example", WebDomain: "local.example", ForceSSL: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "https://local.example/about", nil)
	req.Header.Set("X-Forwarded-Host", "proxy.example, evil.example")
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("blocked forwarded host status = %d, want 403", rec.Code)
	}
}
