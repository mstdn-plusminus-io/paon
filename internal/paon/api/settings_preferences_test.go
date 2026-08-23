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
		"user%5Bsettings%5D%5Bweb.use_system_scrollbars%5D=1",
		"user%5Bsettings%5D%5Bweb.display_media%5D=show_all",
		"user%5Bsettings%5D%5Bdefault_privacy%5D=private",
		"user%5Bsettings%5D%5Bdefault_quote_policy%5D=followers",
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
	if settings["web.auto_play"] != true || settings["web.use_system_scrollbars"] != true || settings["web.display_media"] != "show_all" || settings["default_privacy"] != "private" || settings["default_quote_policy"] != "nobody" {
		t.Fatalf("settings = %#v", settings)
	}
	languages := updates["chosen_languages"].(models.StringArray)
	if len(languages) != 3 || languages[0] != "ja" || languages[1] != "en" || languages[2] != "ja" {
		t.Fatalf("languages = %#v", languages)
	}
}

func TestMastodon45EmojiStylePreferenceIsValidatedAndRendered(t *testing.T) {
	html := settingsPreferencesAppearanceHTML(models.User{}, map[string]any{"web.emoji_style": "native"}, "en")
	for _, want := range []string{`name="user[settings_attributes][web.emoji_style]"`, `value="native" selected`, "Emoji style"} {
		if !strings.Contains(html, want) {
			t.Fatalf("emoji style preference is missing %q: %s", want, html)
		}
	}
	for _, value := range []string{"auto", "native", "twemoji"} {
		settings, err := preferencesSettingsFromForm(map[string][]string{"user[settings][web.emoji_style]": {value}})
		if err != nil || settings["web.emoji_style"] != value {
			t.Fatalf("emoji style %q = %#v, %v", value, settings, err)
		}
	}
	if _, err := preferencesSettingsFromForm(map[string][]string{"user[settings][web.emoji_style]": {"invalid"}}); err == nil {
		t.Fatal("invalid emoji style was accepted")
	}
}

func TestMastodon44SystemScrollbarPreferenceIsRendered(t *testing.T) {
	html := settingsPreferencesAppearanceHTML(models.User{}, map[string]any{"web.use_system_scrollbars": true}, "en")
	id := `id="user_settings_attributes_web.use_system_scrollbars"`
	idIndex := strings.Index(html, id)
	if idIndex < 0 {
		t.Fatalf("system scrollbar setting is missing %q: %s", id, html)
	}
	inputStart := strings.LastIndex(html[:idIndex], "<input")
	inputEndOffset := strings.Index(html[idIndex:], ">")
	if inputStart < 0 || inputEndOffset < 0 {
		t.Fatalf("system scrollbar setting input is malformed: %s", html)
	}
	input := html[inputStart : idIndex+inputEndOffset+1]
	for _, want := range []string{
		`name="user[settings_attributes][web.use_system_scrollbars]"`,
		`value="1"`,
		` checked`,
	} {
		if !strings.Contains(input, want) {
			t.Fatalf("system scrollbar setting input is missing %q: %s", want, input)
		}
	}
	for _, want := range []string{
		`Use system&#39;s default scrollbar`,
		`Applies only to desktop browsers based on Safari and Chrome`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("system scrollbar setting is missing %q: %s", want, html)
		}
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
		"user%5Bsettings%5D%5Bdefault_quote_policy%5D=bad",
	} {
		req := httptest.NewRequest(http.MethodPut, "/settings/preferences/appearance", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		c := echo.NewContext(req, httptest.NewRecorder(), echo.New())

		if _, _, err := parseSettingsPreferencesPayload(c); err == nil {
			t.Fatalf("expected invalid enum error for %s", body)
		}
	}
}

func TestMastodon45DefaultQuotePolicyIsSavedAndRenderedOnPostingDefaults(t *testing.T) {
	settings := map[string]any{"theme": "system"}
	applyUserSettingsAttributes(settings, map[string]any{"default_quote_policy": "followers"})
	if settings["default_quote_policy"] != "followers" {
		t.Fatalf("saved default_quote_policy = %#v", settings["default_quote_policy"])
	}

	account := models.Account{}
	english := settingsPreferencesPostingDefaultsHTML(account, settings, "en")
	for _, want := range []string{
		`action="/settings/preferences/posting_defaults"`,
		`name="user[settings_attributes][default_quote_policy]"`,
		`value="followers" selected`,
		`Who can quote`,
		`These settings will be used as defaults when you create new posts`,
	} {
		if !strings.Contains(english, want) {
			t.Fatalf("English quote policy setting is missing %q: %s", want, english)
		}
	}
	if strings.Index(english, `value="public"`) > strings.Index(english, `value="followers"`) || strings.Index(english, `value="followers"`) > strings.Index(english, `value="nobody"`) {
		t.Fatalf("quote policy option order does not match Mastodon 4.5: %s", english)
	}

	japanese := settingsPreferencesPostingDefaultsHTML(models.Account{Locked: true}, settings, "ja")
	for _, want := range []string{"引用できるユーザー", "フォロワーのみ", "デフォルトとして使用", "フォロワーのみの投稿はほかのユーザーから引用できません"} {
		if !strings.Contains(japanese, want) {
			t.Fatalf("Japanese quote policy setting is missing %q: %s", want, japanese)
		}
	}
}

func TestMastodon45PostingDefaultsAreSeparatedFromOtherPreferences(t *testing.T) {
	other := settingsPreferencesOtherHTML(models.User{}, models.Account{}, map[string]any{}, "en")
	if strings.Contains(other, "default_quote_policy") || strings.Contains(other, "default_privacy") {
		t.Fatalf("posting defaults must not remain embedded in Other preferences: %s", other)
	}
}

func TestMastodon44UnsetTimeZoneSelectsApplicationDefault(t *testing.T) {
	html := settingsTimeZoneSelectField("", "en")
	if !strings.Contains(html, `<option value="Etc/UTC" selected>(GMT+00:00) UTC</option>`) {
		t.Fatalf("unset time zone did not select the Rails application default: %s", html)
	}
	explicit := settingsTimeZoneSelectField("Asia/Tokyo", "en")
	if !strings.Contains(explicit, `<option value="Asia/Tokyo" selected>`) || strings.Contains(explicit, `<option value="Etc/UTC" selected>`) {
		t.Fatalf("explicit time zone was not preserved: %s", explicit)
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
