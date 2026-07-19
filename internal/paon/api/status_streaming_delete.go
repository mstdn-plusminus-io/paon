package api

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"github.com/mstdn-plusminus-io/paon/internal/paon/serializer"
	"gorm.io/gorm"
)

func statusStreamPayload(event string, payload any) string {
	return announcementStreamPayload(event, payload)
}

func statusUpdateStreamPayload(cfg config.Config, event string, status models.Status) string {
	status = statusWithoutHashtagPreviewCards(status)
	return statusStreamPayload(event, serializer.StatusFromModel(cfg, status, nil))
}

func statusDeleteStreamPayload(statusID int64) string {
	return statusStreamPayload("delete", strconv.FormatInt(statusID, 10))
}

func (s *Server) publishStatusUpdateEvent(event string, status models.Status) {
	if s == nil || s.db == nil || status.ID == 0 || event == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	s.publishStatusUpdateEventWithContext(ctx, s.db, event, status)
}

func (s *Server) publishStatusUpdateEventWithContext(ctx context.Context, database *gorm.DB, event string, status models.Status) {
	if s == nil || database == nil || status.ID == 0 || event == "" {
		return
	}
	cfg := redisConfig(s.cfg)
	payload := statusUpdateStreamPayload(s.cfg, event, status)
	// Home and list streams are published by FeedInsert -> PushUpdate after
	// hydrating the recipient-specific relationship fields.
	for _, channel := range s.statusStreamingChannels(ctx, database, cfg.prefix, status) {
		_, _ = s.redisCommand(ctx, "PUBLISH", channel, payload)
	}
}

func (s *Server) publishStatusDelete(status models.Status) {
	if s == nil || s.db == nil || status.ID == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	s.publishStatusDeleteWithContext(ctx, s.db, status)
}

func (s *Server) publishStatusDeletesForQuery(ctx context.Context, database *gorm.DB, statusIDs *gorm.DB) {
	if s == nil || database == nil || statusIDs == nil {
		return
	}
	var statuses []models.Status
	if err := database.WithContext(ctx).
		Preload("Account").
		Preload("MediaAttachments").
		Preload("Mentions").
		Preload("Tags").
		Where("id IN (?)", statusIDs).
		Find(&statuses).Error; err != nil {
		return
	}
	for _, status := range statuses {
		s.publishStatusDeleteWithContext(ctx, database, status)
	}
}

func (s *Server) publishBatchedAccountDeletionStatusDeletesForQuery(ctx context.Context, database *gorm.DB, statusIDs *gorm.DB, now time.Time) {
	if s == nil || database == nil || statusIDs == nil {
		return
	}
	publicCutoffID := mastodonSnowflakeIDAt(now.Add(-14*24*time.Hour), false)
	var statuses []models.Status
	if err := database.WithContext(ctx).
		Preload("Account").
		Preload("MediaAttachments").
		Preload("Tags").
		Where("id IN (?)", statusIDs).
		Find(&statuses).Error; err != nil {
		return
	}
	cfg := redisConfig(s.cfg)
	for _, status := range statuses {
		payload := statusDeleteStreamPayload(status.ID)
		status = s.statusForDeleteStreaming(ctx, database, status)
		for _, channel := range s.statusBatchedDeletionStreamingChannels(ctx, database, cfg.prefix, status, publicCutoffID) {
			_, _ = s.redisCommand(ctx, "PUBLISH", channel, payload)
		}
	}
}

func (s *Server) publishStatusAndReblogDeletesForIDs(ctx context.Context, database *gorm.DB, ids []int64) {
	if s == nil || database == nil || len(ids) == 0 {
		return
	}
	statusIDs := database.Model(&models.Status{}).Select("id").Where("id IN ?", ids)
	s.publishStatusDeletesForQuery(ctx, database, statusIDs)
	reblogIDs := database.Model(&models.Status{}).Select("id").Where("reblog_of_id IN (?)", statusIDs)
	s.publishStatusDeletesForQuery(ctx, database, reblogIDs)
}

func (s *Server) applyAdminDeletedStatusSideEffects(ctx context.Context, database *gorm.DB, ids []int64) {
	if s == nil || database == nil || len(ids) == 0 {
		return
	}
	s.publishStatusAndReblogDeletesForIDs(ctx, database, ids)
	statusIDs := database.Model(&models.Status{}).Select("id").Where("id IN ?", ids)
	var statuses []models.Status
	if err := database.WithContext(ctx).
		Select("id", "account_id", "reblog_of_id").
		Where("id IN (?) OR reblog_of_id IN (?)", statusIDs, statusIDs).
		Find(&statuses).Error; err != nil {
		return
	}
	for _, status := range statuses {
		s.invalidateStatusCache(ctx, status.ID)
		_ = s.removeStatusFromRailsFeeds(ctx, database, status)
		s.meiliDeleteStatusBestEffort(ctx, status.ID)
		s.deleteStatusQuoteBestEffort(ctx, status.ID)
	}
}

func (s *Server) publishStatusDeleteWithContext(ctx context.Context, database *gorm.DB, status models.Status) {
	if s == nil || status.ID == 0 {
		return
	}
	cfg := redisConfig(s.cfg)
	payload := statusDeleteStreamPayload(status.ID)
	status = s.statusForDeleteStreaming(ctx, database, status)
	for _, channel := range s.statusDeleteStreamingChannels(ctx, database, cfg.prefix, status) {
		_, _ = s.redisCommand(ctx, "PUBLISH", channel, payload)
	}
}

func (s *Server) statusForDeleteStreaming(ctx context.Context, database *gorm.DB, status models.Status) models.Status {
	if s == nil || database == nil || status.ID == 0 {
		return status
	}
	if !status.Local.Valid && status.Account.ID == 0 && status.AccountID != 0 {
		var account models.Account
		if err := database.WithContext(ctx).Select("id", "domain", "silenced_at").Where("id = ?", status.AccountID).First(&account).Error; err == nil {
			status.Account = account
		}
	}
	if len(status.MediaAttachments) == 0 {
		var media []models.MediaAttachment
		if err := database.WithContext(ctx).Select("id", "status_id").Where("status_id = ?", status.ID).Find(&media).Error; err == nil {
			status.MediaAttachments = media
		}
	}
	if len(status.Tags) == 0 {
		var tags []models.Tag
		if err := database.WithContext(ctx).
			Table("tags").
			Select("tags.id, tags.name").
			Joins("JOIN statuses_tags ON statuses_tags.tag_id = tags.id").
			Where("statuses_tags.status_id = ?", status.ID).
			Find(&tags).Error; err == nil {
			status.Tags = tags
		}
	}
	return status
}

func (s *Server) statusStreamingChannels(ctx context.Context, database *gorm.DB, prefix string, status models.Status) []string {
	broadcastable := s.statusBroadcastableToPublicStreams(ctx, database, status)
	channels := statusPublicStreamingChannels(prefix, status, broadcastable)
	channels = append(channels, statusTagStreamingChannels(prefix, status, broadcastable)...)
	if database != nil && status.Visibility == 3 {
		channels = append(channels, s.statusDirectStreamingChannels(ctx, database, prefix, status.ID)...)
	}
	return uniqueStrings(channels)
}

func (s *Server) statusDeleteStreamingChannels(ctx context.Context, database *gorm.DB, prefix string, status models.Status) []string {
	channels := statusDeletePublicStreamingChannels(prefix, status)
	channels = append(channels, statusDeleteTagStreamingChannels(prefix, status)...)
	if database != nil && status.AccountID != 0 {
		channels = append(channels, s.statusHomeStreamingChannels(ctx, database, prefix, status.AccountID)...)
		channels = append(channels, s.statusListStreamingChannels(ctx, database, prefix, status.AccountID)...)
	}
	if database != nil && !status.ReblogOfID.Valid {
		channels = append(channels, s.statusMentionHomeDeleteChannels(ctx, database, prefix, status.ID)...)
	}
	return uniqueStrings(channels)
}

func (s *Server) statusBatchedDeletionStreamingChannels(ctx context.Context, database *gorm.DB, prefix string, status models.Status, publicCutoffID int64) []string {
	var channels []string
	if status.ID > publicCutoffID {
		channels = append(channels, statusBatchedDeletePublicStreamingChannels(prefix, status)...)
		channels = append(channels, statusBatchedDeleteTagStreamingChannels(prefix, status)...)
	}
	if database != nil && status.AccountID != 0 {
		channels = append(channels, s.statusHomeStreamingChannels(ctx, database, prefix, status.AccountID)...)
		channels = append(channels, s.statusListStreamingChannels(ctx, database, prefix, status.AccountID)...)
	}
	return uniqueStrings(channels)
}

func (s *Server) statusBroadcastableToPublicStreams(ctx context.Context, database *gorm.DB, status models.Status) bool {
	if status.Visibility != 0 || status.ReblogOfID.Valid || status.AccountID == 0 {
		return false
	}
	if status.Account.ID != 0 {
		return !status.Account.SilencedAt.Valid
	}
	if database == nil {
		return true
	}
	var account models.Account
	if err := database.WithContext(ctx).Select("id", "silenced_at").First(&account, status.AccountID).Error; err != nil {
		return true
	}
	return !account.SilencedAt.Valid
}

func statusPublicStreamingChannels(prefix string, status models.Status, broadcastable bool) []string {
	if !broadcastable || (status.Reply && status.InReplyToAccountID.Valid && status.InReplyToAccountID.Int64 != status.AccountID) {
		return nil
	}
	channels := []string{prefix + "timeline:public"}
	local, known := statusStreamingLocality(status)
	if known {
		if local {
			channels = append(channels, prefix+"timeline:public:local")
		} else {
			channels = append(channels, prefix+"timeline:public:remote")
		}
	}
	if len(status.MediaAttachments) > 0 {
		channels = append(channels, prefix+"timeline:public:media")
		if known {
			if local {
				channels = append(channels, prefix+"timeline:public:local:media")
			} else {
				channels = append(channels, prefix+"timeline:public:remote:media")
			}
		}
	}
	return channels
}

func statusDeletePublicStreamingChannels(prefix string, status models.Status) []string {
	if status.Visibility != 0 || status.ReblogOfID.Valid {
		return nil
	}
	channels := []string{prefix + "timeline:public"}
	local, known := statusStreamingLocality(status)
	if known {
		if local {
			channels = append(channels, prefix+"timeline:public:local")
		} else {
			channels = append(channels, prefix+"timeline:public:remote")
		}
	}
	if len(status.MediaAttachments) > 0 {
		channels = append(channels, prefix+"timeline:public:media")
		if known {
			if local {
				channels = append(channels, prefix+"timeline:public:local:media")
			} else {
				channels = append(channels, prefix+"timeline:public:remote:media")
			}
		}
	}
	return channels
}

func statusBatchedDeletePublicStreamingChannels(prefix string, status models.Status) []string {
	if status.Visibility != 0 {
		return nil
	}
	channels := []string{prefix + "timeline:public"}
	local, known := statusStreamingLocality(status)
	if known {
		if local {
			channels = append(channels, prefix+"timeline:public:local")
		} else {
			channels = append(channels, prefix+"timeline:public:remote")
		}
	}
	if len(status.MediaAttachments) > 0 {
		channels = append(channels, prefix+"timeline:public:media")
		if known {
			if local {
				channels = append(channels, prefix+"timeline:public:local:media")
			} else {
				channels = append(channels, prefix+"timeline:public:remote:media")
			}
		}
	}
	return channels
}

func statusTagStreamingChannels(prefix string, status models.Status, broadcastable bool) []string {
	if !broadcastable || len(status.Tags) == 0 {
		return nil
	}
	local, known := statusStreamingLocality(status)
	channels := make([]string, 0, len(status.Tags)*2)
	for _, tag := range status.Tags {
		name := strings.ToLower(strings.TrimSpace(tag.Name))
		if name == "" {
			continue
		}
		channels = append(channels, prefix+"timeline:hashtag:"+name)
		if known && local {
			channels = append(channels, prefix+"timeline:hashtag:"+name+":local")
		}
	}
	return channels
}

func statusBatchedDeleteTagStreamingChannels(prefix string, status models.Status) []string {
	if status.Visibility != 0 || len(status.Tags) == 0 {
		return nil
	}
	local, known := statusStreamingLocality(status)
	channels := make([]string, 0, len(status.Tags)*2)
	for _, tag := range status.Tags {
		name := strings.ToLower(strings.TrimSpace(tag.Name))
		if name == "" {
			continue
		}
		channels = append(channels, prefix+"timeline:hashtag:"+name)
		if known && local {
			channels = append(channels, prefix+"timeline:hashtag:"+name+":local")
		}
	}
	return channels
}

func statusDeleteTagStreamingChannels(prefix string, status models.Status) []string {
	if status.Visibility != 0 || status.ReblogOfID.Valid || len(status.Tags) == 0 {
		return nil
	}
	local, known := statusStreamingLocality(status)
	channels := make([]string, 0, len(status.Tags)*2)
	for _, tag := range status.Tags {
		name := strings.ToLower(strings.TrimSpace(tag.Name))
		if name == "" {
			continue
		}
		channels = append(channels, prefix+"timeline:hashtag:"+name)
		if known && local {
			channels = append(channels, prefix+"timeline:hashtag:"+name+":local")
		}
	}
	return channels
}

func statusStreamingLocality(status models.Status) (bool, bool) {
	if status.Local.Valid {
		return status.Local.Bool, true
	}
	if status.Account.ID != 0 {
		return status.Account.Local(), true
	}
	return false, false
}

func (s *Server) statusHomeStreamingChannels(ctx context.Context, database *gorm.DB, prefix string, accountID int64) []string {
	var accountIDs []int64
	_ = database.WithContext(ctx).Table("follows").
		Select("DISTINCT follows.account_id").
		Joins("JOIN users ON users.account_id = follows.account_id").
		Where("follows.target_account_id = ?", accountID).
		Pluck("follows.account_id", &accountIDs).Error
	if statusAccountHasLocalUser(ctx, database, accountID) {
		accountIDs = append(accountIDs, accountID)
	}
	channels := make([]string, 0, len(accountIDs))
	for _, id := range accountIDs {
		if id != 0 {
			channels = append(channels, prefix+"timeline:"+strconv.FormatInt(id, 10))
		}
	}
	return channels
}

func (s *Server) statusListStreamingChannels(ctx context.Context, database *gorm.DB, prefix string, accountID int64) []string {
	var listIDs []int64
	_ = database.WithContext(ctx).Table("lists").
		Select("DISTINCT lists.id").
		Joins("JOIN users ON users.account_id = lists.account_id").
		Joins("JOIN list_accounts ON list_accounts.list_id = lists.id").
		Where("list_accounts.account_id = ?", accountID).
		Pluck("lists.id", &listIDs).Error
	channels := make([]string, 0, len(listIDs))
	for _, id := range listIDs {
		if id != 0 {
			channels = append(channels, prefix+"timeline:list:"+strconv.FormatInt(id, 10))
		}
	}
	return channels
}

func (s *Server) statusDirectStreamingChannels(ctx context.Context, database *gorm.DB, prefix string, statusID int64) []string {
	var accountIDs []int64
	_ = database.WithContext(ctx).Table("mentions").
		Select("DISTINCT mentions.account_id").
		Joins("JOIN users ON users.account_id = mentions.account_id").
		Where("mentions.status_id = ?", statusID).
		Pluck("mentions.account_id", &accountIDs).Error
	channels := make([]string, 0, len(accountIDs))
	for _, id := range accountIDs {
		if id != 0 {
			channels = append(channels, prefix+"timeline:direct:"+strconv.FormatInt(id, 10))
		}
	}
	return channels
}

func (s *Server) statusMentionHomeDeleteChannels(ctx context.Context, database *gorm.DB, prefix string, statusID int64) []string {
	var accountIDs []int64
	_ = database.WithContext(ctx).Table("mentions").
		Select("DISTINCT mentions.account_id").
		Where("mentions.status_id = ? AND mentions.silent = false", statusID).
		Pluck("mentions.account_id", &accountIDs).Error
	channels := make([]string, 0, len(accountIDs))
	for _, id := range accountIDs {
		if id != 0 {
			channels = append(channels, prefix+"timeline:"+strconv.FormatInt(id, 10))
		}
	}
	return channels
}

func statusAccountHasLocalUser(ctx context.Context, database *gorm.DB, accountID int64) bool {
	var count int64
	_ = database.WithContext(ctx).Table("users").Where("account_id = ?", accountID).Count(&count).Error
	return count > 0
}
