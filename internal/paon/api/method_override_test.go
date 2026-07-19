package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
)

func TestMethodOverrideHeaderRunsBeforeRouting(t *testing.T) {
	e := echo.New()
	e.Pre(methodOverrideMiddleware)
	e.DELETE("/resource", func(c *echo.Context) error {
		return c.String(http.StatusOK, c.Request().Method)
	})

	req := httptest.NewRequest(http.MethodPost, "/resource", nil)
	req.Header.Set("X-HTTP-Method-Override", "DELETE")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || rec.Body.String() != http.MethodDelete {
		t.Fatalf("status = %d body = %q", rec.Code, rec.Body.String())
	}
}

func TestMethodOverrideFormRunsBeforeRoutingAndRestoresBody(t *testing.T) {
	e := echo.New()
	e.Pre(methodOverrideMiddleware)
	e.PATCH("/resource", func(c *echo.Context) error {
		body, err := io.ReadAll(c.Request().Body)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), "name=alice") {
			t.Fatalf("body was not restored: %q", string(body))
		}
		if got := c.FormValue("authenticity_token"); got != "csrf-token" {
			t.Fatalf("form value after method override = %q", got)
		}
		return c.String(http.StatusOK, c.Request().Method)
	})

	req := httptest.NewRequest(http.MethodPost, "/resource", strings.NewReader("_method=patch&name=alice&authenticity_token=csrf-token"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || rec.Body.String() != http.MethodPatch {
		t.Fatalf("status = %d body = %q", rec.Code, rec.Body.String())
	}
}

func TestMethodOverrideIgnoresUnsupportedMethods(t *testing.T) {
	e := echo.New()
	e.Pre(methodOverrideMiddleware)
	e.POST("/resource", func(c *echo.Context) error {
		return c.String(http.StatusOK, c.Request().Method)
	})

	req := httptest.NewRequest(http.MethodPost, "/resource", strings.NewReader("_method=get"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || rec.Body.String() != http.MethodPost {
		t.Fatalf("status = %d body = %q", rec.Code, rec.Body.String())
	}
}

func TestAPITrailingSlashMiddlewareRewritesAPIRoutesBeforeRouting(t *testing.T) {
	e := echo.New()
	e.Pre(apiTrailingSlashMiddleware)
	e.DELETE("/api/v1/domain_blocks", func(c *echo.Context) error {
		if c.Request().URL.RawQuery != "domain=remote.example" {
			t.Fatalf("RawQuery = %q", c.Request().URL.RawQuery)
		}
		return c.String(http.StatusOK, c.Request().URL.Path)
	})

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/domain_blocks/?domain=remote.example", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || rec.Body.String() != "/api/v1/domain_blocks" {
		t.Fatalf("status = %d body = %q", rec.Code, rec.Body.String())
	}
}

func TestAPITrailingSlashMiddlewareLeavesNonAPIRoutesUntouched(t *testing.T) {
	e := echo.New()
	e.Pre(apiTrailingSlashMiddleware)
	e.GET("/packs/*", func(c *echo.Context) error {
		return c.String(http.StatusOK, c.Request().URL.Path)
	})

	req := httptest.NewRequest(http.MethodGet, "/packs/", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || rec.Body.String() != "/packs/" {
		t.Fatalf("status = %d body = %q", rec.Code, rec.Body.String())
	}
}

func TestRailsMethodOverrideReachesExistingMemberRoutes(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	for _, route := range []struct {
		method string
		path   string
	}{
		{http.MethodDelete, "/oauth/authorized_applications/1"},
		{http.MethodDelete, "/admin/site_uploads/1"},
		{http.MethodDelete, "/admin/invites/1"},
		{http.MethodDelete, "/admin/rules/1"},
		{http.MethodDelete, "/admin/roles/1"},
		{http.MethodPatch, "/admin/users/1/role"},
		{http.MethodDelete, "/admin/users/1/two_factor_authentication"},
		{http.MethodPatch, "/admin/tags/1"},
		{http.MethodDelete, "/admin/warning_presets/1"},
		{http.MethodDelete, "/admin/announcements/1"},
		{http.MethodDelete, "/admin/relays/1"},
		{http.MethodDelete, "/admin/webhooks/1"},
		{http.MethodDelete, "/admin/accounts/1"},
		{http.MethodPatch, "/admin/accounts/1/change_email"},
		{http.MethodDelete, "/admin/domain_allows/1"},
		{http.MethodDelete, "/admin/instances/example.com"},
		{http.MethodDelete, "/admin/account_moderation_notes/1"},
		{http.MethodDelete, "/admin/report_notes/1"},
		{http.MethodDelete, "/invites/1"},
		{http.MethodDelete, "/settings/profile/pictures/avatar"},
		{http.MethodDelete, "/settings/imports/1"},
		{http.MethodPatch, "/settings/applications/1"},
		{http.MethodDelete, "/settings/aliases/1"},
		{http.MethodDelete, "/settings/featured_tags/1"},
		{http.MethodDelete, "/settings/sessions/1"},
		{http.MethodDelete, "/settings/delete"},
		{http.MethodDelete, "/settings/security_keys/1"},
	} {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, route.path, strings.NewReader("_method="+strings.ToLower(route.method)))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			rec := httptest.NewRecorder()
			s.echo.ServeHTTP(rec, req)
			if rec.Code == http.StatusMethodNotAllowed || rec.Code == http.StatusNotFound {
				t.Fatalf("method override did not route to handler: status = %d body = %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestRailsMethodOverrideReachesExistingSingletonUpdateRoutes(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{
		"/auth/setup",
		"/relationships",
		"/statuses_cleanup",
		"/admin/settings/branding",
		"/admin/settings/registrations",
		"/admin/settings/discovery",
		"/admin/settings/about",
		"/admin/settings/appearance",
		"/admin/settings/content_retention",
		"/admin/follow_recommendations",
		"/settings/profile",
		"/settings/preferences/appearance",
		"/settings/preferences/notifications",
		"/settings/preferences/other",
		"/settings/privacy",
	} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, path, strings.NewReader("_method=patch"))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			rec := httptest.NewRecorder()
			s.echo.ServeHTTP(rec, req)
			if rec.Code == http.StatusMethodNotAllowed || rec.Code == http.StatusNotFound {
				t.Fatalf("method override did not route to singleton update handler: status = %d body = %s", rec.Code, rec.Body.String())
			}
		})
	}
}
