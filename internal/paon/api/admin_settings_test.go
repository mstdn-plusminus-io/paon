package api

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
)

func TestAdminSettingsBrandingRequiresWebSession(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/settings/branding?tab=site", nil)
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	want := "/auth/sign_in?redirect_to=" + url.QueryEscape("/admin/settings/branding?tab=site")
	if got := rec.Header().Get("Location"); got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}
}

func TestAdminSettingsBrandingPostWithoutOverrideUsesRailsNotFound(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/admin/settings/branding", nil)
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
}

func TestValidateAdminBrandingSettingsRejectsInvalidValues(t *testing.T) {
	valid := adminBrandingSettings{SiteTitle: "Paon", SiteContactUsername: "admin", SiteContactEmail: "admin@example.com"}
	if err := validateAdminBrandingSettings(valid); err != nil {
		t.Fatal(err)
	}
	if err := validateAdminBrandingSettings(adminBrandingSettings{SiteContactUsername: "admin", SiteContactEmail: "admin@example.com"}); err == nil {
		t.Fatal("expected blank title to be rejected")
	}
	long := valid
	long.SiteShortDescription = strings.Repeat("x", 201)
	if err := validateAdminBrandingSettings(long); err == nil {
		t.Fatal("expected long description to be rejected")
	}
}

func TestAdminSettingsUpdateRedirectMessagesUseLocaleKeys(t *testing.T) {
	files := map[string][]string{
		"admin_settings.go": {
			"updateAdminSettingsBranding",
		},
		"admin_about.go": {
			"updateAdminSettingsAbout",
		},
		"admin_registrations.go": {
			"updateAdminSettingsRegistrations",
		},
		"admin_discovery.go": {
			"updateAdminSettingsDiscovery",
		},
		"admin_content_retention.go": {
			"updateAdminSettingsContentRetention",
		},
		"admin_appearance.go": {
			"updateAdminSettingsAppearance",
		},
	}
	for file, funcs := range files {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, fn := range funcs {
			for _, want := range []string{
				`adminSettingsInvalidMessage(locale,`,
				`adminSettingsDatabaseUnavailableMessage(locale)`,
				`adminSettingsSavedMessage(locale,`,
			} {
				if !functionBodyContains(t, src, fn, want) {
					t.Fatalf("%s:%s missing localized redirect helper %q", file, fn, want)
				}
			}
			for _, forbidden := range []string{
				`settings are invalid"`,
				`settings saved"`,
				`DATABASE_URL is not set"`,
			} {
				if functionBodyContains(t, src, fn, forbidden) {
					t.Fatalf("%s:%s still contains display literal %q", file, fn, forbidden)
				}
			}
		}
	}
}

func TestAdminSettingsMessagesResolveJapaneseLocale(t *testing.T) {
	if got := adminSettingsSavedMessage("ja", "branding"); !strings.Contains(got, "保存しました") || strings.Contains(got, "Branding settings saved") {
		t.Fatalf("Japanese saved message = %q", got)
	}
	if got := adminSettingsInvalidMessage("ja", "registrations"); !strings.Contains(got, "不正") || strings.Contains(got, "Registration settings are invalid") {
		t.Fatalf("Japanese invalid message = %q", got)
	}
	if got := adminSettingErrorText("ja", errAdminSetting("Site title can't be blank")); got == "Site title can't be blank" || !strings.Contains(got, "サーバー名") {
		t.Fatalf("Japanese validation message = %q", got)
	}
	if got := adminSettingErrorText("ja", errAdminSetting("Site upload must be a readable image")); got == "Site upload must be a readable image" || !strings.Contains(got, "画像") || !strings.Contains(got, "読み取") {
		t.Fatalf("Japanese site upload readable-image message = %q", got)
	}
	if got := adminSettingsDatabaseUnavailableMessage("ja"); got == "DATABASE_URL is not set" || !strings.Contains(got, "DATABASE_URL") {
		t.Fatalf("Japanese database message = %q", got)
	}
}

func TestServerRenderedFallbackMessagesResolveJapaneseLocale(t *testing.T) {
	cases := []struct {
		key      string
		fallback string
		want     string
		fn       func(string, string, string) string
	}{
		{"admin.dashboard.not_permitted", "You are not allowed to view the dashboard.", "ダッシュボード", adminT},
		{"admin.settings.not_permitted", "You are not allowed to manage settings.", "設定", adminT},
		{"admin.software_updates.disabled", "Software update checks are disabled.", "無効", adminT},
		{"admin.software_updates.not_permitted", "You are not allowed to view software updates.", "ソフトウェア更新", adminT},
		{"admin.invites.not_permitted", "You are not allowed to manage invites.", "招待", adminT},
		{"invites.not_permitted", "You are not allowed to invite users.", "招待", settingsT},
		{"auth.invalid_password", "Current password is invalid", "現在のパスワード", settingsT},
		{"users.invalid_password", "Password is invalid", "パスワード", settingsT},
		{"users.password_confirmation_mismatch", "Password confirmation does not match", "一致", settingsT},
		{"users.email_taken", "Email has already been taken", "使用", settingsT},
		{"users.invalid_email", "Email is invalid", "メールアドレス", settingsT},
		{"webauthn_credentials.challenge_expired", "WebAuthn challenge is missing or expired", "期限切れ", settingsT},
		{"webauthn_credentials.already_exists", "Security key already exists", "既に存在", settingsT},
		{"webauthn_credentials.nickname", "Name", "名前", settingsT},
		{"webauthn_credentials.created", "Created", "作成日時", settingsT},
	}
	for _, tt := range cases {
		got := tt.fn("ja", tt.key, tt.fallback)
		if got == tt.fallback || !strings.Contains(got, tt.want) {
			t.Fatalf("%s Japanese message = %q, want localized text containing %q", tt.key, got, tt.want)
		}
	}
}

func TestAdminSettingsBrandingHTMLIncludesRailsFields(t *testing.T) {
	html := adminSettingsBrandingHTML(adminBrandingSettings{
		SiteTitle:            "Paon",
		SiteContactUsername:  "admin",
		SiteContactEmail:     "admin@example.com",
		SiteShortDescription: "Short",
		FaviconUploadID:      12,
		FaviconURL:           "https://example.com/favicon.png",
		AppIconUploadID:      13,
		AppIconURL:           "https://example.com/app-icon.png",
	}, "saved", "")

	for _, want := range []string{
		"Branding",
		`class="content__heading__tabs"`,
		`id="branding" class="selected simple-navigation-active-leaf" href="/admin/settings/branding"`,
		`href="/admin/settings/content_retention"`,
		`class="simple_form new_form_admin_settings"`,
		`class="fields-row"`,
		`class="actions"`,
		`action="/admin/settings/branding"`,
		`enctype="multipart/form-data"`,
		`value="Paon" name="form_admin_settings[site_title]"`,
		`value="admin" name="form_admin_settings[site_contact_username]"`,
		`value="admin@example.com" name="form_admin_settings[site_contact_email]"`,
		`maxlength="200" class="text optional" name="form_admin_settings[site_short_description]"`,
		`name="form_admin_settings[thumbnail]" id="form_admin_settings_thumbnail"`,
		`name="form_admin_settings[favicon]" id="form_admin_settings_favicon" accept="image/jpeg,image/png,image/gif,image/webp"`,
		`name="form_admin_settings[app_icon]" id="form_admin_settings_app_icon" accept="image/jpeg,image/png,image/gif,image/webp"`,
		`src="https://example.com/favicon.png"`,
		`action="/admin/site_uploads/12"`,
		`src="https://example.com/app-icon.png"`,
		`action="/admin/site_uploads/13"`,
		`class="input with_label string optional form_admin_settings_site_title field_with_hint"`,
		`class="input with_block_label text optional form_admin_settings_site_short_description field_with_hint"`,
		"Short",
		"saved",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("branding html missing %q: %s", want, html)
		}
	}
}
