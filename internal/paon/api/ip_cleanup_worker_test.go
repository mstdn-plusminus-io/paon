package api

import (
	"os"
	"testing"
	"time"
)

func TestIPCleanupConstantsMatchRailsScheduler(t *testing.T) {
	if ipCleanupWorkerInterval != 24*time.Hour {
		t.Fatalf("ipCleanupWorkerInterval = %s", ipCleanupWorkerInterval)
	}
	if defaultIPRetentionSeconds != 31556952 || defaultSessionRetentionSeconds != 31556952 {
		t.Fatalf("default retention seconds = %d/%d", defaultIPRetentionSeconds, defaultSessionRetentionSeconds)
	}
}

func TestIPCleanupWorkerUsesRailsSchedulerShape(t *testing.T) {
	src, err := os.ReadFile("ip_cleanup_worker.go")
	if err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		functionName string
		want         string
	}{
		{"runIPCleanupWorker", `s.cleanupIPData(ctx, now.UTC())`},
		{"cleanupIPData", `s.cleanupExpiredSessionActivations(ctx, now.Add(-sessionRetentionPeriod()))`},
		{"cleanupIPData", `ipCutoff := now.Add(-ipRetentionPeriod())`},
		{"cleanupIPData", `s.nullOldSessionActivationIPs(ctx, ipCutoff)`},
		{"cleanupIPData", `s.nullOldUserSignUpIPs(ctx, ipCutoff)`},
		{"cleanupIPData", `s.deleteOldLoginActivities(ctx, ipCutoff)`},
		{"cleanupIPData", `s.nullOldOAuthAccessTokenIPs(ctx, ipCutoff)`},
		{"cleanupIPData", `s.deleteExpiredIPBlocks(ctx, now)`},
		{"cleanupExpiredSessionActivations", `DELETE FROM web_push_subscriptions`},
		{"cleanupExpiredSessionActivations", `DELETE FROM oauth_access_tokens`},
		{"cleanupExpiredSessionActivations", `DELETE FROM session_activations WHERE updated_at < ?`},
		{"nullOldSessionActivationIPs", `UPDATE session_activations SET ip = NULL WHERE updated_at < ?`},
		{"nullOldUserSignUpIPs", `UPDATE users SET sign_up_ip = NULL WHERE current_sign_in_at < ?`},
		{"deleteOldLoginActivities", `DELETE FROM login_activities WHERE created_at < ?`},
		{"nullOldOAuthAccessTokenIPs", `UPDATE oauth_access_tokens SET last_used_ip = NULL WHERE last_used_at < ?`},
		{"deleteExpiredIPBlocks", `DELETE FROM ip_blocks WHERE expires_at IS NOT NULL AND expires_at < ?`},
		{"deleteExpiredIPBlocks", `s.invalidateIPBlockCache(ctx)`},
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
	if !functionBodyContains(t, startup, "StartBackgroundWorkers", "workers.Go(ctx, s.runIPCleanupWorker)") {
		t.Fatal("StartBackgroundWorkers does not start IP cleanup worker")
	}
}
