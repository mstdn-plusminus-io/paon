package api

import (
	"testing"
	"time"
)

func TestMuteExpirationConstantsMatchRailsDeleteMuteWorkerCadence(t *testing.T) {
	if muteExpirationWorkerInterval != time.Minute {
		t.Fatalf("muteExpirationWorkerInterval = %s", muteExpirationWorkerInterval)
	}
	if muteExpirationBatchSize != 100 {
		t.Fatalf("muteExpirationBatchSize = %d", muteExpirationBatchSize)
	}
}
