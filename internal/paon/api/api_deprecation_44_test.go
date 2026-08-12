package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
)

func TestDeprecatedAPIHandlerSetsTimestampHeader(t *testing.T) {
	e := echo.New()
	e.GET("/deprecated", deprecatedAPIHandler("@1668384000", func(c *echo.Context) error {
		return c.NoContent(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodGet, "/deprecated", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if got := rec.Header().Get("Deprecation"); got != "@1668384000" {
		t.Fatalf("Deprecation = %q, want %q", got, "@1668384000")
	}
}

func TestMastodon44DeprecatedAPIRoutes(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		registration string
		want         string
	}{
		{`e.GET("/api/v1/accounts/:id/identity_proofs"`, `deprecatedAPIHandler("@1648598400", s.identityProofs)`},
		{`e.GET("/api/v1/filters"`, `deprecatedAPIHandler("@1668384000", s.v1Filters)`},
		{`e.POST("/api/v1/filters"`, `deprecatedAPIHandler("@1668384000", s.createV1Filter)`},
		{`e.GET("/api/v1/filters/:id"`, `deprecatedAPIHandler("@1668384000", s.showV1Filter)`},
		{`e.PUT("/api/v1/filters/:id"`, `deprecatedAPIHandler("@1668384000", s.updateV1Filter)`},
		{`e.PATCH("/api/v1/filters/:id"`, `deprecatedAPIHandler("@1668384000", s.updateV1Filter)`},
		{`e.DELETE("/api/v1/filters/:id"`, `deprecatedAPIHandler("@1668384000", s.deleteV1Filter)`},
		{`e.GET("/api/v1/suggestions"`, `deprecatedAPIHandler("@1621123200", s.suggestionsV1)`},
		{`e.GET("/api/v1/trends"`, `deprecatedAPIHandler("@1648598400", s.trendingTags)`},
	}

	for _, test := range tests {
		start := strings.Index(string(src), test.registration)
		if start < 0 {
			t.Fatalf("missing route registration %s", test.registration)
		}
		line := strings.SplitN(string(src[start:]), "\n", 2)[0]
		if !strings.Contains(line, test.want) {
			t.Fatalf("route %s missing %s: %s", test.registration, test.want, line)
		}
	}

	for _, excluded := range []string{
		`e.DELETE("/api/v1/suggestions/:id", s.deleteSuggestion)`,
		`e.GET("/api/v1/trends/tags", s.trendingTags)`,
	} {
		if !strings.Contains(string(src), excluded) {
			t.Fatalf("non-deprecated route must remain unwrapped: %s", excluded)
		}
	}
}
