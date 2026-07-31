package api

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
)

func TestAdminDomainAllowNewRequiresWebSession(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/domain_allows/new?_domain=example.com", nil)
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	want := "/auth/sign_in?redirect_to=" + url.QueryEscape("/admin/domain_allows/new?_domain=example.com")
	if got := rec.Header().Get("Location"); got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}
}

func TestParseAdminDomainAllowFormAcceptsRailsNestedField(t *testing.T) {
	form := url.Values{}
	form.Set("domain_allow[domain]", " Example.COM ")

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/admin/domain_allows", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	got, err := parseAdminDomainAllowForm(c)
	if err != nil {
		t.Fatal(err)
	}
	if got != (adminDomainAllowForm{Domain: "Example.COM"}) {
		t.Fatalf("form = %#v", got)
	}
}

func TestAdminDomainAllowFormHTMLIncludesRailsFields(t *testing.T) {
	html := adminDomainAllowFormHTML(adminDomainAllowForm{Domain: "example.com"}, "bad", "en")
	for _, want := range []string{
		"Allow federation with domain",
		`action="/admin/domain_allows"`,
		`name="domain_allow[domain]" value="example.com"`,
		"bad",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("domain allow form html missing %q: %s", want, html)
		}
	}
}

func TestAdminDomainAllowRedirectErrorsUseLocaleKeys(t *testing.T) {
	goSrc, err := os.ReadFile("admin_domain_allows_web.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		fn   string
		want string
	}{
		{"createAdminDomainAllowWeb", `locale := s.webLocale(c, user)`},
		{"createAdminDomainAllowWeb", `adminDomainAllowMessage(locale, "errors.invalid", "Domain allow is invalid")`},
		{"createAdminDomainAllowWeb", `adminDomainAllowMessage(locale, "errors.domain_invalid", "Domain is invalid")`},
		{"createAdminDomainAllowWeb", `adminDomainAllowMessage(locale, "errors.database_unavailable", "DATABASE_URL is not set")`},
		{"destroyAdminDomainAllowWeb", `locale := s.webLocale(c, user)`},
		{"destroyAdminDomainAllowWeb", `adminDomainAllowMessage(locale, "errors.database_unavailable", "DATABASE_URL is not set")`},
	} {
		if !functionBodyContains(t, goSrc, check.fn, check.want) {
			t.Fatalf("%s missing localized redirect helper %q", check.fn, check.want)
		}
	}
	for _, check := range []struct {
		fn        string
		forbidden string
	}{
		{"createAdminDomainAllowWeb", `QueryEscape("Domain allow is invalid")`},
		{"createAdminDomainAllowWeb", `QueryEscape("Domain is invalid")`},
		{"createAdminDomainAllowWeb", `QueryEscape("DATABASE_URL is not set")`},
		{"destroyAdminDomainAllowWeb", `QueryEscape("DATABASE_URL is not set")`},
	} {
		if functionBodyContains(t, goSrc, check.fn, check.forbidden) {
			t.Fatalf("%s still contains display literal %q", check.fn, check.forbidden)
		}
	}
}

func TestAdminDomainAllowMessagesResolveJapaneseLocale(t *testing.T) {
	for _, check := range []struct {
		key       string
		fallback  string
		forbidden string
		want      string
	}{
		{"created_msg", "Domain allow created", "Domain allow", "連合"},
		{"destroyed_msg", "Domain allow removed", "Domain allow", "連合"},
		{"errors.domain_invalid", "Domain is invalid", "Domain is invalid", "不正"},
		{"errors.invalid", "Domain allow is invalid", "Domain allow", "不正"},
	} {
		got := adminDomainAllowMessage("ja", check.key, check.fallback)
		if strings.Contains(got, check.forbidden) || !strings.Contains(got, check.want) {
			t.Fatalf("adminDomainAllowMessage(%q) = %q", check.key, got)
		}
	}
}
