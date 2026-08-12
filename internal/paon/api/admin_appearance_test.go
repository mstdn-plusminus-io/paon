package api

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
)

func TestAdminSettingsAppearanceRequiresWebSession(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/settings/appearance?tab=theme", nil)
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	want := "/auth/sign_in?redirect_to=" + url.QueryEscape("/admin/settings/appearance?tab=theme")
	if got := rec.Header().Get("Location"); got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}
}

func TestValidateAdminAppearanceSettingsRejectsInvalidTheme(t *testing.T) {
	if err := validateAdminAppearanceSettings(adminAppearanceSettings{Theme: "default"}); err != nil {
		t.Fatal(err)
	}
	if err := validateAdminAppearanceSettings(adminAppearanceSettings{Theme: "unknown"}); err == nil {
		t.Fatal("expected invalid theme to be rejected")
	}
}

func TestAdminThemeSettingFallsBackToSystem(t *testing.T) {
	if got := adminThemeSetting("mastodon-light"); got != "mastodon-light" {
		t.Fatalf("theme = %q", got)
	}
	if got := adminThemeSetting("not-real"); got != "system" {
		t.Fatalf("theme = %q, want system", got)
	}
}

func TestAdminSettingsAppearanceHTMLIncludesRailsFields(t *testing.T) {
	html := adminSettingsAppearanceHTML(adminAppearanceSettings{Theme: "contrast", CustomCSS: "body{}"}, "saved", "")

	for _, want := range []string{
		"Appearance",
		`action="/admin/settings/appearance"`,
		`enctype="multipart/form-data"`,
		`name="form_admin_settings[theme]"`,
		`value="contrast" selected`,
		`name="form_admin_settings[custom_css]"`,
		`name="form_admin_settings[mascot]" accept="image/*"`,
		"body{}",
		"saved",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("appearance html missing %q: %s", want, html)
		}
	}
}
