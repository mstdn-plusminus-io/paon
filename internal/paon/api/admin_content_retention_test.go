package api

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
)

func TestAdminSettingsContentRetentionRequiresWebSession(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/settings/content_retention?tab=cache", nil)
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	want := "/auth/sign_in?redirect_to=" + url.QueryEscape("/admin/settings/content_retention?tab=cache")
	if got := rec.Header().Get("Location"); got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}
}

func TestParseAdminContentRetentionSettingsAcceptsRailsNestedFields(t *testing.T) {
	form := url.Values{}
	form.Set("form_admin_settings[media_cache_retention_period]", " 14 ")
	form.Set("form_admin_settings[content_cache_retention_period]", "  ")
	form.Set("form_admin_settings[backups_retention_period]", " 30 ")

	e := echo.New()
	req := httptest.NewRequest(http.MethodPatch, "/admin/settings/content_retention", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	got, err := parseAdminContentRetentionSettings(c)
	if err != nil {
		t.Fatal(err)
	}
	want := adminContentRetentionSettings{
		MediaCacheRetentionPeriod:   "14",
		ContentCacheRetentionPeriod: "",
		BackupsRetentionPeriod:      "30",
	}
	if got != want {
		t.Fatalf("settings = %#v, want %#v", got, want)
	}
}

func TestAdminRetentionIntegerSettingFallsBackToBlank(t *testing.T) {
	if got := adminRetentionIntegerSetting("30"); got != "30" {
		t.Fatalf("period = %q", got)
	}
	if got := adminRetentionIntegerSetting("-1"); got != "-1" {
		t.Fatalf("period = %q, want -1", got)
	}
	if got := adminRetentionIntegerSetting("not-real"); got != "" {
		t.Fatalf("period = %q, want blank", got)
	}
}

func TestAdminSettingsContentRetentionHTMLIncludesRailsFields(t *testing.T) {
	html := adminSettingsContentRetentionHTML(adminContentRetentionSettings{
		MediaCacheRetentionPeriod:   "14",
		ContentCacheRetentionPeriod: "7",
		BackupsRetentionPeriod:      "30",
	}, "saved", "")

	for _, want := range []string{
		"Content retention",
		`action="/admin/settings/content_retention"`,
		`name="form_admin_settings[media_cache_retention_period]"`,
		`value="14"`,
		`name="form_admin_settings[content_cache_retention_period]"`,
		`value="7"`,
		`name="form_admin_settings[backups_retention_period]"`,
		`value="30"`,
		"saved",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("content retention html missing %q: %s", want, html)
		}
	}
}
