package api

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestSchedulerRedisLockHelpersUseRailsLikeNXEXAndTokenRelease(t *testing.T) {
	src, err := os.ReadFile("scheduler_lock.go")
	if err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		functionName string
		want         string
	}{
		{"runSchedulerWithRedisLock", `"SET", key, token, "NX", "EX", formatRedisSeconds(ttl)`},
		{"runSchedulerWithRedisLock", `fn()`},
		{"runSchedulerWithRedisLock", `return false`},
		{"runSchedulerWithRedisLock", `defer s.releaseSchedulerRedisLock(ctx, key, token)`},
		{"runSchedulerWithRedisLock", `"SET", markerKey, token, "NX", "EX", formatRedisSeconds(schedulerCadence(name, ttl))`},
		{"runSchedulerWithRedisLock", `stopRenewal := s.renewSchedulerRedisLock(ctx, key, token, ttl)`},
		{"renewSchedulerRedisLock", `schedulerRedisLockRenewScript`},
		{"releaseSchedulerRedisLock", `"EVAL", schedulerRedisLockReleaseScript, "1", key, token`},
	}
	for _, check := range checks {
		if !functionBodyContains(t, src, check.functionName, check.want) {
			t.Fatalf("%s missing %q", check.functionName, check.want)
		}
	}
	if !strings.Contains(schedulerRedisLockReleaseScript, `redis.call("GET", KEYS[1]) == ARGV[1]`) {
		t.Fatal("scheduler lock release must compare the token before deleting")
	}
	if !strings.Contains(schedulerRedisLockRenewScript, `redis.call("GET", KEYS[1]) == ARGV[1]`) || !strings.Contains(schedulerRedisLockRenewScript, `redis.call("EXPIRE", KEYS[1], ARGV[2])`) {
		t.Fatal("scheduler lock renewal must compare the owner token before extending")
	}
	if !redisOK("OK") || !redisOK("ok") || redisOK(nil) || redisOK("BUSY") {
		t.Fatal("redisOK must only accept Redis OK replies")
	}
	if got := formatRedisSeconds(90 * time.Second); got != "90" {
		t.Fatalf("formatRedisSeconds = %q", got)
	}
	if got := formatRedisSeconds(0); got != "1" {
		t.Fatalf("formatRedisSeconds zero = %q", got)
	}
}

func TestSchedulerCadenceUsesLogicalRunnerIntervalNotRailsLockTTL(t *testing.T) {
	for name, want := range map[string]time.Duration{
		"accounts_statuses_cleanup_scheduler": time.Minute,
		"suspended_user_cleanup_scheduler":    time.Minute,
		"indexing_scheduler":                  indexingWorkerInterval,
		"scheduled_statuses_scheduler":        scheduledStatusPublishWorkerInterval,
		"trends_refresh_scheduler":            trendsRefreshWorkerInterval,
		"trends_review_scheduler":             trendsReviewWorkerInterval,
		"software_update_check_scheduler":     softwareUpdateCheckWorkerInterval,
		"auto_close_registrations_scheduler":  autoCloseRegistrationsWorkerInterval,
		"instance_refresh_scheduler":          instanceRefreshWorkerInterval,
		"admin_metrics_prewarm_scheduler":     adminMetricsPrewarmWorkerInterval,
		"featured_tag_refresh_scheduler":      featuredTagRefreshWorkerInterval,
		"profile_verification_scheduler":      profileVerificationWorkerInterval,
	} {
		if got := schedulerCadence(name, 24*time.Hour); got != want {
			t.Fatalf("schedulerCadence(%q) = %v, want %v", name, got, want)
		}
	}
	if got := schedulerCadence("vacuum_scheduler:backups_vacuum", 24*time.Hour); got != 24*time.Hour {
		t.Fatalf("vacuum fallback cadence = %v", got)
	}
}
