package api

import (
	"database/sql"
	"net"
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

func TestAdminEmailDomainBlocksRequiresWebSession(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/email_domain_blocks?page=2", nil)
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	want := "/auth/sign_in?redirect_to=" + url.QueryEscape("/admin/email_domain_blocks?page=2")
	if got := rec.Header().Get("Location"); got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}
}

func TestParseAdminEmailDomainBlockFormAcceptsRailsNestedFields(t *testing.T) {
	form := url.Values{}
	form.Set("email_domain_block[domain]", " Example.COM ")
	form.Add("email_domain_block[other_domains][]", " mx1.example.com \nMx2.Example.com")
	form.Add("email_domain_block[other_domains][]", "bad domain")
	form.Add("email_domain_block[other_domains][]", "mx1.example.com")

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/admin/email_domain_blocks", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	got, err := parseAdminEmailDomainBlockForm(c)
	if err != nil {
		t.Fatal(err)
	}
	if got.Domain != "Example.COM" || len(got.OtherDomains) != 2 || got.OtherDomains[0] != "mx1.example.com" || got.OtherDomains[1] != "mx2.example.com" {
		t.Fatalf("form = %#v", got)
	}
}

func TestResolveEmailDomainBlockMXDomainsNormalizesRecords(t *testing.T) {
	old := lookupEmailDomainBlockMX
	lookupEmailDomainBlockMX = func(domain string) ([]*net.MX, error) {
		if domain != "example.com" {
			t.Fatalf("lookup domain = %q", domain)
		}
		return []*net.MX{
			{Host: "Mx2.Example.com."},
			{Host: "mx1.example.com."},
			{Host: "mx1.example.com."},
			{Host: "bad domain."},
		}, nil
	}
	defer func() { lookupEmailDomainBlockMX = old }()

	got := resolveEmailDomainBlockMXDomains("example.com")
	if len(got) != 2 || got[0] != "mx1.example.com" || got[1] != "mx2.example.com" {
		t.Fatalf("resolved domains = %#v", got)
	}
}

func TestParseAdminEmailDomainBlockIDs(t *testing.T) {
	form := url.Values{}
	form.Add("form_email_domain_block_batch[email_domain_block_ids][]", "1")
	form.Add("form_email_domain_block_batch[email_domain_block_ids][]", "bad")
	form.Add("form_email_domain_block_batch[email_domain_block_ids][]", "2")

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/admin/email_domain_blocks/batch", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	got := parseAdminEmailDomainBlockIDs(c)
	if len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("ids = %#v", got)
	}
}

func TestAdminEmailDomainBlocksHTMLIncludesRailsFields(t *testing.T) {
	html := adminEmailDomainBlocksHTML([]models.EmailDomainBlock{
		{ID: 2, Domain: "example.com"},
		{ID: 3, Domain: "mx.example.com", ParentID: sql.NullInt64{Int64: 2, Valid: true}},
	}, "saved", "", "1")
	for _, want := range []string{
		"Blocked email domains",
		`href="/admin/email_domain_blocks/new"`,
		`action="/admin/email_domain_blocks/batch"`,
		`name="form_email_domain_block_batch[email_domain_block_ids][]" value="2"`,
		"example.com",
		"mx.example.com",
		"#2",
		"saved",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("email domain blocks html missing %q: %s", want, html)
		}
	}
}

func TestAdminEmailDomainBlocksHTMLRendersRailsPaginationLinks(t *testing.T) {
	rows := make([]models.EmailDomainBlock, adminRailsDefaultPageSize)
	for i := range rows {
		rows[i] = models.EmailDomainBlock{ID: int64(i + 1), Domain: "example.com"}
	}
	html := adminEmailDomainBlocksHTML(rows, "", "", "2")
	for _, want := range []string{
		`href="/admin/email_domain_blocks?page=1"`,
		`href="/admin/email_domain_blocks?page=3"`,
		"Previous",
		"Next",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("email domain blocks html missing pagination %q: %s", want, html)
		}
	}
}

func TestAdminEmailDomainBlockRowsWithChildrenKeepsRailsParentPages(t *testing.T) {
	rows := adminEmailDomainBlockRowsWithChildren(
		[]models.EmailDomainBlock{{ID: 5, Domain: "example.com"}, {ID: 3, Domain: "example.net"}},
		[]models.EmailDomainBlock{
			{ID: 4, Domain: "mx.example.net", ParentID: sql.NullInt64{Int64: 3, Valid: true}},
			{ID: 6, Domain: "mx.example.com", ParentID: sql.NullInt64{Int64: 5, Valid: true}},
		},
	)
	if len(rows) != 4 || rows[0].ID != 5 || rows[1].ID != 6 || rows[2].ID != 3 || rows[3].ID != 4 {
		t.Fatalf("rows = %#v", rows)
	}
}

func TestAdminEmailDomainBlockModelsUseRailsPageSizeAndOffset(t *testing.T) {
	src, err := os.ReadFile("admin_email_domain_blocks_web.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		functionName string
		want         string
	}{
		{"adminEmailDomainBlocksPage", "s.adminEmailDomainBlockModels(c)"},
		{"adminEmailDomainBlocksPage", "adminEmailDomainBlocksHTML(rows, c.QueryParam(\"notice\"), c.QueryParam(\"error\"), adminTrendsPageValue(c), s.webLocale(c, user))"},
		{"adminEmailDomainBlockModels", "Where(\"parent_id IS NULL\")"},
		{"adminEmailDomainBlockModels", "Offset(adminRailsPageOffset(c))"},
		{"adminEmailDomainBlockModels", "Limit(adminRailsDefaultPageSize)"},
		{"adminEmailDomainBlockModels", "Where(\"parent_id IN ?\", parentIDs)"},
	} {
		if !functionBodyContains(t, src, check.functionName, check.want) {
			t.Fatalf("%s missing %q", check.functionName, check.want)
		}
	}
}

func TestAdminEmailDomainBlockMessagesResolveJapaneseLocale(t *testing.T) {
	for _, check := range []struct {
		key       string
		fallback  string
		forbidden string
		want      string
	}{
		{"created_msg", "Successfully blocked e-mail domain", "Successfully", "メールドメイン"},
		{"deleted_msg", "E-mail domain blocks deleted", "E-mail domain", "削除"},
		{"no_email_domain_block_selected", "No e-mail domain blocks were changed as none were selected", "No e-mail", "選択"},
		{"errors.domain_invalid", "Domain is invalid", "Domain is invalid", "不正"},
		{"errors.taken", "Domain has already been taken", "Domain has already", "すでに"},
	} {
		got := adminEmailDomainBlockMessage("ja", check.key, check.fallback)
		if strings.Contains(got, check.forbidden) || !strings.Contains(got, check.want) {
			t.Fatalf("adminEmailDomainBlockMessage(%q) = %q", check.key, got)
		}
	}
}

func TestAdminEmailDomainBlockFormHTMLIncludesRailsFields(t *testing.T) {
	html := adminEmailDomainBlockFormHTML(adminEmailDomainBlockForm{Domain: "example.com", OtherDomains: []string{"mx.example.com"}}, "bad", "en")
	for _, want := range []string{
		"Add new",
		`action="/admin/email_domain_blocks"`,
		`name="email_domain_block[domain]" value="example.com"`,
		`name="email_domain_block[other_domains][]"`,
		"mx.example.com",
		`name="resolve" value="1"`,
		"bad",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("email domain block form html missing %q: %s", want, html)
		}
	}
}

func TestAdminEmailDomainBlockResolvedFormHTMLIncludesMXChoices(t *testing.T) {
	html := adminEmailDomainBlockFormHTML(adminEmailDomainBlockForm{Domain: "example.com", OtherDomains: []string{"mx.example.com"}, Resolved: true}, "", "en")
	for _, want := range []string{
		`name="email_domain_block[domain]" value="example.com"`,
		`readonly`,
		"Resolved MX records",
		`name="email_domain_block[other_domains][]" value="mx.example.com" checked`,
		`name="save" value="1"`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("resolved email domain block form html missing %q: %s", want, html)
		}
	}
}
