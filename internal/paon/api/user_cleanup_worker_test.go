package api

import (
	"testing"
	"time"
)

func TestUserCleanupConstantsMatchRailsScheduler(t *testing.T) {
	if userCleanupWorkerInterval != 24*time.Hour {
		t.Fatalf("userCleanupWorkerInterval = %s", userCleanupWorkerInterval)
	}
	if userCleanupBatchSize != 1000 {
		t.Fatalf("userCleanupBatchSize = %d", userCleanupBatchSize)
	}
	if unconfirmedUserTTL != 48*time.Hour {
		t.Fatalf("unconfirmedUserTTL = %s", unconfirmedUserTTL)
	}
	if discardedStatusRetentionTTL != 30*24*time.Hour {
		t.Fatalf("discardedStatusRetentionTTL = %s", discardedStatusRetentionTTL)
	}
}
