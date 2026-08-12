package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
)

func TestHealthMatchesRailsPlainOK(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	s := &Server{}
	if err := s.health(c); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "OK" {
		t.Fatalf("body = %q", rec.Body.String())
	}
	if contentType := rec.Header().Get("Content-Type"); contentType != "text/plain; charset=utf-8" {
		t.Fatalf("Content-Type = %q", contentType)
	}
}

func TestHealthOptionalFormatMatchesRailsPlainOK(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "http://example.com/health.json", nil)
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "OK" {
		t.Fatalf("body = %q", rec.Body.String())
	}
	if contentType := rec.Header().Get("Content-Type"); contentType != "text/plain; charset=utf-8" {
		t.Fatalf("Content-Type = %q", contentType)
	}
}

func TestReadyReportsUnavailableWithoutDatabase(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/health/ready", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	s := &Server{}
	if err := s.ready(c); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "database unavailable" {
		t.Fatalf("body = %q", rec.Body.String())
	}
}

func TestReadyChecksMastodonSchema(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	for _, want := range []string{
		`paondb.SchemaAvailable(s.db)`,
		`schema unavailable`,
		`RedisAvailable(c.Request().Context(), s.cfg)`,
		`redis unavailable`,
		`s.cfg.MeiliEnabled`,
		`MeiliAvailable(c.Request().Context(), s.cfg)`,
		`meilisearch unavailable`,
		`web.ValidatePublicAssets(s.cfg)`,
		`public assets unavailable`,
		`web.ValidateServerRenderedLocales(s.cfg)`,
		`server-rendered locales unavailable`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("ready handler missing schema readiness check %q", want)
		}
	}
	redisSrc, err := os.ReadFile("redis_pubsub.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`redisAvailabilityConfigs(cfg)`,
		`cacheRedisConfig(cfg)`,
		`sidekiqRedisConfig(cfg)`,
	} {
		if !strings.Contains(string(redisSrc), want) {
			t.Fatalf("RedisAvailable must cover role-specific Redis readiness; missing %q", want)
		}
	}
}

func TestNewServerFailsFastWithoutConfiguredUIPackManifest(t *testing.T) {
	_, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com", PublicDir: t.TempDir()}, nil)
	if err == nil {
		t.Fatal("NewServer returned nil error without configured UI pack manifest")
	}
	if !strings.Contains(err.Error(), "read public packs manifest") {
		t.Fatalf("NewServer error = %v", err)
	}
}
