package api

import (
	"database/sql"
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

func TestAdminDomainBlockNewRequiresWebSession(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/domain_blocks/new?_domain=example.com", nil)
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	want := "/auth/sign_in?redirect_to=" + url.QueryEscape("/admin/domain_blocks/new?_domain=example.com")
	if got := rec.Header().Get("Location"); got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}
}

func TestAdminDomainBlockHelpers(t *testing.T) {
	if !adminDomainBlockSeverityAllowed("silence") || adminDomainBlockSeverityAllowed("bad") {
		t.Fatal("unexpected severity allowlist result")
	}
	if got := adminDomainBlockSeverityLabel(models.DomainBlockSeverity(1)); got != "suspend" {
		t.Fatalf("severity = %q", got)
	}
	block := models.DomainBlock{Domain: "example.com", Severity: models.DomainBlockSeverity(2), RejectMedia: true, PrivateComment: sql.NullString{String: "private", Valid: true}}
	form := adminDomainBlockFormFromModel(block)
	if form.Domain != "example.com" || form.Severity != "noop" || !form.RejectMedia || form.PrivateComment != "private" {
		t.Fatalf("form = %#v", form)
	}
}

func TestAdminDomainBlockFormHTMLIncludesRailsFields(t *testing.T) {
	html := adminDomainBlockFormHTML("Edit domain block", "/admin/domain_blocks/2", "put", adminDomainBlockForm{Domain: "example.com", Severity: "suspend", RejectMedia: true, PrivateComment: "Private"}, true, "Save changes", "bad")
	for _, want := range []string{
		"Edit domain block",
		`action="/admin/domain_blocks/2"`,
		`name="_method" value="put"`,
		`name="domain_block[domain]" value="example.com"`,
		`readonly`,
		`name="domain_block[severity]"`,
		`value="suspend" selected`,
		`name="domain_block[reject_media]" value="1" checked`,
		`name="domain_block[private_comment]" value="Private"`,
		"Save changes",
		"bad",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("domain block form html missing %q: %s", want, html)
		}
	}
	newHTML := adminDomainBlockFormHTML("New domain block", "/admin/domain_blocks", "", adminDomainBlockForm{Severity: "silence"}, false, "Create block", "", "en")
	if !strings.Contains(newHTML, "Create block") || strings.Contains(newHTML, "Save changes") {
		t.Fatalf("domain block new submit label mismatch: %s", newHTML)
	}
}

func TestAdminDomainBlockConfirmHTMLIncludesHiddenFields(t *testing.T) {
	html := adminDomainBlockConfirmHTML(adminDomainBlockForm{Domain: "example.com", Severity: "suspend", RejectMedia: true, PublicComment: "Public"}, "/admin/domain_blocks", "en")
	for _, want := range []string{
		"Confirm domain block for example.com",
		`action="/admin/domain_blocks"`,
		`name="domain_block[domain]" value="example.com"`,
		`name="domain_block[severity]" value="suspend"`,
		`name="domain_block[reject_media]" value="1"`,
		`name="domain_block[public_comment]" value="Public"`,
		`name="confirm" value="1"`,
		`You are about to suspend <strong>example.com</strong>`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("domain block confirm html missing %q: %s", want, html)
		}
	}
	if strings.Contains(html, `&lt;strong&gt;example.com&lt;/strong&gt;`) {
		t.Fatalf("domain block confirm html escaped Rails HTML description: %s", html)
	}
	escaped := adminDomainBlockConfirmHTML(adminDomainBlockForm{Domain: `<script>alert(1)</script>`, Severity: "suspend"}, "/admin/domain_blocks", "en")
	if !strings.Contains(escaped, `<strong>&lt;script&gt;alert(1)&lt;/script&gt;</strong>`) || strings.Contains(escaped, `<strong><script>`) {
		t.Fatalf("domain block confirm html did not escape interpolated domain: %s", escaped)
	}
}

func TestAdminDomainBlockMessagesResolveJapaneseLocale(t *testing.T) {
	for _, check := range []struct {
		key       string
		fallback  string
		forbidden string
		want      string
	}{
		{"created_msg", "Domain block created", "Domain block", "ドメインブロック"},
		{"destroyed_msg", "Domain block removed", "Domain block", "ドメインブロック"},
		{"no_domain_block_selected", "No domain blocks were changed as none were selected", "No domain", "選択"},
		{"errors.domain_invalid", "Domain is invalid", "Domain is invalid", "不正"},
		{"errors.taken", "Domain has already been taken", "Domain has already", "すでに"},
	} {
		got := adminDomainBlockMessage("ja", check.key, check.fallback)
		if strings.Contains(got, check.forbidden) || !strings.Contains(got, check.want) {
			t.Fatalf("adminDomainBlockMessage(%q) = %q", check.key, got)
		}
	}
}

func TestParseAdminDomainBlockBatchFormsAcceptsRailsImportFields(t *testing.T) {
	form := url.Values{}
	form.Set("save", "1")
	form.Set("form_domain_block_batch[domain_blocks_attributes][0][enabled]", "1")
	form.Set("form_domain_block_batch[domain_blocks_attributes][0][domain]", " Example.COM ")
	form.Set("form_domain_block_batch[domain_blocks_attributes][0][severity]", "suspend")
	form.Set("form_domain_block_batch[domain_blocks_attributes][0][reject_media]", "1")
	form.Set("form_domain_block_batch[domain_blocks_attributes][0][reject_reports]", "0")
	form.Set("form_domain_block_batch[domain_blocks_attributes][0][obfuscate]", "1")
	form.Set("form_domain_block_batch[domain_blocks_attributes][0][private_comment]", " Private ")
	form.Set("form_domain_block_batch[domain_blocks_attributes][0][public_comment]", " Public ")
	form.Set("form_domain_block_batch[domain_blocks_attributes][1][enabled]", "0")
	form.Set("form_domain_block_batch[domain_blocks_attributes][1][domain]", "ignored.example")

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/admin/domain_blocks/batch", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	got, err := parseAdminDomainBlockBatchForms(c)
	if err != nil {
		t.Fatal(err)
	}
	want := []adminDomainBlockForm{{Domain: "Example.COM", Severity: "suspend", RejectMedia: true, RejectReports: false, PrivateComment: " Private ", PublicComment: " Public ", Obfuscate: true}}
	if len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("batch forms = %#v, want %#v", got, want)
	}

	src, err := os.ReadFile("admin_domain_blocks_web.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`PrivateComment: row.values["private_comment"]`,
		`PublicComment:  row.values["public_comment"]`,
	} {
		if !functionBodyContains(t, src, "parseAdminDomainBlockBatchForms", want) {
			t.Fatalf("parseAdminDomainBlockBatchForms missing Rails raw comment fragment %q", want)
		}
	}
}

func TestAdminDomainBlockBatchRouteRequiresWebSession(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/admin/domain_blocks/batch", strings.NewReader("save=1"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	want := "/auth/sign_in?redirect_to=" + url.QueryEscape("/admin/domain_blocks/batch")
	if got := rec.Header().Get("Location"); got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}
}

func TestAdminDomainBlockRawMemberPostIsNotFound(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/admin/domain_blocks/5", strings.NewReader(""))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
}
