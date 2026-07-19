package api

import (
	"database/sql"
	"encoding/csv"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
)

func TestExportAccountAddressMatchesRailsCSVHandle(t *testing.T) {
	s := &Server{cfg: config.Config{LocalDomain: "social.example"}}
	if got := s.exportAccountAddress(exportAccountRow{Username: "alice"}); got != "alice@social.example" {
		t.Fatalf("local address = %q", got)
	}
	if got := s.exportAccountAddress(exportAccountRow{Username: "bob", Domain: sql.NullString{String: "remote.example", Valid: true}}); got != "bob@remote.example" {
		t.Fatalf("remote address = %q", got)
	}
}

func TestExportStatusURIUsesActivityPubURI(t *testing.T) {
	s := &Server{cfg: config.Config{Scheme: "https", WebDomain: "social.example"}}
	local := exportBookmarkRow{StatusID: 123, AccountUsername: "alice"}
	if got := s.exportStatusURI(local); got != "https://social.example/users/alice/statuses/123" {
		t.Fatalf("local status URI = %q", got)
	}

	remote := exportBookmarkRow{
		StatusID:        456,
		StatusURI:       sql.NullString{String: "https://remote.example/objects/456", Valid: true},
		StatusURL:       sql.NullString{String: "https://remote.example/@bob/456", Valid: true},
		AccountUsername: "bob",
		AccountDomain:   sql.NullString{String: "remote.example", Valid: true},
	}
	if got := s.exportStatusURI(remote); got != "https://remote.example/objects/456" {
		t.Fatalf("remote status URI = %q", got)
	}
}

func TestCSVBytesSetsAttachmentAndHeaders(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/settings/exports/follows.csv", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())

	data := string(csvBytes("follows.csv", [][]string{{"Account address", "Show boosts"}}, func(w *csv.Writer) error {
		return w.Write([]string{"alice@example.test", "true"})
	}, c))

	if rec.Header().Get("Content-Disposition") != `attachment; filename="follows.csv"` {
		t.Fatalf("content disposition = %q", rec.Header().Get("Content-Disposition"))
	}
	if !strings.Contains(data, "Account address,Show boosts\nalice@example.test,true\n") {
		t.Fatalf("csv data = %q", data)
	}
}

func TestDomainBlockExportNormalizesLegacyDomainRows(t *testing.T) {
	src, err := os.ReadFile("exports.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, src, "exportDomainBlocksCSVBytes", `accountDomainBlockDisplayDomain(row.Domain)`) {
		t.Fatal("domain block exports must normalize legacy account_domain_blocks rows like Rails domain normalization")
	}
}

func TestSettingsExportRequiresWebAuthentication(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodGet, "/settings/exports/follows.csv", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())

	if err := s.exportFollowsCSV(c); err != nil {
		t.Fatal(err)
	}
	want := "/auth/sign_in?redirect_to=" + url.QueryEscape("/settings/exports/follows.csv")
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != want {
		t.Fatalf("status = %d location = %q", rec.Code, rec.Header().Get("Location"))
	}
}
