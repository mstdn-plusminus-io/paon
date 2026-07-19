package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/labstack/echo/v5"
)

func TestPublicRESTCacheIfUnauthenticatedSkipsSessionCookies(t *testing.T) {
	for _, name := range []string{sessionCookieName, railsSessionCookieName, railsSessionIDCookieName} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/timelines/public", nil)
			req.AddCookie(&http.Cookie{Name: name, Value: "session"})
			rec := httptest.NewRecorder()
			c := echo.NewContext(req, rec, echo.New())

			publicRESTCacheIfUnauthenticated(c, 15)

			if got := rec.Header().Get("Cache-Control"); got != "" {
				t.Fatalf("Cache-Control = %q, want empty for session cookie %s", got, name)
			}
		})
	}
}

func TestPublicRESTCacheIfUnauthenticatedAllowsAnonymousRequests(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/timelines/public", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())

	publicRESTCacheIfUnauthenticated(c, 15)

	if got := rec.Header().Get("Cache-Control"); got != "max-age=15, public, stale-while-revalidate=30, stale-if-error=86400" {
		t.Fatalf("Cache-Control = %q", got)
	}
}

func TestPublicRESTCacheIfUnauthenticatedSkipsBearerToken(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/timelines/public", nil)
	req.Header.Set("Authorization", "Bearer token")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())

	publicRESTCacheIfUnauthenticated(c, 15)

	if got := rec.Header().Get("Cache-Control"); got != "" {
		t.Fatalf("Cache-Control = %q, want empty for Authorization header", got)
	}
}

func TestPublicRESTCacheIfUnauthenticatedSkipsDoorkeeperTokenParams(t *testing.T) {
	for _, target := range []string{
		"/api/v1/timelines/public?access_token=query-token",
		"/api/v1/timelines/public?bearer_token=query-token",
		"/api/v1/timelines/public?access_token=+query-token+",
		"/api/v1/timelines/public?bearer_token=+query-token+",
	} {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		rec := httptest.NewRecorder()
		c := echo.NewContext(req, rec, echo.New())

		publicRESTCacheIfUnauthenticated(c, 15)

		if got := rec.Header().Get("Cache-Control"); got != "" {
			t.Fatalf("%s Cache-Control = %q, want empty for Doorkeeper token param", target, got)
		}
	}
}

func TestRailsCacheIfUnauthenticatedEndpointsSetAuthorizationVaryAndPublicCache(t *testing.T) {
	checks := map[string][]string{
		"server.go": {
			"getAccount",
			"lookupAccount",
			"getStatus",
			"publicTimeline",
			"tagTimeline",
			"accountStatuses",
			"statusContext",
		},
		"account_follows.go": {
			"accountFollowers",
			"accountFollowing",
		},
		"status_extras.go": {
			"statusHistory",
			"statusCard",
			"favouritedBy",
			"rebloggedBy",
		},
		"polls.go": {
			"getPoll",
		},
		"directory.go": {
			"directory",
		},
		"tags.go": {
			"showTag",
		},
	}

	for file, funcs := range checks {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, fn := range funcs {
			if !functionBodyContains(t, src, fn, `c.Response().Header().Set("Vary", "Authorization")`) {
				t.Fatalf("%s:%s does not set Rails default API Vary header", file, fn)
			}
			if !functionBodyContains(t, src, fn, `publicRESTCacheIfUnauthenticated(c, 15)`) {
				t.Fatalf("%s:%s does not apply Rails cache_if_unauthenticated! semantics", file, fn)
			}
		}
	}
}
