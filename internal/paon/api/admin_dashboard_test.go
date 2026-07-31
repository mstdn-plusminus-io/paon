package api

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	paondb "github.com/mstdn-plusminus-io/paon/internal/paon/db"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestAdminRootRedirectsToDashboard(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/admin?from=nav", nil)
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if got, want := rec.Header().Get("Location"), "/admin/dashboard?from=nav"; got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}
}

func TestAdminDashboardRequiresWebSession(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/dashboard?range=30", nil)
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	want := "/auth/sign_in?redirect_to=" + url.QueryEscape("/admin/dashboard?range=30")
	if got := rec.Header().Get("Location"); got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}
}

func TestAdminSettingsRedirectsMatchRails(t *testing.T) {
	for _, path := range []string{"/admin/settings", "/admin/settings/edit"} {
		s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		s.echo.ServeHTTP(rec, req)

		if rec.Code != http.StatusMovedPermanently {
			t.Fatalf("%s status = %d body = %s", path, rec.Code, rec.Body.String())
		}
		if got, want := rec.Header().Get("Location"), "/admin/settings/branding"; got != want {
			t.Fatalf("%s Location = %q, want %q", path, got, want)
		}
	}
}

func TestAdminDashboardCountsNilDB(t *testing.T) {
	counts, err := (&Server{}).adminDashboardCounts()
	if err != nil {
		t.Fatal(err)
	}
	if counts != (adminDashboardCounts{}) {
		t.Fatalf("counts = %#v", counts)
	}
}

func TestAdminDashboardHTMLIncludesPendingCountsAndLinks(t *testing.T) {
	html := adminDashboardHTML(adminDashboardCounts{PendingUsers: 2, PendingReports: 3, PendingTags: 5, PendingAppeals: 7}, "en")
	for _, want := range []string{
		"Dashboard",
		`<body class="admin">`,
		`class="dashboard"`,
		`class="dashboard__item"`,
		`data-admin-component="Counter"`,
		`data-admin-component="Dimension"`,
		`data-admin-component="Retention"`,
		`data-admin-component="Trends"`,
		`&#34;measure&#34;:&#34;new_users&#34;`,
		`&#34;dimension&#34;:&#34;sources&#34;`,
		`/packs/js/admin.js`,
		`href="/admin/accounts?status=pending"`,
		`href="/admin/reports"`,
		`href="/admin/trends/tags?status=pending_review"`,
		`href="/admin/disputes/appeals?status=pending"`,
		"<strong>2</strong> pending users",
		"<strong>3</strong> pending reports",
		"<strong>5</strong> pending hashtags",
		"<strong>7</strong> pending appeals",
		`class="dashboard__quick-access"`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("dashboard html missing %q: %s", want, html)
		}
	}
}

func TestAdminShellAndSettingsFormsKeepMastodonAssetsAndClasses(t *testing.T) {
	appAssets.Store(appAssetPaths{})

	dashboard := adminDashboardHTML(adminDashboardCounts{}, "en", "mastodon-light")
	for _, want := range []string{
		`<html lang="en">`,
		`<body class="admin">`,
		`<main role="main">`,
		`/packs/css/common.css`,
		`/packs/css/mastodon-light.css`,
		`/packs/js/common.js`,
		`/packs/js/admin.js`,
		`class="dashboard"`,
	} {
		if !strings.Contains(dashboard, want) {
			t.Fatalf("admin dashboard shell missing %q: %s", want, dashboard)
		}
	}

	settings := adminSettingsBrandingHTML(adminBrandingSettings{SiteTitle: "Paon", SiteContactUsername: "admin", SiteContactEmail: "admin@example.com"}, "", "", "en", "mastodon-light")
	for _, want := range []string{
		`<html lang="en">`,
		`<body class="app-body">`,
		`<main role="main">`,
		`/packs/css/common.css`,
		`/packs/css/mastodon-light.css`,
		`/packs/js/common.js`,
		`<nav class="content__heading__tabs"><div>`,
		`class="simple_form new_form_admin_settings"`,
		`class="fields-group"`,
		`class="fields-row"`,
		`class="actions"`,
		`name="form_admin_settings[site_title]"`,
	} {
		if !strings.Contains(settings, want) {
			t.Fatalf("admin settings form missing %q: %s", want, settings)
		}
	}
}

func TestAdminDashboardHTMLIncludesSystemChecks(t *testing.T) {
	html := adminDashboardHTMLWithChecks(adminDashboardCounts{}, []adminDashboardSystemCheck{
		{Key: "rules_check", Action: "/admin/rules"},
		{Key: "software_version_critical_check", Action: "/admin/software_updates", Critical: true},
	}, "en")
	for _, want := range []string{
		`class="flash-message-stack"`,
		`class="flash-message warning"`,
		`class="flash-message alert"`,
		`You haven't defined any server rules.`,
		`href="/admin/rules"`,
		`Manage server rules`,
		`A critical Paon update is available`,
		`href="/admin/software_updates"`,
		`See available updates`,
		`class="dashboard"`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("dashboard html missing system check %q: %s", want, html)
		}
	}
}

func TestAdminDashboardSystemCheckHelpers(t *testing.T) {
	if got := adminDashboardRailsSchemaVersion(); got != paondb.RequiredMastodonSchemaVersion() {
		t.Fatalf("schema version = %q", got)
	}
	s := &Server{cfg: config.Config{UpdateCheckURL: "https://updates.example", Version: "4.3.0"}}
	check, ok := s.adminDashboardSoftwareVersionCheckFromUpdates([]models.SoftwareUpdate{
		{Version: "4.3.1", Type: 0},
	})
	if !ok || check.Key != "software_version_patch_check" || check.Critical {
		t.Fatalf("patch check = %#v ok=%v", check, ok)
	}
	check, ok = s.adminDashboardSoftwareVersionCheckFromUpdates([]models.SoftwareUpdate{
		{Version: "4.3.1", Type: 1},
		{Version: "4.3.2", Type: 2, Urgent: true},
	})
	if !ok || check.Key != "software_version_critical_check" || !check.Critical {
		t.Fatalf("critical check = %#v ok=%v", check, ok)
	}
	_, ok = s.adminDashboardSoftwareVersionCheckFromUpdates([]models.SoftwareUpdate{{Version: "4.2.9", Type: 0}})
	if ok {
		t.Fatalf("old update should not be pending")
	}
}

func TestAdminDashboardSystemChecksNilDB(t *testing.T) {
	checks := (&Server{}).adminDashboardSystemChecks(&models.User{RoleID: sql.NullInt64{Valid: true, Int64: 1}})
	if len(checks) != 0 {
		t.Fatalf("checks = %#v", checks)
	}
}
