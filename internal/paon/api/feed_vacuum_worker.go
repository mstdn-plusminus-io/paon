package api

import (
	"context"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

const (
	feedVacuumWorkerInterval = 24 * time.Hour
	feedVacuumBatchSize      = 1000
	defaultUserActiveDays    = 7
)

func (s *Server) runFeedVacuumWorker(ctx context.Context) {
	ticker := time.NewTicker(feedVacuumWorkerInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			s.runSchedulerWithRedisLock(ctx, "vacuum_scheduler:feeds_vacuum", 24*time.Hour, func() {
				s.vacuumInactiveFeeds(ctx, now.UTC())
			})
		}
	}
}

func (s *Server) vacuumInactiveFeeds(ctx context.Context, now time.Time) int {
	if s == nil || s.db == nil {
		return 0
	}
	cutoff := now.Add(-time.Duration(userActiveDays()) * 24 * time.Hour)
	cleaned := s.vacuumInactiveHomeFeeds(ctx, cutoff)
	cleaned += s.vacuumInactiveListFeeds(ctx, cutoff)
	return cleaned
}

func (s *Server) vacuumInactiveHomeFeeds(ctx context.Context, cutoff time.Time) int {
	cleaned := 0
	lastID := int64(0)
	for {
		var rows []struct {
			ID        int64
			AccountID int64
		}
		if err := s.db.WithContext(ctx).
			Model(&models.User{}).
			Select("id, account_id").
			Where("id > ? AND confirmed_at IS NOT NULL AND current_sign_in_at < ?", lastID, cutoff).
			Order("id ASC").
			Limit(feedVacuumBatchSize).
			Scan(&rows).Error; err != nil {
			return cleaned
		}
		if len(rows) == 0 {
			return cleaned
		}
		for _, row := range rows {
			_ = s.clearHomeFeedCacheContext(ctx, row.AccountID)
			lastID = row.ID
			cleaned++
		}
	}
}

func (s *Server) vacuumInactiveListFeeds(ctx context.Context, cutoff time.Time) int {
	cleaned := 0
	lastID := int64(0)
	for {
		var listIDs []int64
		if err := s.db.WithContext(ctx).
			Model(&models.List{}).
			Joins("JOIN users ON users.account_id = lists.account_id").
			Where("lists.id > ? AND users.confirmed_at IS NOT NULL AND users.current_sign_in_at < ?", lastID, cutoff).
			Order("lists.id ASC").
			Limit(feedVacuumBatchSize).
			Pluck("lists.id", &listIDs).Error; err != nil {
			return cleaned
		}
		if len(listIDs) == 0 {
			return cleaned
		}
		for _, listID := range listIDs {
			_ = s.clearListFeedCacheContext(ctx, listID)
			lastID = listID
			cleaned++
		}
	}
}

// vacuumOrphanedFeeds implements the deliberately more aggressive
// `tootctl feeds vacuum` behavior. Unlike the daily scheduler above it starts
// from Redis, so keys left behind by deleted users/lists are discoverable.
func (s *Server) vacuumOrphanedFeeds(ctx context.Context, now time.Time) int {
	cutoff := now.Add(-time.Duration(userActiveDays()) * 24 * time.Hour)
	return s.vacuumOrphanedRedisFeeds(ctx, "home", cutoff) + s.vacuumOrphanedRedisFeeds(ctx, "list", cutoff)
}

func (s *Server) vacuumOrphanedRedisFeeds(ctx context.Context, feedType string, cutoff time.Time) int {
	if s == nil || s.db == nil || (feedType != "home" && feedType != "list") {
		return 0
	}
	prefix := redisConfig(s.cfg).prefix
	pattern := prefix + "feed:" + feedType + ":*"
	ids := map[int64]struct{}{}
	cursor := "0"
	for {
		value, err := s.redisCommand(ctx, "SCAN", cursor, "MATCH", pattern, "COUNT", strconv.Itoa(feedVacuumBatchSize))
		if err != nil {
			return 0
		}
		next, keys, ok := redisScanKeys(value)
		if !ok {
			return 0
		}
		for _, key := range keys {
			if id, ok := orphanedFeedIDFromRedisKey(prefix, feedType, key); ok {
				ids[id] = struct{}{}
			}
		}
		if next == "0" {
			break
		}
		cursor = next
	}
	allIDs := make([]int64, 0, len(ids))
	for id := range ids {
		allIDs = append(allIDs, id)
	}
	cleaned := 0
	for start := 0; start < len(allIDs); start += feedVacuumBatchSize {
		end := min(start+feedVacuumBatchSize, len(allIDs))
		batch := allIDs[start:end]
		var activeIDs []int64
		query := s.db.WithContext(ctx).Table("users").Where("users.confirmed_at IS NOT NULL AND users.current_sign_in_at >= ?", cutoff)
		if feedType == "home" {
			query = query.Where("users.account_id IN ?", batch).Pluck("users.account_id", &activeIDs)
		} else {
			query = query.Joins("JOIN lists ON lists.account_id = users.account_id").Where("lists.id IN ?", batch).Pluck("lists.id", &activeIDs)
		}
		if query.Error != nil {
			return cleaned
		}
		active := make(map[int64]struct{}, len(activeIDs))
		for _, id := range activeIDs {
			active[id] = struct{}{}
		}
		for _, id := range batch {
			if _, ok := active[id]; ok {
				continue
			}
			var err error
			if feedType == "home" {
				err = s.clearHomeFeedCacheContext(ctx, id)
			} else {
				err = s.clearListFeedCacheContext(ctx, id)
			}
			if err == nil {
				cleaned++
			}
		}
	}
	return cleaned
}

func orphanedFeedIDFromRedisKey(prefix string, feedType string, key string) (int64, bool) {
	raw, ok := strings.CutPrefix(key, prefix+"feed:"+feedType+":")
	if !ok {
		return 0, false
	}
	if index := strings.IndexByte(raw, ':'); index >= 0 {
		raw = raw[:index]
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	return id, err == nil && id > 0
}

func userActiveDays() int {
	raw, ok := os.LookupEnv("USER_ACTIVE_DAYS")
	if !ok {
		return defaultUserActiveDays
	}
	raw = strings.TrimLeftFunc(raw, unicode.IsSpace)
	sign := 1
	if strings.HasPrefix(raw, "-") {
		sign = -1
		raw = raw[1:]
	} else if strings.HasPrefix(raw, "+") {
		raw = raw[1:]
	}
	i := 0
	for i < len(raw) && raw[i] >= '0' && raw[i] <= '9' {
		i++
	}
	if i == 0 {
		return 0
	}
	days, err := strconv.Atoi(raw[:i])
	if err != nil {
		return 0
	}
	return sign * days
}
