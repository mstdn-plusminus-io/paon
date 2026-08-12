package api

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestAdminWarningPresetsRequiresWebSession(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/warning_presets?order=title", nil)
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	want := "/auth/sign_in?redirect_to=" + url.QueryEscape("/admin/warning_presets?order=title")
	if got := rec.Header().Get("Location"); got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}
}

func TestValidateAdminWarningPresetForm(t *testing.T) {
	if err := validateAdminWarningPresetForm(adminWarningPresetForm{Text: "Stop posting spam."}); err != nil {
		t.Fatal(err)
	}
	if err := validateAdminWarningPresetForm(adminWarningPresetForm{Text: " Stop posting spam. "}); err != nil {
		t.Fatal(err)
	}
	if err := validateAdminWarningPresetForm(adminWarningPresetForm{Title: "Spam"}); err == nil {
		t.Fatal("expected blank text to be rejected")
	}
	if err := validateAdminWarningPresetForm(adminWarningPresetForm{Text: " \t\n "}); err == nil {
		t.Fatal("expected whitespace-only text to be rejected")
	}

	src, err := os.ReadFile("admin_warning_presets.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		fn   string
		want string
	}{
		{"parseAdminWarningPresetForm", `Title: lastFormValue(req.Form, "account_warning_preset[title]")`},
		{"parseAdminWarningPresetForm", `Text:  lastFormValue(req.Form, "account_warning_preset[text]")`},
		{"validateAdminWarningPresetForm", `strings.TrimSpace(form.Text) == ""`},
		{"insertAdminWarningPreset", `Title:     form.Title`},
		{"insertAdminWarningPreset", `Text:      form.Text`},
		{"updateAdminWarningPresetModel", `"title":      form.Title`},
		{"updateAdminWarningPresetModel", `"text":       form.Text`},
	} {
		if !functionBodyContains(t, src, check.fn, check.want) {
			t.Fatalf("%s missing Rails warning preset persistence fragment %q", check.fn, check.want)
		}
	}
}

func TestAdminWarningPresetMessagesResolveJapaneseLocale(t *testing.T) {
	for _, check := range []struct {
		key       string
		fallback  string
		forbidden string
		want      string
	}{
		{"errors.invalid", "Warning preset is invalid", "Warning preset", "不正"},
		{"errors.text_blank", "Warning text can't be blank", "Warning text", "警告文"},
	} {
		got := adminWarningPresetMessage("ja", check.key, check.fallback)
		if strings.Contains(got, check.forbidden) || !strings.Contains(got, check.want) {
			t.Fatalf("adminWarningPresetMessage(%q) = %q", check.key, got)
		}
	}
	if got := adminWarningPresetErrorText("ja", errAdminSetting("Warning text can't be blank")); strings.Contains(got, "Warning text") || !strings.Contains(got, "警告文") {
		t.Fatalf("adminWarningPresetErrorText blank = %q", got)
	}
}

func TestAdminWarningPresetsHTMLIncludesRailsFields(t *testing.T) {
	html := adminWarningPresetsIndexHTML([]models.AccountWarningPreset{{ID: 2, Title: "Spam", Text: "No spam"}}, adminWarningPresetForm{Title: "Draft", Text: "Draft text"}, "saved", "", "en")

	for _, want := range []string{
		"Warning presets",
		`action="/admin/warning_presets"`,
		`name="account_warning_preset[title]" value="Draft"`,
		`name="account_warning_preset[text]"`,
		"Draft text",
		`href="/admin/warning_presets/2/edit"`,
		"Spam",
		"No spam",
		`class="announcements-list"`,
		`data-method="delete"`,
		`href="/admin/warning_presets/2"`,
		"saved",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("warning presets html missing %q: %s", want, html)
		}
	}
}
