package api

import (
	"context"
	"database/sql"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

const (
	userCleanupWorkerInterval   = 24 * time.Hour
	userCleanupBatchSize        = 1000
	unconfirmedUserTTL          = 48 * time.Hour
	discardedStatusRetentionTTL = 30 * 24 * time.Hour
)

func (s *Server) runUserCleanupWorker(ctx context.Context) {
	ticker := time.NewTicker(userCleanupWorkerInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			s.runSchedulerWithRedisLock(ctx, "user_cleanup_scheduler", 24*time.Hour, func() {
				s.cleanupUsersAndDiscardedStatuses(ctx, now.UTC())
			})
		}
	}
}

func (s *Server) cleanupUsersAndDiscardedStatuses(ctx context.Context, now time.Time) int {
	if s == nil || s.db == nil {
		return 0
	}
	cleaned := s.cleanupUnconfirmedUsers(ctx, now.Add(-unconfirmedUserTTL))
	cleaned += s.cleanupDiscardedStatuses(ctx, now.Add(-discardedStatusRetentionTTL))
	return cleaned
}

func (s *Server) cleanupUnconfirmedUsers(ctx context.Context, cutoff time.Time) int {
	cleaned := 0
	for {
		var rows []struct {
			ID        int64
			AccountID int64
		}
		if err := s.db.WithContext(ctx).
			Model(&models.User{}).
			Select("id, account_id").
			Where("confirmed_at IS NULL AND confirmation_sent_at <= ?", cutoff).
			Order("id ASC").
			Limit(userCleanupBatchSize).
			Scan(&rows).Error; err != nil {
			return cleaned
		}
		if len(rows) == 0 {
			return cleaned
		}
		userIDs := make([]int64, 0, len(rows))
		accountIDs := make([]int64, 0, len(rows))
		for _, row := range rows {
			userIDs = append(userIDs, row.ID)
			accountIDs = append(accountIDs, row.AccountID)
		}
		var accounts []models.Account
		if err := s.db.WithContext(ctx).
			Where("id IN ?", accountIDs).
			Where("avatar_file_name IS NOT NULL OR header_file_name IS NOT NULL").
			Find(&accounts).Error; err != nil {
			return cleaned
		}
		for _, account := range accounts {
			s.removeAccountImageObjects(account)
			s.removeAccountLocalImageFiles(account.ID)
		}
		if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := tx.Where("target_account_id IN ?", accountIDs).Delete(&models.AccountModerationNote{}).Error; err != nil {
				return err
			}
			if err := tx.Where("user_id IN ?", userIDs).Delete(&models.WebauthnCredential{}).Error; err != nil {
				return err
			}
			if err := tx.Delete(&models.Account{}, accountIDs).Error; err != nil {
				return err
			}
			return tx.Delete(&models.User{}, userIDs).Error
		}); err != nil {
			return cleaned
		}
		cleaned += len(rows)
	}
}

func (s *Server) cleanupDiscardedStatuses(ctx context.Context, cutoff time.Time) int {
	cleaned := 0
	lastID := int64(0)
	for {
		var statuses []models.Status
		if err := s.db.WithContext(ctx).
			Model(&models.Status{}).
			Select("id").
			Where("id > ? AND deleted_at IS NOT NULL AND deleted_at <= ?", lastID, cutoff).
			Order("id ASC").
			Limit(userCleanupBatchSize).
			Find(&statuses).Error; err != nil {
			return cleaned
		}
		if len(statuses) == 0 {
			return cleaned
		}
		ids := make([]int64, 0, len(statuses))
		for _, status := range statuses {
			ids = append(ids, status.ID)
			lastID = status.ID
		}
		options := asynqRemovalPayload{Immediate: true, SkipStreaming: true}
		if s.enqueueRemovalTasksForStatusIDs(ids, options) {
			cleaned += len(ids)
			continue
		}
		for _, id := range ids {
			purgeIDs := s.discardedStatusAndUnreportedReblogIDs(ctx, id)
			if len(purgeIDs) == 0 {
				purgeIDs = []int64{id}
			}
			s.applyAdminDeletedStatusSideEffects(ctx, s.db, purgeIDs)
			if err := s.syncPermanentStatusRemovalCounters(ctx, purgeIDs, time.Now().UTC()); err != nil {
				return cleaned
			}
			if err := s.purgeDiscardedStatuses(ctx, purgeIDs); err != nil {
				return cleaned
			}
		}
		cleaned += len(ids)
	}
}

func (s *Server) purgeDiscardedStatus(ctx context.Context, statusID int64) error {
	return s.purgeDiscardedStatuses(ctx, []int64{statusID})
}

func (s *Server) purgeDiscardedStatuses(ctx context.Context, statusIDs []int64) error {
	if s == nil || s.db == nil {
		return nil
	}
	statusIDs = uniqueInt64s(statusIDs)
	if len(statusIDs) == 0 {
		return nil
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var attachments []models.MediaAttachment
		if err := tx.Where("status_id IN ?", statusIDs).Find(&attachments).Error; err != nil {
			return err
		}
		for _, attachment := range attachments {
			s.removeMediaAttachmentLocalFiles(attachment)
		}
		if len(attachments) > 0 {
			attachmentIDs := make([]int64, 0, len(attachments))
			for _, attachment := range attachments {
				attachmentIDs = append(attachmentIDs, attachment.ID)
			}
			if err := tx.Delete(&models.MediaAttachment{}, attachmentIDs).Error; err != nil {
				return err
			}
		}
		return tx.Delete(&models.Status{}, statusIDs).Error
	})
}

func (s *Server) discardedStatusAndUnreportedReblogIDs(ctx context.Context, statusID int64) []int64 {
	if s == nil || s.db == nil || statusID == 0 {
		return nil
	}
	var ids []int64
	_ = s.db.WithContext(ctx).Unscoped().
		Model(&models.Status{}).
		Where("id = ? OR (reblog_of_id = ? AND NOT EXISTS (SELECT 1 FROM reports WHERE reports.target_account_id = statuses.account_id AND reports.action_taken_at IS NULL AND statuses.id = ANY(reports.status_ids)))", statusID, statusID).
		Pluck("id", &ids).Error
	return uniqueInt64s(ids)
}

func (s *Server) discardedUnreportedReblogIDs(ctx context.Context, status models.Status) []int64 {
	if s == nil || s.db == nil || status.ID == 0 || status.ReblogOfID.Valid {
		return nil
	}
	var ids []int64
	_ = s.db.WithContext(ctx).Unscoped().
		Model(&models.Status{}).
		Where("reblog_of_id = ? AND deleted_at IS NOT NULL AND NOT EXISTS (SELECT 1 FROM reports WHERE reports.target_account_id = statuses.account_id AND reports.action_taken_at IS NULL AND statuses.id = ANY(reports.status_ids))", status.ID).
		Pluck("id", &ids).Error
	return uniqueInt64s(ids)
}

func (s *Server) syncPermanentStatusRemovalCounters(ctx context.Context, statusIDs []int64, now time.Time) error {
	if s == nil || s.db == nil {
		return nil
	}
	statusIDs = uniqueInt64s(statusIDs)
	if len(statusIDs) == 0 {
		return nil
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		type removedStatusCounterTarget struct {
			ID          int64
			AccountID   int64
			InReplyToID sql.NullInt64
			ReblogOfID  sql.NullInt64
			Visibility  int
		}
		var statuses []removedStatusCounterTarget
		if err := tx.Unscoped().
			Model(&models.Status{}).
			Select("id, account_id, in_reply_to_id, reblog_of_id, visibility").
			Where("id IN ?", statusIDs).
			Find(&statuses).Error; err != nil {
			return err
		}
		var accountIDs []int64
		var statusStatIDs []int64
		for _, status := range statuses {
			if statusCountsTowardAccountStats(status.Visibility) {
				accountIDs = append(accountIDs, status.AccountID)
			}
			if status.InReplyToID.Valid && statusCountsTowardReplyStats(status.Visibility) {
				statusStatIDs = append(statusStatIDs, status.InReplyToID.Int64)
			}
			if status.ReblogOfID.Valid {
				statusStatIDs = append(statusStatIDs, status.ReblogOfID.Int64)
			}
			statusStatIDs = append(statusStatIDs, status.ID)
		}
		accountIDs = uniqueInt64s(accountIDs)
		if len(accountIDs) > 0 {
			if err := tx.Exec(`
UPDATE account_stats
SET statuses_count = (
    SELECT COUNT(*) FROM statuses
    WHERE statuses.account_id = account_stats.account_id
      AND statuses.deleted_at IS NULL
      AND statuses.visibility <> ?
),
    updated_at = ?
WHERE account_id IN ?
`, 3, now, accountIDs).Error; err != nil {
				return err
			}
		}
		statusStatIDs = uniqueInt64s(statusStatIDs)
		if len(statusStatIDs) > 0 {
			if err := tx.Exec(`
UPDATE status_stats
SET replies_count = (
    SELECT COUNT(*) FROM statuses
    WHERE statuses.in_reply_to_id = status_stats.status_id
      AND statuses.deleted_at IS NULL
      AND statuses.visibility IN ?
),
    reblogs_count = (
    SELECT COUNT(*) FROM statuses
    WHERE statuses.reblog_of_id = status_stats.status_id
      AND statuses.deleted_at IS NULL
),
    updated_at = ?
WHERE status_id IN ?
`, []int{0, 1}, now, statusStatIDs).Error; err != nil {
				return err
			}
		}
		return nil
	})
}
