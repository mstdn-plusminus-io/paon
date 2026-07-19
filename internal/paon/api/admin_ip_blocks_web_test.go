package api

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestAdminIPBlocksRequiresWebSession(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/ip_blocks?page=2", nil)
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	want := "/auth/sign_in?redirect_to=" + url.QueryEscape("/admin/ip_blocks?page=2")
	if got := rec.Header().Get("Location"); got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}
}

func TestParseAdminIPBlockIDs(t *testing.T) {
	form := url.Values{}
	form.Add("form_ip_block_batch[ip_block_ids][]", "1")
	form.Add("form_ip_block_batch[ip_block_ids][]", "bad")
	form.Add("form_ip_block_batch[ip_block_ids][]", "2")

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/admin/ip_blocks/batch", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	got := parseAdminIPBlockIDs(nil, c)
	if len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("ids = %#v", got)
	}
}

func TestAdminBatchFormParamExistsMatchesRailsParamPresence(t *testing.T) {
	form := url.Values{}
	form.Set("save", "")
	form.Set("delete", "1")

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/admin/ip_blocks/batch", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	if !adminBatchFormParamExists(c, "save") {
		t.Fatal("save= must count as a submitted Rails param")
	}
	if !adminBatchFormParamExists(c, "delete") {
		t.Fatal("delete=1 must count as a submitted Rails param")
	}
	if adminBatchFormParamExists(c, "confirm") {
		t.Fatal("missing confirm must not count as a submitted Rails param")
	}
}

func TestAdminIPBlockSeverityHelpers(t *testing.T) {
	if !adminIPBlockSeverityAllowed("no_access") || adminIPBlockSeverityAllowed("bad") {
		t.Fatal("unexpected severity allowlist result")
	}
	if got := adminIPBlockSeverityLabel(5000); got != "sign_up_requires_approval" {
		t.Fatalf("severity = %q", got)
	}
}

func TestAdminIPBlocksHTMLIncludesRailsFields(t *testing.T) {
	html := adminIPBlocksHTML([]models.IPBlock{{ID: 2, IP: "192.0.2.0/24", Severity: 9999, Comment: "Spam", ExpiresAt: sql.NullTime{Time: time.Date(2026, 6, 19, 1, 0, 0, 0, time.UTC), Valid: true}}}, "saved", "", "1")
	for _, want := range []string{
		"IP rules",
		`href="/admin/ip_blocks/new"`,
		`action="/admin/ip_blocks/batch"`,
		`name="form_ip_block_batch[ip_block_ids][]" value="2"`,
		"192.0.2.0/24",
		"Block access",
		`class="batch-table__row"`,
		"Spam",
		"saved",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("ip blocks html missing %q: %s", want, html)
		}
	}
}

func TestAdminIPBlocksHTMLRendersRailsPaginationLinks(t *testing.T) {
	rows := make([]models.IPBlock, adminRailsDefaultPageSize)
	for i := range rows {
		rows[i] = models.IPBlock{ID: int64(i + 1), IP: "192.0.2.0/24", Severity: 9999}
	}
	html := adminIPBlocksHTML(rows, "", "", "2")
	for _, want := range []string{
		`href="/admin/ip_blocks?page=1"`,
		`href="/admin/ip_blocks?page=3"`,
		"Previous",
		"Next",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("ip blocks html missing pagination %q: %s", want, html)
		}
	}
}

func TestAdminIPBlockModelsUseRailsPageSizeAndOffset(t *testing.T) {
	src, err := os.ReadFile("admin_ip_blocks_web.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		functionName string
		want         string
	}{
		{"adminIPBlocksPage", "s.adminIPBlockModels(c)"},
		{"adminIPBlocksPage", "adminIPBlocksHTML(rows, c.QueryParam(\"notice\"), c.QueryParam(\"error\"), adminTrendsPageValue(c), s.webLocale(c, user))"},
		{"adminIPBlockModels", "Offset(adminRailsPageOffset(c))"},
		{"adminIPBlockModels", "Limit(adminRailsDefaultPageSize)"},
	} {
		if !functionBodyContains(t, src, check.functionName, check.want) {
			t.Fatalf("%s missing %q", check.functionName, check.want)
		}
	}
}

func TestAdminIPBlockMessagesResolveJapaneseLocale(t *testing.T) {
	for _, check := range []struct {
		key       string
		fallback  string
		forbidden string
		want      string
	}{
		{"created_msg", "Successfully added new IP rule", "Successfully", "IPルール"},
		{"deleted_msg", "IP blocks deleted", "IP blocks deleted", "削除"},
		{"no_ip_block_selected", "No IP rules were changed as none were selected", "No IP rules", "選択"},
		{"errors.ip_invalid", "IP is invalid", "IP is invalid", "不正"},
		{"errors.taken", "IP has already been taken", "IP has already", "すでに"},
	} {
		got := adminIPBlockMessage("ja", check.key, check.fallback)
		if strings.Contains(got, check.forbidden) || !strings.Contains(got, check.want) {
			t.Fatalf("adminIPBlockMessage(%q) = %q", check.key, got)
		}
	}
}

func TestAdminIPBlockFormHTMLIncludesRailsFields(t *testing.T) {
	html := adminIPBlockFormHTML(adminIPBlockForm{IP: "192.0.2.0/24", Severity: "no_access", Comment: "Spam"}, "bad", "en")
	for _, want := range []string{
		"Create rule",
		`action="/admin/ip_blocks"`,
		`name="ip_block[ip]" value="192.0.2.0/24"`,
		`name="ip_block[expires_in]"`,
		`name="ip_block[severity]"`,
		`value="no_access" selected`,
		`name="ip_block[comment]" value="Spam"`,
		"bad",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("ip block form html missing %q: %s", want, html)
		}
	}
}
