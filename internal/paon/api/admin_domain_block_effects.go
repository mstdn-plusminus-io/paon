package api

import (
	"context"
	"strings"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

func (s *Server) applyAdminDomainBlockEffects(database *gorm.DB, block models.DomainBlock, update bool) error {
	if database == nil {
		return nil
	}
	domain := strings.Trim(strings.ToLower(block.Domain), ".")
	if domain == "" {
		return nil
	}
	if update {
		if err := restoreAdminDomainBlockEffects(database, block, domain); err != nil {
			return err
		}
	}
	switch {
	case adminDomainBlockSuspendsAccounts(block):
		if err := database.Model(&models.Account{}).
			Where(domainAndSubdomainsSQL("domain"), domain, "%."+domain).
			Where("suspended_at IS NULL").
			Updates(map[string]any{
				"suspended_at":      block.CreatedAt,
				"suspension_origin": int64(0),
			}).Error; err != nil {
			return err
		}
		return s.purgeAdminDomainSuspendedAccounts(database, domain, block.CreatedAt)
	case adminDomainBlockSilencesAccounts(block):
		return database.Model(&models.Account{}).
			Where(domainAndSubdomainsSQL("domain"), domain, "%."+domain).
			Where("silenced_at IS NULL").
			Update("silenced_at", block.CreatedAt).Error
	default:
		return nil
	}
}

func (s *Server) enqueueAdminDomainBlockEffectsOrApply(database *gorm.DB, block models.DomainBlock, update bool) error {
	if s != nil && s.enqueueDomainBlockTask(block.ID, update) {
		return nil
	}
	if s == nil {
		return nil
	}
	if err := s.applyAdminDomainBlockEffects(database, block, update); err != nil {
		return err
	}
	if block.RejectMedia && !s.enqueueDomainClearMediaTask(block.ID) {
		return s.clearDomainMediaCache(database, block.Domain)
	}
	return nil
}

func (s *Server) applyAdminDomainUnblockEffects(database *gorm.DB, block models.DomainBlock) error {
	if database == nil {
		return nil
	}
	domain := strings.Trim(strings.ToLower(block.Domain), ".")
	if domain == "" {
		return nil
	}
	if !adminDomainBlockNoop(block) {
		if err := database.Model(&models.Account{}).
			Where(domainAndSubdomainsSQL("domain"), domain, "%."+domain).
			Where("silenced_at = ?", block.CreatedAt).
			Update("silenced_at", nil).Error; err != nil {
			return err
		}
	}
	if adminDomainBlockSuspendsAccounts(block) {
		if err := database.Model(&models.Account{}).
			Where(domainAndSubdomainsSQL("domain"), domain, "%."+domain).
			Where("suspended_at = ?", block.CreatedAt).
			Updates(map[string]any{
				"suspended_at":      nil,
				"suspension_origin": nil,
			}).Error; err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) purgeAdminDomainSuspendedAccounts(database *gorm.DB, domain string, suspendedAt time.Time) error {
	accountIDs := adminDomainSuspendedAccountIDSubquery(database, domain, suspendedAt)
	return s.purgeAdminSuspendedAccountIDs(database, accountIDs)
}

func (s *Server) applyAdminDomainUnallowEffects(database *gorm.DB, domain string) error {
	if s == nil || database == nil || !s.cfg.LimitedFederationMode {
		return nil
	}
	domain = strings.Trim(strings.ToLower(domain), ".")
	if domain == "" {
		return nil
	}
	now := time.Now().UTC()
	if err := database.Model(&models.Account{}).
		Where("lower(domain) = ?", domain).
		Where("suspended_at IS NULL").
		Updates(map[string]any{
			"suspended_at":      now,
			"suspension_origin": int64(0),
		}).Error; err != nil {
		return err
	}
	return s.enqueueAfterUnallowDomainOrRun(context.Background(), database, domain)
}

func (s *Server) enqueueAfterUnallowDomainOrRun(ctx context.Context, database *gorm.DB, domain string) error {
	if s != nil && s.enqueueAfterUnallowDomainTask(domain) {
		return nil
	}
	return s.runAfterUnallowDomainEffects(ctx, database, domain)
}

func (s *Server) runAfterUnallowDomainEffects(ctx context.Context, database *gorm.DB, domain string) error {
	if s == nil || database == nil {
		return nil
	}
	domain = strings.Trim(strings.ToLower(domain), ".")
	if domain == "" {
		return nil
	}
	accountIDs := database.WithContext(ctx).Model(&models.Account{}).Select("id").Where("lower(domain) = ?", domain)
	return s.purgeAdminUnallowedAccountIDs(database.WithContext(ctx), accountIDs)
}

func (s *Server) purgeAdminSuspendedAccountIDs(database *gorm.DB, accountIDs *gorm.DB) error {
	return s.purgeAdminSuspendedAccountIDsWithMode(database, accountIDs, false)
}

func (s *Server) purgeAdminUnallowedAccountIDs(database *gorm.DB, accountIDs *gorm.DB) error {
	return s.purgeAdminSuspendedAccountIDsWithMode(database, accountIDs, true)
}

func (s *Server) purgeAdminSuspendedAccountIDsWithMode(database *gorm.DB, accountIDs *gorm.DB, destroyRows bool) error {
	now := time.Now().UTC()
	followDeliveries, err := s.adminSuspendedRemoteFollowDeliveries(database, accountIDs)
	if err != nil {
		return err
	}
	if err := s.clearAdminDomainSuspendedAccountLocalFiles(database, accountIDs, now); err != nil {
		return err
	}
	if !destroyRows {
		profileUpdates := map[string]any{
			"silenced_at":         nil,
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
		if err := database.Model(&models.Account{}).Where("id IN (?)", accountIDs).Updates(profileUpdates).Error; err != nil {
			return err
		}
		if err := database.Model(&models.AccountStat{}).Where("account_id IN (?)", accountIDs).Updates(map[string]any{
			"statuses_count":  0,
			"followers_count": 0,
			"following_count": 0,
			"updated_at":      now,
		}).Error; err != nil {
			return err
		}
	}
	if err := s.clearAdminSuspendedAccountFeedCaches(context.Background(), database, accountIDs); err != nil {
		return err
	}
	if err := purgeAdminDomainSuspendedAccountAssociations(database, accountIDs, now); err != nil {
		return err
	}
	if err := s.tombstoneAdminDomainSuspendedAccountStatuses(database, accountIDs, now); err != nil {
		return err
	}
	s.deliverAdminSuspendedRemoteFollowActivities(followDeliveries)
	s.applyAdminSuspendedRemoteFollowCacheEffects(context.Background(), followDeliveries)
	if destroyRows {
		return database.Where("id IN (?)", accountIDs).Delete(&models.Account{}).Error
	}
	return nil
}

type adminSuspendedRemoteFollowDelivery struct {
	Kind      string
	Local     models.Account
	Remote    models.Account
	FollowID  int64
	FollowURI string
}

func (s *Server) adminSuspendedRemoteFollowDeliveries(database *gorm.DB, accountIDs *gorm.DB) ([]adminSuspendedRemoteFollowDelivery, error) {
	if s == nil || database == nil {
		return nil, nil
	}
	deliveries := []adminSuspendedRemoteFollowDelivery{}

	var rejectFollows []models.Follow
	if err := database.Model(&models.Follow{}).
		Preload("Account").
		Preload("TargetAccount").
		Joins("JOIN accounts remote_accounts ON remote_accounts.id = follows.account_id").
		Joins("JOIN accounts local_accounts ON local_accounts.id = follows.target_account_id").
		Where("follows.account_id IN (?)", accountIDs).
		Where("remote_accounts.domain IS NOT NULL AND remote_accounts.domain <> ''").
		Where("(local_accounts.domain IS NULL OR local_accounts.domain = '')").
		Find(&rejectFollows).Error; err != nil {
		return nil, err
	}
	for _, follow := range rejectFollows {
		deliveries = append(deliveries, adminSuspendedRemoteFollowDelivery{
			Kind:      "Reject",
			Local:     follow.TargetAccount,
			Remote:    follow.Account,
			FollowID:  follow.ID,
			FollowURI: string(follow.URI),
		})
	}

	var undoFollows []models.Follow
	if err := database.Model(&models.Follow{}).
		Preload("Account").
		Preload("TargetAccount").
		Joins("JOIN accounts local_accounts ON local_accounts.id = follows.account_id").
		Joins("JOIN accounts remote_accounts ON remote_accounts.id = follows.target_account_id").
		Where("follows.target_account_id IN (?)", accountIDs).
		Where("(local_accounts.domain IS NULL OR local_accounts.domain = '')").
		Where("remote_accounts.domain IS NOT NULL AND remote_accounts.domain <> ''").
		Find(&undoFollows).Error; err != nil {
		return nil, err
	}
	for _, follow := range undoFollows {
		deliveries = append(deliveries, adminSuspendedRemoteFollowDelivery{
			Kind:      "Undo",
			Local:     follow.Account,
			Remote:    follow.TargetAccount,
			FollowID:  follow.ID,
			FollowURI: string(follow.URI),
		})
	}

	return deliveries, nil
}

func (s *Server) deliverAdminSuspendedRemoteFollowActivities(deliveries []adminSuspendedRemoteFollowDelivery) {
	if s == nil {
		return
	}
	for _, delivery := range deliveries {
		if !delivery.Local.Local() || delivery.Remote.Local() {
			continue
		}
		switch delivery.Kind {
		case "Reject":
			_ = s.deliverActivityPubFollowResponse("Reject", delivery.Local, delivery.Remote, delivery.FollowID, delivery.FollowURI)
		case "Undo":
			_ = s.deliverActivityPubUndoFollow(delivery.Local, delivery.Remote, delivery.FollowID, delivery.FollowURI)
		}
	}
}

func (s *Server) deliverAdminAccountDeletionActivities(database *gorm.DB, account models.Account) error {
	if s == nil || database == nil || account.ID == 0 {
		return nil
	}
	if account.Local() {
		return s.deliverActivityPubAccountDelete(account)
	}
	if account.Protocol != 1 {
		return nil
	}
	deliveries, err := s.adminSuspendedRemoteFollowDeliveries(database, adminSingleAccountIDSubquery(database, account.ID))
	if err != nil {
		return err
	}
	s.deliverAdminSuspendedRemoteFollowActivities(deliveries)
	return nil
}

func (s *Server) applyAdminSuspensionWorkerEffects(ctx context.Context, database *gorm.DB, accountID int64) error {
	if s == nil || database == nil || accountID == 0 {
		return nil
	}
	accountIDs := adminSingleAccountIDSubquery(database, accountID)
	var account models.Account
	if err := database.Where("id = ?", accountID).First(&account).Error; err != nil {
		return err
	}
	deliveries, err := s.adminSuspensionWorkerRejectDeliveries(database, accountID)
	if err != nil {
		return err
	}
	if err := database.Transaction(func(tx *gorm.DB) error {
		for _, delivery := range deliveries {
			if err := deleteFollow(tx, models.Follow{
				ID:              delivery.FollowID,
				AccountID:       delivery.Remote.ID,
				TargetAccountID: delivery.Local.ID,
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	s.deliverAdminSuspendedRemoteFollowActivities(deliveries)
	s.applyAdminSuspendedRemoteFollowCacheEffects(ctx, deliveries)
	if err := s.applyAdminAccountMediaVisibility(ctx, database, account.ID, true); err != nil {
		return err
	}
	if account.Local() {
		_ = s.deliverActivityPubAccountUpdate(account)
	}
	if err := s.applyAdminSuspensionFeedUnmerge(ctx, database, account); err != nil {
		return err
	}
	return s.clearAdminSuspendedAccountFeedCaches(ctx, database, accountIDs)
}

func (s *Server) enqueueAdminSuspensionOrRun(ctx context.Context, database *gorm.DB, accountID int64) error {
	if s == nil || database == nil || accountID == 0 {
		return nil
	}
	if s.enqueueAdminSuspensionTask(accountID) {
		return nil
	}
	return s.applyAdminSuspensionWorkerEffects(ctx, database, accountID)
}

func (s *Server) adminSuspensionWorkerRejectDeliveries(database *gorm.DB, accountID int64) ([]adminSuspendedRemoteFollowDelivery, error) {
	if s == nil || database == nil || accountID == 0 {
		return nil, nil
	}
	var follows []models.Follow
	if err := database.Model(&models.Follow{}).
		Preload("Account").
		Preload("TargetAccount").
		Joins("JOIN accounts remote_accounts ON remote_accounts.id = follows.account_id").
		Joins("JOIN accounts local_accounts ON local_accounts.id = follows.target_account_id").
		Where("follows.account_id = ?", accountID).
		Where("remote_accounts.domain IS NOT NULL AND remote_accounts.domain <> ''").
		Where("(local_accounts.domain IS NULL OR local_accounts.domain = '')").
		Find(&follows).Error; err != nil {
		return nil, err
	}
	deliveries := make([]adminSuspendedRemoteFollowDelivery, 0, len(follows))
	for _, follow := range follows {
		deliveries = append(deliveries, adminSuspendedRemoteFollowDelivery{
			Kind:      "Reject",
			Local:     follow.TargetAccount,
			Remote:    follow.Account,
			FollowID:  follow.ID,
			FollowURI: string(follow.URI),
		})
	}
	return deliveries, nil
}

func (s *Server) applyAdminSuspendedRemoteFollowCacheEffects(ctx context.Context, deliveries []adminSuspendedRemoteFollowDelivery) {
	if s == nil || len(deliveries) == 0 {
		return
	}
	reindexTargets := map[int64]struct{}{}
	for _, delivery := range deliveries {
		switch delivery.Kind {
		case "Reject":
			s.invalidateFollowRelationshipCaches(ctx, delivery.Remote, delivery.Local.ID)
			if delivery.Local.ID != 0 {
				reindexTargets[delivery.Local.ID] = struct{}{}
			}
		case "Undo":
			s.invalidateFollowRelationshipCaches(ctx, delivery.Local, delivery.Remote.ID)
			if delivery.Local.Local() {
				// Domain suspension undoes a local account's follow of a remote account; mirror
				// Rails SuspendAccountService by unmerging the remote account from the local
				// account's home feed (element-level) instead of dropping the whole cached feed.
				s.unmergeAfterUnfollowBestEffort(ctx, delivery.Remote.ID, delivery.Local)
			}
			if delivery.Remote.ID != 0 {
				reindexTargets[delivery.Remote.ID] = struct{}{}
			}
		}
	}
	ids := make([]int64, 0, len(reindexTargets))
	for id := range reindexTargets {
		ids = append(ids, id)
	}
	s.meiliReindexPrivateStatusesForAccountsBestEffort(ctx, ids...)
}

func (s *Server) applyAdminUnsuspensionWorkerEffects(database *gorm.DB, accountID int64) error {
	if s == nil || database == nil || accountID == 0 {
		return nil
	}
	var account models.Account
	if err := database.Where("id = ?", accountID).First(&account).Error; err != nil {
		return err
	}
	if err := s.applyAdminAccountMediaVisibility(context.Background(), database, account.ID, false); err != nil {
		return err
	}
	if account.Local() {
		if err := s.applyAdminUnsuspensionFeedMerge(context.Background(), database, account); err != nil {
			return err
		}
		_ = s.deliverActivityPubAccountUpdate(account)
		return nil
	}
	if err := database.Model(&models.Account{}).Where("id = ?", account.ID).Updates(map[string]any{"last_webfingered_at": nil}).Error; err != nil {
		return err
	}
	_, _ = s.fetchAndStoreActivityActorForAcct(account.Acct())
	// Rails dispatches RefollowWorker.perform_async on unsuspend so local followers re-follow
	// the remote account (re-enabling delivery/notification). Enqueue the async refollow.
	s.enqueueRefollowTask(account.ID)
	return s.applyAdminUnsuspensionFeedMerge(context.Background(), database, account)
}

func (s *Server) enqueueAdminUnsuspensionOrRun(database *gorm.DB, accountID int64) error {
	if s == nil || database == nil || accountID == 0 {
		return nil
	}
	if s.enqueueAdminUnsuspensionTask(accountID) {
		return nil
	}
	return s.applyAdminUnsuspensionWorkerEffects(database, accountID)
}

func (s *Server) applyAdminAccountMediaVisibility(ctx context.Context, database *gorm.DB, accountID int64, private bool) error {
	if s == nil || database == nil || accountID == 0 {
		return nil
	}
	var attachments []models.MediaAttachment
	if err := database.WithContext(ctx).
		Where("account_id = ?", accountID).
		Where("file_file_name IS NOT NULL OR thumbnail_file_name IS NOT NULL").
		Find(&attachments).Error; err != nil {
		return err
	}
	for _, attachment := range attachments {
		if err := s.applyMediaAttachmentVisibility(ctx, attachment, private); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) applyAdminSuspensionFeedUnmerge(ctx context.Context, database *gorm.DB, account models.Account) error {
	if s == nil || database == nil || account.ID == 0 {
		return nil
	}
	var followers []models.Account
	if err := database.WithContext(ctx).Table("accounts").
		Select("accounts.*").
		Joins("JOIN follows ON follows.account_id = accounts.id").
		Joins("JOIN users ON users.account_id = accounts.id").
		Where("follows.target_account_id = ?", account.ID).
		Where("(accounts.domain IS NULL OR accounts.domain = '')").
		Find(&followers).Error; err != nil {
		return err
	}
	for _, follower := range followers {
		if err := s.unmergeAccountFromHomeFeed(ctx, database, account.ID, follower); err != nil {
			return err
		}
	}
	lists, err := adminAccountListsForLocalDistribution(ctx, database, account.ID)
	if err != nil {
		return err
	}
	for _, list := range lists {
		if err := s.unmergeAccountFromListFeed(ctx, database, account.ID, list); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) applyAdminUnsuspensionFeedMerge(ctx context.Context, database *gorm.DB, account models.Account) error {
	if s == nil || database == nil || account.ID == 0 {
		return nil
	}
	var followers []models.Account
	if err := database.WithContext(ctx).Table("accounts").
		Select("accounts.*").
		Joins("JOIN follows ON follows.account_id = accounts.id").
		Joins("JOIN users ON users.account_id = accounts.id").
		Where("follows.target_account_id = ?", account.ID).
		Where("(accounts.domain IS NULL OR accounts.domain = '')").
		Find(&followers).Error; err != nil {
		return err
	}
	for _, follower := range followers {
		if err := s.mergeAccountIntoHomeFeed(ctx, database, account.ID, follower); err != nil {
			return err
		}
	}
	lists, err := adminAccountListsForLocalDistribution(ctx, database, account.ID)
	if err != nil {
		return err
	}
	for _, list := range lists {
		if err := s.mergeAccountIntoListFeed(ctx, database, account.ID, list); err != nil {
			return err
		}
	}
	return nil
}

func adminAccountListsForLocalDistribution(ctx context.Context, database *gorm.DB, accountID int64) ([]models.List, error) {
	if database == nil || accountID == 0 {
		return nil, nil
	}
	var lists []models.List
	err := database.WithContext(ctx).Table("lists").
		Select("DISTINCT lists.*").
		Joins("JOIN users ON users.account_id = lists.account_id").
		Joins("JOIN list_accounts ON list_accounts.list_id = lists.id").
		Where("list_accounts.account_id = ?", accountID).
		Where("(list_accounts.follow_id IS NOT NULL OR lists.account_id = ?)", accountID).
		Find(&lists).Error
	return lists, err
}

func (s *Server) clearAdminDomainSuspendedAccountLocalFiles(database *gorm.DB, accountIDs *gorm.DB, now time.Time) error {
	if s == nil {
		return nil
	}
	var accounts []models.Account
	if err := database.
		Where("id IN (?)", accountIDs).
		Where("avatar_file_name IS NOT NULL OR header_file_name IS NOT NULL").
		Find(&accounts).Error; err != nil {
		return err
	}
	for _, account := range accounts {
		s.removeAccountImageObjects(account)
		s.removeAccountLocalImageFiles(account.ID)
	}
	var attachments []models.MediaAttachment
	if err := database.
		Where("account_id IN (?)", accountIDs).
		Where("file_file_name IS NOT NULL OR thumbnail_file_name IS NOT NULL").
		Find(&attachments).Error; err != nil {
		return err
	}
	for _, attachment := range attachments {
		s.removeMediaAttachmentLocalFiles(attachment)
	}
	if len(attachments) == 0 {
		return nil
	}
	ids := make([]int64, 0, len(attachments))
	for _, attachment := range attachments {
		ids = append(ids, attachment.ID)
	}
	if err := database.Model(&models.MediaAttachment{}).Where("id IN ?", ids).Updates(clearMediaAttachmentFileUpdates(now)).Error; err != nil {
		return err
	}
	s.invalidateMediaAttachmentParentStatusCaches(context.Background(), attachments)
	return nil
}

func adminSingleAccountIDSubquery(database *gorm.DB, accountID int64) *gorm.DB {
	return database.Model(&models.Account{}).Select("id").Where("id = ?", accountID)
}

func (s *Server) clearAdminSuspendedAccountFeedCaches(ctx context.Context, database *gorm.DB, accountIDs *gorm.DB) error {
	if s == nil || database == nil {
		return nil
	}
	var homeFeedAccountIDs []int64
	if err := database.Table("follows").
		Select("DISTINCT follows.account_id").
		Joins("JOIN users ON users.account_id = follows.account_id").
		Where("follows.target_account_id IN (?)", accountIDs).
		Pluck("follows.account_id", &homeFeedAccountIDs).Error; err != nil {
		return err
	}
	for _, accountID := range homeFeedAccountIDs {
		_ = s.clearHomeFeedCacheContext(ctx, accountID)
	}
	var listIDs []int64
	if err := database.Table("lists").
		Select("DISTINCT lists.id").
		Joins("JOIN list_accounts ON list_accounts.list_id = lists.id").
		Where("list_accounts.account_id IN (?)", accountIDs).
		Pluck("lists.id", &listIDs).Error; err != nil {
		return err
	}
	for _, listID := range listIDs {
		_ = s.clearListFeedCacheContext(ctx, listID)
	}
	return nil
}

func purgeAdminDomainSuspendedAccountAssociations(database *gorm.DB, accountIDs *gorm.DB, now time.Time) error {
	affectedRelationshipAccountIDs, err := accountIDsAffectedByRelationshipDeletion(database, accountIDs)
	if err != nil {
		return err
	}
	for _, table := range []string{
		"account_notes",
		"account_pins",
		"account_domain_blocks",
		"conversation_mutes",
		"devices",
		"featured_tags",
		"list_accounts",
		"scheduled_statuses",
		"status_pins",
		"tag_follows",
	} {
		if err := database.Exec("DELETE FROM "+table+" WHERE account_id IN (?)", accountIDs).Error; err != nil {
			return err
		}
	}
	for _, table := range []string{"follows", "follow_requests", "blocks", "mutes"} {
		if err := database.Exec("DELETE FROM "+table+" WHERE account_id IN (?) OR target_account_id IN (?)", accountIDs, accountIDs).Error; err != nil {
			return err
		}
	}
	if err := database.Exec("DELETE FROM notifications WHERE account_id IN (?) OR from_account_id IN (?)", accountIDs, accountIDs).Error; err != nil {
		return err
	}
	if err := database.Exec(`
UPDATE status_stats
SET favourites_count = GREATEST(0, status_stats.favourites_count - favourite_counts.count)
FROM (
  SELECT status_id, COUNT(*) AS count
  FROM favourites
  WHERE account_id IN (?)
  GROUP BY status_id
) AS favourite_counts
WHERE status_stats.status_id = favourite_counts.status_id
`, accountIDs).Error; err != nil {
		return err
	}
	if err := database.Exec("DELETE FROM favourites WHERE account_id IN (?)", accountIDs).Error; err != nil {
		return err
	}
	if err := database.Exec("DELETE FROM bookmarks WHERE account_id IN (?)", accountIDs).Error; err != nil {
		return err
	}
	if err := recalculateRelationshipCountersForAccountIDs(database, affectedRelationshipAccountIDs, now); err != nil {
		return err
	}
	return nil
}

func accountIDsAffectedByRelationshipDeletion(database *gorm.DB, accountIDs *gorm.DB) ([]int64, error) {
	var affected []int64
	queries := []struct {
		column string
		where  string
	}{
		{column: "id", where: "id IN (?)"},
		{column: "target_account_id", where: "account_id IN (?)"},
		{column: "account_id", where: "target_account_id IN (?)"},
	}
	for index, query := range queries {
		table := "follows"
		if index == 0 {
			table = "accounts"
		}
		var ids []int64
		if err := database.Table(table).
			Select(query.column).
			Where(query.where, accountIDs).
			Pluck(query.column, &ids).Error; err != nil {
			return nil, err
		}
		affected = append(affected, ids...)
	}
	return uniqueInt64s(affected), nil
}

func (s *Server) tombstoneAdminDomainSuspendedAccountStatuses(database *gorm.DB, accountIDs *gorm.DB, now time.Time) error {
	statusIDs := database.Model(&models.Status{}).
		Select("id").
		Where("account_id IN (?)", accountIDs)
	reblogIDs := database.Model(&models.Status{}).
		Select("id").
		Where("reblog_of_id IN (?)", statusIDs)
	affectedStatusIDs, err := statusIDsAffectedByAccountStatusDeletion(database, accountIDs)
	if err != nil {
		return err
	}
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
	if err := recalculateStatusCountersForStatusIDs(database, affectedStatusIDs, now); err != nil {
		return err
	}
	if s != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		s.publishBatchedAccountDeletionStatusDeletesForQuery(ctx, database, statusIDs, now)
		s.publishBatchedAccountDeletionStatusDeletesForQuery(ctx, database, reblogIDs, now)
	}
	return nil
}

func statusIDsAffectedByAccountStatusDeletion(database *gorm.DB, accountIDs *gorm.DB) ([]int64, error) {
	var affected []int64
	for _, column := range []string{"in_reply_to_id", "reblog_of_id"} {
		var ids []int64
		if err := database.Model(&models.Status{}).
			Where("account_id IN (?) AND deleted_at IS NULL AND "+column+" IS NOT NULL", accountIDs).
			Pluck(column, &ids).Error; err != nil {
			return nil, err
		}
		affected = append(affected, ids...)
	}
	return uniqueInt64s(affected), nil
}

func adminDomainSuspendedAccountIDSubquery(database *gorm.DB, domain string, suspendedAt time.Time) *gorm.DB {
	return database.Model(&models.Account{}).
		Select("id").
		Where(domainAndSubdomainsSQL("domain"), domain, "%."+domain).
		Where("suspended_at = ?", suspendedAt)
}

func adminUnallowedDomainAccountIDSubquery(database *gorm.DB, domain string, suspendedAt time.Time) *gorm.DB {
	return database.Model(&models.Account{}).
		Select("id").
		Where("lower(domain) = ?", domain).
		Where("suspended_at = ?", suspendedAt)
}

func restoreAdminDomainBlockEffects(database *gorm.DB, block models.DomainBlock, domain string) error {
	if !adminDomainBlockSilencesAccounts(block) {
		if err := database.Model(&models.Account{}).
			Where(domainAndSubdomainsSQL("domain"), domain, "%."+domain).
			Where("silenced_at = ?", block.CreatedAt).
			Update("silenced_at", nil).Error; err != nil {
			return err
		}
	}
	if !adminDomainBlockSuspendsAccounts(block) {
		if err := database.Model(&models.Account{}).
			Where(domainAndSubdomainsSQL("domain"), domain, "%."+domain).
			Where("suspended_at = ?", block.CreatedAt).
			Updates(map[string]any{
				"suspended_at":      nil,
				"suspension_origin": nil,
			}).Error; err != nil {
			return err
		}
	}
	return nil
}

func adminDomainBlockSilencesAccounts(block models.DomainBlock) bool {
	return domainBlockSeverityIs(block, "silence")
}

func adminDomainBlockSuspendsAccounts(block models.DomainBlock) bool {
	return domainBlockSeverityIs(block, "suspend")
}

func adminDomainBlockNoop(block models.DomainBlock) bool {
	return domainBlockSeverityIs(block, "noop")
}
