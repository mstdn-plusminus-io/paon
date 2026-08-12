package api

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
)

func TestAdminSidekiqProcessCheckPaonGoQueueCoverageListsRailsQueues(t *testing.T) {
	got := paonGoWorkerQueueCoverage(config.Config{})
	for _, want := range adminSidekiqRequiredQueues {
		if !stringSliceContains(got, want) {
			t.Fatalf("paon-go worker queue coverage %#v missing Rails queue %q", got, want)
		}
	}
}

func TestAdminSidekiqProcessCheckRequiresLiveWorkerHeartbeat(t *testing.T) {
	s := &Server{}
	if missing := s.adminDashboardMissingSidekiqQueues(); !reflect.DeepEqual(missing, adminSidekiqRequiredQueues) {
		t.Fatalf("missing queues without paon-go/Rails worker heartbeat = %#v, want %#v", missing, adminSidekiqRequiredQueues)
	}
	if queues := s.adminDashboardPaonGoWorkerQueues(); len(queues) != 0 {
		t.Fatalf("paon-go worker queues without heartbeat = %#v", queues)
	}
	check, ok := s.adminDashboardSidekiqProcessCheck()
	if !ok {
		t.Fatal("missing paon-go worker heartbeat should surface the Rails sidekiq_process_check warning")
	}
	for _, queue := range adminSidekiqRequiredQueues {
		if !strings.Contains(check.Value, queue) {
			t.Fatalf("sidekiq_process_check value %q missing queue %q", check.Value, queue)
		}
	}
}

func TestAdminSidekiqQueueCoverageTracksAsynqRuntimeQueues(t *testing.T) {
	asynqQueues := paonGoAsynqQueueWeights()
	for _, want := range []string{"default", "push", "mailers", "pull", "ingress"} {
		if asynqQueues[want] <= 0 {
			t.Fatalf("paon-go asynq runtime queues missing %q: %#v", want, asynqQueues)
		}
	}
	forbidden := map[string]struct{}{"scheduler": {}}
	for queue := range forbidden {
		if _, ok := asynqQueues[queue]; ok {
			t.Fatalf("%s must stay covered by dedicated paon-go goroutines, not the asynq queue map", queue)
		}
	}
	got := paonGoWorkerQueueCoverage(config.Config{})
	for _, want := range adminSidekiqRequiredQueues {
		if !stringSliceContains(got, want) {
			t.Fatalf("admin paon-go queue coverage %#v missing Rails queue %q", got, want)
		}
	}
}

func TestPaonGoWorkerQueueCoverageReflectsSelectedAsynqQueues(t *testing.T) {
	got := paonGoWorkerQueueCoverage(config.Config{
		RedisNamespace: "mastodon:",
		AsynqQueues:    []string{"push"},
	})
	want := []string{"ingress", "push", "scheduler"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("selected worker queue coverage = %#v, want %#v", got, want)
	}
}

func TestSidekiqProcessQueuesFromRedis(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want []string
	}{
		{`["default","push","mailers"]`, []string{"default", "push", "mailers"}},
		{`pull, scheduler, ingress`, []string{"pull", "scheduler", "ingress"}},
		{`["pull","pull",""]`, []string{"pull"}},
	} {
		if got := sidekiqProcessQueuesFromRedis(tc.raw); !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("sidekiqProcessQueuesFromRedis(%q) = %#v, want %#v", tc.raw, got, tc.want)
		}
	}
}

func TestAdminDashboardSystemChecksWireSidekiqProcessCheck(t *testing.T) {
	src, err := os.ReadFile("admin_dashboard.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, src, "adminDashboardSystemChecks", "s.adminDashboardSidekiqProcessCheck()") {
		t.Fatal("admin dashboard system checks must include Rails Sidekiq process check equivalent")
	}
	workers, err := os.ReadFile("activitypub_retry.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"workers.Go(ctx, s.runPaonGoWorkerHeartbeat)",
		"workers.Go(ctx, s.runActivityPubDeliveryRetryWorker)",
		"workers.Go(ctx, s.runActivityPubInboxProcessingWorker)",
		"s.startAsynqWorker(ctx, workers.markReady)",
		"workers.Go(ctx, s.runScheduledStatusPublishWorker)",
		"workers.Go(ctx, s.runTrendsRefreshWorker)",
	} {
		if !strings.Contains(string(workers), want) {
			t.Fatalf("StartBackgroundWorkers missing queue coverage fragment %q", want)
		}
	}
}

func TestPaonGoWorkerHeartbeatUsesRedisExpiryAndQueuePayload(t *testing.T) {
	src, err := os.ReadFile("admin_system_check_sidekiq.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		fn   string
		want string
	}{
		{"runPaonGoWorkerHeartbeat", `s.recordPaonGoWorkerHeartbeat(ctx, identity, time.Now().UTC())`},
		{"runPaonGoWorkerHeartbeat", `time.NewTicker(paonGoWorkerHeartbeatTTL / 3)`},
		{"runPaonGoWorkerHeartbeat", `defer s.clearPaonGoWorkerHeartbeat(context.Background(), identity)`},
		{"recordPaonGoWorkerHeartbeat", `json.Marshal(paonGoWorkerQueueCoverage(s.cfg))`},
		{"recordPaonGoWorkerHeartbeat", `"ZREMRANGEBYSCORE", processesKey, "-inf", updatedAt`},
		{"recordPaonGoWorkerHeartbeat", `"ZADD", processesKey, expiresAt, identity`},
		{"recordPaonGoWorkerHeartbeat", `"HSET", processKey, "queues", string(queues), "updated_at", updatedAt`},
		{"recordPaonGoWorkerHeartbeat", `"EXPIRE", processKey, ttl`},
		{"adminDashboardPaonGoWorkerQueuesFromHeartbeat", `"ZRANGEBYSCORE", key, nowUnix, "+inf"`},
		{"paonGoWorkerQueuesForIdentity", `sidekiqProcessQueuesFromRedis(raw)`},
	} {
		if !functionBodyContains(t, src, check.fn, check.want) {
			t.Fatalf("admin_system_check_sidekiq.go:%s missing heartbeat fragment %q", check.fn, check.want)
		}
	}
}
