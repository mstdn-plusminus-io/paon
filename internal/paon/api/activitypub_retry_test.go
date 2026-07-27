package api

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func TestEnqueueActivityPubInboxProcessingJobRequiresAsynq(t *testing.T) {
	job := activityPubInboxProcessingJob{
		ActorID:   42,
		ActorType: "Account",
		Body:      json.RawMessage(`{"type":"Create"}`),
	}

	t.Run("accepted", func(t *testing.T) {
		asynqCalled := false
		err := enqueueActivityPubInboxProcessingJobWithAsynq(
			job,
			func(got activityPubInboxProcessingJob) bool {
				asynqCalled = true
				if got.ActorID != job.ActorID || string(got.Body) != string(job.Body) {
					t.Fatalf("asynq job = %#v, want %#v", got, job)
				}
				return true
			},
		)
		if err != nil {
			t.Fatalf("enqueue error = %v", err)
		}
		if !asynqCalled {
			t.Fatal("Asynq backend was not called")
		}
	})

	t.Run("enqueue failure is returned", func(t *testing.T) {
		err := enqueueActivityPubInboxProcessingJobWithAsynq(
			job,
			func(activityPubInboxProcessingJob) bool { return false },
		)
		if err == nil || !strings.Contains(err.Error(), "Asynq enqueue failed") {
			t.Fatalf("enqueue error = %v", err)
		}
	})

	for name, job := range map[string]activityPubInboxProcessingJob{
		"missing actor": {Body: json.RawMessage(`{"type":"Create"}`)},
		"missing body":  {ActorID: 42},
	} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			err := enqueueActivityPubInboxProcessingJobWithAsynq(
				job,
				func(activityPubInboxProcessingJob) bool { calls++; return true },
			)
			if err == nil {
				t.Fatal("invalid task was accepted")
			}
			if calls != 0 {
				t.Fatalf("backend calls = %d, want 0", calls)
			}
		})
	}
}

func TestEnqueueActivityPubInboxProcessingJobRejectsUnavailableServer(t *testing.T) {
	var server *Server
	if err := server.enqueueActivityPubInboxProcessingJob(42, 0, "Account", []byte(`{"type":"Create"}`)); err == nil {
		t.Fatal("missing Asynq backend was accepted")
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
