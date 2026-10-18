package api

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestSettingsPrivacyNullDiscoverableRendersUncheckedLikeRails(t *testing.T) {
	html := settingsPrivacyHTML(models.Account{ID: 1, Discoverable: sql.NullBool{}}, map[string]any{}, "en")
	if !strings.Contains(html, `name="account[discoverable]" value="1"`) {
		t.Fatalf("privacy html missing discoverable checkbox: %s", html)
	}
	if strings.Contains(html, `name="account[discoverable]" value="1" checked`) {
		t.Fatalf("NULL discoverable must render unchecked like Rails: %s", html)
	}

	html = settingsPrivacyHTML(models.Account{ID: 1, Discoverable: sql.NullBool{Bool: true, Valid: true}}, map[string]any{}, "en")
	if !strings.Contains(html, `name="account[discoverable]" value="1" checked`) {
		t.Fatalf("discoverable=true must render checked: %s", html)
	}
}

func TestSettingsPrivacyRenderSettingsAppliesSiteNoindexDefaultLikeRails(t *testing.T) {
	const indexableChecked = `name="account[settings][indexable]" value="1" checked`

	settings := settingsPrivacyRenderSettings(map[string]any{}, true)
	if settings["noindex"] != true {
		t.Fatalf("missing noindex must fall back to site default: %#v", settings)
	}
	if html := settingsPrivacyHTML(models.Account{ID: 1}, settings, "en"); strings.Contains(html, indexableChecked) {
		t.Fatalf("site default noindex=true must render profile indexable unchecked: %s", html)
	}

	settings = settingsPrivacyRenderSettings(map[string]any{"noindex": nil}, true)
	if settings["noindex"] != true {
		t.Fatalf("nil noindex must fall back to site default: %#v", settings)
	}

	settings = settingsPrivacyRenderSettings(map[string]any{"noindex": false}, true)
	if settings["noindex"] != false {
		t.Fatalf("explicit user noindex must win over site default: %#v", settings)
	}
	if html := settingsPrivacyHTML(models.Account{ID: 1}, settings, "en"); !strings.Contains(html, indexableChecked) {
		t.Fatalf("explicit noindex=false must render profile indexable checked: %s", html)
	}

	if settings := settingsPrivacyRenderSettings(nil, false); settings["noindex"] != false {
		t.Fatalf("nil settings map must be handled: %#v", settings)
	}

	// Without a stored site setting the Rails default (noindex=false) applies.
	user := models.User{Settings: sql.NullString{String: `{}`, Valid: true}}
	html, ok, err := (&Server{}).settingsHTML("/settings/privacy", user, models.Account{ID: 1})
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if !strings.Contains(html, indexableChecked) {
		t.Fatalf("default site noindex=false must render profile indexable checked: %s", html)
	}
}

func TestSettingsPrivacyRenderSitesUseSiteNoindexDefault(t *testing.T) {
	for file, fn := range map[string]string{
		"settings_pages.go":   "settingsHTMLWithOptions",
		"settings_privacy.go": "renderSettingsPrivacyError",
	} {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if !functionBodyContains(t, src, fn, `settingsPrivacyRenderSettings(settings, s.settingBoolValue("noindex", false))`) {
			t.Fatalf("%s must render privacy with the site noindex default", fn)
		}
	}
}

func TestSettingsAppearanceLocaleSelectMarksEffectiveLocaleLikeRails(t *testing.T) {
	cases := []struct {
		name   string
		locale sql.NullString
		loc    string
		want   string
	}{
		{name: "null user locale", locale: sql.NullString{}, loc: "ja", want: "ja"},
		{name: "unavailable user locale", locale: sql.NullString{String: "xx", Valid: true}, loc: "ja", want: "ja"},
		{name: "available user locale", locale: sql.NullString{String: "de", Valid: true}, loc: "ja", want: "de"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			html := settingsPreferencesAppearanceHTML(models.User{Locale: tc.locale}, map[string]any{}, tc.loc)
			if !strings.Contains(html, `<option value="`+tc.want+`" selected>`) {
				t.Fatalf("locale select must mark %q selected: %s", tc.want, html)
			}
			if got := strings.Count(html[strings.Index(html, `name="user[locale]"`):strings.Index(html, `name="user[time_zone]"`)], " selected"); got != 1 {
				t.Fatalf("locale select selected count = %d, want 1", got)
			}
			if tc.want != "af" && strings.Contains(html, `<option value="af" selected>`) {
				t.Fatalf("first option must not be selected: %s", html)
			}
		})
	}
	if got := settingsAppearanceSelectedLocale(models.User{}, "not-a-locale"); got != "en" {
		t.Fatalf("unresolvable effective locale = %q, want en", got)
	}
}

func TestSettingsHTMLHandlesHTMLFormatPathsLikeRails(t *testing.T) {
	user := models.User{Settings: sql.NullString{String: `{}`, Valid: true}}
	for path, want := range map[string]string{
		"/settings/preferences/appearance.html":    `action="/settings/preferences/appearance"`,
		"/settings/preferences/notifications.html": `action="/settings/preferences/notifications"`,
		"/settings/preferences/other.html":         `action="/settings/preferences/other"`,
		"/settings/privacy.html":                   `action="/settings/privacy"`,
		"/settings/privacy.HTML":                   `action="/settings/privacy"`,
	} {
		html, ok, err := (&Server{}).settingsHTML(path, user, models.Account{ID: 1})
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if !ok {
			t.Fatalf("%s must be handled like the extensionless path", path)
		}
		if !strings.Contains(html, want) {
			t.Fatalf("%s missing %s: %s", path, want, html)
		}
	}
	// The 2FA methods index must never be rendered for users without OTP, even via .html.
	if _, ok, _ := (&Server{}).settingsHTML("/settings/two_factor_authentication_methods.html", user, models.Account{ID: 1}); ok {
		t.Fatal("2FA methods index must not render for users without OTP")
	}
}

func TestSettingsPreferencesRedirectPathStripsFormatLikeRails(t *testing.T) {
	e := echo.New()
	got := ""
	handler := func(c *echo.Context) error {
		got = settingsPreferencesRedirectPath(c)
		return c.NoContent(http.StatusNoContent)
	}
	for _, page := range []string{"appearance", "notifications", "other"} {
		e.PUT("/settings/preferences/"+page+".:format", handler)
		e.PUT("/settings/preferences/"+page, handler)
	}
	for path, want := range map[string]string{
		"/settings/preferences/notifications.html": "/settings/preferences/notifications",
		"/settings/preferences/other.html":         "/settings/preferences/other",
		"/settings/preferences/appearance.html":    "/settings/preferences/appearance",
		"/settings/preferences/notifications":      "/settings/preferences/notifications",
		"/settings/preferences/other":              "/settings/preferences/other",
	} {
		got = ""
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, path, nil))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("%s status = %d", path, rec.Code)
		}
		if got != want {
			t.Fatalf("%s redirect path = %q, want %q", path, got, want)
		}
	}
	if got := settingsPathWithoutFormat("/settings/preferences/other.HTML", "HTML"); got != "/settings/preferences/other" {
		t.Fatalf("format strip = %q", got)
	}
	if got := settingsPathWithoutFormat("/settings/preferences/other", ""); got != "/settings/preferences/other" {
		t.Fatalf("extensionless path changed: %q", got)
	}
}

func TestSettingsPreferencesSavedNoticeUsesNewLocaleLikeRails(t *testing.T) {
	s := &Server{}
	c := echo.NewContext(httptest.NewRequest(http.MethodPut, "/settings/preferences/appearance", nil), httptest.NewRecorder(), echo.New())
	user := models.User{ID: 1, Locale: sql.NullString{String: "ja", Valid: true}}

	if got := s.settingsPreferencesSavedLocale(c, user, map[string]any{"locale": sql.NullString{String: "en", Valid: true}}, "ja"); got != "en" {
		t.Fatalf("saved notice locale = %q, want en", got)
	}
	if got := settingsChangeSavedMessage(s.settingsPreferencesSavedLocale(c, user, map[string]any{"locale": sql.NullString{String: "en", Valid: true}}, "ja")); got != "Changes successfully saved!" {
		t.Fatalf("saved notice = %q", got)
	}
	user.Locale = sql.NullString{String: "en", Valid: true}
	if got := s.settingsPreferencesSavedLocale(c, user, map[string]any{"locale": sql.NullString{String: "ja", Valid: true}}, "en"); got != "ja" {
		t.Fatalf("saved notice locale = %q, want ja", got)
	}
	if got := s.settingsPreferencesSavedLocale(c, user, map[string]any{"locale": sql.NullString{}}, "ja"); got != "en" {
		t.Fatalf("cleared locale must fall back to default, got %q", got)
	}
	if got := s.settingsPreferencesSavedLocale(c, user, map[string]any{"time_zone": sql.NullString{}}, "ja"); got != "ja" {
		t.Fatalf("unchanged locale must keep current, got %q", got)
	}

	src, err := os.ReadFile("settings_preferences.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, src, "updateSettingsPreferences", `locale = s.settingsPreferencesSavedLocale(c, *user, userUpdates, locale)`) {
		t.Fatal("updateSettingsPreferences must build the saved notice in the updated locale")
	}
}

func TestSettingsTwoFactorMethodsSecurityKeyLinksArePlainGETLinks(t *testing.T) {
	for _, credentials := range [][]models.WebauthnCredential{nil, {{ID: 7, Nickname: "YubiKey"}}} {
		html := settingsTwoFactorMethodsHTML(models.User{OTPRequiredForLogin: true}, credentials, false, "en")
		if strings.Contains(html, `data-method="get"`) {
			t.Fatalf("security key links must not use data-method=get (rails-ujs turns it into POST): %s", html)
		}
		want := `<a class="table-action-link" href="/settings/security_keys/new">`
		if len(credentials) > 0 {
			want = `<a class="table-action-link" href="/settings/security_keys">`
		}
		if !strings.Contains(html, want) {
			t.Fatalf("two factor html missing %s: %s", want, html)
		}
	}

	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/settings/security_keys/new", "/settings/security_keys"} {
		rec := httptest.NewRecorder()
		s.echo.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusFound || !strings.HasPrefix(rec.Header().Get("Location"), "/auth/sign_in") {
			t.Fatalf("GET %s must reach the GET page handler (sign-in redirect when signed out), status=%d location=%q body=%s", path, rec.Code, rec.Header().Get("Location"), rec.Body.String())
		}
	}
}

func TestSettingsOTPAuthenticationCreateAllowsReprovisioningLikeRails(t *testing.T) {
	src, err := os.ReadFile("two_factor_settings.go")
	if err != nil {
		t.Fatal(err)
	}
	if functionBodyContains(t, src, "createSettingsOTPAuthentication", `user.OTPRequiredForLogin`) {
		t.Fatal("createSettingsOTPAuthentication must not reject OTP users; Rails only guards #show")
	}
	if !functionBodyContains(t, src, "createSettingsOTPAuthentication", `s.settingsRequireChallenge(c, user, "/settings/otp_authentication"`) {
		t.Fatal("createSettingsOTPAuthentication must keep require_challenge!")
	}
	if !functionBodyContains(t, src, "settingsOTPAuthenticationPage", `if user.OTPRequiredForLogin {`) {
		t.Fatal("settingsOTPAuthenticationPage must keep verify_otp_not_enabled")
	}
}
