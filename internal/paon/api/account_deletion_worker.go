package api

import (
	"context"
	"time"

	"github.com/hibiken/asynq"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

const (
	accountDeletionWorkerInterval = time.Minute
	accountDeletionDelay          = 30 * 24 * time.Hour
	accountDeletionMaxPerRun      = 10
	accountDeletionMaxPullQueue   = 50
)

func (s *Server) runAccountDeletionWorker(ctx context.Context) {
	ticker := time.NewTicker(accountDeletionWorkerInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			s.runSchedulerWithRedisLock(ctx, "suspended_user_cleanup_scheduler", 24*time.Hour, func() {
				s.processDueAccountDeletionRequests(ctx, accountDeletionMaxPerRun, now.UTC())
			})
		}
	}
}

func (s *Server) processDueAccountDeletionRequests(ctx context.Context, limit int, now time.Time) int {
	if s == nil || s.db == nil || limit <= 0 {
		return 0
	}
	if s.accountDeletionPullQueueOverloaded(ctx) {
		return 0
	}
	var requests []models.AccountDeletionRequest
	if err := s.db.WithContext(ctx).
		Preload("Account").
		Where("created_at < ?", now.Add(-accountDeletionDelay)).
		Order("id ASC").
		Limit(limit).
		Find(&requests).Error; err != nil {
		return 0
	}
	processed := 0
	for _, request := range requests {
		if !request.AccountID.Valid || request.AccountID.Int64 == 0 {
			continue
		}
		accountID := request.AccountID.Int64
		if s.enqueueAdminAccountDeletionTask(accountID) {
			processed++
			continue
		}
		if err := s.runAdminAccountDeletionWorkerEffects(ctx, accountID, now); err == nil {
			processed++
		}
	}
	return processed
}

func (s *Server) accountDeletionPullQueueOverloaded(ctx context.Context) bool {
	if s == nil {
		return false
	}
	_ = ctx
	inspector := asynq.NewInspector(asynqRedisOpt(s.cfg))
	defer inspector.Close()
	info, err := inspector.GetQueueInfo(s.asynqQueue(asynqQueuePull))
	if err != nil {
		return false
	}
	return info.Size > accountDeletionMaxPullQueue
}

func (s *Server) purgeAccountDeletionRequest(ctx context.Context, accountID int64, now time.Time) error {
	return s.purgeAccountDeletionRequestWithOptions(ctx, accountID, now, accountDeletionPurgeOptions{})
}

type accountDeletionPurgeOptions struct {
	DestroyLocalUser bool
}

func (s *Server) purgeAccountDeletionRequestWithOptions(ctx context.Context, accountID int64, now time.Time, options accountDeletionPurgeOptions) error {
	if s == nil || s.db == nil || accountID == 0 {
		return nil
	}
	disabledUser := false
	destroyedUser := false
	fileCleanup := accountDeletionFileCleanup{}
	var statusDeleteBroadcasts []batchedAccountDeletionStatusDelete
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		accountIDs := adminSingleAccountIDSubquery(tx, accountID)
		destroyRows, err := accountDeletionShouldDestroyRows(tx, accountID)
		if err != nil {
			return err
		}
		reportedStatusIDs, err := accountDeletionReportedStatusIDs(tx, accountID)
		if err != nil {
			return err
		}
		statusIDs, reblogIDs := accountDeletionStatusIDQueries(tx, accountIDs, reportedStatusIDs)
		statusDeleteBroadcasts, err = s.prepareBatchedAccountDeletionStatusDeletes(ctx, tx, now, statusIDs, reblogIDs)
		if err != nil {
			return err
		}
		if err := s.clearAdminSuspendedAccountFeedCaches(ctx, tx, accountIDs); err != nil {
			return err
		}
		if err := s.clearAccountOwnedFeedCaches(ctx, tx, accountIDs); err != nil {
			return err
		}
		preparedCleanup, err := s.prepareAccountDeletionLocalFiles(tx, accountIDs, reportedStatusIDs, now)
		if err != nil {
			return err
		}
		fileCleanup.merge(preparedCleanup)
		if !destroyRows {
			if err := tx.Model(&models.Account{}).Where("id = ?", accountID).Updates(accountDeletionProfileUpdates(now)).Error; err != nil {
				return err
			}
			if options.DestroyLocalUser {
				destroyed, err := destroyAccountDeletionUser(tx, accountID)
				if err != nil {
					return err
				}
				destroyedUser = destroyed
			} else {
				disabled, err := disableAccountDeletionUser(tx, accountID, now)
				if err != nil {
					return err
				}
				disabledUser = disabled
			}
			if err := tx.Model(&models.AccountStat{}).Where("account_id = ?", accountID).Updates(map[string]any{
				"statuses_count":  0,
				"followers_count": 0,
				"following_count": 0,
				"updated_at":      now,
			}).Error; err != nil {
				return err
			}
		}
		if err := purgeAdminDomainSuspendedAccountAssociations(tx, accountIDs, now); err != nil {
			return err
		}
		if err := s.purgeAccountDeletionInteractionAssociations(ctx, tx, accountIDs, now); err != nil {
			return err
		}
		if err := purgeAccountDeletionExtraAssociations(tx, accountIDs, reportedStatusIDs, destroyRows); err != nil {
			return err
		}
		if err := s.tombstoneAccountDeletionStatuses(tx, accountIDs, reportedStatusIDs, now); err != nil {
			return err
		}
		if err := tx.Where("account_id = ?", accountID).Delete(&models.AccountDeletionRequest{}).Error; err != nil {
			return err
		}
		if destroyRows {
			return tx.Delete(&models.Account{}, accountID).Error
		}
		return nil
	})
	if err != nil {
		return err
	}
	fileCleanup.run(s)
	publishCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	s.publishPreparedBatchedAccountDeletionStatusDeletes(publishCtx, statusDeleteBroadcasts)
	if disabledUser || destroyedUser {
		s.publishStreamingKill(accountID, nil)
	}
	return nil
}

func (s *Server) runOwnAccountDeletionWorkerEffects(ctx context.Context, accountID int64, now time.Time) error {
	if s == nil || s.db == nil || accountID == 0 {
		return nil
	}
	var account models.Account
	if err := s.db.WithContext(ctx).Where("id = ?", accountID).First(&account).Error; err != nil {
		return err
	}
	if !account.SuspendedAt.Valid {
		return nil
	}
	_ = s.deliverActivityPubAccountDelete(account)
	return s.purgeAccountDeletionRequestWithOptions(ctx, account.ID, now, accountDeletionPurgeOptions{DestroyLocalUser: true})
}

func accountDeletionShouldDestroyRows(database *gorm.DB, accountID int64) (bool, error) {
	var account models.Account
	if err := database.Select("id", "domain", "suspension_origin").Where("id = ?", accountID).First(&account).Error; err != nil {
		return false, err
	}
	return !account.Local() && account.SuspensionOrigin.Valid && account.SuspensionOrigin.Int64 == 1, nil
}

func (s *Server) deleteRejectedLocalAccountRows(ctx context.Context, actorAccountID int64, account *models.Account, now time.Time) error {
	if s == nil || s.db == nil || account == nil || account.ID == 0 {
		return nil
	}
	fileCleanup := accountDeletionFileCleanup{}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		accountIDs := adminSingleAccountIDSubquery(tx, account.ID)
		reportedStatusIDs, err := accountDeletionReportedStatusIDs(tx, account.ID)
		if err != nil {
			return err
		}
		if err := s.clearAdminSuspendedAccountFeedCaches(ctx, tx, accountIDs); err != nil {
			return err
		}
		if err := s.clearAccountOwnedFeedCaches(ctx, tx, accountIDs); err != nil {
			return err
		}
		preparedCleanup, err := s.prepareAccountDeletionLocalFiles(tx, accountIDs, reportedStatusIDs, now)
		if err != nil {
			return err
		}
		fileCleanup.merge(preparedCleanup)
		if err := purgeAdminDomainSuspendedAccountAssociations(tx, accountIDs, now); err != nil {
			return err
		}
		if err := s.purgeAccountDeletionInteractionAssociations(ctx, tx, accountIDs, now); err != nil {
			return err
		}
		if err := purgeAccountDeletionExtraAssociations(tx, accountIDs, reportedStatusIDs, true); err != nil {
			return err
		}
		if err := s.logAdminAccountAction(tx, actorAccountID, account, "reject", now); err != nil {
			return err
		}
		if account.User.ID != 0 {
			if err := tx.Where("user_id = ?", account.User.ID).Delete(&models.WebauthnCredential{}).Error; err != nil {
				return err
			}
		}
		if err := tx.Delete(&models.Account{}, account.ID).Error; err != nil {
			return err
		}
		if account.User.ID != 0 {
			return tx.Delete(&models.User{}, account.User.ID).Error
		}
		return nil
	})
	if err != nil {
		return err
	}
	fileCleanup.run(s)
	return nil
}

func (s *Server) clearAccountOwnedFeedCaches(ctx context.Context, database *gorm.DB, accountIDs *gorm.DB) error {
	if s == nil || database == nil || accountIDs == nil {
		return nil
	}
	var localAccountIDs []int64
	if err := database.Model(&models.Account{}).
		Select("id").
		Where("id IN (?)", accountIDs).
		Where("(domain IS NULL OR domain = '')").
		Pluck("id", &localAccountIDs).Error; err != nil {
		return err
	}
	if len(localAccountIDs) == 0 {
		return nil
	}
	for _, accountID := range localAccountIDs {
		_ = s.clearHomeFeedCacheContext(ctx, accountID)
	}
	var listIDs []int64
	if err := database.Model(&models.List{}).
		Where("account_id IN ?", localAccountIDs).
		Pluck("id", &listIDs).Error; err != nil {
		return err
	}
	for _, listID := range listIDs {
		_ = s.clearListFeedCacheContext(ctx, listID)
	}
	return nil
}

func (s *Server) deleteRemoteActivityPubActorNow(ctx context.Context, account *models.Account, now time.Time) error {
	return s.deleteRemoteAccountNow(ctx, account, now)
}

func (s *Server) deleteRemoteGoneAccountNow(ctx context.Context, account *models.Account, now time.Time) error {
	return s.deleteRemoteAccountNow(ctx, account, now)
}

func (s *Server) deleteRemoteAccountNow(ctx context.Context, account *models.Account, now time.Time) error {
	if s == nil || s.db == nil || account == nil || account.ID == 0 || account.Local() {
		return nil
	}
	if err := s.db.WithContext(ctx).Model(&models.Account{}).Where("id = ?", account.ID).Updates(map[string]any{
		"suspended_at":      now,
		"suspension_origin": int64(1),
		"updated_at":        now,
	}).Error; err != nil {
		return err
	}
	return s.purgeAccountDeletionRequest(ctx, account.ID, now)
}

func disableAccountDeletionUser(database *gorm.DB, accountID int64, now time.Time) (bool, error) {
	if database == nil || accountID == 0 {
		return false, nil
	}
	var userIDs []int64
	if err := database.
		Model(&models.User{}).
		Where("account_id = ?", accountID).
		Pluck("id", &userIDs).Error; err != nil {
		return false, err
	}
	if len(userIDs) == 0 {
		return false, nil
	}
	if err := database.
		Model(&models.User{}).
		Where("id IN ?", userIDs).
		Updates(map[string]any{"disabled": true, "updated_at": now}).Error; err != nil {
		return false, err
	}
	if err := database.
		Where("user_id IN ? AND uses = 0", userIDs).
		Delete(&models.Invite{}).Error; err != nil {
		return false, err
	}
	return true, nil
}

func destroyAccountDeletionUser(database *gorm.DB, accountID int64) (bool, error) {
	if database == nil || accountID == 0 {
		return false, nil
	}
	var userIDs []int64
	if err := database.
		Model(&models.User{}).
		Where("account_id = ?", accountID).
		Pluck("id", &userIDs).Error; err != nil {
		return false, err
	}
	if len(userIDs) == 0 {
		return false, nil
	}
	if err := database.
		Where("user_id IN ?", userIDs).
		Delete(&models.WebauthnCredential{}).Error; err != nil {
		return false, err
	}
	if err := database.
		Where("id IN ?", userIDs).
		Delete(&models.User{}).Error; err != nil {
		return false, err
	}
	return true, nil
}

func accountDeletionReportedStatusIDs(database *gorm.DB, accountID int64) ([]int64, error) {
	var reports []models.Report
	if err := database.
		Where("target_account_id = ? AND action_taken_at IS NULL", accountID).
		Find(&reports).Error; err != nil {
		return nil, err
	}
	seen := map[int64]struct{}{}
	ids := make([]int64, 0)
	for _, report := range reports {
		for _, id := range report.StatusIDs {
			if id <= 0 {
				continue
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
	}
	return ids, nil
}

type accountDeletionFileCleanup struct {
	accounts    []models.Account
	attachments []models.MediaAttachment
}

func (c *accountDeletionFileCleanup) merge(other accountDeletionFileCleanup) {
	c.accounts = append(c.accounts, other.accounts...)
	c.attachments = append(c.attachments, other.attachments...)
}

func (c accountDeletionFileCleanup) run(s *Server) {
	if s == nil {
		return
	}
	for _, account := range c.accounts {
		s.removeAccountImageObjects(account)
		s.removeAccountLocalImageFiles(account.ID)
	}
	for _, attachment := range c.attachments {
		s.removeMediaAttachmentLocalFiles(attachment)
	}
	s.invalidateMediaAttachmentParentStatusCaches(context.Background(), c.attachments)
}

func (s *Server) prepareAccountDeletionLocalFiles(database *gorm.DB, accountIDs *gorm.DB, reportedStatusIDs []int64, now time.Time) (accountDeletionFileCleanup, error) {
	cleanup := accountDeletionFileCleanup{}
	if s == nil {
		return cleanup, nil
	}
	if err := database.
		Where("id IN (?)", accountIDs).
		Where("avatar_file_name IS NOT NULL OR header_file_name IS NOT NULL").
		Find(&cleanup.accounts).Error; err != nil {
		return cleanup, err
	}
	query := database.
		Where("account_id IN (?)", accountIDs).
		Where("file_file_name IS NOT NULL OR thumbnail_file_name IS NOT NULL")
	if len(reportedStatusIDs) > 0 {
		query = query.Where("status_id IS NULL OR status_id NOT IN ?", reportedStatusIDs)
	}
	if err := query.Find(&cleanup.attachments).Error; err != nil {
		return cleanup, err
	}
	if len(cleanup.attachments) == 0 {
		return cleanup, nil
	}
	ids := make([]int64, 0, len(cleanup.attachments))
	for _, attachment := range cleanup.attachments {
		ids = append(ids, attachment.ID)
	}
	if err := database.Model(&models.MediaAttachment{}).Where("id IN ?", ids).Updates(clearMediaAttachmentFileUpdates(now)).Error; err != nil {
		return cleanup, err
	}
	return cleanup, nil
}

func accountDeletionProfileUpdates(now time.Time) map[string]any {
	return map[string]any{
		"silenced_at":         nil,
		"suspended_at":        now,
		"suspension_origin":   0,
		"locked":              false,
		"memorial":            false,
		"discoverable":        false,
		"trendable":           false,
		"display_name":        "",
		"note":                "",
		"fields":              gorm.Expr("?::jsonb", "[]"),
		"moved_to_account_id": nil,
		"reviewed_at":         nil,
		"requested_review_at": nil,
		"also_known_as":       models.StringArray{},
		"avatar_file_name":    nil,
		"avatar_content_type": nil,
		"avatar_file_size":    nil,
		"avatar_updated_at":   nil,
		"avatar_remote_url":   nil,
		"header_file_name":    nil,
		"header_content_type": nil,
		"header_file_size":    nil,
		"header_updated_at":   nil,
		"header_remote_url":   "",
		"updated_at":          now,
	}
}

func purgeAccountDeletionExtraAssociations(database *gorm.DB, accountIDs *gorm.DB, reportedStatusIDs []int64, destroyRows bool) error {
	for _, table := range []string{
		"account_aliases",
		"account_migrations",
		"account_conversations",
		"custom_filters",
		"lists",
		"report_notes",
	} {
		if err := database.Exec("DELETE FROM "+table+" WHERE account_id IN (?)", accountIDs).Error; err != nil {
			return err
		}
	}
	if err := database.Exec("DELETE FROM account_moderation_notes WHERE account_id IN (?)", accountIDs).Error; err != nil {
		return err
	}
	if destroyRows {
		if err := database.Exec("DELETE FROM account_moderation_notes WHERE target_account_id IN (?)", accountIDs).Error; err != nil {
			return err
		}
		if err := database.Exec("DELETE FROM reports WHERE account_id IN (?) OR target_account_id IN (?)", accountIDs, accountIDs).Error; err != nil {
			return err
		}
	}
	if len(reportedStatusIDs) > 0 {
		if err := database.Exec("DELETE FROM polls WHERE account_id IN (?) AND status_id NOT IN ?", accountIDs, reportedStatusIDs).Error; err != nil {
			return err
		}
	} else if err := database.Exec("DELETE FROM polls WHERE account_id IN (?)", accountIDs).Error; err != nil {
		return err
	}
	if len(reportedStatusIDs) > 0 {
		if err := database.Exec("DELETE FROM mentions WHERE account_id IN (?) AND status_id NOT IN ?", accountIDs, reportedStatusIDs).Error; err != nil {
			return err
		}
	} else if err := database.Exec("DELETE FROM mentions WHERE account_id IN (?)", accountIDs).Error; err != nil {
		return err
	}
	if len(reportedStatusIDs) > 0 {
		return database.Exec("DELETE FROM media_attachments WHERE account_id IN (?) AND (status_id IS NULL OR status_id NOT IN ?)", accountIDs, reportedStatusIDs).Error
	}
	return database.Exec("DELETE FROM media_attachments WHERE account_id IN (?)", accountIDs).Error
}

func (s *Server) purgeAccountDeletionInteractionAssociations(ctx context.Context, database *gorm.DB, accountIDs *gorm.DB, now time.Time) error {
	var favouritedStatusIDs []int64
	if err := database.
		Model(&models.Favourite{}).
		Distinct("status_id").
		Where("account_id IN (?)", accountIDs).
		Pluck("status_id", &favouritedStatusIDs).Error; err != nil {
		return err
	}
	if len(favouritedStatusIDs) > 0 {
		if err := database.Exec(`
UPDATE status_stats
SET favourites_count = GREATEST(0, status_stats.favourites_count - favourite_counts.count),
    updated_at = ?
FROM (
  SELECT status_id, COUNT(*) AS count
  FROM favourites
  WHERE account_id IN (?)
  GROUP BY status_id
) AS favourite_counts
WHERE status_stats.status_id = favourite_counts.status_id
`, now, accountIDs).Error; err != nil {
			return err
		}
		if s != nil {
			for _, statusID := range favouritedStatusIDs {
				s.invalidateStatusCache(ctx, statusID)
			}
		}
	}
	if err := database.Exec("DELETE FROM notifications WHERE account_id IN (?) OR from_account_id IN (?)", accountIDs, accountIDs).Error; err != nil {
		return err
	}
	if err := database.Exec("DELETE FROM favourites WHERE account_id IN (?)", accountIDs).Error; err != nil {
		return err
	}
	return database.Exec("DELETE FROM bookmarks WHERE account_id IN (?)", accountIDs).Error
}

func (s *Server) tombstoneAccountDeletionStatuses(database *gorm.DB, accountIDs *gorm.DB, reportedStatusIDs []int64, now time.Time) error {
	statusIDs, reblogIDs := accountDeletionStatusIDQueries(database, accountIDs, reportedStatusIDs)
	if err := unlinkDirectStatusesFromConversationsForQuery(context.Background(), database, statusIDs, now); err != nil {
		return err
	}
	if err := database.Model(&models.Status{}).
		Where("(id IN (?) OR id IN (?)) AND deleted_at IS NULL", statusIDs, reblogIDs).
		Updates(map[string]any{"deleted_at": now, "updated_at": now}).Error; err != nil {
		return err
	}
	if err := database.Exec("DELETE FROM status_pins WHERE status_id IN (?) OR status_id IN (?)", statusIDs, reblogIDs).Error; err != nil {
		return err
	}
	return recalculateStatusCounters(database, now)
}

func accountDeletionStatusIDQueries(database *gorm.DB, accountIDs *gorm.DB, reportedStatusIDs []int64) (*gorm.DB, *gorm.DB) {
	statusIDs := database.Model(&models.Status{}).
		Select("id").
		Where("account_id IN (?)", accountIDs)
	if len(reportedStatusIDs) > 0 {
		statusIDs = statusIDs.Where("id NOT IN ?", reportedStatusIDs)
	}
	reblogIDs := database.Model(&models.Status{}).
		Select("id").
		Where("reblog_of_id IN (?)", statusIDs)
	return statusIDs, reblogIDs
}

func unlinkDirectStatusesFromConversationsForQuery(ctx context.Context, database *gorm.DB, statusIDs *gorm.DB, now time.Time) error {
	if database == nil || statusIDs == nil {
		return nil
	}
	var statuses []models.Status
	if err := database.WithContext(ctx).
		Select("id", "visibility", "conversation_id").
		Where("id IN (?) AND visibility = ?", statusIDs, 3).
		Find(&statuses).Error; err != nil {
		return err
	}
	for _, status := range statuses {
		if err := unlinkDirectStatusFromConversations(ctx, database, status, now); err != nil {
			return err
		}
	}
	return nil
}
