package api

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestBackgroundWorkersWaitReadyUsesInitializationBarrier(t *testing.T) {
	workers := newBackgroundWorkers()
	result := make(chan error, 1)
	go func() {
		result <- workers.WaitReady(context.Background())
	}()

	select {
	case err := <-result:
		t.Fatalf("WaitReady returned before worker initialization: %v", err)
	default:
	}

	workers.markReady(nil)
	if err := <-result; err != nil {
		t.Fatalf("WaitReady after successful initialization: %v", err)
	}
}

func TestBackgroundWorkersWaitReadyReturnsStartupFailure(t *testing.T) {
	workers := newBackgroundWorkers()
	want := errors.New("asynq startup failed")
	workers.markReady(want)
	if got := workers.WaitReady(context.Background()); !errors.Is(got, want) {
		t.Fatalf("WaitReady error = %v, want %v", got, want)
	}
}

func TestAsynqReadinessIsReportedAfterHandlerRegistration(t *testing.T) {
	src, err := os.ReadFile("asynq_workers.go")
	if err != nil {
		t.Fatal(err)
	}
	body := functionBody(t, src, "startAsynqWorker")
	start := strings.Index(body, "srv.Start(s.newAsynqServeMux())")
	ready := strings.Index(body, "ready(err)")
	if start < 0 || ready < 0 || ready < start {
		t.Fatal("Asynq readiness must be reported only after handlers are registered and Start returns")
	}
}
