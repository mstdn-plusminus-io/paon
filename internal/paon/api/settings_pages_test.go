package api

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestSettingsPagePassesRailsSiteTitleToRenderer(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, src, "settingsPage", `s.renderer.SettingsHTML(c.Request().URL.Path, account, s.settingStringValue("site_title", s.cfg.Title))`) {
		t.Fatal("settingsPage must pass Rails site_title setting to the settings renderer")
	}
}

func TestSettingsPrivateNoStoreMiddlewareCoversStandaloneSettingsRoutes(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{"/settings/imports", "/settings/applications", "/settings/login_activities"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		s.echo.ServeHTTP(rec, req)
		if got := rec.Header().Get("Cache-Control"); got != "private, no-store" {
			t.Fatalf("%s Cache-Control = %q", path, got)
		}
	}
}

func TestSettingsNavMatchesRailsNavigationDestinations(t *testing.T) {
	options := settingsHTMLOptions{Functional: true, FunctionalOrMoved: true, Permissions: rolePermissionsAll}
	nav := settingsNavigationHTML("/settings/preferences/appearance", "en", options) +
		settingsNavigationHTML("/settings/privacy", "en", options) +
		settingsNavigationHTML("/settings/export", "en", options) +
		settingsNavigationHTML("/auth/edit", "en", options)
	for _, want := range []string{
		`href="/settings/profile"`,
		`href="/settings/preferences"`,
		`href="/settings/preferences/appearance"`,
		`href="/settings/preferences/notifications"`,
		`href="/settings/preferences/other"`,
		`href="/relationships"`,
		`href="/filters"`,
		`href="/statuses_cleanup"`,
		`href="/auth/edit"`,
		`href="/settings/two_factor_authentication_methods"`,
		`href="/oauth/authorized_applications"`,
		`href="/settings/export"`,
		`href="/settings/imports"`,
		`href="/settings/applications"`,
		`href="/invites"`,
		`href="/admin/trends/statuses"`,
		`href="/admin/reports"`,
		`href="/admin/dashboard"`,
		`href="/asynq"`,
		`href="/auth/sign_out"`,
	} {
		if !strings.Contains(nav, want) {
			t.Fatalf("settings nav missing implemented route %s: %s", want, nav)
		}
	}
}

func TestSettingsPageShellKeepsMastodonAssetsAndFormClasses(t *testing.T) {
	appAssets.Store(appAssetPaths{})
	html := settingsPageShell("Profile", settingsNavForLocale("en"), simpleFormOpen("/settings/profile", "put")+simpleTextInput("Display name", "account[display_name]", "Alice", "text", "required")+simpleSubmit("Save changes")+simpleFormClose(), "en", "mastodon-light", "/packs/js/settings.js")
	for _, want := range []string{
		`<html lang="en">`,
		`<body class="admin theme-mastodon-light no-reduce-motion">`,
		`class="admin-wrapper"`,
		`class="sidebar-wrapper"`,
		`class="sidebar-wrapper__inner"`,
		`class="sidebar"`,
		`class="sidebar__toggle__icon"`,
		`<ul><li id="web">`,
		`class="content-wrapper"`,
		`<main class="content" role="main">`,
		`class="content__heading"`,
		`/packs/css/common.css`,
		`/packs/css/mastodon-light.css`,
		`/packs/js/common.js`,
		`/packs/js/public.js`,
		`/packs/js/admin.js`,
		`class="simple_form"`,
		`class="fields-group"`,
		`class="input with_label required"`,
		`class="actions"`,
		`src="/packs/js/settings.js"`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("settings shell missing %q: %s", want, html)
		}
	}
}

func TestSettingsNavigationAppliesRailsStateAndPermissionBoundaries(t *testing.T) {
	regular := settingsNavigationHTML("/settings/preferences/appearance", "en", settingsHTMLOptions{Functional: true, FunctionalOrMoved: true})
	for _, forbidden := range []string{`id="invites"`, `id="trends"`, `id="moderation"`, `id="admin"`, `id="asynq"`, `PgHero`} {
		if strings.Contains(regular, forbidden) {
			t.Fatalf("regular-user navigation must not contain %q: %s", forbidden, regular)
		}
	}
	for _, want := range []string{`id="profile"`, `id="preferences" class="selected"`, `id="appearance" class="selected simple-navigation-active-leaf"`, `id="relationships"`, `id="filters"`, `id="statuses_cleanup"`, `id="security"`, `id="data"`, `id="development"`, `id="logout"`} {
		if !strings.Contains(regular, want) {
			t.Fatalf("regular-user navigation missing %q: %s", want, regular)
		}
	}

	devops := settingsNavigationHTML("/asynq", "en", settingsHTMLOptions{Permissions: rolePermissionViewDevops})
	if !strings.Contains(devops, `id="asynq" class="selected simple-navigation-active-leaf"`) || !strings.Contains(devops, `href="/asynq"`) {
		t.Fatalf("devops navigation missing selected Asynq dashboard: %s", devops)
	}
	if strings.Contains(devops, `target=`) {
		t.Fatalf("Asynq navigation must open in the current window: %s", devops)
	}
	if strings.Contains(devops, "PgHero") || strings.Contains(devops, "/pghero") {
		t.Fatalf("Go navigation must omit PgHero: %s", devops)
	}
}

func TestSettingsCheckboxFieldHTMLMatchesRailsWithLabelStructure(t *testing.T) {
	got := settingsRecommendedCheckboxFieldWithHint(
		"Group boosts",
		"Recently boosted posts are not shown again",
		"user[settings_attributes][aggregate_reblogs]",
		true,
		"Recommended",
	)
	want := `<div class="fields-group"><div class="input with_label boolean optional user_settings_aggregate_reblogs field_with_hint"><div class="label_input"><label class="boolean optional" for="user_settings_attributes_aggregate_reblogs">Group boosts <span class="recommended">Recommended</span></label><div class="label_input__wrapper"><input type="hidden" name="user[settings_attributes][aggregate_reblogs]" value="0"><label class="checkbox"><input class="boolean optional" type="checkbox" id="user_settings_attributes_aggregate_reblogs" name="user[settings_attributes][aggregate_reblogs]" value="1" checked></label></div></div><span class="hint">Recently boosted posts are not shown again</span></div></div>`
	if got != want {
		t.Fatalf("checkbox html does not match Rails with_label structure:\n got: %s\nwant: %s", got, want)
	}
}

func railsNavigationTopLevelItems(src string) []string {
	re := regexp.MustCompile(`(?m)^\s{4}n\.item :([a-z_]+),`)
	matches := re.FindAllStringSubmatch(src, -1)
	out := make([]string, 0, len(matches))
	for _, match := range matches {
		out = append(out, match[1])
	}
	return out
}

func TestSettingsPreferencesOtherHTMLUsesRailsPrivacyOptions(t *testing.T) {
	user := models.User{Settings: sql.NullString{String: `{}`, Valid: true}}
	html, ok, err := (&Server{}).settingsHTML("/settings/preferences/other", user, models.Account{Locked: true})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("settings html not handled")
	}
	if strings.Contains(html, `value="">Default</option>`) {
		t.Fatalf("default privacy select should not include a blank option like Rails: %s", html)
	}
	if !strings.Contains(html, `value="private" selected`) {
		t.Fatalf("locked accounts should fall back to private default privacy: %s", html)
	}
}

func TestSettingsPrivacyHTMLRendersRailsFields(t *testing.T) {
	account := models.Account{
		Locked:          true,
		Discoverable:    sql.NullBool{Bool: false, Valid: true},
		HideCollections: sql.NullBool{Bool: true, Valid: true},
		Indexable:       true,
	}
	user := models.User{Settings: sql.NullString{String: `{"noindex":true,"show_application":false}`, Valid: true}}
	html, ok, err := (&Server{}).settingsHTML("/settings/privacy", user, account)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("settings html not handled")
	}
	for _, want := range []string{
		`action="/settings/privacy"`,
		`name="account[discoverable]"`,
		`name="account[unlocked]"`,
		`name="account[indexable]" value="1" checked`,
		`name="account[show_collections]"`,
		`name="account[settings][indexable]"`,
		`name="account[settings][show_application]"`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("privacy html missing %s: %s", want, html)
		}
	}
	if strings.Contains(html, `name="account[unlocked]" value="1" checked`) || strings.Contains(html, `name="account[settings][show_application]" value="1" checked`) {
		t.Fatalf("privacy html has unexpected checked field: %s", html)
	}
	if got := strings.Count(html, `class="recommended"`); got != 1 {
		t.Fatalf("privacy html recommended badge count = %d, want 1: %s", got, html)
	}
}

func TestSettingsTwoFactorMethodsHTMLRendersExistingActions(t *testing.T) {
	html := settingsTwoFactorMethodsHTML(models.User{OTPRequiredForLogin: true}, []models.WebauthnCredential{{
		ID:        7,
		Nickname:  "YubiKey",
		CreatedAt: time.Date(2026, 6, 20, 1, 2, 3, 0, time.UTC),
	}}, "en")
	for _, want := range []string{
		`href="/settings/two_factor_authentication/recovery_codes"`,
		`href="/settings/two_factor_authentication_methods/disable"`,
		`href="/settings/security_keys"`,
		`data-method="post"`,
		`Two-factor authentication is enabled`,
		`class="table"`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("two factor html missing %s: %s", want, html)
		}
	}
	if strings.Contains(html, `account-security__tabs`) {
		t.Fatalf("two factor html must not add non-Rails content tabs: %s", html)
	}
}

func TestSettingsTwoFactorMethodsRedirectsToSetupWhenDisabledLikeRails(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`c.Request().URL.Path == "/settings/two_factor_authentication_methods" && !user.OTPRequiredForLogin`,
		`c.Redirect(http.StatusFound, "/settings/otp_authentication")`,
	} {
		if !functionBodyContains(t, src, "settingsPage", want) {
			t.Fatalf("settingsPage missing disabled two-factor redirect %q", want)
		}
	}
}

func TestSettingsHTMLUsesUserLocaleForServerRenderedLabels(t *testing.T) {
	user := models.User{Locale: sql.NullString{String: "ja", Valid: true}, Approved: true, ConfirmedAt: sql.NullTime{Time: time.Now(), Valid: true}}
	html, ok, err := (&Server{}).settingsHTML("/settings/preferences/appearance", user, models.Account{})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("settings html not handled")
	}
	for _, want := range []string{
		`<html lang="ja">`,
		`>外観<`,
		`>プロフィール<`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("localized settings html missing %s: %s", want, html)
		}
	}
}

func TestSettingsHTMLUsesUserThemeForServerRenderedHead(t *testing.T) {
	user := models.User{Settings: sql.NullString{String: `{"theme":"mastodon-light"}`, Valid: true}}
	html, ok, err := (&Server{}).settingsHTML("/settings/preferences/appearance", user, models.Account{})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("settings html not handled")
	}
	if !strings.Contains(html, `/packs/css/mastodon-light.css`) {
		t.Fatalf("settings html did not use configured theme: %s", html)
	}
}
