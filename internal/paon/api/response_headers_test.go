package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
)

func TestRequestIDHeaderIsGeneratedForResponses(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instance", nil)
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)

	got := rec.Header().Get("X-Request-Id")
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`).MatchString(got) {
		t.Fatalf("X-Request-Id = %q", got)
	}
}

func TestHandleAPIErrorRedactsUnexpectedInternalDetails(t *testing.T) {
	secret := `pq: relation "users_private" does not exist at postgres://admin:password@db.internal/mastodon`
	for _, test := range []struct {
		name   string
		target string
		accept string
		want   string
	}{
		{name: "JSON API", target: "/api/v1/accounts/1", accept: "application/json", want: http.StatusText(http.StatusInternalServerError)},
		{name: "browser HTML", target: "/settings/profile", accept: "text/html", want: "something went wrong on our end."},
	} {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, test.target, nil)
			req.Header.Set("Accept", test.accept)
			rec := httptest.NewRecorder()
			c := echo.NewContext(req, rec, echo.New())
			c.Response().Header().Set(echo.HeaderXRequestID, "request-123")
			handleAPIError(c, errors.New(secret))
			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), secret) || strings.Contains(rec.Body.String(), "users_private") || strings.Contains(rec.Body.String(), "password") {
				t.Fatalf("internal error detail leaked: %s", rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), test.want) {
				t.Fatalf("safe public error text missing: %s", rec.Body.String())
			}
		})
	}
}

func TestErrorPageMatchesRailsSharedErrorLayout(t *testing.T) {
	appAssets.Store(appAssetPaths{})
	page := errorPageHTML(http.StatusInternalServerError, "en", "Paon", "mastodon-light", "database password must not appear")
	for _, want := range []string{
		`<html lang="en">`,
		`<title>This page is not correct - Paon</title>`,
		`href="/packs/css/common.css"`,
		`href="/packs/css/mastodon-light.css"`,
		`src="/packs/js/common.js"`,
		`src="/packs/js/error.js"`,
		`<body class="error">`,
		`src="/oops.png"`,
		`something went wrong on our end.`,
	} {
		if !strings.Contains(page, want) {
			t.Fatalf("error layout missing %q: %s", want, page)
		}
	}
	if strings.Contains(page, "database password") {
		t.Fatalf("internal publicMessage leaked into status error page: %s", page)
	}
}

func TestHandleAPIErrorRedactsEchoFiveHundredButPreservesExplicitPublicError(t *testing.T) {
	for _, test := range []struct {
		name        string
		err         error
		wantMessage string
		reject      string
	}{
		{name: "Echo internal", err: echo.NewHTTPError(http.StatusInternalServerError, "redis://:secret@cache.internal"), wantMessage: "Internal Server Error", reject: "cache.internal"},
		{name: "typed public", err: publicAPIError(http.StatusServiceUnavailable, "Search is temporarily unavailable", errors.New("meilisearch node 10.0.0.8 failed"), http.Header{"Retry-After": {"60"}}), wantMessage: "Search is temporarily unavailable", reject: "10.0.0.8"},
	} {
		t.Run(test.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c := echo.NewContext(httptest.NewRequest(http.MethodGet, "/api/v2/search", nil), rec, echo.New())
			handleAPIError(c, test.err)
			if !strings.Contains(rec.Body.String(), test.wantMessage) || strings.Contains(rec.Body.String(), test.reject) {
				t.Fatalf("body = %s", rec.Body.String())
			}
			if test.name == "typed public" && rec.Header().Get("Retry-After") != "60" {
				t.Fatalf("Retry-After = %q", rec.Header().Get("Retry-After"))
			}
		})
	}
}

func TestRequestIDHeaderPreservesValidInboundID(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instance", nil)
	req.Header.Set("X-Request-Id", "client-request-42")
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)

	if got := rec.Header().Get("X-Request-Id"); got != "client-request-42" {
		t.Fatalf("X-Request-Id = %q", got)
	}
}

func TestRequestIDHeaderSanitizesInboundIDLikeRails(t *testing.T) {
	if got := sanitizedRequestID(" client/request_id@42! ok "); got != "clientrequestid42ok" {
		t.Fatalf("sanitizedRequestID punctuation = %q", got)
	}

	raw := strings.Repeat("a", 300)
	if got := sanitizedRequestID(raw); len(got) != 255 || strings.Contains(got, "/") {
		t.Fatalf("sanitizedRequestID length/punctuation = len %d value %q", len(got), got)
	}
}

func TestAPIRateLimitHeadersMatchRailsUnauthenticatedDefaults(t *testing.T) {
	e := echo.New()
	e.Use(apiRateLimitHeadersMiddleware)
	e.GET("/api/v1/instance", func(c *echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"title": "Paon"})
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instance", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if got := rec.Header().Get("X-RateLimit-Limit"); got != "300" {
		t.Fatalf("X-RateLimit-Limit = %q", got)
	}
	if got := rec.Header().Get("X-RateLimit-Remaining"); got != "300" {
		t.Fatalf("X-RateLimit-Remaining = %q", got)
	}
	if _, err := time.Parse("2006-01-02T15:04:05.000000Z", rec.Header().Get("X-RateLimit-Reset")); err != nil {
		t.Fatalf("X-RateLimit-Reset = %q: %v", rec.Header().Get("X-RateLimit-Reset"), err)
	}
}

func TestAPIRateLimitHeadersTreatDoorkeeperTokenParamsAsAuthenticated(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		fn   string
		want string
	}{
		{"rackAttackRequestToken", `requestRawQueryParamValue(req, "access_token")`},
		{"rackAttackRequestToken", `requestRawQueryParamValue(req, "bearer_token")`},
		{"requestHasTokenParam", `requestRawQueryParamValue(req, "access_token") != ""`},
		{"requestRawQueryParamValue", `return lastValue(req.URL.Query()[key])`},
	} {
		if !functionBodyContains(t, src, check.fn, check.want) {
			t.Fatalf("%s missing raw Doorkeeper token query fragment %q", check.fn, check.want)
		}
	}
	for _, forbidden := range []string{
		`strings.TrimSpace(query.Get("access_token"))`,
		`strings.TrimSpace(query.Get("bearer_token"))`,
	} {
		if strings.Contains(string(src), forbidden) {
			t.Fatalf("rate/cache token query checks must not trim opaque Doorkeeper params via %q", forbidden)
		}
	}

	e := echo.New()
	e.Use(apiRateLimitHeadersMiddleware)
	e.GET("/api/v1/instance", func(c *echo.Context) error {
		return c.String(http.StatusOK, "ok")
	})

	for _, target := range []string{
		"/api/v1/instance?access_token=query-token",
		"/api/v1/instance?bearer_token=query-token",
		"/api/v1/instance?access_token=+query-token+",
		"/api/v1/instance?bearer_token=+query-token+",
	} {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		rec := httptest.NewRecorder()

		e.ServeHTTP(rec, req)

		if got := rec.Header().Get("X-RateLimit-Limit"); got != "30000" {
			t.Fatalf("%s X-RateLimit-Limit = %q", target, got)
		}
	}
}

func TestAPIRateLimitHeadersPreferMoreRestrictiveRailsAPIThrottle(t *testing.T) {
	e := echo.New()
	e.Use(apiRateLimitHeadersMiddleware)
	e.POST("/api/v1/apps", func(c *echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"name": "client"})
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/apps", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if got := rec.Header().Get("X-RateLimit-Limit"); got != "5" {
		t.Fatalf("X-RateLimit-Limit = %q", got)
	}
	if got := rec.Header().Get("X-RateLimit-Remaining"); got != "5" {
		t.Fatalf("X-RateLimit-Remaining = %q", got)
	}
}

func TestAPIRateLimitHeadersMatchRailsVersionedMediaThrottle(t *testing.T) {
	e := echo.New()
	e.Use(apiRateLimitHeadersMiddleware)
	for _, path := range []string{"/api/v1/media", "/api/v2/media"} {
		e.POST(path, func(c *echo.Context) error {
			return c.JSON(http.StatusOK, map[string]string{"id": "1"})
		})
	}

	for _, path := range []string{"/api/v1/media", "/api/v2/media"} {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		req.Header.Set("Authorization", "Bearer token")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		if got := rec.Header().Get("X-RateLimit-Limit"); got != "30000" {
			t.Fatalf("%s X-RateLimit-Limit = %q", path, got)
		}
		if got := rec.Header().Get("X-RateLimit-Remaining"); got != "30000" {
			t.Fatalf("%s X-RateLimit-Remaining = %q", path, got)
		}
	}
}

func rackAttackFormRequest(method string, target string, body string) *http.Request {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return req
}

func TestRackAttackThrottleUsesRedisCounterAndFixedWindowKey(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`e.Use(server.rackAttackThrottleMiddleware)`,
		`s.redisCommand(redisCtx, "INCR", key)`,
		`s.redisCommand(redisCtx, "EXPIRE", key, strconv.FormatInt(periodSeconds, 10))`,
		`rackAttackLogHit("throttle", c)`,
		`return rackAttackThrottledResponse(c, candidate, now, s.webLocale(c, nil))`,
	} {
		if !strings.Contains(string(src), want) {
			t.Fatalf("server.go must contain %q", want)
		}
	}

	now := time.Unix(600, 0).UTC()
	key := rackAttackThrottleRedisKey("paon:", rackAttackThrottleCandidate{
		name:     "throttle_media_proxy",
		limit:    railsMediaProxyLimit,
		period:   railsMediaProxyPeriod,
		identity: "203.0.113.10",
	}, now)
	if want := "paon:paon:rack_attack:throttle_media_proxy:203.0.113.10:1"; key != want {
		t.Fatalf("rackAttackThrottleRedisKey = %q, want %q", key, want)
	}
}

func TestRackAttackThrottledResponseMatchesRailsResponderShape(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/media_proxy/123/original/file.png", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	now := time.Unix(600, 0).UTC()

	err := rackAttackThrottledResponse(c, rackAttackThrottleCandidate{
		name:     "throttle_media_proxy",
		limit:    railsMediaProxyLimit,
		period:   railsMediaProxyPeriod,
		identity: "203.0.113.10",
	}, now, "en")
	if err != nil {
		t.Fatal(err)
	}

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := rec.Header().Get("X-RateLimit-Limit"); got != "30000" {
		t.Fatalf("X-RateLimit-Limit = %q", got)
	}
	if got := rec.Header().Get("X-RateLimit-Remaining"); got != "0" {
		t.Fatalf("X-RateLimit-Remaining = %q", got)
	}
	if got := rec.Header().Get("X-RateLimit-Reset"); got != "1970-01-01T00:20:00.000000Z" {
		t.Fatalf("X-RateLimit-Reset = %q", got)
	}
	if !strings.Contains(rec.Body.String(), `"error"`) {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestAPIRateLimitHeadersDoNotOverwriteHandlerHeaders(t *testing.T) {
	e := echo.New()
	e.Use(apiRateLimitHeadersMiddleware)
	e.GET("/api/v1/statuses", func(c *echo.Context) error {
		c.Response().Header().Set("X-RateLimit-Limit", "7")
		c.Response().Header().Set("X-RateLimit-Remaining", "2")
		c.Response().Header().Set("X-RateLimit-Reset", "2030-01-01T00:00:00.000000Z")
		return c.NoContent(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/statuses", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if got := rec.Header().Get("X-RateLimit-Limit"); got != "7" {
		t.Fatalf("X-RateLimit-Limit = %q", got)
	}
	if got := rec.Header().Get("X-RateLimit-Remaining"); got != "2" {
		t.Fatalf("X-RateLimit-Remaining = %q", got)
	}
	if got := rec.Header().Get("X-RateLimit-Reset"); got != "2030-01-01T00:00:00.000000Z" {
		t.Fatalf("X-RateLimit-Reset = %q", got)
	}
}

func TestStatusFamilyRateLimitHeadersOverrideGenericAPIThrottle(t *testing.T) {
	e := echo.New()
	e.Use(apiRateLimitHeadersMiddleware)
	e.POST("/api/v1/statuses", func(c *echo.Context) error {
		setStatusFamilyRateLimitHeaders(c, railsStatusFamilyLimit-1)
		return c.JSON(http.StatusOK, map[string]string{"id": "1"})
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/statuses", nil)
	req.Header.Set("Authorization", "Bearer token")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if got := rec.Header().Get("X-RateLimit-Limit"); got != "3000" {
		t.Fatalf("X-RateLimit-Limit = %q", got)
	}
	if got := rec.Header().Get("X-RateLimit-Remaining"); got != "2999" {
		t.Fatalf("X-RateLimit-Remaining = %q", got)
	}
	if _, err := time.Parse("2006-01-02T15:04:05.000000Z", rec.Header().Get("X-RateLimit-Reset")); err != nil {
		t.Fatalf("X-RateLimit-Reset = %q: %v", rec.Header().Get("X-RateLimit-Reset"), err)
	}
}

func TestFollowsFamilyRateLimitHeadersOverrideGenericAPIThrottle(t *testing.T) {
	e := echo.New()
	e.Use(apiRateLimitHeadersMiddleware)
	e.POST("/api/v1/accounts/:id/follow", func(c *echo.Context) error {
		setFollowsFamilyRateLimitHeaders(c, railsFollowsFamilyLimit-1)
		return c.JSON(http.StatusOK, map[string]string{"ok": "true"})
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/accounts/42/follow", nil)
	req.Header.Set("Authorization", "Bearer token")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if got := rec.Header().Get("X-RateLimit-Limit"); got != "4000" {
		t.Fatalf("X-RateLimit-Limit = %q", got)
	}
	if got := rec.Header().Get("X-RateLimit-Remaining"); got != "3999" {
		t.Fatalf("X-RateLimit-Remaining = %q", got)
	}
	if _, err := time.Parse("2006-01-02T15:04:05.000000Z", rec.Header().Get("X-RateLimit-Reset")); err != nil {
		t.Fatalf("X-RateLimit-Reset = %q: %v", rec.Header().Get("X-RateLimit-Reset"), err)
	}
}

func TestReportsFamilyRateLimitHeadersOverrideGenericAPIThrottle(t *testing.T) {
	e := echo.New()
	e.Use(apiRateLimitHeadersMiddleware)
	e.POST("/api/v1/reports", func(c *echo.Context) error {
		setReportsFamilyRateLimitHeaders(c, railsReportsFamilyLimit-1)
		return c.JSON(http.StatusOK, map[string]string{"id": "1"})
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/reports", nil)
	req.Header.Set("Authorization", "Bearer token")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if got := rec.Header().Get("X-RateLimit-Limit"); got != "400" {
		t.Fatalf("X-RateLimit-Limit = %q", got)
	}
	if got := rec.Header().Get("X-RateLimit-Remaining"); got != "399" {
		t.Fatalf("X-RateLimit-Remaining = %q", got)
	}
	if _, err := time.Parse("2006-01-02T15:04:05.000000Z", rec.Header().Get("X-RateLimit-Reset")); err != nil {
		t.Fatalf("X-RateLimit-Reset = %q: %v", rec.Header().Get("X-RateLimit-Reset"), err)
	}
}

func TestStatusFamilyRateLimitHeadersApplyToRailsOverrideActions(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, fn := range []string{"createStatus", "updateStatus", "reblogStatus"} {
		if !functionBodyContains(t, src, fn, `setStatusFamilyRateLimitHeaders(c, railsStatusFamilyLimit-1)`) {
			t.Fatalf("%s must apply Rails statuses-family rate-limit headers", fn)
		}
	}
	for _, fn := range []string{"createStatus", "updateStatus"} {
		if !functionBodyContains(t, src, fn, `s.consumeRailsFamilyRateLimit(c, *account, railsRateLimitFamilyStatuses, now)`) {
			t.Fatalf("%s must consume Rails statuses-family rate-limit counter", fn)
		}
		if !functionBodyContains(t, src, fn, `s.rollbackRailsFamilyRateLimit(c.Request().Context(), *account, railsRateLimitFamilyStatuses, now)`) {
			t.Fatalf("%s must rollback Rails statuses-family rate-limit counter on DB failure", fn)
		}
	}
	if functionBodyContains(t, src, "reblogStatus", `railsRateLimitFamilyStatuses`) {
		t.Fatal("reblogStatus must keep Rails headers but must not consume statuses-family counter without with_rate_limit")
	}
}

func TestReportsFamilyRateLimitHeadersApplyToRailsOverrideActions(t *testing.T) {
	src, err := os.ReadFile("reports.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, src, "createReport", `setReportsFamilyRateLimitHeaders(c, railsReportsFamilyLimit-1)`) {
		t.Fatal("reports.go:createReport must apply Rails reports-family rate-limit headers")
	}
	if !functionBodyContains(t, src, "createReport", `s.consumeRailsFamilyRateLimit(c, *account, railsRateLimitFamilyReports, now)`) {
		t.Fatal("reports.go:createReport must consume Rails reports-family rate-limit counter")
	}
	if !functionBodyContains(t, src, "createReport", `s.rollbackRailsFamilyRateLimit(c.Request().Context(), *account, railsRateLimitFamilyReports, now)`) {
		t.Fatal("reports.go:createReport must rollback Rails reports-family rate-limit counter on DB failure")
	}
}

func TestFollowsFamilyRateLimitHeadersApplyToRailsOverrideActions(t *testing.T) {
	checks := map[string][]string{
		"relationships.go": {"followAccount"},
		"tags.go":          {"followTag"},
	}
	for file, fns := range checks {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, fn := range fns {
			if !functionBodyContains(t, src, fn, `setFollowsFamilyRateLimitHeaders(c, railsFollowsFamilyLimit-1)`) {
				t.Fatalf("%s:%s must apply Rails follows-family rate-limit headers", file, fn)
			}
			if !functionBodyContains(t, src, fn, `s.consumeRailsFamilyRateLimit(c, *account, railsRateLimitFamilyFollows, now)`) {
				t.Fatalf("%s:%s must consume Rails follows-family rate-limit counter", file, fn)
			}
			if !functionBodyContains(t, src, fn, `s.rollbackRailsFamilyRateLimit(c.Request().Context(), *account, railsRateLimitFamilyFollows, now)`) {
				t.Fatalf("%s:%s must rollback Rails follows-family rate-limit counter on DB failure", file, fn)
			}
		}
	}
}
