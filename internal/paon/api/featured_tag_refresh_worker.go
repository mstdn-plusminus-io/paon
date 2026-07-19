package api

import (
	"context"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

const (
	featuredTagRefreshWorkerInterval = 24 * time.Hour
	featuredTagRefreshBatchSize      = 100
)

func (s *Server) runFeaturedTagRefreshWorker(ctx context.Context) {
	ticker := time.NewTicker(featuredTagRefreshWorkerInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			s.runSchedulerWithRedisLock(ctx, "featured_tag_refresh_scheduler", featuredTagRefreshWorkerInterval, func() {
				s.refreshFeaturedTagStats(ctx, now.UTC())
			})
		}
	}
}

func (s *Server) refreshFeaturedTagStats(ctx context.Context, now time.Time) int {
	if s == nil || s.db == nil {
		return 0
	}
	refreshed := 0
	var lastID int64
	for {
		var tags []models.FeaturedTag
		if err := s.db.WithContext(ctx).
			Select("id", "account_id", "tag_id").
			Where("id > ? AND tag_id IS NOT NULL", lastID).
			Order("id ASC").
			Limit(featuredTagRefreshBatchSize).
			Find(&tags).Error; err != nil {
			return refreshed
		}
		if len(tags) == 0 {
			return refreshed
		}
		for _, tag := range tags {
			lastID = tag.ID
			stats, err := featuredStats(s.db.WithContext(ctx), tag.AccountID, tag.TagID)
			if err != nil {
				continue
			}
			if err := s.db.WithContext(ctx).
				Model(&models.FeaturedTag{}).
				Where("id = ?", tag.ID).
				Updates(map[string]any{
					"statuses_count": stats.StatusesCount,
					"last_status_at": stats.LastStatusAt,
					"updated_at":     now,
				}).Error; err == nil {
				refreshed++
			}
		}
	}
}
