package api

import (
	"os"
	"testing"
	"time"
)

func TestStatusVacuumConstantsMatchRailsCadence(t *testing.T) {
	if statusVacuumWorkerInterval != 24*time.Hour {
		t.Fatalf("statusVacuumWorkerInterval = %s", statusVacuumWorkerInterval)
	}
	if statusVacuumBatchSize != 1000 {
		t.Fatalf("statusVacuumBatchSize = %d", statusVacuumBatchSize)
	}
}

func TestStatusVacuumWorkerUsesRailsStatusesVacuumShape(t *testing.T) {
	src, err := os.ReadFile("status_vacuum_worker.go")
	if err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		functionName string
		want         string
	}{
		{"runStatusVacuumWorker", `s.vacuumRemoteStatuses(ctx, now.UTC())`},
		{"vacuumRemoteStatuses", `days, ok := s.contentCacheRetentionDays()`},
		{"vacuumRemoteStatuses", `cutoffID := mastodonSnowflakeIDAt(now.Add(-time.Duration(days)*24*time.Hour), false)`},
		{"vacuumRemoteStatuses", `Joins("JOIN accounts ON accounts.id = statuses.account_id")`},
		{"vacuumRemoteStatuses", `Where("accounts.domain IS NOT NULL AND accounts.domain <> ''")`},
		{"vacuumRemoteStatuses", `Where("statuses.deleted_at IS NULL")`},
		{"vacuumRemoteStatuses", `Where("statuses.id < ?", cutoffID)`},
		{"vacuumRemoteStatuses", `Limit(statusVacuumBatchSize)`},
		{"vacuumRemoteStatuses", `unlinkDirectStatusFromConversations(ctx, tx, status, now)`},
		{"vacuumRemoteStatuses", `Delete(&models.Status{}, ids)`},
		{"unlinkDirectStatusFromConversations", `status.Visibility != 3`},
		{"unlinkDirectStatusFromConversations", `array_remove(status_ids, ?)`},
		{"unlinkDirectStatusFromConversations", `DELETE FROM account_conversations`},
	}
	for _, check := range checks {
		if !functionBodyContains(t, src, check.functionName, check.want) {
			t.Fatalf("%s missing %q", check.functionName, check.want)
		}
	}
	startup, err := os.ReadFile("activitypub_retry.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, startup, "StartBackgroundWorkers", "workers.Go(ctx, s.runStatusVacuumWorker)") {
		t.Fatal("StartBackgroundWorkers does not start status vacuum worker")
	}
}
