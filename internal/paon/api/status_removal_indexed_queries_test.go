package api

import (
	"os"
	"strings"
	"testing"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestRemovalReportFilteringKeepsAccountBoundaries(t *testing.T) {
	candidates := map[int64]map[int64]struct{}{
		10: {1: {}, 2: {}},
		20: {3: {}},
	}
	reported := make(map[int64]struct{})
	markReportedStatusIDs(reported, candidates, []models.Report{
		{TargetAccountID: 10, StatusIDs: models.Int64Array{2, 3}},
		{TargetAccountID: 20, StatusIDs: models.Int64Array{1, 3}},
	})
	if statusRemovalReported(reported, 1) {
		t.Fatal("status 1 must not be protected by another account's report")
	}
	for _, statusID := range []int64{2, 3} {
		if !statusRemovalReported(reported, statusID) {
			t.Fatalf("status %d must remain protected by its target account report", statusID)
		}
	}
	statuses := []models.Status{{ID: 1, AccountID: 10}, {ID: 2, AccountID: 10}, {ID: 3, AccountID: 20}}
	ids := unreportedStatusIDs(statuses, reported)
	if len(ids) != 1 || ids[0] != 1 {
		t.Fatalf("unreported IDs = %#v, want [1]", ids)
	}
}

func TestRemovalQueriesUseIndexedLookupBoundaries(t *testing.T) {
	serverSource, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	cleanupSource, err := os.ReadFile("user_cleanup_worker.go")
	if err != nil {
		t.Fatal(err)
	}
	reportSource, err := os.ReadFile("status_removal_reports.go")
	if err != nil {
		t.Fatal(err)
	}
	for file, source := range map[string][]byte{
		"server.go":              serverSource,
		"user_cleanup_worker.go": cleanupSource,
	} {
		if strings.Contains(string(source), "ANY(reports.status_ids)") || strings.Contains(string(source), "ANY(status_ids)") {
			t.Fatalf("%s must not use correlated report array membership", file)
		}
	}
	discardBody := functionBody(t, serverSource, "discardStatusRowsForRemoval")
	for _, want := range []string{
		`Where("id = ? AND deleted_at IS NULL", statusID)`,
		`Where("reblog_of_id = ? AND deleted_at IS NULL", statusID)`,
		`unresolvedReportedStatusIDs(tx, candidates)`,
	} {
		if !sourceContains([]byte(discardBody), want) {
			t.Fatalf("discardStatusRowsForRemoval missing indexed boundary %q", want)
		}
	}
	if strings.Contains(discardBody, "id = ? OR reblog_of_id = ?") {
		t.Fatal("discardStatusRowsForRemoval must not combine primary-key and reblog lookups with OR")
	}
	for _, function := range []string{"discardedStatusAndUnreportedReblogIDs", "discardedUnreportedReblogIDs"} {
		body := functionBody(t, cleanupSource, function)
		if !sourceContains([]byte(body), `Where("reblog_of_id = ?`) {
			t.Fatalf("%s must start reblog lookup from reblog_of_id", function)
		}
		if strings.Contains(body, "NOT EXISTS") || strings.Contains(body, " ANY(") {
			t.Fatalf("%s must filter unresolved reports after indexed lookup", function)
		}
	}
	reportBody := functionBody(t, reportSource, "unresolvedReportedStatusIDs")
	if !sourceContains([]byte(reportBody), `Where("target_account_id IN ? AND action_taken_at IS NULL", accountIDs[start:end])`) {
		t.Fatal("unresolved reports must use the target_account_id index in bounded batches")
	}
}
