package api

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
)

func TestAdminSettingsAboutRequiresWebSession(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/settings/about?tab=terms", nil)
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	want := "/auth/sign_in?redirect_to=" + url.QueryEscape("/admin/settings/about?tab=terms")
	if got := rec.Header().Get("Location"); got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}
}

func TestValidateAdminAboutSettings(t *testing.T) {
	valid := adminAboutSettings{ShowDomainBlocks: "disabled", ShowDomainBlocksReason: "users", StatusPageURL: "https://status.example.com"}
	if err := validateAdminAboutSettings(valid); err != nil {
		t.Fatal(err)
	}
	if err := validateAdminAboutSettings(adminAboutSettings{ShowDomainBlocks: "public", ShowDomainBlocksReason: "users"}); err == nil {
		t.Fatal("expected invalid domain block visibility to be rejected")
	}
	if err := validateAdminAboutSettings(adminAboutSettings{ShowDomainBlocks: "disabled", ShowDomainBlocksReason: "all", StatusPageURL: "ftp://status.example.com"}); err == nil {
		t.Fatal("expected invalid status page url to be rejected")
	}
}

func TestAdminSettingsAboutHTMLIncludesRailsFields(t *testing.T) {
	html := adminSettingsAboutHTML(adminAboutSettings{
		SiteExtendedDescription: "About",
		ShowDomainBlocks:        "users",
		ShowDomainBlocksReason:  "all",
		StatusPageURL:           "https://status.example.com",
		SiteTerms:               "Terms",
	}, "saved", "")

	for _, want := range []string{
		"About",
		`action="/admin/settings/about"`,
		`name="form_admin_settings[site_extended_description]"`,
		`name="form_admin_settings[show_domain_blocks]"`,
		`value="users" selected`,
		`name="form_admin_settings[show_domain_blocks_rationale]"`,
		`value="all" selected`,
		`name="form_admin_settings[status_page_url]" value="https://status.example.com"`,
		`name="form_admin_settings[site_terms]"`,
		"About",
		"Terms",
		"saved",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("about html missing %q: %s", want, html)
		}
	}
}
