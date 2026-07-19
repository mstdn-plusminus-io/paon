package api

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestOAuthVacuumConstantsMatchRailsVacuumScheduler(t *testing.T) {
	if oauthVacuumWorkerInterval != 24*time.Hour {
		t.Fatalf("oauthVacuumWorkerInterval = %s", oauthVacuumWorkerInterval)
	}
	if oauthVacuumBatchSize != 1000 {
		t.Fatalf("oauthVacuumBatchSize = %d", oauthVacuumBatchSize)
	}
	for name, query := range map[string]string{
		"tokens": expiredOAuthAccessTokensSQL,
		"grants": expiredOAuthAccessGrantsSQL,
	} {
		for _, want := range []string{
			"expires_in IS NOT NULL",
			"created_at + make_interval(secs => expires_in) < ?",
			"revoked_at IS NOT NULL AND revoked_at < ?",
		} {
			if !strings.Contains(query, want) {
				t.Fatalf("%s SQL missing %q: %s", name, want, query)
			}
		}
	}
}

func TestOAuthVacuumWorkerUsesRailsDeleteShape(t *testing.T) {
	src, err := os.ReadFile("oauth_vacuum_worker.go")
	if err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		functionName string
		want         string
	}{
		{"runOAuthVacuumWorker", `s.vacuumExpiredOAuth(ctx, now.UTC())`},
		{"vacuumExpiredOAuth", `s.vacuumExpiredOAuthAccessTokens(ctx, now)`},
		{"vacuumExpiredOAuth", `s.vacuumExpiredOAuthAccessGrants(ctx, now)`},
		{"vacuumExpiredOAuthAccessTokens", `Model(&models.OAuthAccessToken{})`},
		{"vacuumExpiredOAuthAccessTokens", `Where(expiredOAuthAccessTokensSQL, now, now)`},
		{"vacuumExpiredOAuthAccessTokens", `Limit(oauthVacuumBatchSize)`},
		{"vacuumExpiredOAuthAccessTokens", `Delete(&models.OAuthAccessToken{}, ids)`},
		{"vacuumExpiredOAuthAccessGrants", `Model(&models.OAuthAccessGrant{})`},
		{"vacuumExpiredOAuthAccessGrants", `Where(expiredOAuthAccessGrantsSQL, now, now)`},
		{"vacuumExpiredOAuthAccessGrants", `Limit(oauthVacuumBatchSize)`},
		{"vacuumExpiredOAuthAccessGrants", `Delete(&models.OAuthAccessGrant{}, ids)`},
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
	if !functionBodyContains(t, startup, "StartBackgroundWorkers", "workers.Go(ctx, s.runOAuthVacuumWorker)") {
		t.Fatal("StartBackgroundWorkers does not start OAuth vacuum worker")
	}
}
