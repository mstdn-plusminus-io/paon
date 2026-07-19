package api

import (
	"errors"
	"strings"
	"testing"
	"time"

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

func TestRedisRetrySuccessorsIncrementAttemptsWithoutMutatingCurrentClaim(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	job := activityPubInboxProcessingJob{ActorID: 1, Body: []byte(`{"type":"Create"}`), Attempts: 2}
	encoded, runAt, err := nextActivityPubInboxRetry(job, now)
	if err != nil {
		t.Fatal(err)
	}
	if job.Attempts != 2 {
		t.Fatalf("current claim was mutated: attempts=%d", job.Attempts)
	}
	if !strings.Contains(encoded, `"attempts":3`) || !runAt.After(now) {
		t.Fatalf("successor = %s runAt=%s", encoded, runAt)
	}
}

func TestActivityPubInboxValidationClassificationKeepsDatabaseFailuresRetryable(t *testing.T) {
	for _, err := range []error{errFeaturedTagInvalidName, errFeaturedTagDuplicate, errFeaturedTagLimit} {
		if !activityPubInboxPermanentValidationError(err) {
			t.Fatalf("featured-tag validation error must be discarded: %v", err)
		}
	}
	if activityPubInboxPermanentValidationError(errors.New("postgres serialization failure")) {
		t.Fatal("transient database error was classified as permanent validation")
	}
}
