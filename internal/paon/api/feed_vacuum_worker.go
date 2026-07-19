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
