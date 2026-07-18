package api

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestParseSettingsPreferencesPayloadAcceptsRailsNestedSettings(t *testing.T) {
	body := strings.Join([]string{
		"user%5Blocale%5D=ja",
		"user%5Btime_zone%5D=Asia%2FTokyo",
		"user%5Bsettings%5D%5Bweb.auto_play%5D=0",
		"user%5Bsettings%5D%5Bweb.auto_play%5D=1",
		"user%5Bsettings%5D%5Bweb.display_media%5D=show_all",
		"user%5Bsettings%5D%5Bdefault_privacy%5D=private",
		"user%5Bchosen_languages%5D%5B%5D=",
		"user%5Bchosen_languages%5D%5B%5D=ja",
		"user%5Bchosen_languages%5D%5B%5D=en",
		"user%5Bchosen_languages%5D%5B%5D=ja",
	}, "&")
	req := httptest.NewRequest(http.MethodPut, "/settings/preferences/appearance", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c := echo.NewContext(req, httptest.NewRecorder(), echo.New())

	updates, settings, err := parseSettingsPreferencesPayload(c)
	if err != nil {
		t.Fatal(err)
	}
	locale := updates["locale"].(sql.NullString)
	if !locale.Valid || locale.String != "ja" {
		t.Fatalf("locale = %#v", locale)
	}
	if settings["web.auto_play"] != true || settings["web.display_media"] != "show_all" || settings["default_privacy"] != "private" {
		t.Fatalf("settings = %#v", settings)
	}
	languages := updates["chosen_languages"].(models.StringArray)
	if len(languages) != 3 || languages[0] != "ja" || languages[1] != "en" || languages[2] != "ja" {
		t.Fatalf("languages = %#v", languages)
	}
}

func TestParseSettingsPreferencesPayloadClearsChosenLanguagesLikeRails(t *testing.T) {
	body := strings.Join([]string{
		"user%5Bchosen_languages%5D%5B%5D=",
		"user%5Bchosen_languages%5D%5B%5D= ",
	}, "&")
	req := httptest.NewRequest(http.MethodPut, "/settings/preferences/appearance", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c := echo.NewContext(req, httptest.NewRecorder(), echo.New())

	updates, _, err := parseSettingsPreferencesPayload(c)
	if err != nil {
		t.Fatal(err)
	}
	if value, ok := updates["chosen_languages"]; !ok || value != nil {
		t.Fatalf("chosen_languages = %#v, want nil update", value)
	}
}

func TestPreferencesSettingsRejectsInvalidEnum(t *testing.T) {
	for _, body := range []string{
		"user%5Bsettings%5D%5Bweb.display_media%5D=bad",
		"user%5Bsettings%5D%5Bdefault_privacy%5D=",
		"user%5Bsettings%5D%5Bdefault_privacy%5D=direct",
	} {
		req := httptest.NewRequest(http.MethodPut, "/settings/preferences/appearance", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		c := echo.NewContext(req, httptest.NewRecorder(), echo.New())

		if _, _, err := parseSettingsPreferencesPayload(c); err == nil {
			t.Fatalf("expected invalid enum error for %s", body)
		}
	}
}

func TestSettingsPreferencesUpdateRequiresWebAuthentication(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodPut, "/settings/preferences/appearance", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())

	if err := s.updateSettingsPreferences(c); err != nil {
		t.Fatal(err)
	}
	want := "/auth/sign_in?redirect_to=" + url.QueryEscape("/settings/preferences/appearance")
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != want {
		t.Fatalf("status = %d location = %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestSettingsPreferencesSavedFlashMatchesRailsGenericLocale(t *testing.T) {
	src, err := os.ReadFile("settings_preferences.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, src, "updateSettingsPreferences", `settingsChangeSavedMessage(locale)`) {
		t.Fatal("updateSettingsPreferences must use Rails generic.changes_saved_msg flash")
	}
	if functionBodyContains(t, src, "updateSettingsPreferences", `QueryEscape("Preferences saved")`) {
		t.Fatal("updateSettingsPreferences must not use fixed Go-only success flash")
	}
}
