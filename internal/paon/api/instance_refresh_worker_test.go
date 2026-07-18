package api

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestInstanceRefreshConstantsMatchRailsScheduler(t *testing.T) {
	if instanceRefreshWorkerInterval != time.Hour {
		t.Fatalf("instanceRefreshWorkerInterval = %s", instanceRefreshWorkerInterval)
	}
	if instanceRefreshMeiliBatchSize != 1000 {
		t.Fatalf("instanceRefreshMeiliBatchSize = %d", instanceRefreshMeiliBatchSize)
	}
}

func TestInstanceRefreshWorkerUsesRailsSchedulerShape(t *testing.T) {
	src, err := os.ReadFile("instance_refresh_worker.go")
	if err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		functionName string
		want         string
	}{
		{"runInstanceRefreshWorker", `s.refreshInstances(ctx)`},
		{"refreshInstances", `s.refreshInstancesMaterializedView()`},
		{"refreshInstances", `s.cfg.MeiliEnabled && strings.TrimSpace(s.cfg.MeiliHost) != ""`},
		{"refreshInstances", `s.deployMeiliInstances(ctx, instanceRefreshMeiliBatchSize, "", func(meiliDeployProgress) error`},
	}
	for _, check := range checks {
		if !functionBodyContains(t, src, check.functionName, check.want) {
			t.Fatalf("%s missing %q", check.functionName, check.want)
		}
	}
	body := functionBody(t, src, "runInstanceRefreshWorker")
	refreshAt := strings.Index(body, `s.refreshInstances(ctx)`)
	tickerAt := strings.Index(body, `time.NewTicker(instanceRefreshWorkerInterval)`)
	if refreshAt < 0 || tickerAt < 0 || refreshAt > tickerAt {
		t.Fatal("instance refresh worker must refresh once at startup before waiting for the hourly ticker")
	}
	startup, err := os.ReadFile("activitypub_retry.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, startup, "StartBackgroundWorkers", "workers.Go(ctx, s.runInstanceRefreshWorker)") {
		t.Fatal("StartBackgroundWorkers does not start instance refresh worker")
	}
}
