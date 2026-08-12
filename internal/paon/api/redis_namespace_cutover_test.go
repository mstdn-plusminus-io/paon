package api

import (
	"context"
	"strings"
	"testing"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
)

func TestRedisNamespaceCutoverNameIsExplicitAndGlobSafe(t *testing.T) {
	for _, valid := range []string{"mastodon", "social.example", "tenant-01"} {
		if got, err := validateRedisNamespaceCutoverName(valid); err != nil || got != valid {
			t.Fatalf("validate(%q) = %q, %v", valid, got, err)
		}
	}
	for _, invalid := range []string{"", "mastodon:", "two words", "tenant*", "tenant?", "tenant[1]"} {
		if _, err := validateRedisNamespaceCutoverName(invalid); err == nil {
			t.Fatalf("invalid namespace %q was accepted", invalid)
		}
	}
	if got := redisGlobLiteral(`tenant[1]*?\x`); got != `tenant\[1\]\*\?\\x` {
		t.Fatalf("escaped glob = %q", got)
	}
}

func TestRedisNamespaceCutoverRejectsPrefixDifferentFromConfiguredNamespace(t *testing.T) {
	_, err := CutoverRedisNamespace(context.Background(), config.Config{RedisNamespace: "tenant:"}, "other", true)
	if err == nil || !strings.Contains(err.Error(), "does not match configured REDIS_NAMESPACE") {
		t.Fatalf("mismatched namespace error = %v", err)
	}
}

func TestRedisNamespaceCutoverPlansRailsCacheAndPaonAsynqPrefixes(t *testing.T) {
	cfg := config.Config{
		RedisURL:        "redis://base.example.test:6379/0",
		SidekiqRedisURL: "redis://worker.example.test:6379/0",
		CacheRedisURL:   "redis://cache.example.test:6379/0",
	}
	topologies := redisNamespaceCutoverTopologies(cfg, "tenant")
	if len(topologies) != 3 {
		t.Fatalf("topologies = %#v", topologies)
	}
	all := make([]string, 0)
	for _, topology := range topologies {
		for _, transform := range topology.transforms {
			all = append(all, transform.sourcePrefix+"=>"+transform.targetPrefix)
		}
	}
	joined := strings.Join(all, "\n")
	for _, want := range []string{
		"tenant:=>",
		"tenant_cache:=>cache:",
		"asynq:{tenant:=>asynq:{",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("transforms missing %q: %s", want, joined)
		}
	}
}

func TestRedisNamespaceCutoverDeduplicatesSharedRedisTopology(t *testing.T) {
	cfg := config.Config{RedisURL: "redis://redis.example.test:6379/0", SidekiqRedisURL: "redis://redis.example.test:6379/0", CacheRedisURL: "redis://redis.example.test:6379/0"}
	topologies := redisNamespaceCutoverTopologies(cfg, "tenant")
	if len(topologies) != 1 {
		t.Fatalf("topologies = %d, want one shared endpoint", len(topologies))
	}
	if got := strings.Join(topologies[0].names, ","); got != "base,sidekiq,cache" {
		t.Fatalf("topology names = %q", got)
	}
}
