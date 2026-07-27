package api

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/hibiken/asynq"
	"gorm.io/gorm"
)

func TestWorkerLookupErrorDiscardsOnlyRecordNotFound(t *testing.T) {
	transient := errors.New("database connection reset")
	for _, test := range []struct {
		name    string
		err     error
		wantNil bool
	}{
		{name: "nil", err: nil, wantNil: true},
		{name: "record not found", err: gorm.ErrRecordNotFound, wantNil: true},
		{name: "wrapped record not found", err: errors.Join(errors.New("lookup"), gorm.ErrRecordNotFound), wantNil: true},
		{name: "transient database failure", err: transient, wantNil: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := workerLookupError("worker lookup", test.err)
			if (got == nil) != test.wantNil {
				t.Fatalf("workerLookupError() = %v, wantNil=%v", got, test.wantNil)
			}
			if got != nil && !errors.Is(got, transient) {
				t.Fatalf("transient cause was not preserved: %v", got)
			}
		})
	}
}

func TestAsynqEnqueueAcceptedDistinguishesDuplicatesFromFailures(t *testing.T) {
	if !asynqEnqueueAccepted(nil) || !asynqEnqueueAccepted(asynq.ErrDuplicateTask) || !asynqEnqueueAccepted(asynq.ErrTaskIDConflict) {
		t.Fatal("normal and coalesced Asynq enqueue results must be accepted")
	}
	if asynqEnqueueAccepted(errors.New("redis unavailable")) {
		t.Fatal("Redis enqueue failure was misclassified as accepted")
	}
	if asynqEnqueueAccepted(errors.New("duplicate database row")) {
		t.Fatal("unrelated string containing duplicate was misclassified")
	}
}

func TestRedisRetryLeaseScriptsRequireClaimOwnership(t *testing.T) {
	for _, script := range []string{redisRetryAckScript, redisRetryReplaceScript} {
		if !strings.Contains(script, "HGET") || !strings.Contains(script, "ARGV[2]") {
			t.Fatalf("retry acknowledgement/replacement lacks owner fencing: %s", script)
		}
	}
	for _, want := range []string{"ZRANGEBYSCORE", "ZREM", "processing", "lease_until", "HSET"} {
		if !strings.Contains(redisRetryClaimScript, want) {
			t.Fatalf("retry claim script missing %q", want)
		}
	}
}

func TestActivityPubProcessingFailuresRemainOnAsynqRetryPath(t *testing.T) {
	retrySource, err := os.ReadFile("activitypub_retry.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(retrySource), "activityPubInboxPermanentValidationError") {
		t.Fatal("ActivityPub processing still discards handler errors before Asynq can retry and archive them")
	}
	if !strings.Contains(string(retrySource), "return activityPubProcessingError(job.Body, actor.ID, job.DeliveredToAccountID, err)") {
		t.Fatal("ActivityPub processing does not return handler errors to Asynq")
	}

	asynqSource, err := os.ReadFile("asynq_workers.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(asynqSource), "asynq.MaxRetry(activityPubInboxProcessingRetryLimit)") {
		t.Fatal("ActivityPub processing task lacks the retry option required for eventual Asynq archival")
	}
}
