package api

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
)

func TestAdminExportDomainBlocksRequiresWebSession(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/export_domain_blocks/export.csv", nil)
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	want := "/auth/sign_in?redirect_to=" + url.QueryEscape("/admin/export_domain_blocks/export.csv")
	if got := rec.Header().Get("Location"); got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}

	for _, path := range []string{
		"/admin/export_domain_blocks/export",
		"/admin/export_domain_allows/export",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		s.echo.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want 404 for Rails CSV-constrained route", path, rec.Code)
		}
	}
}

func TestParseAdminDomainCSVRowsWithHeaders(t *testing.T) {
	rows, err := parseAdminDomainCSVRows(strings.NewReader("#domain,#severity,#reject_media\n Example.COM ,suspend,true\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0]["#domain"] != "Example.COM" || rows[0]["#severity"] != "suspend" || rows[0]["#reject_media"] != "true" {
		t.Fatalf("rows = %#v", rows)
	}
}

func TestParseAdminDomainCSVRowsWithoutHeaders(t *testing.T) {
	rows, err := parseAdminDomainCSVRows(strings.NewReader("example.com\nremote.example\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0]["#domain"] != "example.com" || rows[1]["#domain"] != "remote.example" {
		t.Fatalf("rows = %#v", rows)
	}
}

func TestAdminDomainExportFormHTMLIncludesRailsFieldName(t *testing.T) {
	html := adminDomainExportFormHTML("Import domain blocks", "/admin/export_domain_blocks/import", "bad")
	for _, want := range []string{
		"Import domain blocks",
		`action="/admin/export_domain_blocks/import"`,
		`enctype="multipart/form-data"`,
		`name="admin_import[data]"`,
		"bad",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("export form html missing %q: %s", want, html)
		}
	}
}

func TestParseAdminDomainImportUploadRequiresRailsRoot(t *testing.T) {
	e := echo.New()
	for _, tc := range []struct {
		field   string
		wantErr bool
	}{
		{field: "admin_import[data]"},
		{field: "data", wantErr: true},
	} {
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		part, err := writer.CreateFormFile(tc.field, "domain_blocks.csv")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write([]byte("#domain,#severity\nexample.com,suspend\n")); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}

		req := httptest.NewRequest(http.MethodPost, "/admin/export_domain_blocks/import", &body)
		req.Header.Set("Content-Type", writer.FormDataContentType())
		c := echo.NewContext(req, httptest.NewRecorder(), e)

		rows, _, err := parseAdminDomainImportUploadWithName(c)
		if tc.wantErr {
			if err == nil || err.Error() != "No file selected" {
				t.Fatalf("%s err = %v, want No file selected", tc.field, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s err = %v", tc.field, err)
		}
		if len(rows) != 1 || rows[0]["#domain"] != "example.com" {
			t.Fatalf("%s rows = %#v", tc.field, rows)
		}
	}
}

func TestAdminDomainExportImportRedirectErrorsUseLocaleKeys(t *testing.T) {
	src, err := os.ReadFile("admin_domain_exports.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		fn   string
		want string
	}{
		{"importAdminDomainAllowsCSV", `locale := s.webLocale(c, user)`},
		{"importAdminDomainAllowsCSV", `adminDomainExportAllowMessage(locale, "errors.database_unavailable", "DATABASE_URL is not set")`},
		{"importAdminDomainBlocksCSV", `locale := s.webLocale(c, user)`},
		{"importAdminDomainBlocksCSV", `adminDomainExportBlockMessage(locale, "errors.database_unavailable", "DATABASE_URL is not set")`},
	} {
		if !functionBodyContains(t, src, check.fn, check.want) {
			t.Fatalf("%s missing localized redirect fragment %q", check.fn, check.want)
		}
	}
	for _, forbidden := range []struct {
		fn   string
		text string
	}{
		{"importAdminDomainAllowsCSV", `QueryEscape("DATABASE_URL is not set")`},
		{"importAdminDomainBlocksCSV", `QueryEscape("DATABASE_URL is not set")`},
	} {
		if functionBodyContains(t, src, forbidden.fn, forbidden.text) {
			t.Fatalf("%s still contains non-localized DB redirect literal %q", forbidden.fn, forbidden.text)
		}
	}
}

func TestAdminDomainExportMessagesResolveJapaneseLocale(t *testing.T) {
	if got := adminDomainExportAllowMessage("en", "errors.database_unavailable", "DATABASE_URL is not set"); got != "DATABASE_URL is not set" {
		t.Fatalf("allow en database message = %q", got)
	}
	if got := adminDomainExportAllowMessage("ja", "errors.database_unavailable", "DATABASE_URL is not set"); got == "DATABASE_URL is not set" || !strings.Contains(got, "DATABASE_URL") {
		t.Fatalf("allow ja database message = %q", got)
	}
	if got := adminDomainExportBlockMessage("en", "errors.database_unavailable", "DATABASE_URL is not set"); got != "DATABASE_URL is not set" {
		t.Fatalf("block en database message = %q", got)
	}
	if got := adminDomainExportBlockMessage("ja", "errors.database_unavailable", "DATABASE_URL is not set"); got == "DATABASE_URL is not set" || !strings.Contains(got, "DATABASE_URL") {
		t.Fatalf("block ja database message = %q", got)
	}
}
