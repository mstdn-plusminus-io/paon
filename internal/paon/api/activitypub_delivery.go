package api

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"github.com/mstdn-plusminus-io/paon/internal/paon/telemetry"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const activityPubDeliveryFailureDaysThreshold = 7
const activityPubDeliveryStoplightFailureThreshold = 10
const activityPubDeliveryStoplightCooldown = 60 * time.Second

func (s *Server) deliverActivityPubFollow(local models.Account, remote models.Account, followID int64, followURI string) error {
	if remote.Local() {
		return nil
	}
	inboxURL := remote.InboxURL
	if strings.TrimSpace(inboxURL) == "" {
		return nil
	}
	body, err := json.Marshal(activityPubFollowPayload(s, local, remote, followID, followURI))
	if err != nil {
		return err
	}
	return s.deliverActivityPubConfigured(local, inboxURL, body, func(job *activityPubDeliveryRetryJob) {
		job.BypassAvailability = true
	})
}

type activityPubRefollowDelivery struct {
	FollowID     int64
	FollowURI    string
	RequestID    int64
	RequestURI   string
	LocalAccount models.Account
}

func (s *Server) refollowLocalFollowersAfterActivityPubKeyChange(ctx context.Context, database *gorm.DB, target models.Account) error {
	if database == nil || target.Local() || target.Protocol != 1 {
		return nil
	}
	now := time.Now().UTC()
	deliveries := []activityPubRefollowDelivery{}
	followCacheEffects := []followRelationshipCacheEffect{}
	if err := database.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var follows []models.Follow
		if err := tx.Preload("Account").
			Joins("JOIN accounts ON accounts.id = follows.account_id").
			Where("follows.target_account_id = ?", target.ID).
			Where("accounts.domain IS NULL").
			Find(&follows).Error; err != nil {
			return err
		}
		for _, follow := range follows {
			followURI := string(follow.URI)
			if followURI == "" {
				followURI = activityPubFollowURI(s, follow.Account, follow.ID)
			}
			if err := deleteFollow(tx, follow); err != nil {
				return err
			}
			followCacheEffects = append(followCacheEffects, followRelationshipCacheEffect{Source: follow.Account, TargetID: target.ID})
			if target.SuspendedAt.Valid {
				deliveries = append(deliveries, activityPubRefollowDelivery{
					FollowID:     follow.ID,
					FollowURI:    followURI,
					LocalAccount: follow.Account,
				})
				continue
			}
			request := models.FollowRequest{
				CreatedAt:       now,
				UpdatedAt:       now,
				AccountID:       follow.AccountID,
				TargetAccountID: follow.TargetAccountID,
				ShowReblogs:     follow.ShowReblogs,
				Notify:          follow.Notify,
				Languages:       follow.Languages,
				URI:             models.NullSafeString(activityPubGeneratedPayloadURI(s)),
			}
			res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&request)
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected == 0 {
				continue
			}
			deliveries = append(deliveries, activityPubRefollowDelivery{
				FollowID:     follow.ID,
				FollowURI:    followURI,
				RequestID:    request.ID,
				RequestURI:   string(request.URI),
				LocalAccount: follow.Account,
			})
		}
		return nil
	}); err != nil {
		return err
	}
	for _, effect := range followCacheEffects {
		s.invalidateFollowRelationshipCaches(ctx, effect.Source, effect.TargetID)
	}
	for _, delivery := range deliveries {
		_ = s.deliverActivityPubUndoFollow(delivery.LocalAccount, target, delivery.FollowID, delivery.FollowURI)
		if delivery.RequestID > 0 {
			_ = s.deliverActivityPubFollow(delivery.LocalAccount, target, delivery.RequestID, delivery.RequestURI)
		}
	}
	return nil
}

func (s *Server) deliverActivityPubMigratedFollow(local models.Account, remote models.Account, followID int64, followURI string, oldTargetAccountID int64) error {
	if remote.Local() {
		return nil
	}
	inboxURL := remote.InboxURL
	if strings.TrimSpace(inboxURL) == "" {
		return nil
	}
	body, err := json.Marshal(activityPubFollowPayload(s, local, remote, followID, followURI))
	if err != nil {
		return err
	}
	return s.deliverActivityPubConfigured(local, inboxURL, body, func(job *activityPubDeliveryRetryJob) {
		job.MigratedFollowOldTargetAccountID = oldTargetAccountID
	})
}

func (s *Server) deliverActivityPubUndoFollow(local models.Account, remote models.Account, followID int64, followURI string) error {
	if remote.Local() {
		return nil
	}
	inboxURL := remote.InboxURL
	if strings.TrimSpace(inboxURL) == "" {
		return nil
	}
	body, err := json.Marshal(activityPubUndoFollowPayload(s, local, remote, followID, followURI))
	if err != nil {
		return err
	}
	return s.deliverActivityPub(local, inboxURL, body)
}

func (s *Server) deliverActivityPubBlock(local models.Account, remote models.Account, blockID int64, blockURI string) error {
	if remote.Local() || remote.Protocol != 1 {
		return nil
	}
	inboxURL := remote.InboxURL
	if strings.TrimSpace(inboxURL) == "" {
		return nil
	}
	body, err := json.Marshal(activityPubBlockPayload(s, local, remote, blockID, blockURI))
	if err != nil {
		return err
	}
	return s.deliverActivityPub(local, inboxURL, body)
}

func (s *Server) deliverActivityPubUndoBlock(local models.Account, remote models.Account, blockID int64, blockURI string) error {
	if remote.Local() || remote.Protocol != 1 {
		return nil
	}
	inboxURL := remote.InboxURL
	if strings.TrimSpace(inboxURL) == "" {
		return nil
	}
	body, err := json.Marshal(activityPubUndoBlockPayload(s, local, remote, blockID, blockURI))
	if err != nil {
		return err
	}
	return s.deliverActivityPub(local, inboxURL, body)
}

func (s *Server) deliverActivityPubRelayFollow(relay models.Relay, activityID string) error {
	account, err := s.representativeActivityPubAccount()
	if err != nil {
		return err
	}
	if activityID == "" {
		activityID = relay.FollowActivityID.String
	}
	body, err := json.Marshal(activityPubRelayFollowPayload(s, account, activityID))
	if err != nil {
		return err
	}
	return s.deliverActivityPub(*account, relay.InboxURL, body)
}

func (s *Server) deliverActivityPubRelayUndoFollow(relay models.Relay, activityID string) error {
	account, err := s.representativeActivityPubAccount()
	if err != nil {
		return err
	}
	followActivityID := relay.FollowActivityID.String
	body, err := json.Marshal(activityPubRelayUndoFollowPayload(s, account, activityID, followActivityID))
	if err != nil {
		return err
	}
	return s.deliverActivityPub(*account, relay.InboxURL, body)
}

func (s *Server) deliverActivityPubStatusToFollowers(status models.Status, activity map[string]any) error {
	if s.db == nil || !status.Account.Local() {
		return nil
	}
	inboxes, err := s.activityPubStatusRecipientInboxesForActivity(status, activity)
	if err != nil {
		return err
	}
	if len(inboxes) == 0 {
		return nil
	}
	signedActivity, err := s.signActivityPubLinkedDataSignaturePayloadIfNeeded(status.Account, status, activity)
	if err != nil {
		return err
	}
	body, err := json.Marshal(signedActivity)
	if err != nil {
		return err
	}
	var lastErr error
	for _, inboxURL := range inboxes {
		if err := s.deliverActivityPubStatusActivity(status.Account, status, inboxURL, body, activity); err != nil {
			lastErr = err
		}
	}
	return lastErr
}

func (s *Server) enqueueOrDeliverActivityPubDistribution(status models.Status) error {
	if status.ID == 0 {
		return nil
	}
	if s.enqueueActivityPubDistributionTask(status.ID) {
		return nil
	}
	activity, err := activityPubOutboxActivityWithError(s, status)
	if err != nil {
		return err
	}
	return s.deliverActivityPubStatusToFollowers(status, activity)
}

func (s *Server) enqueueOrDeliverStatusUpdateDistribution(status models.Status) error {
	if status.ID == 0 {
		return nil
	}
	if s.enqueueStatusUpdateDistributionTask(status.ID) {
		return nil
	}
	activity, err := activityPubUpdateWithError(s, status)
	if err != nil {
		return err
	}
	return s.deliverActivityPubStatusToFollowers(status, activity)
}

func (s *Server) deliverActivityPubStatusActivity(local models.Account, status models.Status, inboxURL string, body []byte, activity map[string]any) error {
	return s.deliverActivityPubConfigured(local, inboxURL, body, func(job *activityPubDeliveryRetryJob) {
		job.SynchronizeFollowers = activityPubStatusDeliverySynchronizeFollowers(status, activity)
	})
}

func (s *Server) activityPubStatusRecipientInboxesForActivity(status models.Status, activity map[string]any) ([]string, error) {
	return s.activityPubStatusRecipientInboxesConfigured(status, activityPubStatusDeliveryUnsafeReach(activity))
}

func activityPubStatusDeliveryUnsafeReach(activity map[string]any) bool {
	value, _ := activity["type"].(string)
	if value == "Delete" {
		return true
	}
	if value != "Undo" {
		return false
	}
	object, _ := activity["object"].(map[string]any)
	objectType, _ := object["type"].(string)
	return objectType == "Announce"
}

func activityPubStatusDeliverySynchronizeFollowers(status models.Status, activity map[string]any) bool {
	return status.Visibility == 2 && !activityPubStatusDeliveryUnsafeReach(activity)
}

func (s *Server) deliverActivityPubPollUpdate(status models.Status) error {
	if s.db == nil || !status.Account.Local() || status.Poll == nil || status.Poll.ID == 0 {
		return nil
	}
	inboxes, err := s.activityPubPollUpdateRecipientInboxes(status)
	if err != nil {
		return err
	}
	if len(inboxes) == 0 {
		return nil
	}
	payload, err := activityPubPollUpdateWithError(s, status)
	if err != nil {
		return err
	}
	activity, err := s.signActivityPubLinkedDataSignaturePayloadIfNeeded(status.Account, status, payload)
	if err != nil {
		return err
	}
	body, err := json.Marshal(activity)
	if err != nil {
		return err
	}
	var lastErr error
	for _, inboxURL := range inboxes {
		if err := s.deliverActivityPub(status.Account, inboxURL, body); err != nil {
			lastErr = err
		}
	}
	return lastErr
}

func (s *Server) deliverActivityPubPollVotes(local models.Account, poll models.Poll, votes []models.PollVote) error {
	if s.db == nil || len(votes) == 0 {
		return nil
	}
	var owner models.Account
	if err := s.db.Where("id = ? AND suspended_at IS NULL", poll.AccountID).First(&owner).Error; err != nil {
		return err
	}
	if owner.Local() {
		return nil
	}
	inboxURL := owner.InboxURL
	if strings.TrimSpace(inboxURL) == "" {
		return nil
	}
	var status models.Status
	if poll.StatusID.Valid {
		if err := s.db.Preload("Account").Where("id = ?", poll.StatusID.Int64).First(&status).Error; err != nil {
			return err
		}
	}
	var lastErr error
	for _, vote := range votes {
		body, err := json.Marshal(activityPubVote(s, local, poll, owner, status, vote))
		if err != nil {
			lastErr = err
			continue
		}
		if err := s.deliverActivityPub(local, inboxURL, body); err != nil {
			lastErr = err
		}
	}
	return lastErr
}

// deliverActivityPubActivityToAccountReach mirrors Mastodon 4.3's
// ActivityPub::UpdateDistributionWorker. Profile updates must reach every
// server which may retain a copy of the actor, not just current followers.
func (s *Server) deliverActivityPubActivityToAccountReach(account models.Account, activity map[string]any) error {
	if s.db == nil || !account.Local() {
		return nil
	}
	signedActivity, err := s.signActivityPubLinkedDataSignaturePayloadWhenEnabled(account, activity)
	if err != nil {
		return err
	}
	body, err := json.Marshal(signedActivity)
	if err != nil {
		return err
	}
	return s.deliverActivityPubRawToAccountReach(account, body, nil)
}

func (s *Server) deliverActivityPubRawDistribution(account models.Account, activity map[string]any, excludeInboxes []string) error {
	if s.db == nil || !account.Local() {
		return nil
	}
	signedActivity, err := s.signActivityPubLinkedDataSignaturePayloadWhenEnabled(account, activity)
	if err != nil {
		return err
	}
	body, err := json.Marshal(signedActivity)
	if err != nil {
		return err
	}
	if s.enqueueRawDistributionTask(account.ID, body, excludeInboxes) {
		return nil
	}
	return s.deliverActivityPubRawToFollowers(account, body, excludeInboxes)
}

func (s *Server) deliverActivityPubAccountRawDistribution(account models.Account, activity map[string]any) error {
	if s.db == nil || !account.Local() {
		return nil
	}
	signedActivity, err := s.signActivityPubLinkedDataSignaturePayloadWhenEnabled(account, activity)
	if err != nil {
		return err
	}
	body, err := json.Marshal(signedActivity)
	if err != nil {
		return err
	}
	if s.enqueueAccountRawDistributionTask(account.ID, body, nil) {
		return nil
	}
	return s.deliverActivityPubRawToAccountReach(account, body, nil)
}

func (s *Server) deliverActivityPubRawToFollowers(account models.Account, body []byte, excludeInboxes []string) error {
	if s.db == nil || !account.Local() || len(body) == 0 {
		return nil
	}
	inboxes, err := s.activityPubRemoteFollowerInboxes(account.ID)
	if err != nil {
		return err
	}
	if len(excludeInboxes) > 0 {
		inboxes = excludeActivityPubInboxes(inboxes, excludeInboxes)
	}
	if len(inboxes) == 0 {
		return nil
	}
	var lastErr error
	for _, inboxURL := range inboxes {
		if err := s.deliverActivityPub(account, inboxURL, body); err != nil {
			lastErr = err
		}
	}
	return lastErr
}

func (s *Server) deliverActivityPubRawToAccountReach(account models.Account, body []byte, excludeInboxes []string) error {
	if s.db == nil || !account.Local() || len(body) == 0 {
		return nil
	}
	inboxes, err := s.activityPubAccountReachInboxes(account)
	if err != nil {
		return err
	}
	if len(excludeInboxes) > 0 {
		inboxes = excludeActivityPubInboxes(inboxes, excludeInboxes)
	}
	if len(inboxes) == 0 {
		return nil
	}
	var lastErr error
	for _, inboxURL := range inboxes {
		if err := s.deliverActivityPub(account, inboxURL, body); err != nil {
			lastErr = err
		}
	}
	return lastErr
}

func excludeActivityPubInboxes(inboxes []string, excludeInboxes []string) []string {
	if len(inboxes) == 0 || len(excludeInboxes) == 0 {
		return inboxes
	}
	excluded := make(map[string]struct{}, len(excludeInboxes))
	for _, inbox := range excludeInboxes {
		inbox = strings.TrimSpace(inbox)
		if inbox != "" {
			excluded[inbox] = struct{}{}
		}
	}
	if len(excluded) == 0 {
		return inboxes
	}
	out := inboxes[:0]
	for _, inbox := range inboxes {
		if _, ok := excluded[inbox]; !ok {
			out = append(out, inbox)
		}
	}
	return out
}

func (s *Server) deliverActivityPubAccountUpdate(account models.Account) error {
	if s.db == nil || !account.Local() {
		return nil
	}
	return s.deliverActivityPubAccountUpdateNow(account)
}

func (s *Server) enqueueActivityPubAccountUpdate(account models.Account, delay time.Duration) error {
	if s.db == nil || !account.Local() {
		return nil
	}
	if s.enqueueAccountUpdateTask(account.ID, delay) {
		return nil
	}
	return s.deliverActivityPubAccountUpdateNow(account)
}

func (s *Server) deliverActivityPubAccountUpdateNow(account models.Account) error {
	return s.deliverActivityPubAccountUpdateNowWithSigningKey(account, "")
}

func (s *Server) deliverActivityPubAccountUpdateNowWithSigningKey(account models.Account, privateKey string) error {
	if s.db == nil || !account.Local() {
		return nil
	}
	var fresh models.Account
	if err := s.db.Where("id = ?", account.ID).First(&fresh).Error; err != nil {
		return err
	}
	if strings.TrimSpace(privateKey) == "" {
		return s.deliverActivityPubActivityToAccountReach(fresh, activityPubActorUpdate(s, fresh))
	}
	signer := fresh
	signer.PrivateKey = sql.NullString{String: privateKey, Valid: true}
	signedActivity, err := s.signActivityPubLinkedDataSignaturePayloadWhenEnabled(signer, activityPubActorUpdate(s, fresh))
	if err != nil {
		return err
	}
	body, err := json.Marshal(signedActivity)
	if err != nil {
		return err
	}
	return s.deliverActivityPubRawToAccountReach(signer, body, nil)
}

func (s *Server) deliverActivityPubAccountDelete(account models.Account) error {
	if s.db == nil || !account.Local() {
		return nil
	}
	deliveryInboxes, lowPriorityInboxes, err := s.activityPubAccountDeleteRecipientInboxesByPriority(account.ID)
	if err != nil {
		return err
	}
	return s.deliverActivityPubAccountDeleteToPriorityInboxes(account, deliveryInboxes, lowPriorityInboxes)
}

func (s *Server) deliverActivityPubAccountDeleteToInboxes(account models.Account, inboxes []string) error {
	return s.deliverActivityPubAccountDeleteToPriorityInboxes(account, inboxes, nil)
}

func (s *Server) deliverActivityPubAccountDeleteToPriorityInboxes(account models.Account, deliveryInboxes []string, lowPriorityInboxes []string) error {
	deliveryInboxes = compactActivityPubInboxes(deliveryInboxes)
	lowPriorityInboxes = removeActivityPubInboxes(compactActivityPubInboxes(lowPriorityInboxes), deliveryInboxes)
	inboxes := append(deliveryInboxes, lowPriorityInboxes...)
	if len(inboxes) == 0 {
		return nil
	}
	activity, err := s.signActivityPubLinkedDataSignaturePayload(account, activityPubDeleteActor(s, account))
	if err != nil {
		return err
	}
	body, err := json.Marshal(activity)
	if err != nil {
		return err
	}
	var lastErr error
	for _, inboxURL := range deliveryInboxes {
		if err := s.deliverActivityPub(account, inboxURL, body); err != nil {
			lastErr = err
		}
	}
	for _, inboxURL := range lowPriorityInboxes {
		if err := s.deliverActivityPubConfigured(account, inboxURL, body, func(job *activityPubDeliveryRetryJob) {
			job.RetryLimit = 8
		}); err != nil {
			lastErr = err
		}
	}
	return lastErr
}

func (s *Server) deliverActivityPubMove(migration models.AccountMigration) error {
	if s.db == nil {
		return nil
	}
	if migration.ID != 0 && s.enqueueMoveDistributionTask(migration.ID) {
		return nil
	}
	return s.deliverActivityPubMoveNow(migration)
}

func (s *Server) deliverActivityPubMoveNow(migration models.AccountMigration) error {
	if s.db == nil {
		return nil
	}
	if migration.Account.ID == 0 || migration.TargetAccount.ID == 0 {
		if err := s.db.Preload("Account").Preload("TargetAccount").Where("id = ?", migration.ID).First(&migration).Error; err != nil {
			return err
		}
	}
	if !migration.Account.Local() || migration.TargetAccount.ID == 0 {
		return nil
	}
	inboxes, err := s.activityPubMoveRecipientInboxes(migration.Account.ID)
	if err != nil {
		return err
	}
	if len(inboxes) == 0 {
		return nil
	}
	activity, err := s.signActivityPubLinkedDataSignaturePayloadWhenEnabled(migration.Account, activityPubMove(s, migration))
	if err != nil {
		return err
	}
	body, err := json.Marshal(activity)
	if err != nil {
		return err
	}
	var lastErr error
	for _, inboxURL := range inboxes {
		if err := s.deliverActivityPub(migration.Account, inboxURL, body); err != nil {
			lastErr = err
		}
	}
	return lastErr
}

func (s *Server) activityPubStatusRecipientInboxes(status models.Status) ([]string, error) {
	return s.activityPubStatusRecipientInboxesConfigured(status, false)
}

func (s *Server) activityPubStatusRecipientInboxesConfigured(status models.Status, unsafe bool) ([]string, error) {
	seen := map[string]struct{}{}
	inboxes := []string{}
	addInbox := func(inboxURL string) {
		inboxURL = strings.TrimSpace(inboxURL)
		if inboxURL == "" {
			return
		}
		if _, ok := seen[inboxURL]; ok {
			return
		}
		seen[inboxURL] = struct{}{}
		inboxes = append(inboxes, inboxURL)
	}
	if status.Visibility != 3 && status.Visibility != 4 {
		followerInboxes, err := s.activityPubRemoteFollowerInboxesConfigured(status.AccountID, unsafe)
		if err != nil {
			return nil, err
		}
		for _, inboxURL := range followerInboxes {
			addInbox(inboxURL)
		}
	}
	if s.db != nil && (status.Visibility == 0 || status.Visibility == 1) && status.InReplyToAccountID.Valid {
		replyFollowerInboxes, err := s.activityPubRemoteFollowersOfLocalReplyAccountInboxesConfigured(status, unsafe)
		if err != nil {
			return nil, err
		}
		for _, inboxURL := range replyFollowerInboxes {
			addInbox(inboxURL)
		}
	}
	for _, mention := range status.Mentions {
		account := mention.Account
		if account.ID == 0 || account.Local() || (!unsafe && account.SuspendedAt.Valid) {
			continue
		}
		addInbox(activityPubPreferredInboxURL(account.SharedInboxURL, account.InboxURL))
	}
	if s.db == nil {
		return inboxes, nil
	}
	mentionInboxes, err := s.activityPubStatusMentionInboxesConfigured(status.ID, unsafe)
	if err != nil {
		return nil, err
	}
	for _, inboxURL := range mentionInboxes {
		addInbox(inboxURL)
	}
	reachedInboxes, err := s.activityPubStatusReachedAccountInboxes(status, unsafe)
	if err != nil {
		return nil, err
	}
	for _, inboxURL := range reachedInboxes {
		addInbox(inboxURL)
	}
	if status.Visibility == 0 {
		relayInboxes, err := s.activityPubEnabledRelayInboxes()
		if err != nil {
			return nil, err
		}
		for _, inboxURL := range relayInboxes {
			addInbox(inboxURL)
		}
	}
	return inboxes, nil
}

func (s *Server) activityPubStatusMentionInboxes(statusID int64) ([]string, error) {
	return s.activityPubStatusMentionInboxesConfigured(statusID, false)
}

func (s *Server) activityPubStatusMentionInboxesConfigured(statusID int64, unsafe bool) ([]string, error) {
	if s.db == nil || statusID == 0 {
		return nil, nil
	}
	rows := []struct {
		InboxURL       string `gorm:"column:inbox_url"`
		SharedInboxURL string `gorm:"column:shared_inbox_url"`
	}{}
	query := s.db.Model(&models.Mention{}).
		Select("accounts.inbox_url, accounts.shared_inbox_url").
		Joins("JOIN accounts ON accounts.id = mentions.account_id").
		Where("mentions.status_id = ? AND accounts.domain IS NOT NULL AND accounts.domain <> '' AND accounts.protocol = ?", statusID, 1)
	if !unsafe {
		query = query.Where("accounts.suspended_at IS NULL")
	}
	err := query.Find(&rows).Error
	if err != nil {
		return nil, err
	}
	return s.compactAvailableActivityPubInboxes(activityPubInboxesFromRows(rows)), nil
}

func (s *Server) activityPubStatusReachedAccountInboxes(status models.Status, unsafe bool) ([]string, error) {
	if s.db == nil || status.ID == 0 {
		return nil, nil
	}
	accountIDs := make([]int64, 0, 8)
	addAccountID := func(id int64) {
		if id != 0 {
			accountIDs = append(accountIDs, id)
		}
	}
	distributable := status.Visibility == 0 || status.Visibility == 1
	if status.ReblogOfID.Valid {
		// Status reach is the sole Announce/Undo delivery path. It includes the
		// original author and prefers their shared inbox.
		var reblogOfAccountID int64
		if err := s.db.Model(&models.Status{}).
			Where("id = ?", status.ReblogOfID.Int64).
			Pluck("account_id", &reblogOfAccountID).Error; err != nil {
			return nil, err
		}
		addAccountID(reblogOfAccountID)
		return s.activityPubAccountInboxesConfigured(accountIDs, unsafe)
	}
	if distributable && status.InReplyToAccountID.Valid {
		addAccountID(status.InReplyToAccountID.Int64)
	}
	var quotedAccountIDs []int64
	if err := s.db.Model(&models.Quote{}).
		Where("status_id = ? AND quoted_account_id IS NOT NULL", status.ID).
		Pluck("quoted_account_id", &quotedAccountIDs).Error; err != nil {
		return nil, err
	}
	accountIDs = append(accountIDs, quotedAccountIDs...)
	if distributable || unsafe {
		var interacted []int64
		if err := s.db.Model(&models.Quote{}).
			Where("quoted_status_id = ?", status.ID).
			Pluck("account_id", &interacted).Error; err != nil {
			return nil, err
		}
		accountIDs = append(accountIDs, interacted...)
		interacted = nil
		reblogQuery := s.db.Model(&models.Status{}).Where("reblog_of_id = ?", status.ID)
		if unsafe && status.DeletedAt.Valid {
			reblogQuery = reblogQuery.Where("(deleted_at IS NULL OR deleted_at = ?)", status.DeletedAt.Time)
		} else {
			reblogQuery = reblogQuery.Where("deleted_at IS NULL")
		}
		if err := reblogQuery.Pluck("account_id", &interacted).Error; err != nil {
			return nil, err
		}
		accountIDs = append(accountIDs, interacted...)
		interacted = nil
		if err := s.db.Model(&models.Favourite{}).
			Where("status_id = ?", status.ID).
			Pluck("account_id", &interacted).Error; err != nil {
			return nil, err
		}
		accountIDs = append(accountIDs, interacted...)
		interacted = nil
		if err := s.db.Model(&models.Status{}).
			Where("in_reply_to_id = ? AND deleted_at IS NULL", status.ID).
			Pluck("account_id", &interacted).Error; err != nil {
			return nil, err
		}
		accountIDs = append(accountIDs, interacted...)
	}
	return s.activityPubAccountInboxesConfigured(accountIDs, unsafe)
}

func (s *Server) activityPubRemoteFollowersOfLocalReplyAccountInboxes(status models.Status) ([]string, error) {
	return s.activityPubRemoteFollowersOfLocalReplyAccountInboxesConfigured(status, false)
}

func (s *Server) activityPubRemoteFollowersOfLocalReplyAccountInboxesConfigured(status models.Status, unsafe bool) ([]string, error) {
	if s.db == nil || !status.InReplyToAccountID.Valid {
		return nil, nil
	}
	rows := []struct {
		InboxURL       string `gorm:"column:inbox_url"`
		SharedInboxURL string `gorm:"column:shared_inbox_url"`
	}{}
	query := s.db.Model(&models.Follow{}).
		Select("accounts.inbox_url, accounts.shared_inbox_url").
		Joins("JOIN accounts ON accounts.id = follows.account_id").
		Joins("JOIN accounts reply_accounts ON reply_accounts.id = follows.target_account_id").
		Where("follows.target_account_id = ?", status.InReplyToAccountID.Int64).
		Where("reply_accounts.domain IS NULL").
		Where("accounts.domain IS NOT NULL AND accounts.domain <> '' AND accounts.protocol = ?", 1).
		Where("NOT EXISTS (SELECT 1 FROM account_domain_blocks WHERE account_domain_blocks.account_id = ? AND lower(account_domain_blocks.domain) = lower(accounts.domain))", status.AccountID)
	if !unsafe {
		query = query.Where("accounts.suspended_at IS NULL")
	}
	err := query.Find(&rows).Error
	if err != nil {
		return nil, err
	}
	return s.compactAvailableActivityPubInboxes(activityPubInboxesFromRows(rows)), nil
}

func (s *Server) activityPubAccountInboxes(accountIDs []int64) ([]string, error) {
	return s.activityPubAccountInboxesConfigured(accountIDs, true)
}

func (s *Server) activityPubAccountInboxesConfigured(accountIDs []int64, unsafe bool) ([]string, error) {
	if s.db == nil || len(accountIDs) == 0 {
		return nil, nil
	}
	var inboxes []string
	query := s.db.Model(&models.Account{}).
		Where("id IN ? AND domain IS NOT NULL AND domain <> '' AND protocol = ?", accountIDs, 1)
	if !unsafe {
		query = query.Where("suspended_at IS NULL")
	}
	if err := query.Pluck("COALESCE(NULLIF(shared_inbox_url, ''), inbox_url)", &inboxes).Error; err != nil {
		return nil, err
	}
	return s.compactAvailableActivityPubInboxes(inboxes), nil
}

func (s *Server) activityPubPollUpdateRecipientInboxes(status models.Status) ([]string, error) {
	inboxes, err := s.activityPubPollUpdateMentionInboxes(status)
	if err != nil {
		return nil, err
	}
	if status.Visibility != 3 {
		followerInboxes, err := s.activityPubRemoteFollowerInboxes(status.AccountID)
		if err != nil {
			return nil, err
		}
		inboxes = append(inboxes, followerInboxes...)
	}
	reblogInboxes, err := s.activityPubRemoteReblogInboxes(status.ID)
	if err != nil {
		return nil, err
	}
	inboxes = append(inboxes, reblogInboxes...)
	pollVoteInboxes, err := s.activityPubRemotePollVoteInboxes(status.Poll.ID)
	if err != nil {
		return nil, err
	}
	inboxes = append(inboxes, pollVoteInboxes...)
	if status.Visibility == 0 {
		relayInboxes, err := s.activityPubEnabledRelayInboxes()
		if err != nil {
			return nil, err
		}
		inboxes = append(inboxes, relayInboxes...)
	}
	return compactActivityPubInboxes(inboxes), nil
}

func (s *Server) activityPubPollUpdateMentionInboxes(status models.Status) ([]string, error) {
	rows := []struct {
		InboxURL       string `gorm:"column:inbox_url"`
		SharedInboxURL string `gorm:"column:shared_inbox_url"`
	}{}
	for _, mention := range status.Mentions {
		account := mention.Account
		if account.ID == 0 || account.Local() || account.SuspendedAt.Valid {
			continue
		}
		rows = append(rows, struct {
			InboxURL       string `gorm:"column:inbox_url"`
			SharedInboxURL string `gorm:"column:shared_inbox_url"`
		}{InboxURL: account.InboxURL, SharedInboxURL: account.SharedInboxURL})
	}
	if len(rows) > 0 || s.db == nil || status.ID == 0 {
		return s.compactAvailableActivityPubInboxes(activityPubInboxesFromRows(rows)), nil
	}
	err := s.db.Model(&models.Mention{}).
		Select("accounts.inbox_url, accounts.shared_inbox_url").
		Joins("JOIN accounts ON accounts.id = mentions.account_id").
		Where("mentions.status_id = ? AND accounts.domain IS NOT NULL AND accounts.domain <> '' AND accounts.protocol = ?", status.ID, 1).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	return s.compactAvailableActivityPubInboxes(activityPubInboxesFromRows(rows)), nil
}

func (s *Server) activityPubAccountDeleteRecipientInboxes(accountID int64) ([]string, error) {
	deliveryInboxes, lowPriorityInboxes, err := s.activityPubAccountDeleteRecipientInboxesByPriority(accountID)
	if err != nil {
		return nil, err
	}
	inboxes := append(deliveryInboxes, lowPriorityInboxes...)
	return compactActivityPubInboxes(inboxes), nil
}

func (s *Server) activityPubAccountDeleteRecipientInboxesByPriority(accountID int64) ([]string, []string, error) {
	followerInboxes, err := s.activityPubRemoteFollowerInboxes(accountID)
	if err != nil {
		return nil, nil, err
	}
	relayInboxes, err := s.activityPubEnabledRelayInboxes()
	if err != nil {
		return nil, nil, err
	}
	allRemoteInboxes, err := s.activityPubAllRemoteAccountInboxes()
	if err != nil {
		return nil, nil, err
	}
	deliveryInboxes := compactActivityPubInboxes(append(followerInboxes, relayInboxes...))
	lowPriorityInboxes := removeActivityPubInboxes(compactActivityPubInboxes(allRemoteInboxes), deliveryInboxes)
	return deliveryInboxes, lowPriorityInboxes, nil
}

func (s *Server) activityPubMoveRecipientInboxes(accountID int64) ([]string, error) {
	followerInboxes, err := s.activityPubRemoteFollowerInboxes(accountID)
	if err != nil {
		return nil, err
	}
	blockerInboxes, err := s.activityPubRemoteBlockedByInboxes(accountID)
	if err != nil {
		return nil, err
	}
	relayInboxes, err := s.activityPubEnabledRelayInboxes()
	if err != nil {
		return nil, err
	}
	inboxes := append(followerInboxes, blockerInboxes...)
	inboxes = append(inboxes, relayInboxes...)
	return compactActivityPubInboxes(inboxes), nil
}

func (s *Server) deliverActivityPubActivityToStatusAuthor(account models.Account, status models.Status, activity map[string]any) error {
	if status.Account.Local() {
		return nil
	}
	inboxURL := status.Account.InboxURL
	if strings.TrimSpace(inboxURL) == "" {
		return nil
	}
	body, err := json.Marshal(activity)
	if err != nil {
		return err
	}
	return s.deliverActivityPub(account, inboxURL, body)
}

func (s *Server) forwardActivityPubStatusActivity(actor models.Account, status models.Status, body []byte) error {
	plan, err := s.prepareForwardActivityPubStatusActivity(actor, status, body)
	if err != nil || plan == nil {
		return err
	}
	return s.deliverForwardedActivityPubStatusActivity(*plan, body)
}

func (s *Server) forwardActivityPubCreateReply(actor models.Account, status models.Status, body []byte) error {
	if s == nil || s.db == nil || !activityPubStatusForwardable(status, body) || !status.InReplyToID.Valid {
		return nil
	}
	var parent models.Status
	if err := s.db.Preload("Account").Where("statuses.id = ?", status.InReplyToID.Int64).First(&parent).Error; err != nil {
		return nil
	}
	if !parent.Account.Local() {
		return nil
	}
	excludeInbox := activityPubPreferredInboxURL(actor.SharedInboxURL, actor.InboxURL)
	excludeInboxes := []string(nil)
	if excludeInbox != "" {
		excludeInboxes = []string{excludeInbox}
	}
	if s.enqueueRawDistributionTask(parent.AccountID, body, excludeInboxes) {
		return nil
	}
	return s.deliverActivityPubRawToFollowers(parent.Account, body, excludeInboxes)
}

type activityPubForwardingPlan struct {
	SignatureAccount models.Account
	Inboxes          []string
}

func (s *Server) prepareForwardActivityPubStatusActivity(actor models.Account, status models.Status, body []byte) (*activityPubForwardingPlan, error) {
	if s == nil || s.db == nil || !activityPubStatusForwardable(status, body) {
		return nil, nil
	}
	signatureAccount, inboxes, err := s.activityPubForwarderSignatureAccountAndInboxes(actor, status)
	if err != nil || signatureAccount == nil || len(inboxes) == 0 {
		return nil, err
	}
	return &activityPubForwardingPlan{SignatureAccount: *signatureAccount, Inboxes: inboxes}, nil
}

func (s *Server) deliverForwardedActivityPubStatusActivity(plan activityPubForwardingPlan, body []byte) error {
	var lastErr error
	for _, inboxURL := range plan.Inboxes {
		if err := s.deliverActivityPubConfigured(plan.SignatureAccount, inboxURL, body, func(job *activityPubDeliveryRetryJob) {
			job.RetryLimit = 8
		}); err != nil {
			lastErr = err
		}
	}
	return lastErr
}

func activityPubStatusForwardable(status models.Status, body []byte) bool {
	if !activityPubStatusDistributable(status) {
		return false
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return false
	}
	return activityJSONLDValuePresent(activityJSONLDValue(raw, "signature"))
}

func activityJSONLDValuePresent(value any) bool {
	if typed, ok := value.([]any); ok {
		return len(typed) > 0
	}
	switch typed := activityJSONLDSingle(value).(type) {
	case nil:
		return false
	case string:
		return strings.TrimSpace(typed) != ""
	case bool:
		return typed
	case map[string]any:
		return len(typed) > 0
	default:
		return true
	}
}

func (s *Server) activityPubForwarderSignatureAccountAndInboxes(actor models.Account, status models.Status) (*models.Account, []string, error) {
	localRebloggerIDs, err := s.activityPubLocalRebloggerIDs(status.ID)
	if err != nil {
		return nil, nil, err
	}
	localReplyAccount, err := s.activityPubLocalReplyAccount(status)
	if err != nil {
		return nil, nil, err
	}
	inboxes := []string{}
	if len(localRebloggerIDs) > 0 {
		rebloggerFollowerInboxes, err := s.activityPubRemoteFollowerInboxesForAccounts(localRebloggerIDs)
		if err != nil {
			return nil, nil, err
		}
		inboxes = append(inboxes, rebloggerFollowerInboxes...)
	}
	if localReplyAccount != nil {
		replyFollowerInboxes, err := s.activityPubRemoteFollowerInboxes(localReplyAccount.ID)
		if err != nil {
			return nil, nil, err
		}
		inboxes = append(inboxes, replyFollowerInboxes...)
	}
	inboxes = removeActivityPubInbox(inboxes, activityPubPreferredInboxURL(actor.SharedInboxURL, actor.InboxURL))
	inboxes = compactActivityPubInboxes(inboxes)
	if len(inboxes) == 0 {
		return nil, nil, nil
	}
	if localReplyAccount != nil {
		return localReplyAccount, inboxes, nil
	}
	if len(localRebloggerIDs) == 0 {
		return nil, nil, nil
	}
	var account models.Account
	if err := s.db.Where("id = ?", localRebloggerIDs[0]).First(&account).Error; err != nil {
		return nil, nil, err
	}
	return &account, inboxes, nil
}

func (s *Server) activityPubLocalRebloggerIDs(statusID int64) ([]int64, error) {
	if statusID == 0 {
		return nil, nil
	}
	var ids []int64
	err := s.db.Model(&models.Status{}).
		Select("statuses.account_id").
		Joins("JOIN accounts ON accounts.id = statuses.account_id").
		Where("statuses.reblog_of_id = ? AND statuses.deleted_at IS NULL", statusID).
		Where("accounts.domain IS NULL").
		Order("statuses.id ASC").
		Pluck("statuses.account_id", &ids).Error
	return ids, err
}

func (s *Server) activityPubLocalReplyAccount(status models.Status) (*models.Account, error) {
	if !status.InReplyToID.Valid {
		return nil, nil
	}
	var account models.Account
	err := s.db.Model(&models.Account{}).
		Select("accounts.*").
		Joins("JOIN statuses ON statuses.account_id = accounts.id").
		Where("statuses.id = ?", status.InReplyToID.Int64).
		Where("statuses.deleted_at IS NULL").
		Where("accounts.domain IS NULL").
		First(&account).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &account, err
}

func (s *Server) activityPubRemoteFollowerInboxesForAccounts(accountIDs []int64) ([]string, error) {
	if len(accountIDs) == 0 {
		return nil, nil
	}
	rows := []struct {
		InboxURL       string `gorm:"column:inbox_url"`
		SharedInboxURL string `gorm:"column:shared_inbox_url"`
	}{}
	err := s.db.Model(&models.Follow{}).
		Select("accounts.inbox_url, accounts.shared_inbox_url").
		Joins("JOIN accounts ON accounts.id = follows.account_id").
		Where("follows.target_account_id IN ? AND accounts.domain IS NOT NULL AND accounts.domain <> '' AND accounts.protocol = ?", accountIDs, 1).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	return s.compactAvailableActivityPubInboxes(activityPubInboxesFromRows(rows)), nil
}

func removeActivityPubInbox(inboxes []string, blocked string) []string {
	blocked = strings.TrimSpace(blocked)
	if blocked == "" {
		return inboxes
	}
	out := inboxes[:0]
	for _, inbox := range inboxes {
		if strings.TrimSpace(inbox) == blocked {
			continue
		}
		out = append(out, inbox)
	}
	return out
}

func removeActivityPubInboxes(inboxes []string, blocked []string) []string {
	blockedSet := make(map[string]struct{}, len(blocked))
	for _, inbox := range blocked {
		inbox = strings.TrimSpace(inbox)
		if inbox != "" {
			blockedSet[inbox] = struct{}{}
		}
	}
	out := inboxes[:0]
	for _, inbox := range inboxes {
		if _, ok := blockedSet[strings.TrimSpace(inbox)]; ok {
			continue
		}
		out = append(out, inbox)
	}
	return out
}

func (s *Server) activityPubRemoteReblogInboxes(statusID int64) ([]string, error) {
	rows := []struct {
		InboxURL       string `gorm:"column:inbox_url"`
		SharedInboxURL string `gorm:"column:shared_inbox_url"`
	}{}
	err := s.db.Model(&models.Status{}).
		Select("accounts.inbox_url, accounts.shared_inbox_url").
		Joins("JOIN accounts ON accounts.id = statuses.account_id").
		Where("statuses.reblog_of_id = ? AND statuses.deleted_at IS NULL AND accounts.domain IS NOT NULL AND accounts.domain <> '' AND accounts.protocol = ?", statusID, 1).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	return s.compactAvailableActivityPubInboxes(activityPubInboxesFromRows(rows)), nil
}

func (s *Server) activityPubRemotePollVoteInboxes(pollID int64) ([]string, error) {
	rows := []struct {
		InboxURL       string `gorm:"column:inbox_url"`
		SharedInboxURL string `gorm:"column:shared_inbox_url"`
	}{}
	err := s.db.Model(&models.PollVote{}).
		Select("accounts.inbox_url, accounts.shared_inbox_url").
		Joins("JOIN accounts ON accounts.id = poll_votes.account_id").
		Where("poll_votes.poll_id = ? AND accounts.domain IS NOT NULL AND accounts.domain <> '' AND accounts.protocol = ?", pollID, 1).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	return s.compactAvailableActivityPubInboxes(activityPubInboxesFromRows(rows)), nil
}

func (s *Server) activityPubRemoteFollowerInboxes(accountID int64) ([]string, error) {
	return s.activityPubRemoteFollowerInboxesConfigured(accountID, true)
}

func (s *Server) activityPubRemoteFollowerInboxesConfigured(accountID int64, unsafe bool) ([]string, error) {
	rows := []struct {
		InboxURL       string `gorm:"column:inbox_url"`
		SharedInboxURL string `gorm:"column:shared_inbox_url"`
	}{}
	query := s.db.Model(&models.Follow{}).
		Select("accounts.inbox_url, accounts.shared_inbox_url").
		Joins("JOIN accounts ON accounts.id = follows.account_id").
		Where("follows.target_account_id = ? AND accounts.domain IS NOT NULL AND accounts.domain <> '' AND accounts.protocol = ?", accountID, 1)
	if !unsafe {
		query = query.Where("accounts.suspended_at IS NULL")
	}
	err := query.Find(&rows).Error
	if err != nil {
		return nil, err
	}
	return s.compactAvailableActivityPubInboxes(activityPubInboxesFromRows(rows)), nil
}

func (s *Server) activityPubAccountReachInboxes(account models.Account) ([]string, error) {
	cutoff := activityPubAccountReachCutoff(account, time.Now().UTC())
	inboxes := []string{}
	for _, query := range []func() ([]string, error){
		func() ([]string, error) { return s.activityPubRemoteFollowerInboxes(account.ID) },
		func() ([]string, error) { return s.activityPubReporterInboxes(account.ID) },
		func() ([]string, error) { return s.activityPubRecentlyMentionedInboxes(account.ID, cutoff) },
		func() ([]string, error) { return s.activityPubRecentlyFollowedInboxes(account.ID, cutoff) },
		func() ([]string, error) { return s.activityPubRecentlyRequestedInboxes(account.ID, cutoff) },
		func() ([]string, error) { return s.activityPubEnabledRelayInboxes() },
	} {
		rows, err := query()
		if err != nil {
			return nil, err
		}
		inboxes = append(inboxes, rows...)
	}
	return compactActivityPubInboxes(inboxes), nil
}

func activityPubAccountReachCutoff(account models.Account, now time.Time) time.Time {
	anchor := now.UTC()
	if account.SuspendedAt.Valid && account.SuspensionOrigin.Valid && account.SuspensionOrigin.Int64 == 0 {
		anchor = account.SuspendedAt.Time.UTC()
	}
	return anchor.Add(-48 * time.Hour)
}

func (s *Server) activityPubReporterInboxes(accountID int64) ([]string, error) {
	rows := []struct {
		InboxURL       string `gorm:"column:inbox_url"`
		SharedInboxURL string `gorm:"column:shared_inbox_url"`
	}{}
	err := s.db.Model(&models.Report{}).
		Select("accounts.inbox_url, accounts.shared_inbox_url").
		Joins("JOIN accounts ON accounts.id = reports.account_id").
		Where("reports.target_account_id = ? AND accounts.domain IS NOT NULL AND accounts.domain <> '' AND accounts.protocol = ?", accountID, 1).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	return s.compactAvailableActivityPubInboxes(activityPubInboxesFromRows(rows)), nil
}

func (s *Server) activityPubRecentlyMentionedInboxes(accountID int64, cutoff time.Time) ([]string, error) {
	var statusIDs []int64
	oldestStatusID := mastodonSnowflakeIDAt(cutoff, false)
	if err := s.db.Model(&models.Status{}).
		Where("account_id = ? AND deleted_at IS NULL AND id >= ?", accountID, oldestStatusID).
		Order("id DESC").
		Limit(200).
		Pluck("id", &statusIDs).Error; err != nil {
		return nil, err
	}
	if len(statusIDs) == 0 {
		return nil, nil
	}
	rows := []struct {
		InboxURL       string `gorm:"column:inbox_url"`
		SharedInboxURL string `gorm:"column:shared_inbox_url"`
	}{}
	err := s.db.Model(&models.Mention{}).
		Select("accounts.inbox_url, accounts.shared_inbox_url").
		Joins("JOIN accounts ON accounts.id = mentions.account_id").
		Where("mentions.status_id IN ? AND accounts.domain IS NOT NULL AND accounts.domain <> '' AND accounts.protocol = ?", statusIDs, 1).
		Limit(2000).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	return s.compactAvailableActivityPubInboxes(activityPubInboxesFromRows(rows)), nil
}

func (s *Server) activityPubRecentlyFollowedInboxes(accountID int64, cutoff time.Time) ([]string, error) {
	rows := []struct {
		InboxURL       string `gorm:"column:inbox_url"`
		SharedInboxURL string `gorm:"column:shared_inbox_url"`
	}{}
	err := s.db.Model(&models.Follow{}).
		Select("accounts.inbox_url, accounts.shared_inbox_url").
		Joins("JOIN accounts ON accounts.id = follows.target_account_id").
		Where("follows.account_id = ? AND follows.created_at >= ? AND accounts.domain IS NOT NULL AND accounts.domain <> '' AND accounts.protocol = ?", accountID, cutoff, 1).
		Limit(2000).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	return s.compactAvailableActivityPubInboxes(activityPubInboxesFromRows(rows)), nil
}

func (s *Server) activityPubRecentlyRequestedInboxes(accountID int64, cutoff time.Time) ([]string, error) {
	rows := []struct {
		InboxURL       string `gorm:"column:inbox_url"`
		SharedInboxURL string `gorm:"column:shared_inbox_url"`
	}{}
	err := s.db.Model(&models.FollowRequest{}).
		Select("accounts.inbox_url, accounts.shared_inbox_url").
		Joins("JOIN accounts ON accounts.id = follow_requests.target_account_id").
		Where("follow_requests.account_id = ? AND follow_requests.created_at >= ? AND accounts.domain IS NOT NULL AND accounts.domain <> '' AND accounts.protocol = ?", accountID, cutoff, 1).
		Limit(2000).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	return s.compactAvailableActivityPubInboxes(activityPubInboxesFromRows(rows)), nil
}

func (s *Server) activityPubAllRemoteAccountInboxes() ([]string, error) {
	rows := []struct {
		InboxURL       string `gorm:"column:inbox_url"`
		SharedInboxURL string `gorm:"column:shared_inbox_url"`
	}{}
	err := s.db.Model(&models.Account{}).
		Select("inbox_url, shared_inbox_url").
		Where("domain IS NOT NULL AND domain <> '' AND protocol = ?", 1).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	return s.compactAvailableActivityPubInboxes(activityPubInboxesFromRows(rows)), nil
}

func (s *Server) activityPubRemoteBlockedByInboxes(accountID int64) ([]string, error) {
	rows := []struct {
		InboxURL       string `gorm:"column:inbox_url"`
		SharedInboxURL string `gorm:"column:shared_inbox_url"`
	}{}
	err := s.db.Model(&models.Block{}).
		Select("accounts.inbox_url, accounts.shared_inbox_url").
		Joins("JOIN accounts ON accounts.id = blocks.account_id").
		Where("blocks.target_account_id = ? AND accounts.domain IS NOT NULL AND accounts.domain <> '' AND accounts.protocol = ?", accountID, 1).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	return s.compactAvailableActivityPubInboxes(activityPubInboxesFromRows(rows)), nil
}

func activityPubInboxesFromRows(rows []struct {
	InboxURL       string `gorm:"column:inbox_url"`
	SharedInboxURL string `gorm:"column:shared_inbox_url"`
}) []string {
	seen := map[string]struct{}{}
	inboxes := make([]string, 0, len(rows))
	for _, row := range rows {
		inboxURL := activityPubPreferredInboxURL(row.SharedInboxURL, row.InboxURL)
		if inboxURL == "" {
			continue
		}
		if _, ok := seen[inboxURL]; ok {
			continue
		}
		seen[inboxURL] = struct{}{}
		inboxes = append(inboxes, inboxURL)
	}
	return inboxes
}

func activityPubPreferredInboxURL(sharedInboxURL string, inboxURL string) string {
	if strings.TrimSpace(sharedInboxURL) != "" {
		return sharedInboxURL
	}
	return inboxURL
}

func (s *Server) activityPubEnabledRelayInboxes() ([]string, error) {
	var rows []models.Relay
	if err := s.db.Model(&models.Relay{}).Where("state = ?", relayStateAccepted).Find(&rows).Error; err != nil {
		return nil, err
	}
	inboxes := make([]string, 0, len(rows))
	for _, row := range rows {
		inboxes = append(inboxes, row.InboxURL)
	}
	return compactActivityPubInboxes(inboxes), nil
}

func compactActivityPubInboxes(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if !activityPubHTTPURIAllowedRaw(value) {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func (s *Server) compactAvailableActivityPubInboxes(values []string) []string {
	inboxes := compactActivityPubInboxes(values)
	if s == nil || s.db == nil || len(inboxes) == 0 {
		return inboxes
	}

	domains := make([]string, 0, len(inboxes))
	validInboxes := make([]string, 0, len(inboxes))
	domainByInbox := make(map[string]string, len(inboxes))
	for _, inbox := range inboxes {
		host := normalizeDeliveryStatsHost(inbox)
		if host == "" {
			continue
		}
		domainByInbox[inbox] = host
		domains = append(domains, host)
		validInboxes = append(validInboxes, inbox)
	}
	if len(domains) == 0 {
		return nil
	}

	var unavailableDomains []string
	if err := s.db.Model(&models.UnavailableDomain{}).Where("domain IN ?", uniqueStrings(domains)).Pluck("domain", &unavailableDomains).Error; err != nil {
		return validInboxes
	}
	unavailable := make(map[string]struct{}, len(unavailableDomains))
	for _, domain := range unavailableDomains {
		domain = strings.TrimSpace(strings.ToLower(domain))
		if domain != "" {
			unavailable[domain] = struct{}{}
		}
	}

	out := make([]string, 0, len(validInboxes))
	for _, inbox := range validInboxes {
		host := domainByInbox[inbox]
		if _, ok := unavailable[host]; ok {
			continue
		}
		out = append(out, inbox)
	}
	return out
}

func activityPubFollowPayload(s *Server, local models.Account, remote models.Account, followID int64, followURI string) map[string]any {
	if followURI == "" {
		followURI = activityPubFollowURI(s, local, followID)
	}
	return map[string]any{
		"@context": activityPubActivityStreamsContext(),
		"id":       followURI,
		"type":     "Follow",
		"actor":    activityPubActorURL(s, local),
		"object":   activityPubAccountTagManagerURI(s, remote),
	}
}

func activityPubUndoFollowPayload(s *Server, local models.Account, remote models.Account, followID int64, followURI string) map[string]any {
	if followURI == "" {
		followURI = activityPubFollowURIForSerializer(s, local, followID)
	}
	idPart := strconv.FormatInt(followID, 10)
	if followID <= 0 {
		idPart = ""
	}
	localActor := activityPubActorURL(s, local)
	remoteActor := activityPubAccountTagManagerURI(s, remote)
	return map[string]any{
		"@context": activityPubActivityStreamsContext(),
		"id":       localActor + "#follows/" + idPart + "/undo",
		"type":     "Undo",
		"actor":    localActor,
		"object": map[string]any{
			"id":     followURI,
			"type":   "Follow",
			"actor":  localActor,
			"object": remoteActor,
		},
	}
}

func activityPubBlockPayload(s *Server, local models.Account, remote models.Account, blockID int64, blockURI string) map[string]any {
	if blockURI == "" {
		blockURI = activityPubBlockURIForSerializer(s, local, blockID)
	}
	return map[string]any{
		"@context": activityPubActivityStreamsContext(),
		"id":       blockURI,
		"type":     "Block",
		"actor":    activityPubActorURL(s, local),
		"object":   activityPubAccountTagManagerURI(s, remote),
	}
}

func activityPubUndoBlockPayload(s *Server, local models.Account, remote models.Account, blockID int64, blockURI string) map[string]any {
	return map[string]any{
		"@context": activityPubActivityStreamsContext(),
		"id":       activityPubBlockURIForSerializer(s, local, blockID) + "/undo",
		"type":     "Undo",
		"actor":    activityPubActorURL(s, local),
		"object":   activityPubNestedSerializerObject(activityPubBlockPayload(s, local, remote, blockID, blockURI)),
	}
}

func activityPubMove(s *Server, migration models.AccountMigration) map[string]any {
	actor := activityPubActorURL(s, migration.Account)
	return map[string]any{
		"@context": activityPubActivityStreamsContext(),
		"id":       activityPubMoveURIForSerializer(s, migration),
		"type":     "Move",
		"actor":    actor,
		"object":   actor,
		"target":   activityPubAccountTagManagerURI(s, migration.TargetAccount),
	}
}

func activityPubMoveURIForSerializer(s *Server, migration models.AccountMigration) string {
	actor := activityPubActorURL(s, migration.Account)
	if migration.ID <= 0 {
		return actor + "#moves/"
	}
	return actor + "#moves/" + strconv.FormatInt(migration.ID, 10)
}

func activityPubRelayFollowPayload(s *Server, local *models.Account, activityID string) map[string]any {
	actor := activityPubActorID(s, *local)
	return map[string]any{
		"@context": activityPubActivityStreamsContext(),
		"id":       activityID,
		"type":     "Follow",
		"actor":    actor,
		"object":   "https://www.w3.org/ns/activitystreams#Public",
	}
}

func activityPubRelayUndoFollowPayload(s *Server, local *models.Account, activityID string, followActivityID string) map[string]any {
	actor := activityPubActorID(s, *local)
	return map[string]any{
		"@context": activityPubActivityStreamsContext(),
		"id":       activityID,
		"type":     "Undo",
		"actor":    actor,
		"object": map[string]any{
			"id":     followActivityID,
			"type":   "Follow",
			"actor":  actor,
			"object": "https://www.w3.org/ns/activitystreams#Public",
		},
	}
}

func activityPubFollowURI(s *Server, local models.Account, followID int64) string {
	return activityPubActorURL(s, local) + "#follows/" + strconv.FormatInt(followID, 10)
}

func activityPubFollowURIForSerializer(s *Server, local models.Account, followID int64) string {
	if followID <= 0 {
		return activityPubActorURL(s, local) + "#follows/"
	}
	return activityPubFollowURI(s, local, followID)
}

func activityPubBlockURI(s *Server, local models.Account, blockID int64) string {
	return activityPubActorURL(s, local) + "#blocks/" + strconv.FormatInt(blockID, 10)
}

func activityPubBlockURIForSerializer(s *Server, local models.Account, blockID int64) string {
	if blockID <= 0 {
		return activityPubActorURL(s, local) + "#blocks/"
	}
	return activityPubBlockURI(s, local, blockID)
}

func (s *Server) deliverActivityPubFollowResponse(kind string, local models.Account, remote models.Account, followID int64, followURI string) error {
	inboxURL := activityPubPreferredInboxURL(remote.SharedInboxURL, remote.InboxURL)
	if strings.TrimSpace(inboxURL) == "" {
		return fmt.Errorf("activitypub %s Follow response target account_id=%d has no inbox", kind, remote.ID)
	}
	body, err := json.Marshal(activityPubFollowResponsePayload(s, kind, local, remote, followID, followURI))
	if err != nil {
		return err
	}
	return s.deliverActivityPub(local, inboxURL, body)
}

func activityPubFollowResponsePayload(s *Server, kind string, local models.Account, remote models.Account, followID int64, followURI string) map[string]any {
	localActor := activityPubActorURL(s, local)
	remoteActor := activityPubAccountTagManagerURI(s, remote)
	action := "accepts"
	if kind == "Reject" {
		action = "rejects"
	}
	idPart := ""
	if followID > 0 {
		idPart = strconv.FormatInt(followID, 10)
	}
	if followURI == "" {
		followURI = remoteActor + "#follows/" + idPart
	}
	return map[string]any{
		"@context": activityPubActivityStreamsContext(),
		"id":       localActor + "#" + action + "/follows/" + idPart,
		"type":     kind,
		"actor":    localActor,
		"object": map[string]any{
			"id":     followURI,
			"type":   "Follow",
			"actor":  remoteActor,
			"object": localActor,
		},
	}
}

func (s *Server) deliverActivityPub(local models.Account, inboxURL string, body []byte) error {
	return s.deliverActivityPubConfigured(local, inboxURL, body, nil)
}

func (s *Server) deliverActivityPubConfigured(local models.Account, inboxURL string, body []byte, configureRetry func(*activityPubDeliveryRetryJob)) error {
	if !local.PrivateKey.Valid || strings.TrimSpace(local.PrivateKey.String) == "" {
		return nil
	}
	parsed, err := url.Parse(inboxURL)
	if err != nil || parsed.Host == "" || !activityFetchHostAllowed(parsed.Hostname()) {
		return fmt.Errorf("remote host is not allowed")
	}
	host := normalizeDeliveryStatsHost(parsed.Hostname())
	if host == "" {
		return fmt.Errorf("remote host is not allowed")
	}
	retry := activityPubDeliveryRetryJob{
		SourceAccountID: local.ID,
		InboxURL:        inboxURL,
		Body:            append(json.RawMessage(nil), body...),
		CreatedAt:       time.Now().UTC().Unix(),
	}
	if configureRetry != nil {
		configureRetry(&retry)
	}
	if s.enqueueActivityPubDeliveryTask(retry) {
		return nil
	}
	if !retry.BypassAvailability && !s.activityPubDeliveryAvailable(host) {
		return nil
	}
	if err := s.deliverActivityPubOnce(context.Background(), local, inboxURL, body, host, retry.SynchronizeFollowers); err != nil {
		s.trackActivityPubDeliveryFailure(host)
		s.enqueueActivityPubDeliveryRetryConfigured(local, inboxURL, body, configureRetry)
		return err
	}
	_ = s.afterActivityPubDeliveryRetrySuccess(context.Background(), retry)
	return nil
}

func (s *Server) deliverActivityPubOnce(ctx context.Context, local models.Account, inboxURL string, body []byte, host string, synchronizeFollowers bool) (err error) {
	finishTelemetry := func(int, error) {}
	if s != nil && s.cfg.OpenTelemetryEnabled {
		ctx, finishTelemetry = telemetry.StartFederation(ctx, "outbound")
	}
	statusCode := 0
	defer func() { finishTelemetry(statusCode, err) }()
	if s.activityPubDeliveryStoplightOpen(inboxURL) {
		return fmt.Errorf("activitypub delivery stoplight is open inbox=%q", inboxURL)
	}
	key, err := activityPrivateKey(local.PrivateKey.String)
	if err != nil {
		return fmt.Errorf("activitypub delivery load signing key source_account_id=%d: %w", local.ID, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, inboxURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("activitypub delivery create request inbox=%q: %w", inboxURL, err)
	}
	req.Header.Set("Content-Type", "application/activity+json")
	req.Header.Set("Date", time.Now().UTC().Format(http.TimeFormat))
	req.Header.Set("Digest", activityDigestHeader(body))
	req.Host = activityPubRequestHost(req)
	req.Header.Set("Host", req.Host)
	req.Header.Set("User-Agent", paonUserAgent(s.cfg))
	req.Header.Set("Accept-Encoding", "gzip")
	if synchronizeFollowers && activityPubDeliveryFollowersSynchronizationEnabled() {
		header, err := s.activityPubCollectionSynchronizationHeader(local, inboxURL)
		if err != nil {
			return err
		}
		if header != "" {
			req.Header.Set("Collection-Synchronization", header)
		}
	}
	headers := []string{"host", "date", "digest", "content-type"}
	if req.Header.Get("Collection-Synchronization") != "" {
		headers = append(headers, "collection-synchronization")
	}
	headers = append(headers, "(request-target)")
	if err := s.signActivityPubRequest(req, local, key, headers); err != nil {
		return fmt.Errorf("activitypub delivery sign request source_account_id=%d inbox=%q: %w", local.ID, inboxURL, err)
	}
	resp, err := activityHTTPClientForActivityDelivery(s, local, key, headers).Do(req)
	if err != nil {
		s.trackActivityPubDeliveryStoplightFailure(inboxURL)
		return fmt.Errorf("activitypub delivery request source_account_id=%d inbox=%q: %w", local.ID, inboxURL, err)
	}
	defer resp.Body.Close()
	statusCode = resp.StatusCode
	permanentlySuspended := false
	if resp.StatusCode == http.StatusUnauthorized {
		permanentlySuspended, err = s.accountSuspendedPermanently(&local)
		if err != nil {
			return err
		}
	}
	switch activityPubDeliveryResponseDispositionFor(resp.StatusCode, permanentlySuspended) {
	case activityPubDeliveryResponseSucceeded:
		s.trackActivityPubDeliveryStoplightSuccess(inboxURL)
		s.trackActivityPubDeliverySuccess(host)
		return nil
	case activityPubDeliveryResponseDiscarded:
		responseSnippet := activityPubResponseSnippet(resp.Body)
		logActivityPubDeliveryRejected(local.ID, inboxURL, resp.StatusCode, responseSnippet)
		s.trackActivityPubDeliveryStoplightSuccess(inboxURL)
		s.trackActivityPubDeliveryStats(host, "failure")
		return nil
	}
	responseSnippet := activityPubResponseSnippet(resp.Body)
	s.trackActivityPubDeliveryStoplightFailure(inboxURL)
	return fmt.Errorf("activitypub delivery failed source_account_id=%d inbox=%q status=%d response=%q", local.ID, inboxURL, resp.StatusCode, responseSnippet)
}

func activityHTTPClientForActivityDelivery(s *Server, signer models.Account, key *rsa.PrivateKey, headers []string) *http.Client {
	if activityHTTPClient == nil {
		return nil
	}
	client := *activityHTTPClient
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 {
			return http.ErrUseLastResponse
		}
		if !activityRedirectAllowed(req, via) {
			return fmt.Errorf("remote host is not allowed")
		}
		req.Header.Del("Signature")
		req.Host = activityPubRequestHost(req)
		req.Header.Set("Host", req.Host)
		req.Header.Set("Date", time.Now().UTC().Format(http.TimeFormat))
		if s == nil || key == nil {
			return fmt.Errorf("activitypub redirect signing key is missing")
		}
		return s.signActivityPubRequest(req, signer, key, headers)
	}
	return &client
}

func activityPubDeliveryFollowersSynchronizationEnabled() bool {
	return os.Getenv("DISABLE_FOLLOWERS_SYNCHRONIZATION") != "true"
}

func activityPubDeliveryStoplightKey(prefix string, inboxURL string) string {
	inboxURL = strings.TrimSpace(inboxURL)
	if inboxURL == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(inboxURL))
	return prefix + "activitypub:delivery:stoplight:" + fmt.Sprintf("%x", sum[:])
}

func (s *Server) activityPubDeliveryStoplightOpen(inboxURL string) bool {
	if s == nil || s.db == nil {
		return false
	}
	key := activityPubDeliveryStoplightKey(redisConfig(s.cfg).prefix, inboxURL)
	if key == "" {
		return false
	}
	value, err := s.redisCommand(context.Background(), "GET", key)
	if err != nil {
		return false
	}
	return redisInt(value) >= activityPubDeliveryStoplightFailureThreshold
}

func (s *Server) trackActivityPubDeliveryStoplightFailure(inboxURL string) {
	if s == nil || s.db == nil {
		return
	}
	key := activityPubDeliveryStoplightKey(redisConfig(s.cfg).prefix, inboxURL)
	if key == "" {
		return
	}
	ctx := context.Background()
	if _, err := s.redisCommand(ctx, "INCR", key); err != nil {
		return
	}
	_, _ = s.redisCommand(ctx, "EXPIRE", key, strconv.FormatInt(int64(activityPubDeliveryStoplightCooldown/time.Second), 10))
}

func (s *Server) trackActivityPubDeliveryStoplightSuccess(inboxURL string) {
	if s == nil || s.db == nil {
		return
	}
	key := activityPubDeliveryStoplightKey(redisConfig(s.cfg).prefix, inboxURL)
	if key == "" {
		return
	}
	_, _ = s.redisCommand(context.Background(), "DEL", key)
}

func activityPubDeliveryResponseSuccessful(status int) bool {
	return status >= 200 && status < 300
}

type activityPubDeliveryResponseDisposition uint8

const (
	activityPubDeliveryResponseRetry activityPubDeliveryResponseDisposition = iota
	activityPubDeliveryResponseSucceeded
	activityPubDeliveryResponseDiscarded
)

func activityPubDeliveryResponseDispositionFor(status int, sourcePermanentlySuspended bool) activityPubDeliveryResponseDisposition {
	if activityPubDeliveryResponseSuccessful(status) {
		return activityPubDeliveryResponseSucceeded
	}
	if activityPubDeliveryResponseErrorUnsalvageable(status) ||
		activityPubDeliveryAuthorizationFailureUnsalvageable(status, sourcePermanentlySuspended) {
		return activityPubDeliveryResponseDiscarded
	}
	return activityPubDeliveryResponseRetry
}

func activityPubDeliveryResponseErrorUnsalvageable(status int) bool {
	return status == http.StatusNotImplemented || (status >= 400 && status < 500 && status != http.StatusUnauthorized && status != http.StatusRequestTimeout && status != http.StatusTooManyRequests)
}

func activityPubDeliveryAuthorizationFailureUnsalvageable(status int, sourcePermanentlySuspended bool) bool {
	return sourcePermanentlySuspended && status == http.StatusUnauthorized
}

func (s *Server) signActivityPubFetchRequest(req *http.Request, account models.Account) error {
	return s.signActivityPubFetchRequestWithAccept(req, account, true)
}

func (s *Server) signActivityPubFetchRequestWithAccept(req *http.Request, account models.Account, signAccept bool) error {
	key, err := activityPrivateKey(account.PrivateKey.String)
	if err != nil {
		return err
	}
	req.Host = activityPubRequestHost(req)
	req.Header.Set("Date", time.Now().UTC().Format(http.TimeFormat))
	req.Header.Set("Host", req.Host)
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", paonUserAgent(s.cfg))
	}
	headers := []string{"host", "date"}
	if signAccept && req.Header.Get("Accept") != "" {
		headers = append(headers, "accept")
	}
	headers = append(headers, "(request-target)")
	return s.signActivityPubRequest(req, account, key, headers)
}

func (s *Server) signActivityPubRequest(req *http.Request, account models.Account, key *rsa.PrivateKey, headers []string) error {
	if req.Host == "" {
		req.Host = activityPubRequestHost(req)
	}
	params := map[string]string{"headers": strings.Join(headers, " ")}
	signedString := buildActivitySignedString(req, params, headers, true)
	sum := sha256.Sum256([]byte(signedString))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		return err
	}
	req.Header.Set("Signature", `keyId="`+activityPubActorID(s, account)+`#main-key",algorithm="rsa-sha256",headers="`+strings.Join(headers, " ")+`",signature="`+base64.StdEncoding.EncodeToString(signature)+`"`)
	return nil
}

func activityPubRequestHost(req *http.Request) string {
	if req == nil {
		return ""
	}
	if req.URL != nil {
		if host := req.URL.Hostname(); host != "" {
			return host
		}
	}
	host := strings.TrimSpace(req.Host)
	if host == "" {
		return ""
	}
	if parsed, err := url.Parse("//" + host); err == nil && parsed.Hostname() != "" {
		return parsed.Hostname()
	}
	return host
}

func (s *Server) activityPubCollectionSynchronizationHeader(local models.Account, inboxURL string) (string, error) {
	origin := activityPubInboxOrigin(inboxURL)
	if origin == "" {
		return "", nil
	}
	digest, err := s.activityPubRemoteFollowersHash(local.ID, origin)
	if err != nil {
		return "", err
	}
	actor := activityPubActorURL(s, local)
	return `collectionId="` + actor + `/followers", digest="` + digest + `", url="` + actor + `/followers_synchronization"`, nil
}

func activityPubInboxOrigin(inboxURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(inboxURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return ""
	}
	return scheme + "://" + strings.ToLower(parsed.Host)
}

func (s *Server) activityPubRemoteFollowersHash(accountID int64, origin string) (string, error) {
	if s == nil || s.db == nil || accountID == 0 || origin == "" {
		return "", nil
	}
	var rows []struct {
		URI sql.NullString `gorm:"column:uri"`
	}
	like := escapeSQLLike(strings.TrimRight(origin, "/")) + "/%"
	if err := s.db.Table("accounts").
		Select("accounts.uri").
		Joins("JOIN follows ON follows.account_id = accounts.id").
		Where("follows.target_account_id = ?", accountID).
		Where(`accounts.uri = ? OR accounts.uri LIKE ? ESCAPE '\'`, origin, like).
		Find(&rows).Error; err != nil {
		return "", err
	}
	digest := make([]byte, sha256.Size)
	for _, row := range rows {
		if !row.URI.Valid || strings.TrimSpace(row.URI.String) == "" {
			continue
		}
		sum := sha256.Sum256([]byte(row.URI.String))
		for i := range digest {
			digest[i] ^= sum[i]
		}
	}
	return fmt.Sprintf("%x", digest), nil
}

func (s *Server) activityPubDeliveryAvailable(host string) bool {
	host = normalizeDeliveryStatsHost(host)
	if s.db == nil || host == "" {
		return true
	}
	var count int64
	if err := s.db.Model(&models.UnavailableDomain{}).Where("domain = ?", host).Count(&count).Error; err != nil {
		return true
	}
	return count == 0
}

func (s *Server) trackActivityPubDeliverySuccess(host string) {
	host = normalizeDeliveryStatsHost(host)
	if host == "" {
		return
	}
	s.trackActivityPubDeliveryStats(host, "success")
	if s.db == nil {
		return
	}
	key := exhaustedDeliveriesRedisKey(redisConfig(s.cfg).prefix, host)
	if key != "" {
		_, _ = s.redisCommand(context.Background(), "DEL", key)
	}
	if err := s.db.Where("domain = ?", host).Delete(&models.UnavailableDomain{}).Error; err == nil {
		s.invalidateUnavailableDomainsCache(context.Background())
	}
}

func (s *Server) trackActivityPubDeliveryFailure(host string) {
	host = normalizeDeliveryStatsHost(host)
	if host == "" {
		return
	}
	s.trackActivityPubDeliveryStats(host, "failure")
	if s.db == nil {
		return
	}
	key := exhaustedDeliveriesRedisKey(redisConfig(s.cfg).prefix, host)
	if key == "" {
		return
	}
	ctx := context.Background()
	today := time.Now().UTC().Format("20060102")
	if _, err := s.redisCommand(ctx, "SADD", key, today); err != nil {
		return
	}
	days, err := s.redisCommand(ctx, "SCARD", key)
	if err != nil || redisInt(days) < activityPubDeliveryFailureDaysThreshold {
		return
	}
	now := time.Now().UTC()
	row := models.UnavailableDomain{Domain: host, CreatedAt: now, UpdatedAt: now}
	result := s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "domain"}},
		DoNothing: true,
	}).Create(&row)
	if result.Error == nil && result.RowsAffected > 0 {
		s.invalidateUnavailableDomainsCache(context.Background())
	}
}

func (s *Server) trackActivityPubDeliveryStats(host string, result string) {
	if s == nil || host == "" || (result != "success" && result != "failure") {
		return
	}
	key := deliveryStatsRedisKey(s.cfg.RedisNamespace, host, result, time.Now().UTC())
	_, _ = s.redisCommand(context.Background(), "INCR", key)
}

func activityPrivateKey(privateKeyPEM string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(privateKeyPEM))
	if block == nil {
		return nil, fmt.Errorf("invalid private key")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("invalid private key")
	}
	rsaKey, ok := key.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("invalid private key")
	}
	return rsaKey, nil
}
