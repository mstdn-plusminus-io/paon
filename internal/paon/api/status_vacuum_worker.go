package api

import (
	"context"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

const (
	statusVacuumWorkerInterval = 24 * time.Hour
	statusVacuumBatchSize      = 1000
)

func (s *Server) runStatusVacuumWorker(ctx context.Context) {
	ticker := time.NewTicker(statusVacuumWorkerInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			s.runSchedulerWithRedisLock(ctx, "vacuum_scheduler:statuses_vacuum", 24*time.Hour, func() {
				s.vacuumRemoteStatuses(ctx, now.UTC())
			})
		}
	}
}

func (s *Server) vacuumRemoteStatuses(ctx context.Context, now time.Time) int {
	if s == nil || s.db == nil {
		return 0
	}
	days, ok := s.contentCacheRetentionDays()
	if !ok {
		return 0
	}
	cutoffID := mastodonSnowflakeIDAt(now.Add(-time.Duration(days)*24*time.Hour), false)
	deleted := 0
	for {
		var statuses []models.Status
		if err := s.db.WithContext(ctx).
			Model(&models.Status{}).
			Select("statuses.id, statuses.visibility, statuses.conversation_id").
			Joins("JOIN accounts ON accounts.id = statuses.account_id").
			Where("accounts.domain IS NOT NULL AND accounts.domain <> ''").
			Where("statuses.deleted_at IS NULL").
			Where("statuses.id < ?", cutoffID).
			Order("statuses.id ASC").
			Limit(statusVacuumBatchSize).
			Find(&statuses).Error; err != nil {
			return deleted
		}
		if len(statuses) == 0 {
			return deleted
		}
		ids := make([]int64, 0, len(statuses))
		err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			for _, status := range statuses {
				ids = append(ids, status.ID)
				if err := unlinkDirectStatusFromConversations(ctx, tx, status, now); err != nil {
					return err
				}
			}
			return tx.WithContext(ctx).Delete(&models.Status{}, ids).Error
		})
		if err != nil {
			return deleted
		}
		deleted += len(ids)
	}
}

func (s *Server) contentCacheRetentionDays() (int, bool) {
	if s == nil {
		return 0, false
	}
	return parsePositiveRetentionDays(s.settingValue("content_cache_retention_period", ""))
}

func unlinkDirectStatusFromConversations(ctx context.Context, database *gorm.DB, status models.Status, now time.Time) error {
	if database == nil || status.Visibility != 3 || !status.ConversationID.Valid {
		return nil
	}
	if err := database.WithContext(ctx).Exec(`
UPDATE account_conversations
SET status_ids = array_remove(status_ids, ?),
    last_status_id = (SELECT MAX(remaining.id) FROM unnest(array_remove(status_ids, ?)) AS remaining(id)),
    updated_at = ?
WHERE conversation_id = ? AND ? = ANY(status_ids)
`, status.ID, status.ID, now, status.ConversationID.Int64, status.ID).Error; err != nil {
		return err
	}
	return database.WithContext(ctx).Exec(`
DELETE FROM account_conversations
WHERE conversation_id = ? AND array_length(status_ids, 1) IS NULL
`, status.ConversationID.Int64).Error
}
