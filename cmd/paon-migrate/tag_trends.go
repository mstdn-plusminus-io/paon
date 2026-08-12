package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/mstdn-plusminus-io/paon/internal/paon/migrate"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

const (
	legacyTagTrendsAllKey     = "trending_tags:all"
	legacyTagTrendsAllowedKey = "trending_tags:allowed"
)

type legacyTagTrendRedisClient interface {
	ZRangeWithScores(context.Context, string, int64, int64) *redis.ZSliceCmd
	ZRange(context.Context, string, int64, int64) *redis.StringSliceCmd
	Del(context.Context, ...string) *redis.IntCmd
}

func configureLegacyTagTrendBackfill(rawRedisURL string, options *migrate.Options) (*redis.Client, error) {
	if options == nil {
		return nil, errors.New("migration options are not configured")
	}
	if options.Mastodon44SkipTagTrendBackfill || strings.TrimSpace(rawRedisURL) == "" {
		return nil, nil
	}
	redisOptions, err := legacyTagTrendRedisOptions(rawRedisURL)
	if err != nil {
		return nil, err
	}
	client := redis.NewClient(redisOptions)
	wireLegacyTagTrendBackfill(options, client)
	return client, nil
}

func legacyTagTrendRedisOptions(rawRedisURL string) (*redis.Options, error) {
	rawRedisURL = strings.TrimSpace(rawRedisURL)
	if strings.HasPrefix(rawRedisURL, "unix://") {
		path := strings.TrimSpace(strings.TrimPrefix(rawRedisURL, "unix://"))
		if path == "" {
			return nil, errors.New("parse REDIS_URL for Mastodon 4.4 tag trend migration: unix socket path is required")
		}
		return &redis.Options{Network: "unix", Addr: path}, nil
	}
	options, err := redis.ParseURL(rawRedisURL)
	if err != nil {
		return nil, fmt.Errorf("parse REDIS_URL for Mastodon 4.4 tag trend migration: %w", err)
	}
	return options, nil
}

func wireLegacyTagTrendBackfill(options *migrate.Options, client legacyTagTrendRedisClient) {
	options.Mastodon44TagTrendBackfill = func(ctx context.Context, database *gorm.DB) error {
		rows, err := readLegacyTagTrendRows(ctx, client)
		if err != nil {
			return err
		}
		if options.Logf != nil {
			options.Logf("Mastodon 4.4 tag trend Redis backfill read %d row(s) from unnamespaced legacy keys", len(rows))
		}
		return migrate.UpsertLegacyTagTrendRows(ctx, database, rows)
	}
	options.Mastodon44TagTrendBackfillPostCommit = func(ctx context.Context) error {
		if _, err := client.Del(ctx, legacyTagTrendsAllowedKey, legacyTagTrendsAllKey).Result(); err != nil {
			return fmt.Errorf("delete unnamespaced keys %s and %s: %w", legacyTagTrendsAllowedKey, legacyTagTrendsAllKey, err)
		}
		if options.Logf != nil {
			options.Logf("Mastodon 4.4 tag trend Redis backfill removed unnamespaced legacy keys after PostgreSQL commit")
		}
		return nil
	}
}

func readLegacyTagTrendRows(ctx context.Context, client legacyTagTrendRedisClient) ([]migrate.LegacyTagTrendRow, error) {
	if client == nil {
		return nil, errors.New("legacy tag trend Redis client is not configured")
	}
	all, err := client.ZRangeWithScores(ctx, legacyTagTrendsAllKey, 0, -1).Result()
	if err != nil {
		return nil, fmt.Errorf("read unnamespaced Redis key %s: %w", legacyTagTrendsAllKey, err)
	}
	allowedMembers, err := client.ZRange(ctx, legacyTagTrendsAllowedKey, 0, -1).Result()
	if err != nil {
		return nil, fmt.Errorf("read unnamespaced Redis key %s: %w", legacyTagTrendsAllowedKey, err)
	}
	allowed := make(map[string]struct{}, len(allowedMembers))
	for _, member := range allowedMembers {
		allowed[member] = struct{}{}
	}
	rows := make([]migrate.LegacyTagTrendRow, 0, len(all))
	for index, item := range all {
		member, ok := item.Member.(string)
		if !ok {
			return nil, fmt.Errorf("Redis key %s member %d has unsupported type %T", legacyTagTrendsAllKey, index+1, item.Member)
		}
		tagID, err := strconv.ParseInt(member, 10, 64)
		if err != nil || tagID <= 0 {
			return nil, fmt.Errorf("Redis key %s member %q is not a positive tag ID", legacyTagTrendsAllKey, member)
		}
		_, isAllowed := allowed[member]
		rows = append(rows, migrate.LegacyTagTrendRow{TagID: tagID, Score: item.Score, Allowed: isAllowed, Language: ""})
	}
	return rows, nil
}
