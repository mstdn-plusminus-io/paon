//go:build integration

package main

import (
	"context"
	"os"
	"testing"

	"github.com/mstdn-plusminus-io/paon/internal/paon/migrate"
	"github.com/redis/go-redis/v9"
)

func TestLegacyTagTrendRedisCutoverUsesOnlyUnnamespacedKeys(t *testing.T) {
	rawURL := os.Getenv("PAON_TEST_REDIS_URL")
	if rawURL == "" {
		t.Fatal("PAON_TEST_REDIS_URL is required for integration tests")
	}
	redisOptions, err := redis.ParseURL(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	client := redis.NewClient(redisOptions)
	defer client.Close()
	ctx := context.Background()
	namespacedKey := "paon-migrate-test:trending_tags:all"
	keys := []string{legacyTagTrendsAllKey, legacyTagTrendsAllowedKey, namespacedKey}
	if err := client.Del(ctx, keys...).Err(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Del(ctx, keys...).Err() })
	if err := client.ZAdd(ctx, legacyTagTrendsAllKey, redis.Z{Member: "4401", Score: 2}, redis.Z{Member: "4402", Score: 8}).Err(); err != nil {
		t.Fatal(err)
	}
	if err := client.ZAdd(ctx, legacyTagTrendsAllowedKey, redis.Z{Member: "4402", Score: 1}).Err(); err != nil {
		t.Fatal(err)
	}
	if err := client.ZAdd(ctx, namespacedKey, redis.Z{Member: "9999", Score: 99}).Err(); err != nil {
		t.Fatal(err)
	}
	rows, err := readLegacyTagTrendRows(ctx, client)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].TagID != 4401 || rows[0].Allowed || rows[1].TagID != 4402 || !rows[1].Allowed {
		t.Fatalf("legacy Redis rows = %#v", rows)
	}
	options := migrate.Options{}
	wireLegacyTagTrendBackfill(&options, client)
	if err := options.Mastodon44TagTrendBackfillPostCommit(ctx); err != nil {
		t.Fatal(err)
	}
	if count, err := client.Exists(ctx, legacyTagTrendsAllKey, legacyTagTrendsAllowedKey).Result(); err != nil || count != 0 {
		t.Fatalf("legacy keys after cleanup = count %d err %v", count, err)
	}
	if count, err := client.Exists(ctx, namespacedKey).Result(); err != nil || count != 1 {
		t.Fatalf("namespaced control key after cleanup = count %d err %v", count, err)
	}
}
