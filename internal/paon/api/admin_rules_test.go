package api

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

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
