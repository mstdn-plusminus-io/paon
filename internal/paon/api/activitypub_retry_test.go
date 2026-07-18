package api

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
)

func TestEnqueueActivityPubInboxProcessingJobBackendBoundaries(t *testing.T) {
	job := activityPubInboxProcessingJob{
		ActorID:   42,
		ActorType: "Account",
		Body:      json.RawMessage(`{"type":"Create"}`),
	}

	t.Run("asynq success skips fallback", func(t *testing.T) {
		asynqCalled := false
		fallbackCalled := false
		err := enqueueActivityPubInboxProcessingJobWithBackends(
			context.Background(),
			job,
			func(got activityPubInboxProcessingJob) bool {
				asynqCalled = true
				if got.ActorID != job.ActorID || string(got.Body) != string(job.Body) {
					t.Fatalf("asynq job = %#v, want %#v", got, job)
				}
				return true
			},
			func(context.Context, activityPubInboxProcessingJob) error {
				fallbackCalled = true
				return nil
			},
		)
		if err != nil {
			t.Fatalf("enqueue error = %v", err)
		}
		if !asynqCalled || fallbackCalled {
			t.Fatalf("asynqCalled=%t fallbackCalled=%t", asynqCalled, fallbackCalled)
		}
	})

	t.Run("fallback success is accepted", func(t *testing.T) {
		fallbackCalled := false
		err := enqueueActivityPubInboxProcessingJobWithBackends(
			context.Background(),
			job,
			func(activityPubInboxProcessingJob) bool { return false },
			func(_ context.Context, got activityPubInboxProcessingJob) error {
				fallbackCalled = true
				if got.ActorID != job.ActorID || string(got.Body) != string(job.Body) {
					t.Fatalf("fallback job = %#v, want %#v", got, job)
				}
				return nil
			},
		)
		if err != nil {
			t.Fatalf("enqueue error = %v", err)
		}
		if !fallbackCalled {
			t.Fatal("fallback was not called after asynq rejected the job")
		}
	})

	t.Run("both backends fail", func(t *testing.T) {
		fallbackErr := errors.New("redis zadd failed")
		err := enqueueActivityPubInboxProcessingJobWithBackends(
			context.Background(),
			job,
			func(activityPubInboxProcessingJob) bool { return false },
			func(context.Context, activityPubInboxProcessingJob) error { return fallbackErr },
		)
		if !errors.Is(err, fallbackErr) {
			t.Fatalf("enqueue error = %v, want wrapped fallback error", err)
		}
	})
}

func TestEnqueueActivityPubInboxProcessingJobNoopInputs(t *testing.T) {
	var nilServer *Server
	if err := nilServer.enqueueActivityPubInboxProcessingJob(42, 0, "Account", []byte(`{"type":"Create"}`)); err != nil {
		t.Fatalf("nil server no-op error = %v", err)
	}
	if err := (&Server{}).enqueueActivityPubInboxProcessingJob(42, 0, "Account", []byte(`{"type":"Create"}`)); err != nil {
		t.Fatalf("missing database no-op error = %v", err)
	}

	for name, job := range map[string]activityPubInboxProcessingJob{
		"missing actor": {Body: json.RawMessage(`{"type":"Create"}`)},
		"missing body":  {ActorID: 42},
	} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			err := enqueueActivityPubInboxProcessingJobWithBackends(
				context.Background(),
				job,
				func(activityPubInboxProcessingJob) bool { calls++; return true },
				func(context.Context, activityPubInboxProcessingJob) error { calls++; return nil },
			)
			if err != nil {
				t.Fatalf("no-op enqueue error = %v", err)
			}
			if calls != 0 {
				t.Fatalf("backend calls = %d, want 0", calls)
			}
		})
	}
}

func TestActivityPubInboxProcessingWorkerIgnoresUnsupportedActorTypeLikeRails(t *testing.T) {
	server := &Server{db: &gorm.DB{}}
	job := activityPubInboxProcessingJob{
		ActorID:   123,
		ActorType: "InstanceActor",
		Body:      json.RawMessage(`{"type":"Delete"}`),
	}
	if err := server.performActivityPubInboxProcessingOnce(context.Background(), job); err != nil {
		t.Fatalf("unsupported actor_type should be ignored like Rails, got error: %v", err)
	}
}

func TestActivityPubDeliveryRetryDelayIsBounded(t *testing.T) {
	noJitter := func(int64) int64 { return 0 }
	if got := activityPubDeliveryRetryDelayWithRand(1, noJitter); got != 15*time.Second {
		t.Fatalf("delay(1) = %s", got)
	}
	if got := activityPubDeliveryRetryDelayWithRand(2, noJitter); got != 16*time.Second {
		t.Fatalf("delay(2) = %s", got)
	}
	if got := activityPubDeliveryRetryDelayWithRand(16, noJitter); got != 50640*time.Second {
		t.Fatalf("delay(16) = %s", got)
	}
	if got := activityPubDeliveryRetryDelayWithRand(20, noJitter); got != 50640*time.Second {
		t.Fatalf("delay(20) = %s", got)
	}
}

func TestActivityPubDeliveryRetryDelayAddsRailsCustomJitter(t *testing.T) {
	attempts := 4
	base := (3*3*3*3 + 15) * int(time.Second)
	got := activityPubDeliveryRetryDelayWithRand(attempts, func(max int64) int64 {
		wantMax := int64((3 * 3 * 3 * 3) * int(time.Second) / 2)
		if max != wantMax {
			t.Fatalf("jitter max = %d, want %d", max, wantMax)
		}
		return int64(1250 * time.Millisecond)
	})
	if got != time.Duration(base)+1250*time.Millisecond {
		t.Fatalf("delay with jitter = %s", got)
	}
}

func TestActivityPubDeliveryRetryWorkerDrainsDueQueueOnStartup(t *testing.T) {
	src, err := os.ReadFile("activitypub_retry.go")
	if err != nil {
		t.Fatal(err)
	}
	body := functionBody(t, src, "runActivityPubDeliveryRetryWorker")
	startupDrain := strings.Index(body, `s.processDueActivityPubDeliveryRetries(ctx, 25)`)
	ticker := strings.Index(body, `time.NewTicker(15 * time.Second)`)
	if startupDrain < 0 || ticker < 0 || startupDrain > ticker {
		t.Fatal("activitypub delivery retry worker must drain due Rails-compatible retry jobs before waiting for the first tick")
	}
}

func TestActivityPubDeliveryHostNormalizesInboxURL(t *testing.T) {
	if got := activityPubDeliveryHost("https://Remote.Example/users/alice/inbox"); got != "remote.example" {
		t.Fatalf("host = %q", got)
	}
	if got := activityPubDeliveryHost("https://bücher.example/users/alice/inbox"); got != "xn--bcher-kva.example" {
		t.Fatalf("IDN host = %q", got)
	}
	if got := activityPubDeliveryHost("not a url"); got != "" {
		t.Fatalf("invalid host = %q", got)
	}
}

func TestRedisStringArray(t *testing.T) {
	got, ok := redisStringArray([]any{"a", "b"})
	if !ok || len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("got=%#v ok=%v", got, ok)
	}
	if _, ok := redisStringArray([]any{"a", int64(1)}); ok {
		t.Fatal("mixed array should fail")
	}
}

func TestActivityPubDeliveryRetryJobPreservesMigratedFollowCleanupMetadata(t *testing.T) {
	job := activityPubDeliveryRetryJob{
		SourceAccountID:                  10,
		InboxURL:                         "https://remote.example/inbox",
		Body:                             []byte(`{"type":"Follow"}`),
		RetryLimit:                       8,
		SynchronizeFollowers:             true,
		BypassAvailability:               true,
		MigratedFollowOldTargetAccountID: 20,
	}
	encoded, err := json.Marshal(job)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"migrated_follow_old_target_account_id":20`) {
		t.Fatalf("encoded job missing migrated follow metadata: %s", encoded)
	}
	if !strings.Contains(string(encoded), `"synchronize_followers":true`) {
		t.Fatalf("encoded job missing follower synchronization metadata: %s", encoded)
	}
	if !strings.Contains(string(encoded), `"bypass_availability":true`) {
		t.Fatalf("encoded job missing availability bypass metadata: %s", encoded)
	}
	if !strings.Contains(string(encoded), `"retry_limit":8`) {
		t.Fatalf("encoded job missing retry limit metadata: %s", encoded)
	}

	var decoded activityPubDeliveryRetryJob
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.MigratedFollowOldTargetAccountID != 20 {
		t.Fatalf("old target id = %d", decoded.MigratedFollowOldTargetAccountID)
	}
	if !decoded.SynchronizeFollowers {
		t.Fatal("synchronize followers metadata was not preserved")
	}
	if !decoded.BypassAvailability {
		t.Fatal("availability bypass metadata was not preserved")
	}
	if decoded.activityPubDeliveryRetryLimit() != 8 {
		t.Fatalf("retry limit = %d", decoded.activityPubDeliveryRetryLimit())
	}
	if (activityPubDeliveryRetryJob{}).activityPubDeliveryRetryLimit() != activityPubDeliveryRetryFailureThreshold {
		t.Fatal("default retry limit should match Rails ActivityPub::DeliveryWorker retry count")
	}
}
