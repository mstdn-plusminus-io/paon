package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
)

func TestCustomCSSImmutableUsesLongPublicCache(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodGet, "/css/custom-deadbeef", nil)
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)

	if err := s.customCSSImmutable(c); err != nil {
		t.Fatalf("customCSSImmutable: %v", err)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/css") {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "max-age=2592000, public, immutable" {
		t.Fatalf("Cache-Control = %q", got)
	}
}

func TestCustomCSSPathUsesContentDigest(t *testing.T) {
	s := &Server{}
	if got := s.customCSSPath(); got != "/custom.css" {
		t.Fatalf("empty custom CSS path = %q", got)
	}
}

func TestMastodon44CustomCSSRoutesAreRegistered(t *testing.T) {
	raw, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatalf("read server.go: %v", err)
	}
	src := string(raw)
	for _, route := range []string{
		`e.GET("/custom.css", s.customCSS)`,
		`e.GET("/css/:id", s.customCSSImmutable)`,
	} {
		if !strings.Contains(src, route) {
			t.Fatalf("server routes missing %s", route)
		}
	}
}
