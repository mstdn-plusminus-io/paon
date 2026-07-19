package api

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
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
