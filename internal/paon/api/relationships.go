package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"github.com/mstdn-plusminus-io/paon/internal/paon/serializer"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *Server) accountRelationships(c *echo.Context) error {
	c.Response().Header().Set("Vary", "Authorization")
	account, _, err := s.requireAccountScope(c, "read", "read:follows")
	if err != nil {
		return err
	}

	ids := relationshipIDs(c)
	if len(ids) == 0 {
		return c.JSON(http.StatusOK, []serializer.Relationship{})
	}

	relationships, err := s.relationshipsFor(account.ID, ids)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, relationships)
}

func (s *Server) followAccount(c *echo.Context) error {
	account, _, err := s.requireAccountScope(c, "follow", "write", "write:follows")
	if err != nil {
		return err
	}
	setFollowsFamilyRateLimitHeaders(c, railsFollowsFamilyLimit-1)
	target, err := s.findAccountByID(c.Param("id"))
	if err != nil || target.SuspendedAt.Valid || accountHiddenFromAccountsShow(target) {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	if target.ID == account.ID {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	disallowed, err := s.followNotAllowed(account, target)
	if err != nil {
		return err
	}
	if disallowed {
		return apiError(c, http.StatusForbidden, "This action is not allowed")
	}

	payload, err := parseFollowPayload(c)
	if err != nil {
		return apiError(c, http.StatusBadRequest, "Malformed request")
	}
	if !validRelationshipLanguages(payload.Languages) {
		return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Languages is invalid")
	}
	now := time.Now().UTC()
	sourceNotFollowingAnyone := s.accountNotFollowingAnyone(c.Request().Context(), account.ID)
	var deliveredFollowID int64
	var deliveredFollowURI string
	var notificationIDs []int64
	var notificationPayloads []asynqLocalNotificationPayload
	followChanged := false
	followInteractionCreated := false
	var existingRelationshipUpdated bool

	err = s.db.Transaction(func(tx *gorm.DB) error {
		var follow models.Follow
		err := tx.Where("account_id = ? AND target_account_id = ?", account.ID, target.ID).First(&follow).Error
		if err == nil {
			existingRelationshipUpdated = true
			return tx.Model(&follow).Updates(followPayloadUpdates(payload, now)).Error
		}
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		var req models.FollowRequest
		err = tx.Where("account_id = ? AND target_account_id = ?", account.ID, target.ID).First(&req).Error
		if err == nil {
			existingRelationshipUpdated = true
			return tx.Model(&req).Updates(followPayloadUpdates(payload, now)).Error
		}
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.removePotentialFriendship(c.Request().Context(), account.ID, target.ID)
	if existingRelationshipUpdated {
		s.invalidateFollowRelationshipCaches(c.Request().Context(), *account, target.ID)
		return s.relationshipResponse(c, account.ID, target)
	}
	if reached, limit, err := s.followLimitReached(c.Request().Context(), *account); err != nil {
		return err
	} else if reached {
		return apiError(c, http.StatusUnprocessableEntity, followLimitReachedMessage(limit))
	}

	rateLimitRecorded, err := s.consumeRailsFamilyRateLimit(c, *account, railsRateLimitFamilyFollows, now)
	if err != nil {
		return err
	}
	if followRequiresRequest(account, target) {
		err = s.db.Transaction(func(tx *gorm.DB) error {
			req := models.FollowRequest{CreatedAt: now, UpdatedAt: now, AccountID: account.ID, TargetAccountID: target.ID, ShowReblogs: payload.ShowReblogs, Notify: payload.Notify, Languages: models.StringArray(payload.Languages), URI: models.NullSafeString(activityPubGeneratedPayloadURI(s))}
			res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&req)
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected > 0 {
				followInteractionCreated = true
				if !target.Local() {
					deliveredFollowID = req.ID
					deliveredFollowURI = string(req.URI)
				}
				if target.Local() {
					notificationPayloads = append(notificationPayloads, asynqLocalNotificationPayload{ReceiverAccountID: target.ID, FromAccountID: account.ID, ActivityID: req.ID, ActivityType: "FollowRequest", Type: "follow_request"})
				}
			}
			return nil
		})
	} else {
		err = s.db.Transaction(func(tx *gorm.DB) error {
			follow := models.Follow{CreatedAt: now, UpdatedAt: now, AccountID: account.ID, TargetAccountID: target.ID, ShowReblogs: payload.ShowReblogs, Notify: payload.Notify, Languages: models.StringArray(payload.Languages), URI: models.NullSafeString(activityPubGeneratedPayloadURI(s))}
			res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&follow)
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected == 0 {
				return nil
			}
			followChanged = true
			followInteractionCreated = true
			if !target.Local() {
				deliveredFollowID = follow.ID
				deliveredFollowURI = string(follow.URI)
			}
			if err := incrementAccountStatCounter(tx, account.ID, accountStatCounterFollowing, 1); err != nil {
				return err
			}
			if err := incrementAccountStatCounter(tx, target.ID, accountStatCounterFollowers, 1); err != nil {
				return err
			}
			notificationPayloads = append(notificationPayloads, asynqLocalNotificationPayload{ReceiverAccountID: target.ID, FromAccountID: account.ID, ActivityID: follow.ID, ActivityType: "Follow", Type: "follow"})
			return nil
		})
	}
	if err != nil {
		if rateLimitRecorded {
			s.rollbackRailsFamilyRateLimit(c.Request().Context(), *account, railsRateLimitFamilyFollows, now)
		}
		return err
	}
	if rateLimitRecorded && !followInteractionCreated {
		s.rollbackRailsFamilyRateLimit(c.Request().Context(), *account, railsRateLimitFamilyFollows, now)
		setFollowsFamilyRateLimitHeaders(c, railsFollowsFamilyLimit)
	}
	createdNotificationIDs, err := s.enqueueOrCreateLocalNotifications(c.Request().Context(), notificationPayloads)
	if err != nil {
		return err
	}
	notificationIDs = append(notificationIDs, createdNotificationIDs...)
	if followInteractionCreated {
		s.activityTrackerIncrementBasic(c.Request().Context(), "activity:interactions", now, 1)
		if sourceNotFollowingAnyone {
			s.markHomeFeedAsPartial(c.Request().Context(), account.ID)
		}
	}
	s.publishNotificationIDs(notificationIDs)
	if followRequiresRequest(account, target) {
		s.invalidateRelationshipCaches(c.Request().Context(), account.ID, target.ID)
	} else {
		s.invalidateFollowRelationshipCaches(c.Request().Context(), *account, target.ID)
	}
	if followChanged {
		s.meiliReindexPrivateStatusesForAccountsBestEffort(c.Request().Context(), target.ID)
		if !followRequiresRequest(account, target) {
			s.mergeAfterDirectFollowBestEffort(c.Request().Context(), target.ID, *account)
		}
	}
	if deliveredFollowID != 0 {
		_ = s.deliverActivityPubFollow(*account, *target, deliveredFollowID, deliveredFollowURI)
	}

	relationships, err := s.relationshipsForAccounts(account.ID, []int64{target.ID}, []models.Account{*target})
	if err != nil {
		return err
	}
	if len(relationships) == 0 {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	return c.JSON(http.StatusOK, relationships[0])
}

func followRequiresRequest(source *models.Account, target *models.Account) bool {
	if source == nil || target == nil {
		return false
	}
	return target.Locked || source.SilencedAt.Valid || (!target.Local() && target.Protocol == 1)
}

func (s *Server) followLimitReached(ctx context.Context, account models.Account) (bool, int64, error) {
	return s.followLimitReachedInDB(ctx, s.db, account)
}

func (s *Server) followLimitReachedInDB(ctx context.Context, db *gorm.DB, account models.Account) (bool, int64, error) {
	if !account.Local() {
		return false, 0, nil
	}
	stat := account.AccountStat
	if stat.AccountID == 0 {
		if db == nil {
			return false, followLimitForAccountStat(stat, 0, 0), nil
		}
		err := db.WithContext(ctx).Select("account_id", "following_count", "followers_count").Where("account_id = ?", account.ID).First(&stat).Error
		if err != nil {
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return false, 0, err
			}
			stat.AccountID = account.ID
		}
	}
	limit := followLimitForAccountStat(stat, s.maxFollowsThreshold(), s.maxFollowsRatio())
	return stat.FollowingCount >= limit, limit, nil
}

func (s *Server) maxFollowsThreshold() int {
	if s != nil && (s.cfg.MaxFollowsThresholdSet || s.cfg.MaxFollowsThreshold > 0) {
		return s.cfg.MaxFollowsThreshold
	}
	return 7500
}

func (s *Server) maxFollowsRatio() float64 {
	if s != nil && (s.cfg.MaxFollowsRatioSet || s.cfg.MaxFollowsRatio > 0) {
		return s.cfg.MaxFollowsRatio
	}
	return 1.1
}

func followLimitForAccountStat(stat models.AccountStat, threshold int, ratio float64) int64 {
	baseLimit := int64(threshold)
	if stat.FollowingCount < baseLimit {
		return baseLimit
	}
	scaled := int64(math.Round(float64(stat.FollowersCount) * ratio))
	if scaled < baseLimit {
		return baseLimit
	}
	return scaled
}

func followLimitReachedMessage(limit int64) string {
	return "Validation failed: You cannot follow more than " + strconv.FormatInt(limit, 10) + " people"
}

func (s *Server) mergeAfterDirectFollowBestEffort(ctx context.Context, fromAccountID int64, intoAccount models.Account) {
	if s == nil || s.db == nil || fromAccountID == 0 || intoAccount.ID == 0 {
		return
	}
	if s.enqueueMergeTask(fromAccountID, intoAccount.ID) {
		return
	}
	_ = ctx
	go func() {
		workerCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = s.mergeAccountIntoHomeFeed(workerCtx, s.db, fromAccountID, intoAccount)
		_, _ = s.redisCommand(workerCtx, "DEL", redisConfig(s.cfg).prefix+"account:"+strconv.FormatInt(intoAccount.ID, 10)+":regeneration")
	}()
}

func (s *Server) unmergeAfterUnfollowBestEffort(ctx context.Context, fromAccountID int64, intoAccount models.Account) {
	if s == nil || s.db == nil || fromAccountID == 0 || intoAccount.ID == 0 {
		return
	}
	if s.enqueueUnmergeTask(fromAccountID, intoAccount.ID) {
		return
	}
	_ = ctx
	go func() {
		workerCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = s.unmergeAccountFromHomeFeed(workerCtx, s.db, fromAccountID, intoAccount)
	}()
}

func (s *Server) restoreAfterUnmuteFeedCache(ctx context.Context, accountID int64, targetID int64) {
	if s == nil || s.db == nil || accountID == 0 || targetID == 0 {
		return
	}
	var count int64
	if err := s.db.WithContext(ctx).Model(&models.Follow{}).Where("account_id = ? AND target_account_id = ?", accountID, targetID).Count(&count).Error; err != nil || count == 0 {
		return
	}
	s.mergeAfterDirectFollowBestEffort(ctx, targetID, models.Account{ID: accountID})
}

func (s *Server) accountNotFollowingAnyone(ctx context.Context, accountID int64) bool {
	if s == nil || s.db == nil || accountID == 0 {
		return false
	}
	var count int64
	if err := s.db.WithContext(ctx).Model(&models.Follow{}).Where("account_id = ?", accountID).Count(&count).Error; err != nil {
		return false
	}
	return count == 0
}

func (s *Server) markHomeFeedAsPartial(ctx context.Context, accountID int64) {
	if s == nil || accountID == 0 {
		return
	}
	_, _ = s.redisCommand(ctx, "SET", redisConfig(s.cfg).prefix+"account:"+strconv.FormatInt(accountID, 10)+":regeneration", "true", "NX", "EX", strconv.FormatInt(int64((24*time.Hour)/time.Second), 10))
}

func (s *Server) followNotAllowed(source *models.Account, target *models.Account) (bool, error) {
	if source == nil || target == nil {
		return true, nil
	}
	if target.MovedToAccountID.Valid || (!target.Local() && target.Protocol == 0) {
		return true, nil
	}
	domain := accountRemoteDomain(target)
	if domain != "" {
		disallowed, err := s.domainNotAllowed(domain)
		if err != nil || disallowed {
			return disallowed, err
		}
		blocked, err := s.accountDomainBlocking(source.ID, domain)
		if err != nil || blocked {
			return blocked, err
		}
	}
	if s == nil || s.db == nil {
		return false, nil
	}
	var count int64
	err := s.db.Model(&models.Block{}).
		Where("(account_id = ? AND target_account_id = ?) OR (account_id = ? AND target_account_id = ?)", target.ID, source.ID, source.ID, target.ID).
		Count(&count).Error
	return count > 0, err
}

func accountRemoteDomain(account *models.Account) string {
	if account == nil || !account.Domain.Valid {
		return ""
	}
	return normalizeDomain(account.Domain.String)
}

func (s *Server) accountDomainBlocking(accountID int64, domain string) (bool, error) {
	domain = normalizeDomain(domain)
	if domain == "" || s == nil || s.db == nil {
		return false, nil
	}
	var count int64
	err := s.db.Model(&models.AccountDomainBlock{}).
		Where("account_id = ? AND lower(domain) = ?", accountID, domain).
		Count(&count).Error
	return count > 0, err
}

func (s *Server) domainNotAllowed(domain string) (bool, error) {
	domain = normalizeDomain(domain)
	if domain == "" || s == nil || s.db == nil {
		return false, nil
	}
	var count int64
	if s.cfg.LimitedFederationMode {
		err := s.db.Model(&models.DomainAllow{}).Where("lower(domain) = ?", domain).Count(&count).Error
		return count == 0, err
	}
	var block models.DomainBlock
	err := s.db.Where("lower(domain) IN ?", domainControlVariants(domain)).
		Order("char_length(domain) DESC").
		Limit(1).
		First(&block).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return domainBlockSeverityIs(block, "suspend"), nil
}

func domainControlVariants(domain string) []string {
	domain = normalizeDomain(domain)
	if domain == "" {
		return nil
	}
	segments := strings.Split(domain, ".")
	variants := make([]string, 0, len(segments))
	for i := range segments {
		variants = append(variants, strings.Join(segments[i:], "."))
	}
	return variants
}

type followPayload struct {
	ShowReblogs    bool
	HasShowReblogs bool
	Notify         bool
	HasNotify      bool
	Languages      []string
	HasLanguages   bool
}

func parseFollowPayload(c *echo.Context) (followPayload, error) {
	payload := followPayload{ShowReblogs: true}
	if requestIsJSON(c) {
		raw := map[string]json.RawMessage{}
		if err := json.NewDecoder(c.Request().Body).Decode(&raw); err != nil {
			return payload, err
		}
		if value, ok := raw["reblogs"]; ok {
			payload.HasShowReblogs = true
			if parsed, present := rawJSONBool(value); present {
				payload.ShowReblogs = parsed
			} else if text := rawJSONString(value); text != "" {
				payload.ShowReblogs = truthy(text)
			}
		}
		if value, ok := raw["notify"]; ok {
			payload.HasNotify = true
			if parsed, present := rawJSONBool(value); present {
				payload.Notify = parsed
			} else if text := rawJSONString(value); text != "" {
				payload.Notify = truthy(text)
			}
		}
		if value, ok := raw["languages"]; ok {
			payload.HasLanguages = true
			payload.Languages = normalizeRelationshipLanguages(rawJSONStringSlice(value))
		}
		return payload, nil
	}
	values, _ := c.FormValues()
	if _, ok := values["reblogs"]; ok {
		payload.HasShowReblogs = true
		payload.ShowReblogs = truthy(lastFormValue(values, "reblogs"))
	}
	if _, ok := values["notify"]; ok {
		payload.HasNotify = true
		payload.Notify = truthy(lastFormValue(values, "notify"))
	}
	if _, ok := values["languages[]"]; ok {
		payload.HasLanguages = true
	}
	if _, ok := values["languages"]; ok {
		payload.HasLanguages = true
	}
	if payload.HasLanguages {
		payload.Languages = relationshipLanguageValues(c)
	}
	return payload, nil
}

func followPayloadUpdates(payload followPayload, now time.Time) map[string]any {
	updates := map[string]any{}
	if payload.HasShowReblogs {
		updates["show_reblogs"] = payload.ShowReblogs
	}
	if payload.HasNotify {
		updates["notify"] = payload.Notify
	}
	if payload.HasLanguages {
		updates["languages"] = models.StringArray(payload.Languages)
	}
	if len(updates) > 0 {
		updates["updated_at"] = now
	}
	return updates
}

func relationshipRedisLockName(sourceID int64, targetID int64) string {
	if sourceID > targetID {
		sourceID, targetID = targetID, sourceID
	}
	return "relationship:" + strconv.FormatInt(sourceID, 10) + ":" + strconv.FormatInt(targetID, 10)
}

func (s *Server) unfollowAccount(c *echo.Context) error {
	account, _, err := s.requireAccountScope(c, "follow", "write", "write:follows")
	if err != nil {
		return err
	}
	target, err := s.findAccountByID(c.Param("id"))
	if err != nil || accountHiddenFromAccountsShow(target) {
		return apiError(c, http.StatusNotFound, "Record not found")
	}

	acquired, releaseRelationshipLock, err := s.acquireActivityPubRedisLock(c.Request().Context(), relationshipRedisLockName(account.ID, target.ID), 15*time.Minute)
	if err != nil {
		return err
	}
	if !acquired {
		return apiError(c, http.StatusServiceUnavailable, "There was a temporary problem serving your request, please try again")
	}
	defer releaseRelationshipLock()

	var undoFollowID int64
	var undoFollowURI string
	followDeleted := false
	err = s.db.Transaction(func(tx *gorm.DB) error {
		var follow models.Follow
		err := tx.Where("account_id = ? AND target_account_id = ?", account.ID, target.ID).First(&follow).Error
		if err == nil {
			undoFollowID = follow.ID
			undoFollowURI = string(follow.URI)
			if undoFollowURI == "" && !target.Local() {
				undoFollowURI = activityPubFollowURI(s, *account, follow.ID)
			}
			if err := deleteFollow(tx, follow); err != nil {
				return err
			}
			followDeleted = true
			return nil
		}
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		var req models.FollowRequest
		err = tx.Where("account_id = ? AND target_account_id = ?", account.ID, target.ID).First(&req).Error
		if err == nil {
			undoFollowID = req.ID
			undoFollowURI = string(req.URI)
			if undoFollowURI == "" && !target.Local() {
				undoFollowURI = activityPubFollowURI(s, *account, req.ID)
			}
			if err := tx.Where("activity_type = ? AND activity_id = ?", "FollowRequest", req.ID).Delete(&models.Notification{}).Error; err != nil {
				return err
			}
			if _, err := deleteListAccountsForRejectedFollowRequest(tx, req.ID); err != nil {
				return err
			}
			return tx.Delete(&req).Error
		}
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.invalidateFollowRelationshipCaches(c.Request().Context(), *account, target.ID)
	if followDeleted {
		s.unmergeAfterUnfollowBestEffort(c.Request().Context(), target.ID, *account)
		s.meiliReindexPrivateStatusesForAccountsBestEffort(c.Request().Context(), target.ID)
	}
	if undoFollowID != 0 {
		_ = s.deliverActivityPubUndoFollow(*account, *target, undoFollowID, undoFollowURI)
	}

	relationships, err := s.relationshipsForAccounts(account.ID, []int64{target.ID}, []models.Account{*target})
	if err != nil {
		return err
	}
	if len(relationships) == 0 {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	return c.JSON(http.StatusOK, relationships[0])
}

func (s *Server) removeFromFollowers(c *echo.Context) error {
	account, target, err := s.relationshipAccounts(c, "follow", "write", "write:follows")
	if err != nil {
		return err
	}
	var removedFollowID int64
	var removedFollowURI string
	followDeleted := false
	err = s.db.Transaction(func(tx *gorm.DB) error {
		var follow models.Follow
		err := tx.Where("account_id = ? AND target_account_id = ?", target.ID, account.ID).First(&follow).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		removedFollowID = follow.ID
		removedFollowURI = string(follow.URI)
		if removedFollowURI == "" && !target.Local() {
			removedFollowURI = activityPubFollowURI(s, *target, follow.ID)
		}
		if err := deleteFollow(tx, follow); err != nil {
			return err
		}
		followDeleted = true
		return nil
	})
	if err != nil {
		return err
	}
	s.invalidateFollowRelationshipCaches(c.Request().Context(), *target, account.ID)
	if followDeleted {
		// Rails RemoveFromFollowersService does not enqueue an UnmergeWorker, so no feed
		// cleanup is performed here; only the follow row is destroyed.
		s.meiliReindexPrivateStatusesForAccountsBestEffort(c.Request().Context(), account.ID)
	}
	if removedFollowID != 0 && account.Local() && !target.Local() {
		_ = s.deliverActivityPubFollowResponse("Reject", *account, *target, removedFollowID, removedFollowURI)
	}
	return s.relationshipResponse(c, account.ID, target)
}

func (s *Server) blockAccount(c *echo.Context) error {
	account, target, err := s.relationshipAccounts(c, "follow", "write", "write:blocks")
	if err != nil {
		return err
	}
	if target.ID == account.ID {
		return s.relationshipResponse(c, account.ID, target)
	}

	now := time.Now().UTC()
	var block *models.Block
	var cleanup accountBlockRelationshipCleanup
	err = s.db.Transaction(func(tx *gorm.DB) error {
		var err error
		cleanup, err = cleanupAccountBlockRelationships(tx, *account, *target)
		if err != nil {
			return err
		}
		block, err = s.createAccountBlock(tx, account.ID, target.ID, now)
		if err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.clearAfterBlockFeedCaches(c.Request().Context(), account.ID, target.ID)
	s.removePotentialFriendship(c.Request().Context(), account.ID, target.ID)
	s.applyAccountBlockRelationshipCleanupEffects(c.Request().Context(), *account, *target, cleanup)
	s.invalidateBlockRelationshipCaches(c.Request().Context(), account.ID, target.ID)
	s.meiliReindexPrivateStatusesForAccountsBestEffort(c.Request().Context(), account.ID, target.ID)
	if block != nil {
		_ = s.deliverActivityPubBlock(*account, *target, block.ID, string(block.URI))
	}
	return s.relationshipResponse(c, account.ID, target)
}

func (s *Server) unblockAccount(c *echo.Context) error {
	account, target, err := s.relationshipAccounts(c, "follow", "write", "write:blocks")
	if err != nil {
		return err
	}
	var block models.Block
	err = s.db.Where("account_id = ? AND target_account_id = ?", account.ID, target.ID).First(&block).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	if err := s.db.Where("account_id = ? AND target_account_id = ?", account.ID, target.ID).Delete(&models.Block{}).Error; err != nil {
		return err
	}
	s.invalidateBlockRelationshipCaches(c.Request().Context(), account.ID, target.ID)
	if block.ID != 0 {
		_ = s.deliverActivityPubUndoBlock(*account, *target, block.ID, string(block.URI))
	}
	return s.relationshipResponse(c, account.ID, target)
}

func (s *Server) muteAccount(c *echo.Context) error {
	account, target, err := s.relationshipAccounts(c, "follow", "write", "write:mutes")
	if err != nil {
		return err
	}
	if target.ID == account.ID {
		return s.relationshipResponse(c, account.ID, target)
	}

	now := time.Now().UTC()
	payload, err := parseAccountMutePayload(c)
	if err != nil {
		return apiError(c, http.StatusBadRequest, "Malformed request")
	}
	var expiresAt any = nil
	var expiresAtTime time.Time
	if payload.Duration != 0 {
		expiresAtTime = now.Add(time.Duration(payload.Duration) * time.Second)
		expiresAt = expiresAtTime
	}

	var muteID int64
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if err := upsertAccountMute(tx, account.ID, target.ID, payload.Notifications, now, expiresAt); err != nil {
			return err
		}
		if payload.Duration != 0 {
			var mute models.Mute
			if err := tx.Select("id").Where("account_id = ? AND target_account_id = ?", account.ID, target.ID).First(&mute).Error; err != nil {
				return err
			}
			muteID = mute.ID
		}
		return nil
	})
	if err != nil {
		return err
	}
	if muteID != 0 {
		s.enqueueDeleteMuteTask(muteID, expiresAtTime)
	}
	if payload.Notifications {
		s.clearAfterBlockFeedCaches(c.Request().Context(), account.ID, target.ID)
	} else {
		s.clearAfterMuteFeedCache(c.Request().Context(), account.ID, target.ID)
	}
	s.removePotentialFriendship(c.Request().Context(), account.ID, target.ID)
	s.invalidateMuteRelationshipCaches(c.Request().Context(), account.ID, target.ID)
	return s.relationshipResponse(c, account.ID, target)
}

type accountMutePayload struct {
	Notifications bool
	Duration      int64
}

func parseAccountMutePayload(c *echo.Context) (accountMutePayload, error) {
	payload := accountMutePayload{Notifications: true}
	if requestIsJSON(c) {
		raw := map[string]json.RawMessage{}
		if err := json.NewDecoder(c.Request().Body).Decode(&raw); err != nil {
			return payload, err
		}
		if value, ok := raw["notifications"]; ok {
			if parsed, present := rawJSONBool(value); present {
				payload.Notifications = parsed
			} else if text := rawJSONString(value); text != "" {
				payload.Notifications = truthy(text)
			}
		}
		if value, ok := raw["duration"]; ok {
			payload.Duration = durationSeconds(rawJSONString(value))
			if payload.Duration == 0 {
				if parsed := int64(rawJSONIntDefault(value)); parsed > 0 {
					payload.Duration = parsed
				}
			}
		}
		return payload, nil
	}
	values, _ := c.FormValues()
	if _, ok := values["notifications"]; ok {
		payload.Notifications = truthy(lastFormValue(values, "notifications"))
	}
	payload.Duration = durationSeconds(lastFormValue(values, "duration"))
	return payload, nil
}

func (s *Server) unmuteAccount(c *echo.Context) error {
	account, target, err := s.relationshipAccounts(c, "follow", "write", "write:mutes")
	if err != nil {
		return err
	}
	if err := s.db.Where("account_id = ? AND target_account_id = ?", account.ID, target.ID).Delete(&models.Mute{}).Error; err != nil {
		return err
	}
	s.restoreAfterUnmuteFeedCache(c.Request().Context(), account.ID, target.ID)
	s.invalidateMuteRelationshipCaches(c.Request().Context(), account.ID, target.ID)
	return s.relationshipResponse(c, account.ID, target)
}

func (s *Server) relationshipAccounts(c *echo.Context, scopes ...string) (*models.Account, *models.Account, error) {
	account, _, err := s.requireAccountScope(c, scopes...)
	if err != nil {
		return nil, nil, err
	}
	target, err := s.findAccountByID(c.Param("id"))
	if err != nil || accountHiddenFromAccountsShow(target) {
		return nil, nil, apiError(c, http.StatusNotFound, "Record not found")
	}
	return account, target, nil
}

func (s *Server) relationshipResponse(c *echo.Context, accountID int64, target *models.Account) error {
	c.Response().Header().Set("Vary", "Authorization")
	relationships, err := s.relationshipsForAccounts(accountID, []int64{target.ID}, []models.Account{*target})
	if err != nil {
		return err
	}
	if len(relationships) == 0 {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	return c.JSON(http.StatusOK, relationships[0])
}

func (s *Server) createAccountBlock(tx *gorm.DB, accountID int64, targetID int64, now time.Time) (*models.Block, error) {
	block := models.Block{CreatedAt: now, UpdatedAt: now, AccountID: accountID, TargetAccountID: targetID}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&block).Error; err != nil {
		return nil, err
	}
	if block.ID == 0 {
		if err := tx.Where("account_id = ? AND target_account_id = ?", accountID, targetID).First(&block).Error; err != nil {
			return nil, err
		}
	}
	if block.URI == "" {
		block.URI = models.NullSafeString(activityPubGeneratedPayloadURI(s))
		if err := tx.Model(&block).Updates(map[string]any{"uri": block.URI, "updated_at": now}).Error; err != nil {
			return nil, err
		}
	}
	return &block, nil
}

type accountBlockRelationshipCleanup struct {
	UndoFollows   []accountBlockFollowDelivery
	RejectFollows []accountBlockFollowDelivery
	Unmerges      []accountBlockUnmerge
}

type accountBlockFollowDelivery struct {
	Local  models.Account
	Remote models.Account
	ID     int64
	URI    string
}

type accountBlockUnmerge struct {
	FromAccountID int64
	IntoAccount   models.Account
}

func cleanupAccountBlockRelationships(tx *gorm.DB, account models.Account, target models.Account) (accountBlockRelationshipCleanup, error) {
	effects := accountBlockRelationshipCleanup{}
	if follow, deleted, err := deleteFollowEdgeReturningFollow(tx, account.ID, target.ID); err != nil {
		return effects, err
	} else if deleted {
		effects.UndoFollows = append(effects.UndoFollows, accountBlockFollowDelivery{Local: account, Remote: target, ID: follow.ID, URI: string(follow.URI)})
		effects.Unmerges = append(effects.Unmerges, accountBlockUnmerge{FromAccountID: target.ID, IntoAccount: account})
	}
	if follow, deleted, err := deleteFollowEdgeReturningFollow(tx, target.ID, account.ID); err != nil {
		return effects, err
	} else if deleted {
		if account.Local() && !target.Local() && target.Protocol == 1 {
			effects.RejectFollows = append(effects.RejectFollows, accountBlockFollowDelivery{Local: account, Remote: target, ID: follow.ID, URI: string(follow.URI)})
		}
		effects.Unmerges = append(effects.Unmerges, accountBlockUnmerge{FromAccountID: account.ID, IntoAccount: target})
	}
	var requestIDs []int64
	if err := tx.Model(&models.FollowRequest{}).
		Where("account_id = ? AND target_account_id = ?", target.ID, account.ID).
		Pluck("id", &requestIDs).Error; err != nil {
		return effects, err
	}
	if len(requestIDs) > 0 {
		if err := tx.Where("activity_type = ? AND activity_id IN ?", "FollowRequest", requestIDs).Delete(&models.Notification{}).Error; err != nil {
			return effects, err
		}
	}
	var requests []models.FollowRequest
	if err := tx.Where("account_id = ? AND target_account_id = ?", target.ID, account.ID).Find(&requests).Error; err != nil {
		return effects, err
	}
	for _, req := range requests {
		if account.Local() && !target.Local() && target.Protocol == 1 {
			effects.RejectFollows = append(effects.RejectFollows, accountBlockFollowDelivery{Local: account, Remote: target, ID: req.ID, URI: string(req.URI)})
		}
	}
	if err := tx.Where("account_id = ? AND target_account_id = ?", target.ID, account.ID).Delete(&models.FollowRequest{}).Error; err != nil {
		return effects, err
	}
	return effects, nil
}

func (s *Server) applyAccountBlockRelationshipCleanupEffects(ctx context.Context, account models.Account, target models.Account, effects accountBlockRelationshipCleanup) {
	for _, effect := range effects.Unmerges {
		s.unmergeAfterUnfollowBestEffort(ctx, effect.FromAccountID, effect.IntoAccount)
	}
	for _, delivery := range effects.UndoFollows {
		uri := delivery.URI
		if uri == "" && !delivery.Remote.Local() {
			uri = activityPubFollowURI(s, delivery.Local, delivery.ID)
		}
		_ = s.deliverActivityPubUndoFollow(delivery.Local, delivery.Remote, delivery.ID, uri)
	}
	for _, delivery := range effects.RejectFollows {
		uri := delivery.URI
		if uri == "" {
			uri = activityPubFollowURI(s, delivery.Remote, delivery.ID)
		}
		_ = s.deliverActivityPubFollowResponse("Reject", delivery.Local, delivery.Remote, delivery.ID, uri)
	}
	if len(effects.UndoFollows) > 0 || len(effects.RejectFollows) > 0 || len(effects.Unmerges) > 0 {
		s.invalidateFollowRelationshipCaches(ctx, account, target.ID)
		s.invalidateFollowRelationshipCaches(ctx, target, account.ID)
	}
}

func afterBlockServiceCleanup(tx *gorm.DB, accountID int64, targetID int64) error {
	if err := tx.Where("account_id = ? AND from_account_id = ?", accountID, targetID).Delete(&models.Notification{}).Error; err != nil {
		return err
	}
	return tx.Exec("DELETE FROM account_conversations WHERE account_id = ? AND ? = ANY(participant_account_ids)", accountID, targetID).Error
}

func (s *Server) clearAfterBlockFeedCaches(ctx context.Context, accountID int64, targetID int64) {
	if s == nil || s.db == nil || accountID == 0 || targetID == 0 {
		return
	}
	_ = ctx
	// Mirror Rails BlockWorker -> AfterBlockService: enqueue cleanup of targetID's
	// statuses/reblogs/mentions from home/list feeds plus notifications/conversations.
	if s.enqueueBlockTask(accountID, targetID) {
		return
	}
	go func() {
		workerCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = s.runAfterBlockWorkerEffects(workerCtx, s.db.WithContext(workerCtx), accountID, targetID)
	}()
}

func (s *Server) clearAfterMuteFeedCache(ctx context.Context, accountID int64, targetID int64) {
	if s == nil || s.db == nil || accountID == 0 || targetID == 0 {
		return
	}
	_ = ctx
	// Mirror Rails MuteWorker: enqueue clearing targetID's statuses/reblogs/mentions
	// from the home feed (Rails FeedManager#clear_from_home).
	if s.enqueueMuteTask(accountID, targetID) {
		return
	}
	go func() {
		workerCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = s.runMuteWorkerEffects(workerCtx, s.db.WithContext(workerCtx), accountID, targetID)
	}()
}

func (s *Server) runAfterBlockWorkerEffects(ctx context.Context, database *gorm.DB, accountID int64, targetID int64) error {
	if s == nil || database == nil || accountID == 0 || targetID == 0 {
		return nil
	}
	if err := afterBlockServiceCleanup(database, accountID, targetID); err != nil {
		return err
	}
	if err := s.clearAccountFromHomeFeed(ctx, database, accountID, targetID); err != nil {
		return err
	}
	var listIDs []int64
	if err := database.WithContext(ctx).Table("lists").
		Select("lists.id").
		Where("lists.account_id = ?", accountID).
		Pluck("lists.id", &listIDs).Error; err != nil {
		return err
	}
	for _, listID := range listIDs {
		if err := s.clearAccountFromListFeed(ctx, database, listID, accountID, targetID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) runMuteWorkerEffects(ctx context.Context, database *gorm.DB, accountID int64, targetID int64) error {
	if s == nil || database == nil || accountID == 0 || targetID == 0 {
		return nil
	}
	return s.clearAccountFromHomeFeed(ctx, database, accountID, targetID)
}

func upsertAccountMute(tx *gorm.DB, accountID int64, targetID int64, hideNotifications bool, now time.Time, expiresAt ...any) error {
	expiresValue := any(nil)
	if len(expiresAt) > 0 {
		expiresValue = expiresAt[0]
	}
	return tx.Exec(`
		INSERT INTO mutes (account_id, target_account_id, hide_notifications, expires_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (account_id, target_account_id)
		DO UPDATE SET hide_notifications = excluded.hide_notifications, expires_at = excluded.expires_at, updated_at = excluded.updated_at
	`, accountID, targetID, hideNotifications, expiresValue, now, now).Error
}

func (s *Server) relationshipsFor(accountID int64, ids []int64) ([]serializer.Relationship, error) {
	var accounts []models.Account
	if err := s.db.Where("id IN ? AND suspended_at IS NULL", ids).Find(&accounts).Error; err != nil {
		return nil, err
	}
	return s.relationshipsForAccounts(accountID, ids, accounts)
}

func (s *Server) relationshipsForAccounts(accountID int64, ids []int64, accounts []models.Account) ([]serializer.Relationship, error) {
	existing := map[int64]struct{}{}
	domainsByID := map[int64]string{}
	for _, account := range accounts {
		existing[account.ID] = struct{}{}
		if account.Domain.Valid && strings.TrimSpace(account.Domain.String) != "" {
			domainsByID[account.ID] = account.Domain.String
		}
	}

	following, err := s.followMap(accountID, ids, false)
	if err != nil {
		return nil, err
	}
	followedBy, err := s.followMap(accountID, ids, true)
	if err != nil {
		return nil, err
	}
	requested, err := s.followRequestMap(accountID, ids, false)
	if err != nil {
		return nil, err
	}
	requestedBy, err := s.followRequestMap(accountID, ids, true)
	if err != nil {
		return nil, err
	}
	blocking, err := s.idSet(&models.Block{}, "account_id = ? AND target_account_id IN ?", accountID, ids, "target_account_id")
	if err != nil {
		return nil, err
	}
	blockedBy, err := s.idSet(&models.Block{}, "target_account_id = ? AND account_id IN ?", accountID, ids, "account_id")
	if err != nil {
		return nil, err
	}
	muting, mutingNotifications, err := s.muteMaps(accountID, ids)
	if err != nil {
		return nil, err
	}
	endorsed, err := s.idSet(&models.AccountPin{}, "account_id = ? AND target_account_id IN ?", accountID, ids, "target_account_id")
	if err != nil {
		return nil, err
	}
	notes, err := s.noteMap(accountID, ids)
	if err != nil {
		return nil, err
	}
	domainBlocking, err := s.domainBlockingMap(accountID, domainsByID)
	if err != nil {
		return nil, err
	}

	out := make([]serializer.Relationship, 0, len(ids))
	for _, id := range ids {
		if _, ok := existing[id]; !ok {
			continue
		}
		follow := following[id]
		req := requested[id]
		out = append(out, serializer.Relationship{
			ID:                  strconv.FormatInt(id, 10),
			Following:           follow != nil,
			ShowingReblogs:      boolFromFollow(follow, req, "reblogs"),
			Notifying:           boolFromFollow(follow, req, "notify"),
			Languages:           languagesFromFollow(follow, req),
			FollowedBy:          followedBy[id] != nil,
			Blocking:            blocking[id],
			BlockedBy:           blockedBy[id],
			Muting:              muting[id],
			MutingNotifications: mutingNotifications[id],
			Requested:           req != nil,
			RequestedBy:         requestedBy[id] != nil,
			DomainBlocking:      domainBlocking[id],
			Endorsed:            endorsed[id],
			Note:                notes[id],
		})
	}
	return out, nil
}

func deleteFollowEdge(tx *gorm.DB, sourceID int64, targetID int64) error {
	_, _, err := deleteFollowEdgeReturningFollow(tx, sourceID, targetID)
	return err
}

func deleteFollowEdgeReturningFollow(tx *gorm.DB, sourceID int64, targetID int64) (models.Follow, bool, error) {
	var follow models.Follow
	err := tx.Where("account_id = ? AND target_account_id = ?", sourceID, targetID).First(&follow).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return models.Follow{}, false, nil
	}
	if err != nil {
		return models.Follow{}, false, err
	}
	if err := deleteFollow(tx, follow); err != nil {
		return models.Follow{}, false, err
	}
	return follow, true, nil
}

func deleteFollow(tx *gorm.DB, follow models.Follow) error {
	if err := tx.Delete(&follow).Error; err != nil {
		return err
	}
	if err := tx.Where("activity_type = ? AND activity_id = ?", "Follow", follow.ID).Delete(&models.Notification{}).Error; err != nil {
		return err
	}
	if err := decrementAccountStatCounter(tx, follow.AccountID, accountStatCounterFollowing, 1); err != nil {
		return err
	}
	if err := tx.Where("account_id = ? AND target_account_id = ?", follow.AccountID, follow.TargetAccountID).Delete(&models.AccountPin{}).Error; err != nil {
		return err
	}
	return decrementAccountStatCounter(tx, follow.TargetAccountID, accountStatCounterFollowers, 1)
}

type relationshipFollow struct {
	ID          int64
	ShowReblogs bool
	Notify      bool
	Languages   []string
}

func (s *Server) followMap(accountID int64, ids []int64, reverse bool) (map[int64]*relationshipFollow, error) {
	rows := []models.Follow{}
	query := s.db.Model(&models.Follow{})
	keyColumn := "target_account_id"
	if reverse {
		query = query.Where("target_account_id = ? AND account_id IN ?", accountID, ids)
		keyColumn = "account_id"
	} else {
		query = query.Where("account_id = ? AND target_account_id IN ?", accountID, ids)
	}
	if err := query.Find(&rows).Error; err != nil {
		return nil, err
	}
	out := map[int64]*relationshipFollow{}
	for _, row := range rows {
		key := row.TargetAccountID
		if keyColumn == "account_id" {
			key = row.AccountID
		}
		out[key] = &relationshipFollow{ID: row.ID, ShowReblogs: row.ShowReblogs, Notify: row.Notify, Languages: []string(row.Languages)}
	}
	return out, nil
}

func (s *Server) followRequestMap(accountID int64, ids []int64, reverse bool) (map[int64]*relationshipFollow, error) {
	rows := []models.FollowRequest{}
	query := s.db.Model(&models.FollowRequest{})
	keyColumn := "target_account_id"
	if reverse {
		query = query.Where("target_account_id = ? AND account_id IN ?", accountID, ids)
		keyColumn = "account_id"
	} else {
		query = query.Where("account_id = ? AND target_account_id IN ?", accountID, ids)
	}
	if err := query.Find(&rows).Error; err != nil {
		return nil, err
	}
	out := map[int64]*relationshipFollow{}
	for _, row := range rows {
		key := row.TargetAccountID
		if keyColumn == "account_id" {
			key = row.AccountID
		}
		out[key] = &relationshipFollow{ID: row.ID, ShowReblogs: row.ShowReblogs, Notify: row.Notify, Languages: []string(row.Languages)}
	}
	return out, nil
}

func (s *Server) idSet(model any, where string, arg1 any, arg2 any, column string) (map[int64]bool, error) {
	rows := []struct {
		ID int64 `gorm:"column:id"`
	}{}
	if err := s.db.Model(model).Select(column+" AS id").Where(where, arg1, arg2).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := map[int64]bool{}
	for _, row := range rows {
		out[row.ID] = true
	}
	return out, nil
}

func (s *Server) muteMaps(accountID int64, ids []int64) (map[int64]bool, map[int64]bool, error) {
	rows := []models.Mute{}
	if err := s.db.Where("account_id = ? AND target_account_id IN ?", accountID, ids).Find(&rows).Error; err != nil {
		return nil, nil, err
	}
	muting := map[int64]bool{}
	notifications := map[int64]bool{}
	for _, row := range rows {
		muting[row.TargetAccountID] = true
		notifications[row.TargetAccountID] = row.HideNotifications
	}
	return muting, notifications, nil
}

func (s *Server) noteMap(accountID int64, ids []int64) (map[int64]string, error) {
	rows := []models.AccountNote{}
	if err := s.db.Where("account_id = ? AND target_account_id IN ?", accountID, ids).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := map[int64]string{}
	for _, row := range rows {
		if !row.TargetAccountID.Valid || row.TargetAccountID.Int64 == 0 {
			continue
		}
		out[row.TargetAccountID.Int64] = row.Comment
	}
	return out, nil
}

func (s *Server) domainBlockingMap(accountID int64, domainsByID map[int64]string) (map[int64]bool, error) {
	out := map[int64]bool{}
	if len(domainsByID) == 0 {
		return out, nil
	}
	domains := make([]string, 0, len(domainsByID))
	seen := map[string]struct{}{}
	for _, domain := range domainsByID {
		normalized := strings.ToLower(strings.TrimSpace(domain))
		if normalized == "" {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		domains = append(domains, normalized)
	}
	if len(domains) == 0 {
		return out, nil
	}
	rows := []models.AccountDomainBlock{}
	if err := s.db.Where("account_id = ? AND lower(domain) IN ?", accountID, domains).Find(&rows).Error; err != nil {
		return nil, err
	}
	blockedDomains := map[string]struct{}{}
	for _, row := range rows {
		blockedDomains[strings.ToLower(strings.TrimSpace(string(row.Domain)))] = struct{}{}
	}
	for id, domain := range domainsByID {
		if _, ok := blockedDomains[strings.ToLower(strings.TrimSpace(domain))]; ok {
			out[id] = true
		}
	}
	return out, nil
}

func createRelationshipNotification(tx *gorm.DB, accountID int64, fromAccountID int64, activityID int64, activityType string, kind string, at time.Time) error {
	_, err := createRelationshipNotificationRow(tx, accountID, fromAccountID, activityID, activityType, kind, at)
	return err
}

// createRelationshipNotificationRowAndEnqueue creates a notification and, when one is created,
// enqueues its e-mail on the asynq mailers queue (mirroring Rails NotifyService#send_email!,
// which runs after every notification creation). Callers that collect the notification ID for
// streaming keep using the returned row.
func (s *Server) createRelationshipNotificationRowAndEnqueue(tx *gorm.DB, accountID int64, fromAccountID int64, activityID int64, activityType string, kind string, at time.Time) (*models.Notification, error) {
	notification, err := createRelationshipNotificationRow(tx, accountID, fromAccountID, activityID, activityType, kind, at)
	if err != nil {
		return nil, err
	}
	if notification != nil {
		needed, err := s.notificationMailNeededForNotification(context.Background(), tx, *notification)
		if err != nil {
			return nil, err
		}
		if needed {
			s.enqueueNotificationMailTask(notification.ID)
		}
	}
	return notification, nil
}

// createRelationshipNotificationAndEnqueue is the error-only variant for callers that do not
// need the notification row; it creates the notification and enqueues its e-mail.
func (s *Server) createRelationshipNotificationAndEnqueue(tx *gorm.DB, accountID int64, fromAccountID int64, activityID int64, activityType string, kind string, at time.Time) error {
	_, err := s.createRelationshipNotificationRowAndEnqueue(tx, accountID, fromAccountID, activityID, activityType, kind, at)
	return err
}

func createRelationshipNotificationRow(tx *gorm.DB, accountID int64, fromAccountID int64, activityID int64, activityType string, kind string, at time.Time) (*models.Notification, error) {
	if activityID == 0 {
		return nil, nil
	}
	duplicate, err := relationshipNotificationDuplicateHandled(tx, accountID, activityID, activityType, kind)
	if err != nil || duplicate {
		return nil, err
	}
	blocked, err := relationshipNotificationBlocked(tx, accountID, fromAccountID, activityID, activityType, kind, at)
	if err != nil || blocked {
		return nil, err
	}
	notification := models.Notification{
		ActivityID:    activityID,
		ActivityType:  activityType,
		CreatedAt:     at,
		UpdatedAt:     at,
		AccountID:     accountID,
		FromAccountID: fromAccountID,
		Type:          models.NullSafeString(kind),
	}
	if err := tx.Create(&notification).Error; err != nil {
		return nil, err
	}
	return &notification, nil
}

func relationshipNotificationDuplicateHandled(tx *gorm.DB, accountID int64, activityID int64, activityType string, kind string) (bool, error) {
	query := tx.Model(&models.Notification{}).
		Where("account_id = ? AND activity_id = ? AND activity_type = ? AND type = ?", accountID, activityID, activityType, kind)
	if kind == "update" {
		return false, query.Delete(&models.Notification{}).Error
	}
	var count int64
	if err := query.Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func relationshipNotificationBlocked(tx *gorm.DB, accountID int64, fromAccountID int64, activityID int64, activityType string, kind string, at time.Time) (bool, error) {
	if accountID == 0 || fromAccountID == 0 {
		return true, nil
	}
	if accountID == fromAccountID && kind != "poll" {
		return true, nil
	}
	var recipient models.Account
	if err := tx.Select("id", "suspended_at").Where("id = ?", accountID).First(&recipient).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return true, nil
		}
		return false, err
	}
	if recipient.SuspendedAt.Valid {
		return true, nil
	}
	var userCount int64
	if err := tx.Model(&models.User{}).Where("account_id = ?", accountID).Count(&userCount).Error; err != nil {
		return false, err
	}
	if userCount == 0 {
		return true, nil
	}
	var recipientUser models.User
	if err := tx.Select("settings", "role_id").Where("account_id = ?", accountID).First(&recipientUser).Error; err != nil {
		return false, err
	}
	userSettings := decodeUserSettings(recipientUser.Settings.String)
	var sender models.Account
	if err := tx.Select("id", "domain", "created_at", "silenced_at").Where("id = ?", fromAccountID).First(&sender).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return true, nil
		}
		return false, err
	}
	if kind == "mention" {
		fromStaff, err := relationshipNotificationFromStaff(tx, recipientUser, sender)
		if err != nil {
			return false, err
		}
		if fromStaff {
			return false, nil
		}
	}
	var blockedCount int64
	if err := tx.Model(&models.Block{}).
		Where("account_id = ? AND target_account_id = ?", accountID, fromAccountID).
		Count(&blockedCount).Error; err != nil {
		return false, err
	}
	if blockedCount > 0 {
		return true, nil
	}
	var mutedCount int64
	if err := tx.Model(&models.Mute{}).
		Where("account_id = ? AND target_account_id = ? AND hide_notifications = TRUE AND (expires_at IS NULL OR expires_at > ?)", accountID, fromAccountID, at).
		Count(&mutedCount).Error; err != nil {
		return false, err
	}
	if mutedCount > 0 {
		return true, nil
	}
	following, err := relationshipNotificationSenderFollowed(tx, accountID, fromAccountID)
	if err != nil {
		return false, err
	}
	if rawBool(userSettings["interactions.must_be_following"], false) && !following {
		return true, nil
	}
	if !following && sender.SilencedAt.Valid {
		return true, nil
	}
	if !following && sender.Domain.Valid && strings.TrimSpace(sender.Domain.String) != "" {
		var domainBlockCount int64
		if err := tx.Model(&models.AccountDomainBlock{}).
			Where("account_id = ? AND lower(domain) = lower(?)", accountID, sender.Domain.String).
			Count(&domainBlockCount).Error; err != nil {
			return false, err
		}
		if domainBlockCount > 0 {
			return true, nil
		}
	}
	senderFollowsRecipient, err := relationshipNotificationSenderFollowsRecipient(tx, accountID, fromAccountID)
	if err != nil {
		return false, err
	}
	if rawBool(userSettings["interactions.must_be_follower"], false) && !senderFollowsRecipient {
		return true, nil
	}
	targetStatus, err := relationshipNotificationTargetStatus(tx, activityID, activityType)
	if err != nil {
		return false, err
	}
	if targetStatus != nil && targetStatus.ConversationID.Valid {
		var conversationMuteCount int64
		if err := tx.Model(&models.ConversationMute{}).
			Where("account_id = ? AND conversation_id = ?", accountID, targetStatus.ConversationID.Int64).
			Count(&conversationMuteCount).Error; err != nil {
			return false, err
		}
		if conversationMuteCount > 0 {
			return true, nil
		}
	}
	if kind == "mention" && targetStatus != nil && targetStatus.Visibility == 3 &&
		rawBool(userSettings["interactions.must_be_following_dm"], false) &&
		!following {
		responded, err := relationshipNotificationResponseToRecipient(tx, accountID, fromAccountID, targetStatus)
		if err != nil {
			return false, err
		}
		if !responded {
			return true, nil
		}
	}
	if kind == "mention" && targetStatus != nil && !following &&
		rawBool(userSettings["interactions.must_be_human"], false) {
		var spam bool
		var err error
		if relationshipNotificationSpamDetectionMethod() == "gpt" {
			spam, err = relationshipNotificationGPTSpamBlocked(tx, sender, targetStatus, at)
		} else {
			spam, err = relationshipNotificationSimpleSpamBlocked(tx, sender, targetStatus.ID, at)
		}
		if err != nil {
			return false, err
		}
		if spam {
			return true, nil
		}
	}
	return false, nil
}

func relationshipNotificationFromStaff(tx *gorm.DB, recipientUser models.User, sender models.Account) (bool, error) {
	if sender.Domain.Valid && strings.TrimSpace(sender.Domain.String) != "" {
		return false, nil
	}
	var senderUser models.User
	if err := tx.Select("role_id").Where("account_id = ?", sender.ID).First(&senderUser).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, err
	}
	if !senderUser.RoleID.Valid {
		return false, nil
	}
	var senderRole models.UserRole
	if err := tx.Where("id = ?", senderUser.RoleID.Int64).First(&senderRole).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, err
	}
	if !senderRole.Highlighted {
		return false, nil
	}
	if recipientUser.RoleID.Valid {
		var recipientRole models.UserRole
		if err := tx.Select("position").Where("id = ?", recipientUser.RoleID.Int64).First(&recipientRole).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return false, err
		} else if err == nil && senderRole.Position <= recipientRole.Position {
			return false, nil
		}
	}
	var everyoneRole models.UserRole
	var everyone *models.UserRole
	if err := tx.Where("id = ?", int64(-99)).First(&everyoneRole).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return false, err
	} else if err == nil {
		everyone = &everyoneRole
	}
	permissions := computedRolePermissionsForUser(&senderRole, everyone)
	return permissions&relationshipNotificationModerationPermissions() != 0, nil
}

func relationshipNotificationModerationPermissions() int64 {
	return rolePermissionViewDashboard |
		rolePermissionViewAuditLog |
		rolePermissionManageUsers |
		rolePermissionManageUserAccess |
		rolePermissionDeleteUserData |
		rolePermissionManageReports |
		rolePermissionManageAppeals |
		rolePermissionManageFederation |
		rolePermissionManageBlocks |
		rolePermissionManageTaxonomies |
		rolePermissionManageInvites
}

func relationshipNotificationSenderFollowed(tx *gorm.DB, accountID int64, fromAccountID int64) (bool, error) {
	var followCount int64
	if err := tx.Model(&models.Follow{}).
		Where("account_id = ? AND target_account_id = ?", accountID, fromAccountID).
		Count(&followCount).Error; err != nil {
		return false, err
	}
	if followCount > 0 {
		return true, nil
	}
	var requestCount int64
	if err := tx.Model(&models.FollowRequest{}).
		Where("account_id = ? AND target_account_id = ?", accountID, fromAccountID).
		Count(&requestCount).Error; err != nil {
		return false, err
	}
	return requestCount > 0, nil
}

func relationshipNotificationSenderFollowsRecipient(tx *gorm.DB, accountID int64, fromAccountID int64) (bool, error) {
	var followCount int64
	if err := tx.Model(&models.Follow{}).
		Where("account_id = ? AND target_account_id = ?", fromAccountID, accountID).
		Count(&followCount).Error; err != nil {
		return false, err
	}
	return followCount > 0, nil
}

func relationshipNotificationTargetStatus(tx *gorm.DB, activityID int64, activityType string) (*models.Status, error) {
	if activityID == 0 {
		return nil, nil
	}
	var statusID int64
	switch activityType {
	case "Mention":
		var mention models.Mention
		if err := tx.Select("status_id").Where("id = ?", activityID).First(&mention).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, nil
			}
			return nil, err
		}
		if !mention.StatusID.Valid {
			return nil, nil
		}
		statusID = mention.StatusID.Int64
	case "Favourite":
		var favourite models.Favourite
		if err := tx.Select("status_id").Where("id = ?", activityID).First(&favourite).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, nil
			}
			return nil, err
		}
		statusID = favourite.StatusID
	case "Status":
		statusID = activityID
	default:
		return nil, nil
	}
	if statusID == 0 {
		return nil, nil
	}
	var status models.Status
	if err := tx.Select("id", "conversation_id", "in_reply_to_id", "visibility", "text").
		Where("id = ?", statusID).
		First(&status).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &status, nil
}

func relationshipNotificationResponseToRecipient(tx *gorm.DB, accountID int64, fromAccountID int64, status *models.Status) (bool, error) {
	if status == nil || !status.InReplyToID.Valid {
		return false, nil
	}
	var count int64
	err := tx.Raw(`
WITH RECURSIVE ancestors(id, in_reply_to_id, mention_id, path, depth) AS (
  SELECT s.id, s.in_reply_to_id, m.id, ARRAY[s.id], 0
  FROM statuses s
  LEFT JOIN mentions m ON m.silent = FALSE AND m.account_id = @sender_id AND m.status_id = s.id
  WHERE s.id = @id
UNION ALL
  SELECT s.id, s.in_reply_to_id, m.id, ancestors.path || s.id, ancestors.depth + 1
  FROM ancestors
  JOIN statuses s ON s.id = ancestors.in_reply_to_id
  LEFT JOIN mentions m ON m.silent = FALSE AND m.account_id = @sender_id AND m.status_id = s.id AND s.account_id = @recipient_id
  WHERE ancestors.mention_id IS NULL AND NOT s.id = ANY(path) AND ancestors.depth < @depth_limit
)
SELECT COUNT(*)
FROM ancestors
JOIN statuses s ON s.id = ancestors.id
WHERE ancestors.mention_id IS NOT NULL AND s.account_id = @recipient_id AND s.visibility = 3
`,
		sql.Named("id", status.InReplyToID.Int64),
		sql.Named("recipient_id", accountID),
		sql.Named("sender_id", fromAccountID),
		sql.Named("depth_limit", 100),
	).Scan(&count).Error
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

func relationshipNotificationSimpleSpamBlocked(tx *gorm.DB, sender models.Account, statusID int64, at time.Time) (bool, error) {
	var stat models.AccountStat
	followersCount := int64(0)
	if err := tx.Select("followers_count").Where("account_id = ?", sender.ID).First(&stat).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return false, err
		}
	} else {
		followersCount = stat.FollowersCount
	}
	var mentionsCount int64
	if err := tx.Model(&models.Mention{}).Where("status_id = ?", statusID).Count(&mentionsCount).Error; err != nil {
		return false, err
	}
	lowFollowers := followersCount < int64(relationshipNotificationEnvInt("SPAMMER_FOLLOWER_THRESHOLD", 5))
	recentAccount := sender.CreatedAt.After(at.AddDate(0, 0, -relationshipNotificationEnvInt("SPAMMER_CREATION_THRESHOLD", 6)))
	tooManyMentions := mentionsCount > int64(relationshipNotificationEnvInt("SPAMMER_MENTION_THRESHOLD", 1))
	return (lowFollowers || recentAccount) && tooManyMentions, nil
}

func relationshipNotificationSpamDetectionMethod() string {
	method, ok := os.LookupEnv("SPAM_DETECTION_METHOD")
	if !ok {
		return "simple"
	}
	return method
}

func relationshipNotificationEnvInt(name string, fallback int) int {
	value, ok := os.LookupEnv(name)
	if !ok {
		return fallback
	}
	value = strings.TrimLeftFunc(value, unicode.IsSpace)
	sign := 1
	if strings.HasPrefix(value, "-") {
		sign = -1
		value = value[1:]
	} else if strings.HasPrefix(value, "+") {
		value = value[1:]
	}
	i := 0
	for i < len(value) && value[i] >= '0' && value[i] <= '9' {
		i++
	}
	if i == 0 {
		return 0
	}
	parsed, err := strconv.Atoi(value[:i])
	if err != nil {
		return 0
	}
	return sign * parsed
}

func relationshipIDs(c *echo.Context) []int64 {
	raw := append([]string{}, c.QueryParams()["id[]"]...)
	raw = append(raw, c.QueryParams()["id"]...)
	out := []int64{}
	for _, value := range raw {
		id := railsToInt64(value)
		if id > 0 {
			out = append(out, id)
		}
	}
	return out
}

func railsToInt64(value string) int64 {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	sign := int64(1)
	if value[0] == '-' || value[0] == '+' {
		if value[0] == '-' {
			sign = -1
		}
		value = value[1:]
	}
	var digits strings.Builder
	for _, r := range value {
		if r < '0' || r > '9' {
			break
		}
		digits.WriteRune(r)
	}
	if digits.Len() == 0 {
		return 0
	}
	parsed, err := strconv.ParseInt(digits.String(), 10, 64)
	if err != nil {
		return 0
	}
	return sign * parsed
}

func relationshipLanguageValues(c *echo.Context) []string {
	if err := c.Request().ParseForm(); err != nil {
		return nil
	}
	raw := append([]string{}, c.Request().Form["languages[]"]...)
	raw = append(raw, c.Request().Form["languages"]...)
	return normalizeRelationshipLanguages(raw)
}

func normalizeRelationshipLanguages(raw []string) []string {
	out := []string{}
	seen := map[string]struct{}{}
	for _, value := range raw {
		language := strings.ToLower(strings.TrimSpace(value))
		if language == "" {
			continue
		}
		if _, ok := seen[language]; ok {
			continue
		}
		seen[language] = struct{}{}
		out = append(out, language)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func validRelationshipLanguages(languages []string) bool {
	if len(languages) == 0 {
		return true
	}
	supported := map[string]struct{}{}
	for _, language := range serializer.SupportedLanguageCodes() {
		supported[language] = struct{}{}
	}
	for _, language := range languages {
		if _, ok := supported[language]; !ok {
			return false
		}
	}
	return true
}

func boolFromFollow(follow *relationshipFollow, requested *relationshipFollow, field string) bool {
	source := follow
	if source == nil {
		source = requested
	}
	if source == nil {
		return false
	}
	if field == "notify" {
		return source.Notify
	}
	return source.ShowReblogs
}

func languagesFromFollow(follow *relationshipFollow, requested *relationshipFollow) []string {
	source := follow
	if source == nil {
		source = requested
	}
	if source == nil || len(source.Languages) == 0 {
		return nil
	}
	return append([]string{}, source.Languages...)
}

func truthy(value string) bool {
	if value == "" {
		return false
	}
	return !falseParam(value)
}

func falseParam(value string) bool {
	switch value {
	case "false", "FALSE", "0", "f", "F", "off", "OFF":
		return true
	default:
		return false
	}
}

func durationSeconds(value string) int64 {
	return railsToInt64(value)
}
