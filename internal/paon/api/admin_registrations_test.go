package api

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
)

func TestAdminSettingsRegistrationsRequiresWebSession(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/settings/registrations?tab=signups", nil)
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	want := "/auth/sign_in?redirect_to=" + url.QueryEscape("/admin/settings/registrations?tab=signups")
	if got := rec.Header().Get("Location"); got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}
}

func TestValidateAdminRegistrationsSettingsRejectsInvalidMode(t *testing.T) {
	if err := validateAdminRegistrationsSettings(adminRegistrationsSettings{RegistrationsMode: "open"}); err != nil {
		t.Fatal(err)
	}
	if err := validateAdminRegistrationsSettings(adminRegistrationsSettings{RegistrationsMode: "invite_only"}); err == nil {
		t.Fatal("expected invalid registration mode to be rejected")
	}
}

func TestAdminSettingsCheckboxUsesLastRailsValue(t *testing.T) {
	values := map[string][]string{"form_admin_settings[require_invite_text]": []string{"0", "1"}}
	if !adminSettingsCheckbox(values, "form_admin_settings[require_invite_text]") {
		t.Fatal("expected last checkbox value to win")
	}
	values = map[string][]string{"form_admin_settings[require_invite_text]": []string{"1", "0"}}
	if adminSettingsCheckbox(values, "form_admin_settings[require_invite_text]") {
		t.Fatal("expected hidden unchecked value to win")
	}
}

func TestAdminSettingsRegistrationsHTMLIncludesRailsFields(t *testing.T) {
	html := adminSettingsRegistrationsHTML(adminRegistrationsSettings{
		RegistrationsMode:          "approved",
		RequireInviteText:          true,
		CaptchaEnabled:             true,
		ClosedRegistrationsMessage: "Closed",
	}, "saved", "")

	for _, want := range []string{
		"Registrations",
		`action="/admin/settings/registrations"`,
		`name="form_admin_settings[registrations_mode]"`,
		`value="approved" selected`,
		`name="form_admin_settings[require_invite_text]" value="1" checked`,
		`name="form_admin_settings[captcha_enabled]" value="1" checked`,
		`name="form_admin_settings[closed_registrations_message]"`,
		"Closed",
		"saved",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("registrations html missing %q: %s", want, html)
		}
	}
}
