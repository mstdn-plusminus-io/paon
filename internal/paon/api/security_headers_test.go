package api

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
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

func TestWebAppReferrerPolicyScopeKeepsSensitiveSurfacesSameOrigin(t *testing.T) {
	for _, path := range []string{"/", "/home", "/@alice/123", "/explore", "/tags/golang", "/about", "/privacy-policy", "/terms-of-service/2026-01-01"} {
		if !webAppReferrerPolicyPath(path) {
			t.Fatalf("web app path %q was excluded", path)
		}
	}
	for _, path := range []string{"/admin", "/admin/settings", "/api/v1/statuses/1", "/auth/sign_in", "/oauth/authorize", "/settings/profile", "/users/alice", "/.well-known/webfinger", "/share", "/media/1", "/statuses_cleanup"} {
		if webAppReferrerPolicyPath(path) {
			t.Fatalf("sensitive/non-web-app path %q was included", path)
		}
	}
}

func TestAllowReferrerOriginSettingOnlyChangesWebAppResponses(t *testing.T) {
	database, err := gorm.Open(postgres.New(postgres.Config{
		DSN:                  "host=localhost user=paon dbname=paon",
		PreferSimpleProtocol: true,
	}), &gorm.Config{DryRun: true, DisableAutomaticPing: true, SkipDefaultTransaction: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Callback().Query().Replace("gorm:query", func(tx *gorm.DB) {
		if setting, ok := tx.Statement.Dest.(*models.Setting); ok {
			*setting = models.Setting{Var: "allow_referrer_origin", Value: sql.NullString{String: "true", Valid: true}}
			tx.RowsAffected = 1
		}
	}); err != nil {
		t.Fatal(err)
	}
	e := echo.New()
	e.Use(securityHeadersMiddleware(database))
	e.GET("/home", func(c *echo.Context) error { return c.NoContent(http.StatusOK) })
	e.GET("/admin", func(c *echo.Context) error { return c.NoContent(http.StatusOK) })
	for path, want := range map[string]string{
		"/home":  "strict-origin-when-cross-origin",
		"/admin": "same-origin",
	} {
		recorder := httptest.NewRecorder()
		e.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if got := recorder.Header().Get("Referrer-Policy"); got != want {
			t.Fatalf("%s Referrer-Policy = %q, want %q", path, got, want)
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

func TestContentSecurityPolicyIncludesValidatedExtraMediaHosts(t *testing.T) {
	policy := railsContentSecurityPolicy(config.Config{
		RailsEnv:        "production",
		Scheme:          "https",
		WebDomain:       "example.com",
		ExtraMediaHosts: []string{"https://media-one.example.test", "http://media-two.example.test:8080"},
	})
	for _, directive := range []string{"img-src", "media-src", "connect-src"} {
		start := strings.Index(policy, directive+" ")
		if start < 0 {
			t.Fatalf("CSP missing %s: %q", directive, policy)
		}
		end := strings.Index(policy[start:], ";")
		value := policy[start:]
		if end >= 0 {
			value = value[:end]
		}
		for _, host := range []string{"https://media-one.example.test", "http://media-two.example.test:8080"} {
			if !strings.Contains(value, host) {
				t.Fatalf("%s missing extra media host %q: %q", directive, host, value)
			}
		}
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
