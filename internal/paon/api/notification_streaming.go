package api

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"github.com/mstdn-plusminus-io/paon/internal/paon/serializer"
)

func notificationStreamPayload(cfg config.Config, notification models.Notification, current *models.Account) string {
	return statusStreamPayload("notification", serializer.NotificationFromModel(cfg, notification, current))
}

func (s *Server) filteredNotificationStreamPayload(cfg config.Config, notification models.Notification, current *models.Account) string {
	return statusStreamPayload("notification", notificationWithStatusFilters(cfg, notification, current, s.accountFilters(current)))
}

func (s *Server) accountFilters(current *models.Account) []streamingFilter {
	if s == nil || s.db == nil || current == nil {
		return nil
	}
	return s.streamingFilters(current.ID)
}

func notificationWithStatusFilters(cfg config.Config, notification models.Notification, current *models.Account, filters []streamingFilter) serializer.Notification {
	item := serializer.NotificationFromModel(cfg, notification, current)
	if current == nil || item.Status == nil || len(filters) == 0 {
		return item
	}
	payload, ok := notificationStatusPayloadMap(*item.Status)
	if !ok {
		return item
	}
	results := streamingFilterResultsFromFilters(payload, filters, "notifications")
	if len(results) == 0 {
		return item
	}
	item.Status.Filtered = streamingFilterResultsAny(results)
	return item
}

func notificationStatusPayloadMap(status serializer.Status) (map[string]any, bool) {
	data, err := json.Marshal(status)
	if err != nil {
		return nil, false
	}
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, false
	}
	return payload, true
}

func streamingFilterResultsAny(results []streamingFilterResult) []any {
	out := make([]any, 0, len(results))
	for _, result := range results {
		out = append(out, result)
	}
	return out
}

func notificationStreamingChannel(prefix string, accountID int64) string {
	return prefix + "timeline:" + strconv.FormatInt(accountID, 10) + ":notifications"
}

func (s *Server) publishNotificationIDs(ids []int64) {
	if s == nil || s.db == nil || len(ids) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	for _, id := range uniqueInt64s(ids) {
		s.publishNotificationIDWithContext(ctx, id)
	}
}

func (s *Server) publishNotificationIDWithContext(ctx context.Context, id int64) {
	if s == nil || s.db == nil || id == 0 {
		return
	}
	var notification models.Notification
	if err := s.db.WithContext(ctx).
		Model(&models.Notification{}).
		Preload("FromAccount.AccountStat").
		Where("id = ?", id).
		First(&notification).Error; err != nil {
		return
	}
	account := models.Account{ID: notification.AccountID}
	_ = s.db.WithContext(ctx).Preload("AccountStat").Where("id = ?", notification.AccountID).First(&account).Error
	notifications := []models.Notification{notification}
	if err := s.hydrateNotificationStatuses(notifications); err != nil {
		return
	}
	if err := s.hydrateNotificationReports(notifications); err != nil {
		return
	}
	if err := s.hydrateNotificationStatusRelationships(notifications, &account); err != nil {
		return
	}
	cfg := redisConfig(s.cfg)
	payload := s.filteredNotificationStreamPayload(s.cfg, notifications[0], &account)
	_, _ = s.redisCommand(ctx, "PUBLISH", notificationStreamingChannel(cfg.prefix, notification.AccountID), payload)
	s.deliverWebPushNotification(ctx, notifications[0], account)
}
