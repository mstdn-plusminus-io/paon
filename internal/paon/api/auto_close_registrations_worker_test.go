package api

import (
	"os"
	"testing"
	"time"
)

func TestAutoCloseRegistrationsConstantsMatchRailsScheduler(t *testing.T) {
	if autoCloseRegistrationsWorkerInterval != time.Hour {
		t.Fatalf("autoCloseRegistrationsWorkerInterval = %s", autoCloseRegistrationsWorkerInterval)
	}
	if autoCloseRegistrationsSignInUpdateFrequency != 24*time.Hour {
		t.Fatalf("autoCloseRegistrationsSignInUpdateFrequency = %s", autoCloseRegistrationsSignInUpdateFrequency)
	}
	if autoCloseRegistrationsModeratorActiveThreshold != 8*24*time.Hour {
		t.Fatalf("autoCloseRegistrationsModeratorActiveThreshold = %s", autoCloseRegistrationsModeratorActiveThreshold)
	}
}

func TestAutoCloseRegistrationsWorkerUsesRailsSchedulerShape(t *testing.T) {
	src, err := os.ReadFile("auto_close_registrations_worker.go")
	if err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		functionName string
		want         string
	}{
		{"runAutoCloseRegistrationsWorker", `s.autoCloseRegistrations(ctx, now.UTC())`},
		{"autoCloseRegistrations", `s.autoCloseRegistrationsDisabledByConfig()`},
		{"autoCloseRegistrations", `pg_advisory_xact_lock`},
		{"autoCloseRegistrations", `normalizeRegistrationsMode(setting.Value.String) != "open"`},
		{"autoCloseRegistrations", `now.Add(-autoCloseRegistrationsModeratorActiveThreshold)`},
		{"autoCloseRegistrations", `"value":      "approved"`},
		{"autoCloseRegistrations", `s.sendAutoCloseRegistrationsMails()`},
		{"hasActiveRegistrationModerator", `rolePermissionManageReports`},
		{"hasActiveRegistrationModerator", `Where("current_sign_in_at >= ?", cutoff)`},
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
	if !functionBodyContains(t, startup, "StartBackgroundWorkers", "workers.Go(ctx, s.runAutoCloseRegistrationsWorker)") {
		t.Fatal("StartBackgroundWorkers does not start auto-close registrations worker")
	}
}
