package api

import (
	"os"
	"reflect"
	"testing"
)

func TestPollNotificationRecipientIDs(t *testing.T) {
	got := pollNotificationRecipientIDs(10, true, []int64{20, 10, 20, 0, 30})
	want := []int64{10, 20, 30}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("recipients = %v, want %v", got, want)
	}

	got = pollNotificationRecipientIDs(10, false, []int64{20, 10, 20})
	want = []int64{20, 10}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("remote owner recipients = %v, want %v", got, want)
	}
}

func TestPollExpirationWorkerIsStarted(t *testing.T) {
	src, err := os.ReadFile("activitypub_retry.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, src, "StartBackgroundWorkers", "workers.Go(ctx, s.runPollExpirationWorker)") {
		t.Fatal("StartBackgroundWorkers does not start poll expiration worker")
	}
}
