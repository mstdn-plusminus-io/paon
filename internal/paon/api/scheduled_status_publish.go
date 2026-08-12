package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

const (
	scheduledStatusPublishWorkerInterval = 5 * time.Minute
	scheduledStatusPublishLookahead      = 5 * time.Minute
)

func (s *Server) runScheduledStatusPublishWorker(ctx context.Context) {
	ticker := time.NewTicker(scheduledStatusPublishWorkerInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			s.runSchedulerWithRedisLock(ctx, "scheduled_statuses_scheduler", time.Hour, func() {
				s.processDueScheduledStatusSchedule(ctx, now.UTC())
			})
		}
	}
}

func (s *Server) processDueScheduledStatusSchedule(ctx context.Context, now time.Time) {
	if s == nil || s.db == nil {
		return
	}
	s.publishDueScheduledStatuses(ctx, now.UTC())
	s.processDueAnnouncementSchedule(ctx, now.UTC())
}

func (s *Server) publishDueScheduledStatuses(ctx context.Context, now time.Time) {
	if s == nil || s.db == nil {
		return
	}
	var scheduled []models.ScheduledStatus
	if err := s.db.WithContext(ctx).
		Where("scheduled_at <= ?", now.UTC().Add(scheduledStatusPublishLookahead)).
		Order("scheduled_at ASC, id ASC").
		Find(&scheduled).Error; err != nil {
		return
	}
	for _, item := range scheduled {
		if !item.ScheduledAt.Valid {
			continue
		}
		if s.enqueuePublishScheduledStatusTask(item.ID, item.ScheduledAt.Time) {
			continue
		}
		if item.ScheduledAt.Time.After(now.UTC()) {
			continue
		}
		_, _ = s.publishScheduledStatus(ctx, item, now.UTC())
	}
}

func (s *Server) publishScheduledStatus(ctx context.Context, scheduled models.ScheduledStatus, now time.Time) (*models.Status, error) {
	payload, mediaIDs, err := statusCreatePayloadFromScheduledParams(scheduled.Params)
	if err != nil {
		s.deleteScheduledStatusBestEffort(ctx, scheduled.ID)
		return nil, nil
	}
	hasPoll := payload.HasPoll && payload.Poll != nil
	if (submittedMediaIDsPresent(mediaIDs) && submittedMediaIDsCount(mediaIDs) > s.maxMediaAttachments()) || (hasPoll && submittedMediaIDsPresent(mediaIDs)) {
		s.deleteScheduledStatusBestEffort(ctx, scheduled.ID)
		return nil, nil
	}
	mediaIDs = compactMediaIDs(mediaIDs)
	sensitive := statusSensitiveValue(payload.statusUpdatePayload)
	applyCreateSpoilerTextFallback(&payload.statusUpdatePayload)

	if !scheduled.AccountID.Valid || scheduled.AccountID.Int64 == 0 {
		s.deleteScheduledStatusBestEffort(ctx, scheduled.ID)
		return nil, nil
	}
	accountID := scheduled.AccountID.Int64
	var account models.Account
	if err := s.db.WithContext(ctx).Preload("User").Where("id = ? AND suspended_at IS NULL", accountID).First(&account).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			s.deleteScheduledStatusBestEffort(ctx, scheduled.ID)
			return nil, nil
		}
		return nil, err
	}
	if account.User.Disabled {
		// Mastodon 4.4 destroys the scheduled row before treating a disabled
		// (frozen) author's publish job as a successful no-op.
		s.deleteScheduledStatusBestEffort(ctx, scheduled.ID)
		return nil, nil
	}

	text := payload.Status
	normalizeStatusContents(&text, &payload.SpoilerText)
	if (strings.TrimSpace(text) == "" && len(mediaIDs) == 0 && !hasPoll) || statusLengthTooLong(text, payload.SpoilerText, s.maxStatusChars()) {
		s.deleteScheduledStatusBestEffort(ctx, scheduled.ID)
		return nil, nil
	}
	if err := validateStatusDisallowedHashtags(ctx, s.db, text); err != nil {
		s.deleteScheduledStatusBestEffort(ctx, scheduled.ID)
		return nil, nil
	}

	var replyTo *models.Status
	if strings.TrimSpace(payload.InReplyToID) != "" {
		replyTo, err = s.findVisibleStatusForAccount(&account, payload.InReplyToID)
		if err != nil {
			s.deleteScheduledStatusBestEffort(ctx, scheduled.ID)
			return nil, nil
		}
		replyTo, err = s.railsStatusReplyTarget(replyTo)
		if err != nil {
			return nil, err
		}
	}
	language := s.statusLanguageForAccount(payload.Language, sql.NullString{}, account)

	status := models.Status{
		Text:          text,
		CreatedAt:     now,
		UpdatedAt:     now,
		AccountID:     account.ID,
		Local:         sql.NullBool{Bool: true, Valid: true},
		Visibility:    s.statusVisibility(account, payload.Visibility),
		Sensitive:     sensitive,
		SpoilerText:   payload.SpoilerText,
		Language:      language,
		ApplicationID: payload.ApplicationID,
	}
	if replyTo != nil {
		status.InReplyToID = sql.NullInt64{Int64: replyTo.ID, Valid: true}
		status.InReplyToAccountID = railsStatusReplyAccountID(account.ID, replyTo)
		status.Reply = true
		status.ConversationID = replyTo.ConversationID
	}

	var notificationIDs []int64
	var notificationPayloads []asynqLocalNotificationPayload
	var conversationIDs []int64
	var indexedTagIDs []int64
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.ensureStatusConversation(tx, &status, now); err != nil {
			return err
		}
		if err := tx.Create(&status).Error; err != nil {
			return err
		}
		if err := tx.Create(&models.StatusStat{StatusID: status.ID, RepliesCount: 0, ReblogsCount: 0, FavouritesCount: 0}).Error; err != nil {
			return err
		}
		mediaIntIDs := mediaIDsToInt64Array(mediaIDs)
		acceptedMediaIDs, err := updateStatusMedia(tx, account.ID, status.ID, mediaIDs, mediaIntIDs, payload.MediaAttributes, true)
		if err != nil {
			return err
		}
		if len(mediaIDs) > 0 {
			if err := tx.Model(&models.Status{}).Where("id = ?", status.ID).Update("ordered_media_attachment_ids", acceptedMediaIDs).Error; err != nil {
				return err
			}
		}
		if hasPoll {
			pollID, _, err := updateStatusPoll(tx, account.ID, status.ID, payload.Poll, now)
			if err != nil {
				return err
			}
			if err := tx.Model(&models.Status{}).Where("id = ?", status.ID).Update("poll_id", pollID).Error; err != nil {
				return err
			}
		}
		mentions, err := s.saveStatusMentionsFromTextAndCollectAccounts(tx, status.ID, account.ID, text, now)
		if err != nil {
			return err
		}
		if unexpected := unexpectedMentionAccounts(mentions.Accounts, payload.AllowedMentions, payload.HasAllowedMentions); len(unexpected) > 0 {
			return unexpectedMentionsError{accounts: unexpected}
		}
		updatedConversationIDs, err := s.addDirectStatusToConversations(tx, status, mentions.Accounts)
		if err != nil {
			return err
		}
		conversationIDs = append(conversationIDs, updatedConversationIDs...)
		notificationIDs = append(notificationIDs, mentions.NotificationIDs...)
		notificationPayloads = append(notificationPayloads, mentions.NotificationPayloads...)
		tagIDs, err := saveStatusTagsFromText(tx, status.ID, text, now)
		if err != nil {
			return err
		}
		indexedTagIDs = append(indexedTagIDs, tagIDs...)
		if err := refreshFeaturedTagStatsForStatusTags(tx, account.ID, status.Visibility, tagIDs, now); err != nil {
			return err
		}
		return tx.Delete(&models.ScheduledStatus{}, scheduled.ID).Error
	}); err != nil {
		var unexpected unexpectedMentionsError
		if errors.As(err, &unexpected) {
			s.deleteScheduledStatusBestEffort(ctx, scheduled.ID)
			return nil, nil
		}
		if mediaAttachmentValidationError(err) {
			s.deleteScheduledStatusBestEffort(ctx, scheduled.ID)
			return nil, nil
		}
		return nil, err
	}

	created := &status
	if err := s.runLocalStatusAfterCreateCommitEffects(s.db.WithContext(ctx), created, account, nil, replyTo, func() {
		if statusCountsTowardLocalActivity(created.Visibility) {
			s.activityTrackerIncrementBasic(ctx, "activity:statuses:local", created.CreatedAt, 1)
		}
	}); err != nil {
		return nil, err
	}
	created, err = s.findStatus(strconv.FormatInt(status.ID, 10))
	if err != nil {
		return nil, err
	}
	createdNotificationIDs, err := s.enqueueOrCreateLocalNotifications(ctx, notificationPayloads)
	if err != nil {
		return nil, err
	}
	notificationIDs = append(notificationIDs, createdNotificationIDs...)
	s.meiliIndexStatusBestEffort(ctx, created.ID)
	s.meiliIndexTagsBestEffort(ctx, indexedTagIDs)
	s.recordStatusTrendUse(ctx, created.ID, created.CreatedAt)
	if err := s.enqueueFASPContentLifecycle(ctx, *created, "new"); err != nil {
		return nil, err
	}
	if created.InReplyToID.Valid {
		if err := s.enqueueFASPTrendForStatus(ctx, *created, "reply"); err != nil {
			return nil, err
		}
	}
	if created.InReplyToAccountID.Valid && created.InReplyToAccountID.Int64 != account.ID {
		s.activityTrackerIncrementBasic(ctx, "activity:interactions", created.CreatedAt, 1)
		s.recordPotentialFriendship(ctx, account.ID, created.InReplyToAccountID.Int64, "reply")
	}
	s.recordTagTrendUse(ctx, account.ID, created.Visibility, indexedTagIDs, now)
	s.publishStatusUpdateEventWithContext(ctx, s.db, "update", *created)
	s.publishConversationIDs(ctx, conversationIDs)
	s.publishNotificationIDs(notificationIDs)
	s.fetchLinkCardForStatusAsync(created.ID)
	s.schedulePollExpirationNotifyWorker(created.Poll)
	_ = s.enqueueOrDeliverActivityPubDistribution(*created)
	return created, nil
}

func (s *Server) deleteScheduledStatusBestEffort(ctx context.Context, id int64) {
	if s == nil || s.db == nil || id == 0 {
		return
	}
	_ = s.db.WithContext(ctx).Delete(&models.ScheduledStatus{}, id).Error
}

func statusCreatePayloadFromScheduledParams(raw models.JSONValue) (statusCreatePayload, []string, error) {
	var params map[string]json.RawMessage
	if err := json.Unmarshal(raw, &params); err != nil {
		return statusCreatePayload{}, nil, err
	}
	var payload statusCreatePayload
	payload.Status = rawJSONString(params["status"])
	payload.Visibility = rawJSONString(params["visibility"])
	payload.SpoilerText = rawJSONString(params["spoiler_text"])
	payload.Language = rawJSONString(params["language"])
	payload.InReplyToID = rawJSONString(params["in_reply_to_id"])
	payload.ApplicationID = rawJSONInt64(params["application_id"])
	if value, ok := rawJSONBool(params["sensitive"]); ok {
		payload.Sensitive = value
		payload.HasSensitive = true
	}
	mediaIDs := rawJSONStringSlice(params["media_ids"])
	payload.MediaIDs = mediaIDs
	payload.HasMediaIDs = len(mediaIDs) > 0
	if rawPoll, ok := params["poll"]; ok && len(rawPoll) > 0 && string(rawPoll) != "null" {
		poll, ok := pollUpdatePayloadFromRawJSON(rawPoll)
		if ok {
			payload.Poll = &poll
			payload.HasPoll = true
		}
	}
	if rawAllowed, ok := params["allowed_mentions"]; ok {
		payload.AllowedMentions = rawJSONStringSlice(rawAllowed)
		payload.HasAllowedMentions = true
	}
	return payload, mediaIDs, nil
}

func pollUpdatePayloadFromRawJSON(raw json.RawMessage) (pollUpdatePayload, bool) {
	var params map[string]json.RawMessage
	if err := json.Unmarshal(raw, &params); err != nil {
		return pollUpdatePayload{}, false
	}
	out := pollUpdatePayload{
		Options:    rawJSONStringSlice(params["options"]),
		Multiple:   rawJSONBoolDefault(params["multiple"]),
		HideTotals: rawJSONBoolDefault(params["hide_totals"]),
		ExpiresIn:  rawJSONIntDefault(params["expires_in"]),
	}
	return out, len(out.Options) > 0
}

func rawJSONString(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var value string
	if err := json.Unmarshal(raw, &value); err == nil {
		return value
	}
	var number json.Number
	if err := json.Unmarshal(raw, &number); err == nil {
		return number.String()
	}
	return ""
}

func rawJSONStringSlice(raw json.RawMessage) []string {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var strings []string
	if err := json.Unmarshal(raw, &strings); err == nil {
		return strings
	}
	var values []json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if item := rawJSONString(value); item != "" {
			out = append(out, item)
		}
	}
	return out
}

func rawJSONBool(raw json.RawMessage) (bool, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return false, false
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err == nil {
		return value, true
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return truthy(text), true
	}
	return false, false
}

func rawJSONBoolDefault(raw json.RawMessage) bool {
	value, _ := rawJSONBool(raw)
	return value
}

func rawJSONIntDefault(raw json.RawMessage) int {
	if len(raw) == 0 || string(raw) == "null" {
		return 0
	}
	var value int
	if err := json.Unmarshal(raw, &value); err == nil {
		return value
	}
	var number json.Number
	if err := json.Unmarshal(raw, &number); err == nil {
		parsed, _ := strconv.Atoi(number.String())
		return parsed
	}
	return 0
}

func rawJSONInt64(raw json.RawMessage) sql.NullInt64 {
	if len(raw) == 0 || string(raw) == "null" {
		return sql.NullInt64{}
	}
	var value int64
	if err := json.Unmarshal(raw, &value); err == nil {
		return sql.NullInt64{Int64: value, Valid: true}
	}
	var number json.Number
	if err := json.Unmarshal(raw, &number); err == nil {
		parsed, err := strconv.ParseInt(number.String(), 10, 64)
		if err == nil {
			return sql.NullInt64{Int64: parsed, Valid: true}
		}
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		parsed, err := strconv.ParseInt(strings.TrimSpace(text), 10, 64)
		if err == nil {
			return sql.NullInt64{Int64: parsed, Valid: true}
		}
	}
	return sql.NullInt64{}
}
