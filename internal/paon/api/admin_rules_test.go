package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestMastodon44AdminRuleNewRouteAndExistingDesignForm(t *testing.T) {
	source, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range []string{
		`e.GET("/admin/rules/new", s.newAdminRulePage)`,
		`e.GET("/admin/rules/new.:format", s.newAdminRulePage)`,
	} {
		if !strings.Contains(string(source), route) {
			t.Fatalf("Mastodon 4.4 rule route missing: %s", route)
		}
	}
	page := adminRuleNewHTML(adminRuleForm{Text: `Be <kind>`, Hint: `Explain "why"`}, "", "", "en")
	for _, want := range []string{
		`action="/admin/rules"`,
		`name="rule[text]"`,
		`name="rule[hint]"`,
		`Be &lt;kind&gt;`,
		`Explain &#34;why&#34;`,
	} {
		if !strings.Contains(page, want) {
			t.Fatalf("new rule page missing %q: %s", want, page)
		}
	}
	index := adminRulesIndexHTML(nil, adminRuleForm{}, "", "", "en")
	if !strings.Contains(index, `href="/admin/rules/new"`) {
		t.Fatalf("rules index does not link the 4.4 new route: %s", index)
	}
}

func TestAdminRulesRequiresWebSession(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/rules?order=priority", nil)
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	want := "/auth/sign_in?redirect_to=" + url.QueryEscape("/admin/rules?order=priority")
	if got := rec.Header().Get("Location"); got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}
}

func TestAdminRuleMessagesResolveJapaneseLocale(t *testing.T) {
	for _, check := range []struct {
		key       string
		fallback  string
		forbidden string
		want      string
	}{
		{"errors.invalid", "Rule is invalid", "Rule is invalid", "不正"},
		{"errors.text_blank", "Rule text can't be blank", "Rule text", "本文"},
		{"errors.text_too_long", "Rule text is too long", "Rule text", "長すぎ"},
	} {
		got := adminRuleMessage("ja", check.key, check.fallback)
		if strings.Contains(got, check.forbidden) || !strings.Contains(got, check.want) {
			t.Fatalf("adminRuleMessage(%q) = %q", check.key, got)
		}
	}
	if got := adminRuleErrorText("ja", errAdminSetting("Rule text can't be blank")); strings.Contains(got, "Rule text") || !strings.Contains(got, "本文") {
		t.Fatalf("adminRuleErrorText blank = %q", got)
	}
}

func TestAdminRuleFormAndHTMLPreserveOptionalHint(t *testing.T) {
	formValues := url.Values{}
	formValues.Set("rule[text]", "Be kind")
	formValues.Set("rule[hint]", "Explain what kindness means <safely>")
	formValues.Set("rule[priority]", "4")
	req := httptest.NewRequest(http.MethodPost, "/admin/rules", strings.NewReader(formValues.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c := echo.NewContext(req, httptest.NewRecorder(), echo.New())

	form, err := parseAdminRuleForm(c)
	if err != nil {
		t.Fatal(err)
	}
	if form.Text != "Be kind" || form.Hint != "Explain what kindness means <safely>" || form.Priority != 4 || !form.PriorityPresent {
		t.Fatalf("parsed admin rule form = %#v", form)
	}

	rule := adminRuleWithForm(models.Rule{ID: 1}, form)
	if rule.Text != form.Text || rule.Hint != form.Hint || rule.Priority != form.Priority {
		t.Fatalf("adminRuleWithForm = %#v", rule)
	}

	html := adminRulesIndexHTML([]models.Rule{{ID: 1, Text: form.Text, Hint: form.Hint}}, form, "", "", "en")
	for _, want := range []string{
		`name="rule[text]"`,
		`name="rule[hint]"`,
		`id="rule_hint"`,
		`Additional information`,
		`Optional. Provide more details about the rule`,
		`Explain what kindness means &lt;safely&gt;`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("admin rules HTML missing %q: %s", want, html)
		}
	}
}

func TestAdminRuleTranslationFormCRUDAndValidation(t *testing.T) {
	values := url.Values{}
	values.Set("rule[text]", "Default rule")
	values.Set("rule[hint]", "Default hint")
	values.Set("rule[translations_attributes][0][id]", "12")
	values.Set("rule[translations_attributes][0][language]", "ja")
	values.Set("rule[translations_attributes][0][text]", "既定のルール")
	values.Set("rule[translations_attributes][0][hint]", "補足")
	values.Set("rule[translations_attributes][1][language]", "zh_CN")
	values.Set("rule[translations_attributes][1][text]", "简体中文规则")
	values.Set("rule[translations_attributes][2][id]", "13")
	values.Set("rule[translations_attributes][2][_destroy]", "1")
	req := httptest.NewRequest(http.MethodPost, "/admin/rules/1", strings.NewReader(values.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c := echo.NewContext(req, httptest.NewRecorder(), echo.New())

	form, err := parseAdminRuleForm(c)
	if err != nil {
		t.Fatal(err)
	}
	if len(form.Translations) != 3 {
		t.Fatalf("translations = %#v", form.Translations)
	}
	if form.Translations[0].ID != 12 || form.Translations[0].Language != "ja" || form.Translations[0].Text != "既定のルール" {
		t.Fatalf("existing translation = %#v", form.Translations[0])
	}
	if form.Translations[1].Language != "zh_CN" {
		t.Fatalf("new translation = %#v", form.Translations[1])
	}
	if !form.Translations[2].Destroy {
		t.Fatalf("destroy translation = %#v", form.Translations[2])
	}
	if err := validateAdminRuleForm(form); err != nil {
		t.Fatalf("valid translated rule: %v", err)
	}

	form.Translations = append(form.Translations, adminRuleTranslationForm{Language: "ja", Text: "重複"})
	if !errors.Is(validateAdminRuleForm(form), errAdminRuleTranslationDuplicate) {
		t.Fatalf("duplicate language was accepted: %#v", form.Translations)
	}
}

func TestLocalizedRuleContentUsesExactThenGenericLocale(t *testing.T) {
	rule := models.Rule{
		Text: "Default",
		Hint: "Default hint",
		Translations: []models.RuleTranslation{
			{Language: "en", Text: "English", Hint: "English hint"},
			{Language: "en-GB", Text: "British", Hint: "British hint"},
			{Language: "ja", Text: "日本語", Hint: "日本語の補足"},
		},
	}
	if text, hint := localizedRuleContent(rule, "en-GB"); text != "British" || hint != "British hint" {
		t.Fatalf("exact translation = %q %q", text, hint)
	}
	if text, hint := localizedRuleContent(rule, "en-US"); text != "English" || hint != "English hint" {
		t.Fatalf("generic translation = %q %q", text, hint)
	}
	if text, hint := localizedRuleContent(rule, "fr"); text != "Default" || hint != "Default hint" {
		t.Fatalf("default translation = %q %q", text, hint)
	}
}

func TestAdminRuleHTMLIncludesTranslationsPreviewAndReordering(t *testing.T) {
	rules := []models.Rule{
		{ID: 1, Text: "First", Translations: []models.RuleTranslation{{ID: 10, Language: "ja", Text: "最初", Hint: "補足"}}},
		{ID: 2, Text: "Second"},
	}
	index := adminRulesIndexHTML(rules, adminRuleForm{}, "", "", "ja")
	for _, want := range []string{"最初", "補足", `/admin/rules/1/move_down`, `/admin/rules/2/move_up`} {
		if !strings.Contains(index, want) {
			t.Fatalf("rules index missing %q: %s", want, index)
		}
	}
	edit := adminRuleEditHTML(rules[0], "", "", "ja")
	for _, want := range []string{
		`name="rule[translations_attributes][0][id]" value="10"`,
		`name="rule[translations_attributes][0][_destroy]"`,
		`name="rule[translations_attributes][1][language]"`,
		`選択中の言語でプレビュー`,
		`最初`,
	} {
		if !strings.Contains(edit, want) {
			t.Fatalf("rule edit missing %q: %s", want, edit)
		}
	}
}
