package api

import (
	"reflect"
	"testing"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
)

func TestFilterCacheKeyUsesRailsNamespace(t *testing.T) {
	if got := filterCacheKey(42); got != "filters:v3:42" {
		t.Fatalf("filterCacheKey = %q", got)
	}
	got := railsCacheRedisKeyCandidates(config.Config{RedisNamespace: "mastodon:"}, filterCacheKey(42))
	want := []string{"filters:v3:42", "cache:filters:v3:42", "mastodon:filters:v3:42", "mastodon_cache:filters:v3:42"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("cache keys = %#v, want %#v", got, want)
	}
}

func TestFilterChangedRedisPayloadParsesForStreaming(t *testing.T) {
	value := []any{"message", "timeline:42", filterChangedRedisPayload()}
	message, ok := redisPubSubMessage(value)
	if !ok {
		t.Fatal("filters_changed payload did not parse")
	}
	if message.Event != "filters_changed" || string(message.Payload) != "{}" {
		t.Fatalf("message = %#v payload=%s", message, string(message.Payload))
	}
}
