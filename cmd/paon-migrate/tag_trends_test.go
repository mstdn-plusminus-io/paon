package main

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/mstdn-plusminus-io/paon/internal/paon/migrate"
	"github.com/redis/go-redis/v9"
)

type fakeLegacyTagTrendRedis struct {
	all          []redis.Z
	allowed      []string
	allErr       error
	allowedErr   error
	delErr       error
	zRangeKeys   []string
	zRangeScores []string
	deletedKeys  []string
}

func (client *fakeLegacyTagTrendRedis) ZRangeWithScores(_ context.Context, key string, _, _ int64) *redis.ZSliceCmd {
	client.zRangeScores = append(client.zRangeScores, key)
	return redis.NewZSliceCmdResult(client.all, client.allErr)
}

func (client *fakeLegacyTagTrendRedis) ZRange(_ context.Context, key string, _, _ int64) *redis.StringSliceCmd {
	client.zRangeKeys = append(client.zRangeKeys, key)
	return redis.NewStringSliceResult(client.allowed, client.allowedErr)
}

func (client *fakeLegacyTagTrendRedis) Del(_ context.Context, keys ...string) *redis.IntCmd {
	client.deletedKeys = append(client.deletedKeys, keys...)
	return redis.NewIntResult(int64(len(keys)), client.delErr)
}

func TestReadLegacyTagTrendRowsUsesUnnamespacedKeys(t *testing.T) {
	client := &fakeLegacyTagTrendRedis{
		all:     []redis.Z{{Member: "102", Score: 2.5}, {Member: "101", Score: 9}},
		allowed: []string{"101"},
	}
	rows, err := readLegacyTagTrendRows(context.Background(), client)
	if err != nil {
		t.Fatal(err)
	}
	want := []migrate.LegacyTagTrendRow{
		{TagID: 102, Score: 2.5, Language: ""},
		{TagID: 101, Score: 9, Allowed: true, Language: ""},
	}
	if !reflect.DeepEqual(rows, want) {
		t.Fatalf("legacy rows = %#v, want %#v", rows, want)
	}
	if !reflect.DeepEqual(client.zRangeScores, []string{"trending_tags:all"}) || !reflect.DeepEqual(client.zRangeKeys, []string{"trending_tags:allowed"}) {
		t.Fatalf("Redis reads used scores=%#v allowed=%#v", client.zRangeScores, client.zRangeKeys)
	}
}

func TestReadLegacyTagTrendRowsRejectsMalformedMember(t *testing.T) {
	client := &fakeLegacyTagTrendRedis{all: []redis.Z{{Member: "not-a-tag", Score: 2}}}
	if _, err := readLegacyTagTrendRows(context.Background(), client); err == nil {
		t.Fatal("malformed tag ID error = nil")
	}
}

func TestWireLegacyTagTrendBackfillDeletesOnlyExactLegacyKeys(t *testing.T) {
	client := &fakeLegacyTagTrendRedis{}
	options := migrate.Options{}
	wireLegacyTagTrendBackfill(&options, client)
	if options.Mastodon44TagTrendBackfill == nil || options.Mastodon44TagTrendBackfillPostCommit == nil {
		t.Fatal("tag trend migration callbacks were not wired")
	}
	if err := options.Mastodon44TagTrendBackfillPostCommit(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"trending_tags:allowed", "trending_tags:all"}
	if !reflect.DeepEqual(client.deletedKeys, want) {
		t.Fatalf("deleted keys = %#v, want %#v", client.deletedKeys, want)
	}
	client.delErr = errors.New("injected Redis delete failure")
	if err := options.Mastodon44TagTrendBackfillPostCommit(context.Background()); err == nil {
		t.Fatal("Redis cleanup error = nil")
	}
}

func TestConfigureLegacyTagTrendBackfillHonorsExplicitSkip(t *testing.T) {
	options := migrate.Options{Mastodon44SkipTagTrendBackfill: true}
	client, err := configureLegacyTagTrendBackfill("not a Redis URL", &options)
	if err != nil || client != nil {
		t.Fatalf("explicit skip configured client=%v err=%v", client, err)
	}
	if options.Mastodon44TagTrendBackfill != nil || options.Mastodon44TagTrendBackfillPostCommit != nil {
		t.Fatal("explicit skip must not wire Redis callbacks")
	}
}

func TestLegacyTagTrendRedisOptionsSupportsUnixSocket(t *testing.T) {
	options, err := legacyTagTrendRedisOptions("unix:///run/redis/redis.sock")
	if err != nil {
		t.Fatal(err)
	}
	if options.Network != "unix" || options.Addr != "/run/redis/redis.sock" {
		t.Fatalf("unix Redis options = %#v", options)
	}
}
