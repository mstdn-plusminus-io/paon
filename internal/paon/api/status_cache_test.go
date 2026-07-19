package api

import (
	"reflect"
	"testing"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
)

func TestStatusCacheKeyMatchesRailsModel(t *testing.T) {
	if got := statusCacheKey(42); got != "v3:statuses/42" {
		t.Fatalf("statusCacheKey = %q", got)
	}
	wantKeys := []string{"v3:statuses/42", "statuses/42"}
	if got := statusCacheKeys(42); !reflect.DeepEqual(got, wantKeys) {
		t.Fatalf("statusCacheKeys = %#v, want %#v", got, wantKeys)
	}
}

func TestStatusCacheInvalidationUsesRailsCacheNamespaces(t *testing.T) {
	keys := []string{}
	for _, key := range statusCacheKeys(42) {
		keys = append(keys, railsCacheRedisKeyCandidates(config.Config{RedisNamespace: "mastodon:"}, key)...)
	}
	got := keys
	want := []string{
		"v3:statuses/42", "cache:v3:statuses/42", "mastodon:v3:statuses/42", "mastodon_cache:v3:statuses/42",
		"statuses/42", "cache:statuses/42", "mastodon:statuses/42", "mastodon_cache:statuses/42",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("cache keys = %#v, want %#v", got, want)
	}
}
