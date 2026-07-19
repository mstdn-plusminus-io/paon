package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type acceptedFollowRequestDelivery struct {
	Requester models.Account
	RequestID int64
	URI       string
}

func updateListAccountsForAcceptedFollowRequest(tx *gorm.DB, requestID int64, followID int64) ([]int64, error) {
	listIDs, err := listIDsForFollowRequest(tx, requestID)
	if err != nil {
		return nil, err
	}
	if err := tx.Model(&models.ListAccount{}).
		Where("follow_request_id = ?", requestID).
		Updates(map[string]any{"follow_request_id": nil, "follow_id": followID}).Error; err != nil {
		return nil, err
	}
	return listIDs, nil
}

func deleteListAccountsForRejectedFollowRequest(tx *gorm.DB, requestID int64) ([]int64, error) {
	listIDs, err := listIDsForFollowRequest(tx, requestID)
	if err != nil {
		return nil, err
	}
	if err := tx.
		Where("follow_request_id = ?", requestID).
		Delete(&models.ListAccount{}).Error; err != nil {
		return nil, err
	}
	return listIDs, nil
}

func listIDsForFollowRequest(tx *gorm.DB, requestID int64) ([]int64, error) {
	var listIDs []int64
	if err := tx.Model(&models.ListAccount{}).
		Where("follow_request_id = ?", requestID).
		Pluck("list_id", &listIDs).Error; err != nil {
		return nil, err
	}
	return uniqueInt64s(listIDs), nil
}

func (s *Server) followRequests(c *echo.Context) error {
	c.Response().Header().Set("Vary", "Authorization")
	account, _, err := s.requireAccountScope(c, "follow", "read", "read:follows")
	if err != nil {
		return err
	}

	query := s.db.Model(&models.FollowRequest{}).
		Preload("Account.AccountStat").
		Preload("Account.User.Role").
		Joins("JOIN accounts ON accounts.id = follow_requests.account_id").
		Where("follow_requests.target_account_id = ? AND accounts.suspended_at IS NULL", account.ID)
	if maxID := c.QueryParam("max_id"); queryParamValuePresent(c, "max_id") {
		query = query.Where("follow_requests.id < ?", maxID)
	}
	if sinceID := c.QueryParam("since_id"); queryParamValuePresent(c, "since_id") {
		query = query.Where("follow_requests.id > ?", sinceID)
	}
	query = query.Order("follow_requests.id DESC")

	limitValue := limit(c, 40, 80)
	var rows []models.FollowRequest
	if err := query.Limit(limitValue).Find(&rows).Error; err != nil {
		return err
	}
	accounts := make([]models.Account, 0, len(rows))
	for _, row := range rows {
		accounts = append(accounts, row.Account)
	}
	if len(rows) > 0 {
		c.Response().Header().Set("Link", limitOnlyPaginationLink(c, rows[0].ID, rows[len(rows)-1].ID, "since_id", len(rows) == limitValue))
	}
	return c.JSON(http.StatusOK, serializeAccounts(s.cfg, accounts))
}

func (s *Server) authorizePendingFollowRequestsForUnlockedAccount(ctx context.Context, account models.Account) error {
	if s == nil || s.db == nil {
		return nil
	}
	var requests []models.FollowRequest
	if err := s.db.
		Preload("Account").
		Joins("JOIN accounts ON accounts.id = follow_requests.account_id").
		Where("follow_requests.target_account_id = ? AND accounts.silenced_at IS NULL", account.ID).
		Order("follow_requests.id ASC").
		Find(&requests).Error; err != nil {
		return err
	}
	if len(requests) == 0 {
		return nil
	}
	enqueuedAll := s.asynqClient != nil
	if enqueuedAll {
		for _, req := range requests {
			if !s.enqueueAuthorizeFollowTask(req.AccountID, account.ID) {
				enqueuedAll = false
				break
			}
		}
	}
	if enqueuedAll {
		return nil
	}

	for _, req := range requests {
		if err := s.authorizeFollowRequestPairNow(ctx, req.AccountID, account.ID); err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
	}
	return nil
}

func (s *Server) authorizeFollowRequestPairNow(ctx context.Context, sourceAccountID int64, targetAccountID int64) error {
	if s == nil || s.db == nil || sourceAccountID == 0 || targetAccountID == 0 {
		return nil
	}
	var target models.Account
	if err := s.db.WithContext(ctx).Where("id = ?", targetAccountID).First(&target).Error; err != nil {
		return err
	}
	var requester models.Account
	if err := s.db.WithContext(ctx).Where("id = ?", sourceAccountID).First(&requester).Error; err != nil {
		return err
	}
	now := time.Now().UTC()
	notificationIDs := make([]int64, 0, 1)
	notificationPayloads := make([]asynqLocalNotificationPayload, 0, 1)
	deliveries := make([]acceptedFollowRequestDelivery, 0, 1)
	affectedListIDs := []int64{}
	acceptedRequesterIDs := []int64{}
	followChanged := false
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		var req models.FollowRequest
		if err := tx.Where("account_id = ? AND target_account_id = ?", sourceAccountID, targetAccountID).First(&req).Error; err != nil {
			return err
		}
		follow := models.Follow{
			CreatedAt:       now,
			UpdatedAt:       now,
			AccountID:       req.AccountID,
			TargetAccountID: target.ID,
			ShowReblogs:     req.ShowReblogs,
			Notify:          req.Notify,
			Languages:       req.Languages,
			URI:             req.URI,
		}
		res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&follow)
		if res.Error != nil {
			return res.Error
		}
		if follow.ID == 0 {
			if err := tx.Where("account_id = ? AND target_account_id = ?", req.AccountID, target.ID).First(&follow).Error; err != nil {
				return err
			}
			if req.URI != "" && follow.URI == "" {
				if err := tx.Model(&follow).Updates(map[string]any{"uri": req.URI, "updated_at": now}).Error; err != nil {
					return err
				}
				follow.URI = req.URI
			}
		}
		if res.RowsAffected > 0 {
			followChanged = true
			if err := incrementAccountStatCounter(tx, req.AccountID, accountStatCounterFollowing, 1); err != nil {
				return err
			}
			if err := incrementAccountStatCounter(tx, target.ID, accountStatCounterFollowers, 1); err != nil {
				return err
			}
		}
		listIDs, err := updateListAccountsForAcceptedFollowRequest(tx, req.ID, follow.ID)
		if err != nil {
			return err
		}
		affectedListIDs = append(affectedListIDs, listIDs...)
		acceptedRequesterIDs = append(acceptedRequesterIDs, req.AccountID)
		if err := tx.Where("activity_type = ? AND activity_id = ?", "FollowRequest", req.ID).Delete(&models.Notification{}).Error; err != nil {
			return err
		}
		notificationPayloads = append(notificationPayloads, asynqLocalNotificationPayload{ReceiverAccountID: target.ID, FromAccountID: req.AccountID, ActivityID: follow.ID, ActivityType: "Follow", Type: "follow"})
		if err := tx.Delete(&req).Error; err != nil {
			return err
		}
		deliveries = append(deliveries, acceptedFollowRequestDelivery{Requester: requester, RequestID: req.ID, URI: string(req.URI)})
		return nil
	}); err != nil {
		return err
	}

	for _, listID := range uniqueInt64s(affectedListIDs) {
		_ = s.clearListFeedCacheContext(ctx, listID)
	}
	for _, requesterID := range uniqueInt64s(acceptedRequesterIDs) {
		_ = s.clearHomeFeedCacheContext(ctx, requesterID)
	}
	createdNotificationIDs, err := s.enqueueOrCreateLocalNotifications(ctx, notificationPayloads)
	if err != nil {
		return err
	}
	notificationIDs = append(notificationIDs, createdNotificationIDs...)
	s.publishNotificationIDs(notificationIDs)
	for _, delivery := range deliveries {
		s.invalidateFollowRelationshipCaches(ctx, delivery.Requester, target.ID)
		_ = s.deliverActivityPubFollowResponse("Accept", target, delivery.Requester, delivery.RequestID, delivery.URI)
	}
	if followChanged {
		s.meiliReindexPrivateStatusesForAccountsBestEffort(ctx, target.ID)
	}
	return nil
}

func (s *Server) authorizeFollowRequest(c *echo.Context) error {
	account, requester, err := s.followRequestAccounts(c)
	if err != nil {
		return err
	}

	var accepted *models.Follow
	var requestID int64
	var followURI string
	var notificationIDs []int64
	var notificationPayloads []asynqLocalNotificationPayload
	affectedListIDs := []int64{}
	followChanged := false
	err = s.db.Transaction(func(tx *gorm.DB) error {
		var req models.FollowRequest
		if err := tx.Where("account_id = ? AND target_account_id = ?", requester.ID, account.ID).First(&req).Error; err != nil {
			return err
		}
		requestID = req.ID
		followURI = string(req.URI)

		now := time.Now().UTC()
		follow := models.Follow{
			CreatedAt:       now,
			UpdatedAt:       now,
			AccountID:       requester.ID,
			TargetAccountID: account.ID,
			ShowReblogs:     req.ShowReblogs,
			Notify:          req.Notify,
			Languages:       req.Languages,
			URI:             req.URI,
		}
		res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&follow)
		if res.Error != nil {
			return res.Error
		}
		if follow.ID == 0 {
			if err := tx.Where("account_id = ? AND target_account_id = ?", requester.ID, account.ID).First(&follow).Error; err != nil {
				return err
			}
			if req.URI != "" && follow.URI == "" {
				if err := tx.Model(&follow).Updates(map[string]any{"uri": req.URI, "updated_at": now}).Error; err != nil {
					return err
				}
				follow.URI = req.URI
			}
		}
		accepted = &follow
		if res.RowsAffected > 0 {
			followChanged = true
			if err := incrementAccountStatCounter(tx, requester.ID, accountStatCounterFollowing, 1); err != nil {
				return err
			}
			if err := incrementAccountStatCounter(tx, account.ID, accountStatCounterFollowers, 1); err != nil {
				return err
			}
		}
		listIDs, err := updateListAccountsForAcceptedFollowRequest(tx, req.ID, follow.ID)
		if err != nil {
			return err
		}
		affectedListIDs = append(affectedListIDs, listIDs...)
		if err := tx.Where("activity_type = ? AND activity_id = ?", "FollowRequest", req.ID).Delete(&models.Notification{}).Error; err != nil {
			return err
		}
		notificationPayloads = append(notificationPayloads, asynqLocalNotificationPayload{ReceiverAccountID: account.ID, FromAccountID: requester.ID, ActivityID: follow.ID, ActivityType: "Follow", Type: "follow"})
		return tx.Delete(&req).Error
	})
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apiError(c, http.StatusNotFound, "Record not found")
		}
		return err
	}
	if accepted != nil {
		for _, listID := range uniqueInt64s(affectedListIDs) {
			_ = s.clearListFeedCacheContext(c.Request().Context(), listID)
		}
		_ = s.clearHomeFeedCacheContext(c.Request().Context(), requester.ID)
		createdNotificationIDs, err := s.enqueueOrCreateLocalNotifications(c.Request().Context(), notificationPayloads)
		if err != nil {
			return err
		}
		notificationIDs = append(notificationIDs, createdNotificationIDs...)
		s.publishNotificationIDs(notificationIDs)
		s.invalidateFollowRelationshipCaches(c.Request().Context(), *requester, account.ID)
		if followChanged {
			s.meiliReindexPrivateStatusesForAccountsBestEffort(c.Request().Context(), account.ID)
		}
		_ = s.deliverActivityPubFollowResponse("Accept", *account, *requester, requestID, followURI)
	}
	return s.relationshipResponse(c, account.ID, requester)
}

func (s *Server) rejectFollowRequest(c *echo.Context) error {
	account, requester, err := s.followRequestAccounts(c)
	if err != nil {
		return err
	}

	var req models.FollowRequest
	affectedListIDs := []int64{}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("account_id = ? AND target_account_id = ?", requester.ID, account.ID).First(&req).Error; err != nil {
			return err
		}
		listIDs, err := deleteListAccountsForRejectedFollowRequest(tx, req.ID)
		if err != nil {
			return err
		}
		affectedListIDs = append(affectedListIDs, listIDs...)
		if err := tx.Where("activity_type = ? AND activity_id = ?", "FollowRequest", req.ID).Delete(&models.Notification{}).Error; err != nil {
			return err
		}
		return tx.Delete(&req).Error
	})
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apiError(c, http.StatusNotFound, "Record not found")
		}
		return err
	}
	for _, listID := range uniqueInt64s(affectedListIDs) {
		_ = s.clearListFeedCacheContext(c.Request().Context(), listID)
	}
	s.invalidateRelationshipCaches(c.Request().Context(), account.ID, requester.ID)
	_ = s.deliverActivityPubFollowResponse("Reject", *account, *requester, req.ID, string(req.URI))
	return s.relationshipResponse(c, account.ID, requester)
}

func (s *Server) followRequestAccounts(c *echo.Context) (*models.Account, *models.Account, error) {
	account, _, err := s.requireAccountScope(c, "follow", "write", "write:follows")
	if err != nil {
		return nil, nil, err
	}
	requester, err := s.findAccountByID(c.Param("id"))
	if err != nil {
		return nil, nil, apiError(c, http.StatusNotFound, "Record not found")
	}
	return account, requester, nil
}
