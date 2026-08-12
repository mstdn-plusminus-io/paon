package api

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestBackgroundWorkerConcurrencyInventoryCoversEveryStartedRunner(t *testing.T) {
	source, err := os.ReadFile("activitypub_retry.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(source)
	if len(backgroundWorkerConcurrencyInventory) != 35 {
		t.Fatalf("inventory has %d entries, want 35", len(backgroundWorkerConcurrencyInventory))
	}
	for name, semantics := range backgroundWorkerConcurrencyInventory {
		if !strings.Contains(body, "s."+name) {
			t.Fatalf("inventory runner %s is not started", name)
		}
		switch semantics.Concurrency {
		case backgroundWorkerSingleton, backgroundWorkerRowClaimed, backgroundWorkerDuplicateSafe, backgroundWorkerQueueConsumer:
		default:
			t.Fatalf("runner %s has invalid concurrency semantics %q", name, semantics.Concurrency)
		}
		if strings.TrimSpace(semantics.Proof) == "" {
			t.Fatalf("runner %s has no concurrency proof", name)
		}
	}
}

func TestBackgroundWorkersWaitForOwnedRunnersAndHonorDrainDeadline(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	workers := newBackgroundWorkers()
	exited := make(chan struct{})
	workers.Go(ctx, func(ctx context.Context) {
		<-ctx.Done()
		close(exited)
	})
	workers.Seal()
	cancel()
	waitCtx, waitCancel := context.WithTimeout(context.Background(), time.Second)
	defer waitCancel()
	if err := workers.Wait(waitCtx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-exited:
	default:
		t.Fatal("Wait returned before runner exited")
	}

	blocked := newBackgroundWorkers()
	release := make(chan struct{})
	blocked.Go(context.Background(), func(context.Context) { <-release })
	blocked.Seal()
	timeoutCtx, timeoutCancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer timeoutCancel()
	if err := blocked.Wait(timeoutCtx); err == nil {
		t.Fatal("Wait did not report drain timeout")
	}
	close(release)
}
