package api

import (
	"os"
	"strings"
	"testing"
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
		`requireAccountScope(c, "write", "write:accounts")`,
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
