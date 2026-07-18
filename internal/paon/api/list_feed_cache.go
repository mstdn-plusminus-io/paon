package api

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

const (
	feedMaxItems      = 800
	feedReblogFalloff = 40
)

var errStatusDistributionLockUnavailable = errors.New("temporary problem distributing status")

func (s *Server) acquireStatusDistributionRedisLock(ctx context.Context, statusID int64) (bool, func(), error) {
	if statusID == 0 {
		return true, func() {}, nil
	}
	return s.acquireActivityPubRedisLock(ctx, "distribute:"+strconv.FormatInt(statusID, 10), 15*time.Minute)
}

func feedRedisKey(prefix string, feedType string, feedID int64) string {
	return prefix + "feed:" + feedType + ":" + strconv.FormatInt(feedID, 10)
}

func feedReblogRedisKey(prefix string, feedType string, feedID int64) string {
	return feedRedisKey(prefix, feedType, feedID) + ":reblogs"
}

func feedReblogStatusRedisKey(prefix string, feedType string, feedID int64, rebloggedID string) string {
	return feedReblogRedisKey(prefix, feedType, feedID) + ":" + rebloggedID
}

func feedRedisDeleteKeys(prefix string, feedType string, feedID int64, rebloggedIDs []string) []string {
	keys := []string{feedRedisKey(prefix, feedType, feedID), feedReblogRedisKey(prefix, feedType, feedID)}
	for _, id := range rebloggedIDs {
		if id == "" {
			continue
		}
		keys = append(keys, feedReblogStatusRedisKey(prefix, feedType, feedID, id))
	}
	return keys
}

func homeFeedRedisKey(prefix string, accountID int64) string {
	return feedRedisKey(prefix, "home", accountID)
}

func listFeedRedisKey(prefix string, listID int64) string {
	return feedRedisKey(prefix, "list", listID)
}

func listFeedReblogRedisKey(prefix string, listID int64) string {
	return feedReblogRedisKey(prefix, "list", listID)
}

func listFeedReblogStatusRedisKey(prefix string, listID int64, rebloggedID string) string {
	return feedReblogStatusRedisKey(prefix, "list", listID, rebloggedID)
}

func listFeedRedisDeleteKeys(prefix string, listID int64, rebloggedIDs []string) []string {
	return feedRedisDeleteKeys(prefix, "list", listID, rebloggedIDs)
}

func (s *Server) clearListFeedCache(listID int64) {
	if s == nil || listID == 0 {
		return
	}
	_ = s.clearListFeedCacheContext(context.Background(), listID)
}

func (s *Server) clearListFeedCacheContext(ctx context.Context, listID int64) error {
	return s.clearFeedCacheContext(ctx, "list", listID)
}

func (s *Server) clearHomeFeedCacheContext(ctx context.Context, accountID int64) error {
	return s.clearFeedCacheContext(ctx, "home", accountID)
}

func (s *Server) clearFeedCacheContext(ctx context.Context, feedType string, feedID int64) error {
	if s == nil || feedType == "" || feedID == 0 {
		return nil
	}
	prefix := redisConfig(s.cfg).prefix
	reblogKey := feedReblogRedisKey(prefix, feedType, feedID)
	value, err := s.redisCommand(ctx, "ZRANGE", reblogKey, "0", "-1")
	if err != nil {
		_, delErr := s.redisCommand(ctx, append([]string{"DEL"}, feedRedisDeleteKeys(prefix, feedType, feedID, nil)...)...)
		return delErr
	}
	rebloggedIDs, _ := redisStringArray(value)
	_, err = s.redisCommand(ctx, append([]string{"DEL"}, feedRedisDeleteKeys(prefix, feedType, feedID, rebloggedIDs)...)...)
	return err
}

type feedTarget struct {
	ID               int64
	AggregateReblogs bool
}

type feedTargetSettingsRow struct {
	ID              int64
	Settings        sql.NullString
	CurrentSignInAt sql.NullTime
}

type listFeedTarget struct {
	List             models.List
	AggregateReblogs bool
}

type listFeedTargetRow struct {
	ID              int64
	AccountID       int64
	RepliesPolicy   int
	Exclusive       bool
	Settings        sql.NullString
	CurrentSignInAt sql.NullTime
}

func (s *Server) removeStatusFromRailsFeeds(ctx context.Context, database *gorm.DB, status models.Status) error {
	if s == nil || database == nil || status.ID == 0 || status.AccountID == 0 {
		return nil
	}
	for _, target := range s.statusHomeFeedTargets(ctx, database, status.AccountID) {
		s.unpushStatusFromFeed(ctx, "home", target.ID, status, target.AggregateReblogs)
	}
	for _, target := range s.statusListFeedTargets(ctx, database, status.AccountID) {
		s.unpushStatusFromFeed(ctx, "list", target.ID, status, target.AggregateReblogs)
	}
	return nil
}

func (s *Server) fanOutStatusToLocalRecipientsSkipNotifications(ctx context.Context, database *gorm.DB, status models.Status) error {
	if s == nil || database == nil || status.ID == 0 || status.AccountID == 0 {
		return nil
	}
	acquired, releaseDistributionLock, err := s.acquireStatusDistributionRedisLock(ctx, status.ID)
	if err != nil {
		return err
	}
	if !acquired {
		return errStatusDistributionLockUnavailable
	}
	defer releaseDistributionLock()
	// Async fan-out mirrors Rails FeedInsertWorker.perform_async per recipient: enumerate
	// recipients synchronously, then enqueue one feed:insert task per home/list recipient so
	// large-follower-set delivery no longer blocks the request handler.
	enqueueHome := func(targets []feedTarget) {
		for _, target := range targets {
			s.enqueueFeedInsertTask(status.ID, "home", target.ID, target.AggregateReblogs)
		}
	}
	switch status.Visibility {
	case 0, 1, 2:
		enqueueHome(s.statusHomeFeedTargetsForLocalDistribution(ctx, database, status.AccountID))
		for _, target := range s.statusListFeedTargetsForLocalDistribution(ctx, database, status.AccountID) {
			if s.filterStatusFromList(ctx, database, status, target.List) {
				continue
			}
			s.enqueueFeedInsertTask(status.ID, "list", target.List.ID, target.AggregateReblogs)
		}
		if s.statusBroadcastableToTagFollowers(ctx, database, status) {
			for _, target := range s.statusTagFeedTargetsForLocalDistribution(ctx, database, status.ID) {
				s.enqueueFeedInsertTask(status.ID, "tags", target.ID, target.AggregateReblogs)
			}
		}
	default:
		enqueueHome(s.statusMentionedFollowerHomeFeedTargetsForLocalDistribution(ctx, database, status))
	}
	return nil
}

func (s *Server) fanOutStatusUpdateToLocalRecipients(ctx context.Context, database *gorm.DB, status models.Status) error {
	if s == nil || database == nil || status.ID == 0 || status.AccountID == 0 {
		return nil
	}
	acquired, releaseDistributionLock, err := s.acquireStatusDistributionRedisLock(ctx, status.ID)
	if err != nil {
		return err
	}
	if !acquired {
		return errStatusDistributionLockUnavailable
	}
	defer releaseDistributionLock()
	// Rails routes status edits through DistributionWorker(update: true), which in turn
	// enqueues FeedInsertWorker jobs. Keep the same request/worker boundary here so
	// list filtering and update unpush behavior happen in the feed worker.
	enqueueHome := func(targets []feedTarget) {
		for _, target := range targets {
			s.enqueueFeedInsertUpdateTask(status.ID, "home", target.ID, target.AggregateReblogs)
		}
	}
	switch status.Visibility {
	case 0, 1, 2:
		enqueueHome(s.statusHomeFeedTargetsForLocalDistribution(ctx, database, status.AccountID))
		for _, target := range s.statusListFeedTargetsForLocalDistribution(ctx, database, status.AccountID) {
			s.enqueueFeedInsertUpdateTask(status.ID, "list", target.List.ID, target.AggregateReblogs)
		}
		if s.statusBroadcastableToTagFollowers(ctx, database, status) {
			for _, target := range s.statusTagFeedTargetsForLocalDistribution(ctx, database, status.ID) {
				s.enqueueFeedInsertUpdateTask(status.ID, "tags", target.ID, target.AggregateReblogs)
			}
		}
	default:
		enqueueHome(s.statusMentionedFollowerHomeFeedTargetsForLocalDistribution(ctx, database, status))
	}
	return s.notifyStatusUpdateRebloggers(ctx, database, status)
}

func (s *Server) notifyStatusUpdateRebloggers(ctx context.Context, database *gorm.DB, status models.Status) error {
	if s == nil || database == nil || status.ID == 0 || status.AccountID == 0 {
		return nil
	}
	var rows []struct {
		AccountID int64 `gorm:"column:account_id"`
	}
	if err := database.WithContext(ctx).
		Table("statuses").
		Select("statuses.account_id").
		Joins("JOIN accounts ON accounts.id = statuses.account_id").
		Where("statuses.reblog_of_id = ? AND statuses.deleted_at IS NULL", status.ID).
		Where("accounts.domain IS NULL OR accounts.domain = ''").
		Scan(&rows).Error; err != nil {
		return err
	}
	var firstErr error
	for _, row := range rows {
		notification, err := s.createRelationshipNotificationRowAndEnqueue(database.WithContext(ctx), row.AccountID, status.AccountID, status.ID, "Status", "update", time.Now().UTC())
		if err != nil && firstErr == nil {
			firstErr = err
			continue
		}
		if notification != nil {
			s.publishNotificationIDWithContext(ctx, notification.ID)
		}
	}
	return firstErr
}

func (s *Server) mergeAccountIntoHomeFeed(ctx context.Context, database *gorm.DB, fromAccountID int64, intoAccount models.Account) error {
	if s == nil || database == nil || fromAccountID == 0 || intoAccount.ID == 0 {
		return nil
	}
	var user models.User
	if err := database.WithContext(ctx).Select("account_id", "current_sign_in_at", "settings").Where("account_id = ?", intoAccount.ID).First(&user).Error; err != nil {
		return nil
	}
	if !userSignedInRecently(user, time.Now().UTC()) {
		return nil
	}
	timelineKey := homeFeedRedisKey(redisConfig(s.cfg).prefix, intoAccount.ID)
	query := s.homeTimelineQuery(&intoAccount).
		Where("statuses.account_id = ?", fromAccountID).
		Where("statuses.visibility IN ?", []int{0, 1, 2}).
		Limit(feedMaxItems / 4)
	if redisInt(s.redisCommandValue(ctx, "ZCARD", timelineKey)) >= feedMaxItems/4 {
		oldestValue, err := s.redisCommand(ctx, "ZRANGE", timelineKey, "0", "0", "WITHSCORES")
		if err == nil {
			if oldest := firstRedisScoreInt(oldestValue); oldest > 0 {
				query = query.Where("statuses.id > ?", oldest)
			}
		}
	}
	var statuses []models.Status
	if err := query.Find(&statuses).Error; err != nil {
		return err
	}
	aggregateReblogs := aggregateReblogsFromSettings(user.Settings)
	for _, status := range statuses {
		_, _ = s.addStatusToFeedContext(ctx, "home", intoAccount.ID, status, aggregateReblogs)
	}
	return s.trimFeedContext(ctx, "home", intoAccount.ID)
}

func (s *Server) unmergeAccountFromHomeFeed(ctx context.Context, database *gorm.DB, fromAccountID int64, intoAccount models.Account) error {
	if s == nil || database == nil || fromAccountID == 0 || intoAccount.ID == 0 {
		return nil
	}
	value, err := s.redisCommand(ctx, "ZRANGE", homeFeedRedisKey(redisConfig(s.cfg).prefix, intoAccount.ID), "0", "-1")
	if err != nil {
		return nil
	}
	timelineIDs, ok := redisStringArray(value)
	if !ok || len(timelineIDs) == 0 {
		return nil
	}
	var statuses []models.Status
	if err := database.WithContext(ctx).
		Select("id, account_id, reblog_of_id").
		Where("account_id = ?", fromAccountID).
		Where("id IN ?", timelineIDs).
		Find(&statuses).Error; err != nil {
		return err
	}
	aggregateReblogs := s.accountAggregatesReblogs(ctx, intoAccount.ID)
	for _, status := range statuses {
		if _, err := s.removeStatusFromFeedContext(ctx, "home", intoAccount.ID, status, aggregateReblogs); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) mergeAccountIntoListFeed(ctx context.Context, database *gorm.DB, fromAccountID int64, list models.List) error {
	if s == nil || database == nil || fromAccountID == 0 || list.ID == 0 || list.AccountID == 0 {
		return nil
	}
	var user models.User
	if err := database.WithContext(ctx).Select("account_id", "current_sign_in_at", "settings").Where("account_id = ?", list.AccountID).First(&user).Error; err != nil {
		return nil
	}
	if !userSignedInRecently(user, time.Now().UTC()) {
		return nil
	}
	timelineKey := listFeedRedisKey(redisConfig(s.cfg).prefix, list.ID)
	query := s.statusQuery().
		Where("statuses.account_id = ?", fromAccountID).
		Where("statuses.visibility IN ?", []int{0, 1, 2}).
		Limit(feedMaxItems / 4)
	if redisInt(s.redisCommandValue(ctx, "ZCARD", timelineKey)) >= feedMaxItems/4 {
		oldestValue, err := s.redisCommand(ctx, "ZRANGE", timelineKey, "0", "0", "WITHSCORES")
		if err == nil {
			if oldest := firstRedisScoreInt(oldestValue); oldest > 0 {
				query = query.Where("statuses.id > ?", oldest)
			}
		}
	}
	var statuses []models.Status
	if err := query.Find(&statuses).Error; err != nil {
		return err
	}
	aggregateReblogs := aggregateReblogsFromSettings(user.Settings)
	for _, status := range statuses {
		if s.filterStatusFromList(ctx, database, status, list) {
			continue
		}
		_, _ = s.addStatusToFeedContext(ctx, "list", list.ID, status, aggregateReblogs)
	}
	return s.trimFeedContext(ctx, "list", list.ID)
}

func (s *Server) unmergeAccountFromListFeed(ctx context.Context, database *gorm.DB, fromAccountID int64, list models.List) error {
	if s == nil || database == nil || fromAccountID == 0 || list.ID == 0 {
		return nil
	}
	value, err := s.redisCommand(ctx, "ZRANGE", listFeedRedisKey(redisConfig(s.cfg).prefix, list.ID), "0", "-1")
	if err != nil {
		return nil
	}
	timelineIDs, ok := redisStringArray(value)
	if !ok || len(timelineIDs) == 0 {
		return nil
	}
	var statuses []models.Status
	if err := database.WithContext(ctx).
		Select("id, account_id, reblog_of_id").
		Where("account_id = ?", fromAccountID).
		Where("id IN ?", timelineIDs).
		Find(&statuses).Error; err != nil {
		return err
	}
	aggregateReblogs := s.accountAggregatesReblogs(ctx, list.AccountID)
	for _, status := range statuses {
		if _, err := s.removeStatusFromFeedContext(ctx, "list", list.ID, status, aggregateReblogs); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) filterStatusFromList(ctx context.Context, database *gorm.DB, status models.Status, list models.List) bool {
	if s == nil || database == nil || status.ID == 0 || list.ID == 0 {
		return true
	}
	switch list.RepliesPolicy {
	case 2:
		return status.Reply && status.InReplyToAccountID.Int64 != status.AccountID
	case 1:
		return false
	default:
		if !status.Reply || status.InReplyToAccountID.Int64 == status.AccountID {
			return false
		}
		var count int64
		_ = database.WithContext(ctx).Table("list_accounts").
			Where("list_id = ? AND account_id = ?", list.ID, status.InReplyToAccountID.Int64).
			Count(&count).Error
		return count == 0
	}
}

func (s *Server) regenerateHomeFeedForReturningUser(ctx context.Context, user models.User, now time.Time) {
	if s == nil || s.db == nil || user.AccountID == 0 || !user.ConfirmedAt.Valid || !returningUserNeedsFeedUpdate(user, now) {
		return
	}
	key := redisConfig(s.cfg).prefix + "account:" + strconv.FormatInt(user.AccountID, 10) + ":regeneration"
	value, err := s.redisCommand(ctx, "SET", key, "true", "NX", "EX", strconv.FormatInt(int64((24*time.Hour)/time.Second), 10))
	if err != nil || value == nil {
		return
	}
	if s.enqueueRegenerationTask(user.AccountID) {
		return
	}
	go func() {
		workerCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		_ = s.populateHomeFeed(workerCtx, s.db, user.AccountID, user.Settings)
		_, _ = s.redisCommand(workerCtx, "DEL", key)
	}()
}

func returningUserNeedsFeedUpdate(user models.User, now time.Time) bool {
	return user.CurrentSignInAt.Valid && user.CurrentSignInAt.Time.Before(now.Add(-userActiveDuration()))
}

func (s *Server) populateHomeFeed(ctx context.Context, database *gorm.DB, accountID int64, settings sql.NullString) error {
	if s == nil || database == nil || accountID == 0 {
		return nil
	}
	var account models.Account
	if err := database.WithContext(ctx).Where("id = ?", accountID).First(&account).Error; err != nil {
		return nil
	}
	var statuses []models.Status
	if err := s.homeTimelineQuery(&account).
		Order("statuses.id DESC").
		Limit(feedMaxItems / 2).
		Find(&statuses).Error; err != nil {
		return err
	}
	aggregateReblogs := aggregateReblogsFromSettings(settings)
	for _, status := range statuses {
		_, _ = s.addStatusToFeedContext(ctx, "home", account.ID, status, aggregateReblogs)
	}
	return s.trimFeedContext(ctx, "home", account.ID)
}

func userSignedInRecently(user models.User, now time.Time) bool {
	if !user.CurrentSignInAt.Valid {
		return false
	}
	return !user.CurrentSignInAt.Time.Before(now.Add(-userActiveDuration()))
}

func userActiveDuration() time.Duration {
	return time.Duration(userActiveDays()) * 24 * time.Hour
}

func (s *Server) statusHomeFeedTargets(ctx context.Context, database *gorm.DB, accountID int64) []feedTarget {
	if database == nil || accountID == 0 {
		return nil
	}
	var rows []feedTargetSettingsRow
	_ = database.WithContext(ctx).Table("users").
		Select("users.account_id AS id, users.settings").
		Joins("JOIN follows ON follows.account_id = users.account_id").
		Where("follows.target_account_id = ?", accountID).
		Scan(&rows).Error

	var self feedTargetSettingsRow
	if err := database.WithContext(ctx).Table("users").
		Select("users.account_id AS id, users.settings").
		Where("users.account_id = ?", accountID).
		Limit(1).
		Scan(&self).Error; err == nil && self.ID != 0 {
		rows = append(rows, self)
	}
	return feedTargetsFromUserSettings(rows)
}

func (s *Server) statusHomeFeedTargetsForLocalDistribution(ctx context.Context, database *gorm.DB, accountID int64) []feedTarget {
	if database == nil || accountID == 0 {
		return nil
	}
	var rows []feedTargetSettingsRow
	_ = database.WithContext(ctx).Table("users").
		Select("users.account_id AS id, users.settings, users.current_sign_in_at").
		Joins("JOIN follows ON follows.account_id = users.account_id").
		Where("follows.target_account_id = ?", accountID).
		Scan(&rows).Error

	var self feedTargetSettingsRow
	if err := database.WithContext(ctx).Table("users").
		Select("users.account_id AS id, users.settings, users.current_sign_in_at").
		Where("users.account_id = ?", accountID).
		Limit(1).
		Scan(&self).Error; err == nil && self.ID != 0 {
		rows = append(rows, self)
	}
	return feedTargetsFromRecentlySignedInUsers(rows, time.Now().UTC())
}

func (s *Server) statusMentionedFollowerHomeFeedTargetsForLocalDistribution(ctx context.Context, database *gorm.DB, status models.Status) []feedTarget {
	if database == nil || status.ID == 0 || status.AccountID == 0 {
		return nil
	}
	var rows []feedTargetSettingsRow
	_ = database.WithContext(ctx).Table("mentions").
		Select("users.account_id AS id, users.settings, users.current_sign_in_at").
		Joins("JOIN follows ON follows.account_id = mentions.account_id").
		Joins("JOIN users ON users.account_id = mentions.account_id").
		Where("mentions.status_id = ? AND follows.target_account_id = ?", status.ID, status.AccountID).
		Scan(&rows).Error
	return feedTargetsFromRecentlySignedInUsers(rows, time.Now().UTC())
}

func (s *Server) statusListFeedTargets(ctx context.Context, database *gorm.DB, accountID int64) []feedTarget {
	if database == nil || accountID == 0 {
		return nil
	}
	var rows []feedTargetSettingsRow
	_ = database.WithContext(ctx).Table("lists").
		Select("lists.id AS id, users.settings").
		Joins("JOIN users ON users.account_id = lists.account_id").
		Joins("JOIN list_accounts ON list_accounts.list_id = lists.id").
		Where("list_accounts.account_id = ?", accountID).
		Scan(&rows).Error
	return feedTargetsFromUserSettings(rows)
}

func (s *Server) statusListFeedTargetsForLocalDistribution(ctx context.Context, database *gorm.DB, accountID int64) []listFeedTarget {
	if database == nil || accountID == 0 {
		return nil
	}
	var rows []listFeedTargetRow
	_ = database.WithContext(ctx).Table("lists").
		Select("lists.id, lists.account_id, lists.replies_policy, lists.exclusive, users.settings, users.current_sign_in_at").
		Joins("JOIN users ON users.account_id = lists.account_id").
		Joins("JOIN list_accounts ON list_accounts.list_id = lists.id").
		Where("list_accounts.account_id = ?", accountID).
		Where("(list_accounts.follow_id IS NOT NULL OR lists.account_id = ?)", accountID).
		Scan(&rows).Error
	targets := make([]listFeedTarget, 0, len(rows))
	seen := make(map[int64]struct{}, len(rows))
	now := time.Now().UTC()
	for _, row := range rows {
		if row.ID == 0 || !userSignedInRecently(models.User{CurrentSignInAt: row.CurrentSignInAt}, now) {
			continue
		}
		if _, ok := seen[row.ID]; ok {
			continue
		}
		seen[row.ID] = struct{}{}
		targets = append(targets, listFeedTarget{
			List: models.List{
				ID:            row.ID,
				AccountID:     row.AccountID,
				RepliesPolicy: row.RepliesPolicy,
				Exclusive:     row.Exclusive,
			},
			AggregateReblogs: aggregateReblogsFromSettings(row.Settings),
		})
	}
	return targets
}

func (s *Server) statusTagFeedTargetsForLocalDistribution(ctx context.Context, database *gorm.DB, statusID int64) []feedTarget {
	if database == nil || statusID == 0 {
		return nil
	}
	var rows []feedTargetSettingsRow
	_ = database.WithContext(ctx).Table("tag_follows").
		Select("users.account_id AS id, users.settings, users.current_sign_in_at").
		Joins("JOIN users ON users.account_id = tag_follows.account_id").
		Joins("JOIN statuses_tags ON statuses_tags.tag_id = tag_follows.tag_id").
		Where("statuses_tags.status_id = ?", statusID).
		Scan(&rows).Error
	return feedTargetsFromRecentlySignedInUsers(rows, time.Now().UTC())
}

func (s *Server) statusBroadcastableToTagFollowers(ctx context.Context, database *gorm.DB, status models.Status) bool {
	if database == nil || status.ID == 0 || status.AccountID == 0 || status.Visibility != 0 || status.ReblogOfID.Valid {
		return false
	}
	var account models.Account
	if err := database.WithContext(ctx).Select("id", "silenced_at").Where("id = ?", status.AccountID).First(&account).Error; err != nil {
		return false
	}
	return !account.SilencedAt.Valid
}

func feedTargetsFromUserSettings(rows []feedTargetSettingsRow) []feedTarget {
	targets := make([]feedTarget, 0, len(rows))
	seen := make(map[int64]struct{}, len(rows))
	for _, row := range rows {
		id := row.ID
		if id == 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		targets = append(targets, feedTarget{
			ID:               id,
			AggregateReblogs: aggregateReblogsFromSettings(row.Settings),
		})
	}
	return targets
}

func feedTargetsFromRecentlySignedInUsers(rows []feedTargetSettingsRow, now time.Time) []feedTarget {
	targets := make([]feedTarget, 0, len(rows))
	seen := make(map[int64]struct{}, len(rows))
	for _, row := range rows {
		if row.ID == 0 || !userSignedInRecently(models.User{CurrentSignInAt: row.CurrentSignInAt}, now) {
			continue
		}
		if _, ok := seen[row.ID]; ok {
			continue
		}
		seen[row.ID] = struct{}{}
		targets = append(targets, feedTarget{
			ID:               row.ID,
			AggregateReblogs: aggregateReblogsFromSettings(row.Settings),
		})
	}
	return targets
}

func aggregateReblogsFromSettings(settings sql.NullString) bool {
	if !settings.Valid {
		return true
	}
	return rawBool(decodeUserSettings(settings.String)["aggregate_reblogs"], true)
}

func (s *Server) addStatusToFeedContext(ctx context.Context, feedType string, feedID int64, status models.Status, aggregateReblogs bool) (bool, error) {
	if s == nil || feedType == "" || feedID == 0 || status.ID == 0 {
		return false, nil
	}
	feedType = feedStorageType(feedType)
	prefix := redisConfig(s.cfg).prefix
	timelineKey := feedRedisKey(prefix, feedType, feedID)
	reblogKey := feedReblogRedisKey(prefix, feedType, feedID)
	statusID := strconv.FormatInt(status.ID, 10)

	if status.ReblogOfID.Valid && aggregateReblogs {
		rebloggedID := strconv.FormatInt(status.ReblogOfID.Int64, 10)
		rank, rankErr := s.redisCommand(ctx, "ZREVRANK", timelineKey, rebloggedID)
		if rankValue, ok := redisOptionalInt(rank, rankErr); ok && rankValue < feedReblogFalloff {
			return false, nil
		}
		added, err := s.redisCommand(ctx, "ZADD", reblogKey, "NX", statusID, rebloggedID)
		if err != nil {
			return false, err
		}
		if redisInt(added) > 0 {
			_, err = s.redisCommand(ctx, "ZADD", timelineKey, statusID, statusID)
			return err == nil, err
		}
		reblogSetKey := feedReblogStatusRedisKey(prefix, feedType, feedID, rebloggedID)
		_, err = s.redisCommand(ctx, "SADD", reblogSetKey, statusID)
		return false, err
	}

	score, scoreErr := s.redisCommand(ctx, "ZSCORE", reblogKey, statusID)
	if _, ok := redisOptionalInt(score, scoreErr); ok {
		return false, nil
	}
	_, err := s.redisCommand(ctx, "ZADD", timelineKey, statusID, statusID)
	return err == nil, err
}

func (s *Server) trimFeedContext(ctx context.Context, feedType string, feedID int64) error {
	if s == nil || feedType == "" || feedID == 0 {
		return nil
	}
	feedType = feedStorageType(feedType)
	prefix := redisConfig(s.cfg).prefix
	timelineKey := feedRedisKey(prefix, feedType, feedID)
	reblogKey := feedReblogRedisKey(prefix, feedType, feedID)
	if _, err := s.redisCommand(ctx, "ZREMRANGEBYRANK", timelineKey, "0", "-"+strconv.Itoa(feedMaxItems+1)); err != nil {
		return err
	}
	falloffValue, err := s.redisCommand(ctx, "ZREVRANGE", timelineKey, strconv.Itoa(feedReblogFalloff), strconv.Itoa(feedReblogFalloff), "WITHSCORES")
	if err != nil {
		return nil
	}
	falloffScore := firstRedisScoreInt(falloffValue)
	if falloffScore == 0 {
		return nil
	}
	value, err := s.redisCommand(ctx, "ZRANGEBYSCORE", reblogKey, "0", strconv.FormatInt(falloffScore, 10))
	if err != nil {
		return nil
	}
	rebloggedIDs, ok := redisStringArray(value)
	if !ok {
		return nil
	}
	for _, rebloggedID := range rebloggedIDs {
		if rebloggedID == "" {
			continue
		}
		_, _ = s.redisCommand(ctx, "ZREM", reblogKey, rebloggedID)
		_, _ = s.redisCommand(ctx, "DEL", feedReblogStatusRedisKey(prefix, feedType, feedID, rebloggedID))
	}
	return nil
}

func (s *Server) redisCommandValue(ctx context.Context, args ...string) any {
	value, err := s.redisCommand(ctx, args...)
	if err != nil {
		return nil
	}
	return value
}

func firstRedisScoreInt(value any) int64 {
	items, ok := redisStringArray(value)
	if !ok || len(items) < 2 {
		return 0
	}
	score, _ := strconv.ParseInt(strings.TrimSpace(items[1]), 10, 64)
	return score
}

func redisOptionalInt(value any, err error) (int64, bool) {
	if err != nil {
		return 0, false
	}
	switch typed := value.(type) {
	case int64:
		return typed, true
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return 0, false
		}
		parsed, parseErr := strconv.ParseInt(trimmed, 10, 64)
		if parseErr != nil {
			return 0, false
		}
		return parsed, true
	default:
		return 0, false
	}
}

// removeStatusFromFeedContext mirrors Rails FeedManager#remove_from_feed. It returns
// whether the status was actually removed from the cached feed so callers can gate
// streaming delete publishes the way Rails' unpush_from_home/unpush_from_list do.
func (s *Server) removeStatusFromFeedContext(ctx context.Context, feedType string, feedID int64, status models.Status, aggregateReblogs bool) (bool, error) {
	if s == nil || feedType == "" || feedID == 0 || status.ID == 0 {
		return false, nil
	}
	feedType = feedStorageType(feedType)
	prefix := redisConfig(s.cfg).prefix
	timelineKey := feedRedisKey(prefix, feedType, feedID)
	reblogKey := feedReblogRedisKey(prefix, feedType, feedID)
	statusID := strconv.FormatInt(status.ID, 10)

	removedValue, err := s.redisCommand(ctx, "ZREM", timelineKey, statusID)
	if err != nil {
		return false, err
	}
	removed := redisInt(removedValue) > 0

	if status.ReblogOfID.Valid && aggregateReblogs {
		if !removed {
			return false, nil
		}
		rebloggedID := strconv.FormatInt(status.ReblogOfID.Int64, 10)
		reblogSetKey := feedReblogStatusRedisKey(prefix, feedType, feedID, rebloggedID)
		_, _ = s.redisCommand(ctx, "SREM", reblogSetKey, statusID)
		_, _ = s.redisCommand(ctx, "ZREM", reblogKey, rebloggedID)
		if otherReblog := s.oldestFeedReblog(ctx, reblogSetKey); otherReblog != 0 {
			otherReblogID := strconv.FormatInt(otherReblog, 10)
			_, _ = s.redisCommand(ctx, "ZADD", timelineKey, otherReblogID, otherReblogID)
			_, _ = s.redisCommand(ctx, "ZADD", reblogKey, otherReblogID, rebloggedID)
		}
		return true, nil
	}

	reblogSetKey := feedReblogStatusRedisKey(prefix, feedType, feedID, statusID)
	_, _ = s.redisCommand(ctx, "DEL", reblogSetKey)
	_, _ = s.redisCommand(ctx, "ZREM", reblogKey, statusID)
	return removed, nil
}

func feedStorageType(feedType string) string {
	if feedType == "tags" {
		return "home"
	}
	return feedType
}

func (s *Server) oldestFeedReblog(ctx context.Context, reblogSetKey string) int64 {
	value, err := s.redisCommand(ctx, "SMEMBERS", reblogSetKey)
	if err != nil {
		return 0
	}
	members, ok := redisStringArray(value)
	if !ok {
		return 0
	}
	var oldest int64
	for _, member := range members {
		id, err := strconv.ParseInt(member, 10, 64)
		if err != nil || id == 0 {
			continue
		}
		if oldest == 0 || id < oldest {
			oldest = id
		}
	}
	return oldest
}

// feedStreamingChannel returns the Redis pub/sub channel Rails publishes streaming
// deletes to for a cached feed: "timeline:<account_id>" for home feeds and
// "timeline:list:<list_id>" for list feeds.
func feedStreamingChannel(prefix string, feedType string, feedID int64) string {
	if feedType == "list" {
		return prefix + "timeline:list:" + strconv.FormatInt(feedID, 10)
	}
	return prefix + "timeline:" + strconv.FormatInt(feedID, 10)
}

// unpushStatusFromFeed mirrors Rails FeedManager#unpush_from_home / unpush_from_list:
// it removes the status from the cached feed and, when something was actually removed,
// publishes a streaming "delete" event to the matching timeline channel.
func (s *Server) unpushStatusFromFeed(ctx context.Context, feedType string, feedID int64, status models.Status, aggregateReblogs bool) {
	if s == nil || feedID == 0 || status.ID == 0 {
		return
	}
	removed, err := s.removeStatusFromFeedContext(ctx, feedType, feedID, status, aggregateReblogs)
	if err != nil || !removed {
		return
	}
	_, _ = s.redisCommand(ctx, "PUBLISH", feedStreamingChannel(redisConfig(s.cfg).prefix, feedType, feedID), statusDeleteStreamPayload(status.ID))
}

// clearAccountFromHomeFeed mirrors Rails FeedManager#clear_from_home: it removes
// statuses from accountID's cached home feed that were authored by targetID, reblog a
// status authored by targetID, or (transitively) mention targetID, while preserving the
// rest of the cached feed and publishing matching streaming deletes. This replaces the
// coarser whole-feed cache clear for block/mute with Rails AfterBlockService semantics.
func (s *Server) clearAccountFromHomeFeed(ctx context.Context, database *gorm.DB, accountID int64, targetID int64) error {
	statuses, ok, err := s.loadFeedStatusesForClear(ctx, database, "home", accountID)
	if err != nil || !ok {
		return err
	}
	candidateIDs, reblogParentIDs := feedClearCandidateIDs(statuses)
	rebloggedSet := feedClearAccountStatusIDs(ctx, database, reblogParentIDs, targetID)
	mentionSet := feedClearMentionStatusIDs(ctx, database, candidateIDs, targetID)
	aggregateReblogs := s.accountAggregatesReblogs(ctx, accountID)
	for _, status := range statuses {
		if !feedClearStatusTargetsAccount(status, targetID, rebloggedSet, mentionSet) {
			continue
		}
		s.unpushStatusFromFeed(ctx, "home", accountID, status, aggregateReblogs)
	}
	return nil
}

// clearAccountFromListFeed mirrors Rails FeedManager#clear_from_list for a single list.
func (s *Server) clearAccountFromListFeed(ctx context.Context, database *gorm.DB, listID int64, accountID int64, targetID int64) error {
	statuses, ok, err := s.loadFeedStatusesForClear(ctx, database, "list", listID)
	if err != nil || !ok {
		return err
	}
	candidateIDs, reblogParentIDs := feedClearCandidateIDs(statuses)
	rebloggedSet := feedClearAccountStatusIDs(ctx, database, reblogParentIDs, targetID)
	mentionSet := feedClearMentionStatusIDs(ctx, database, candidateIDs, targetID)
	aggregateReblogs := s.accountAggregatesReblogs(ctx, accountID)
	for _, status := range statuses {
		if !feedClearStatusTargetsAccount(status, targetID, rebloggedSet, mentionSet) {
			continue
		}
		s.unpushStatusFromFeed(ctx, "list", listID, status, aggregateReblogs)
	}
	return nil
}

func (s *Server) loadFeedStatusesForClear(ctx context.Context, database *gorm.DB, feedType string, feedID int64) ([]models.Status, bool, error) {
	if s == nil || database == nil || feedID == 0 {
		return nil, false, nil
	}
	prefix := redisConfig(s.cfg).prefix
	var timelineKey string
	if feedType == "home" {
		timelineKey = homeFeedRedisKey(prefix, feedID)
	} else {
		timelineKey = feedRedisKey(prefix, feedType, feedID)
	}
	value, err := s.redisCommand(ctx, "ZRANGE", timelineKey, "0", "-1")
	if err != nil {
		return nil, false, nil
	}
	timelineIDs, ok := redisStringArray(value)
	if !ok || len(timelineIDs) == 0 {
		return nil, false, nil
	}
	var statuses []models.Status
	if err := database.WithContext(ctx).
		Select("id, account_id, reblog_of_id").
		Where("id IN ?", timelineIDs).
		Find(&statuses).Error; err != nil {
		return nil, false, err
	}
	return statuses, true, nil
}

// feedClearCandidateIDs collects the feed items' own ids plus their reblog parent ids,
// mirroring Rails' statuses.flat_map { |s| [s.id, s.reblog_of_id] }.compact. The first
// return value feeds the mention lookup, the second feeds the reblog-parent authorship
// lookup.
func feedClearCandidateIDs(statuses []models.Status) (candidateIDs []int64, reblogParentIDs []int64) {
	seen := map[int64]struct{}{}
	for _, status := range statuses {
		candidateIDs = append(candidateIDs, status.ID)
		if status.ReblogOfID.Valid {
			reblogParentIDs = append(reblogParentIDs, status.ReblogOfID.Int64)
			if _, ok := seen[status.ReblogOfID.Int64]; !ok {
				seen[status.ReblogOfID.Int64] = struct{}{}
				candidateIDs = append(candidateIDs, status.ReblogOfID.Int64)
			}
		}
	}
	return candidateIDs, reblogParentIDs
}

// feedClearAccountStatusIDs returns the subset of statusIDs authored by accountID,
// mirroring Rails' Status.where(id: ..., account: target_account).pluck(:id).
func feedClearAccountStatusIDs(ctx context.Context, database *gorm.DB, statusIDs []int64, accountID int64) map[int64]struct{} {
	set := map[int64]struct{}{}
	if len(statusIDs) == 0 || database == nil || accountID == 0 {
		return set
	}
	var ids []int64
	_ = database.WithContext(ctx).Model(&models.Status{}).
		Where("id IN ? AND account_id = ?", statusIDs, accountID).
		Distinct("id").Pluck("id", &ids).Error
	for _, id := range ids {
		set[id] = struct{}{}
	}
	return set
}

// feedClearMentionStatusIDs returns status ids (feed items or their reblog parents) that
// carry an active (non-silent) mention of accountID, mirroring Rails'
// Mention.active.where(status_id: ..., account: target_account).pluck(:status_id).
func feedClearMentionStatusIDs(ctx context.Context, database *gorm.DB, statusIDs []int64, accountID int64) map[int64]struct{} {
	set := map[int64]struct{}{}
	if len(statusIDs) == 0 || database == nil || accountID == 0 {
		return set
	}
	var ids []int64
	_ = database.WithContext(ctx).Model(&models.Mention{}).
		Where("status_id IN ? AND account_id = ? AND silent = false", statusIDs, accountID).
		Pluck("status_id", &ids).Error
	for _, id := range ids {
		set[id] = struct{}{}
	}
	return set
}

// feedClearStatusTargetsAccount reports whether a feed status should be removed for
// targetID, matching Rails clear_from_home's selection: authored by target, reblogs a
// status authored by target, or is/contains an active mention of target.
func feedClearStatusTargetsAccount(status models.Status, targetID int64, rebloggedSet, mentionSet map[int64]struct{}) bool {
	if status.AccountID == targetID {
		return true
	}
	if status.ReblogOfID.Valid {
		if _, ok := rebloggedSet[status.ReblogOfID.Int64]; ok {
			return true
		}
	}
	if _, ok := mentionSet[status.ID]; ok {
		return true
	}
	if status.ReblogOfID.Valid {
		if _, ok := mentionSet[status.ReblogOfID.Int64]; ok {
			return true
		}
	}
	return false
}
