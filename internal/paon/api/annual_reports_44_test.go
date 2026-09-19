package api

import (
	"os"
	"strings"
	"testing"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestMastodon44AnnualReportShowAndReadRoutes(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range []string{
		`e.GET("/api/v1/annual_reports/:id", s.annualReport)`,
		`e.POST("/api/v1/annual_reports/:id/read", s.readAnnualReport)`,
	} {
		if !strings.Contains(string(src), route) {
			t.Fatalf("annual report route missing: %s", route)
		}
	}
}

func TestAnnualReportShowScopesYearToCurrentAccountAndHydratesReferences(t *testing.T) {
	src, err := os.ReadFile("rest_43.go")
	if err != nil {
		t.Fatal(err)
	}
	body := functionBody(t, src, "annualReport")
	for _, want := range []string{
		`requireAccountScope(c, "read", "read:accounts")`,
		`Where("account_id = ? AND year = ?", account.ID, year)`,
		`annualReportReferencedIDs`,
		`annualReportAccounts`,
		`annualReportStatuses`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("annualReport missing %q", want)
		}
	}
}

func TestMastodon46SchemaTwoAnnualReportReferencesItsOwner(t *testing.T) {
	accounts, statuses := annualReportReferencedIDs([]models.GeneratedAnnualReport{
		{AccountID: 42, SchemaVersion: 2, Data: models.JSONValue(`{"top_statuses":{"by_reblogs":"99","by_favourites":null,"by_replies":null}}`)},
	})
	if len(accounts) != 1 || accounts[0] != 42 {
		t.Fatalf("schema 2 account ids = %#v", accounts)
	}
	if len(statuses) != 1 || statuses[0] != 99 {
		t.Fatalf("schema 2 status ids = %#v", statuses)
	}
}
