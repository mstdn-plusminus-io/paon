package api

import (
	"database/sql"
	"testing"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
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

func TestPartitionRemovalStatusIDsByOrigin(t *testing.T) {
	statuses := []models.Status{
		{ID: 11, Account: models.Account{ID: 1}},
		{ID: 22, Account: models.Account{ID: 2, Domain: sql.NullString{String: "remote.example", Valid: true}}},
		{ID: 0, Account: models.Account{ID: 3}},
	}
	localIDs, remoteIDs := partitionRemovalStatusIDsByOrigin(statuses)
	if len(localIDs) != 1 || localIDs[0] != 11 {
		t.Fatalf("local IDs = %#v, want [11]", localIDs)
	}
	if len(remoteIDs) != 1 || remoteIDs[0] != 22 {
		t.Fatalf("remote IDs = %#v, want [22]", remoteIDs)
	}
}
