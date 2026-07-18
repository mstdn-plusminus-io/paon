package api

import (
	"context"
	"encoding/json"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"github.com/mstdn-plusminus-io/paon/internal/paon/serializer"
)

var announcementStatusURLPattern = regexp.MustCompile(`https?://[^\s<>"']+`)

func announcementStreamPayload(event string, payload any) string {
	encoded, _ := json.Marshal(struct {
		Event   string `json:"event"`
		Payload any    `json:"payload"`
	}{Event: event, Payload: payload})
	return string(encoded)
}

func timelineChannelFromSubscribedKey(cfg config.Config, key string) (string, bool) {
	prefix := redisConfig(cfg).prefix
	raw, ok := strings.CutPrefix(key, prefix+"subscribed:timeline:")
	if !ok || raw == "" || strings.Contains(raw, ":") {
		return "", false
	}
	if _, err := strconv.ParseInt(raw, 10, 64); err != nil {
		return "", false
	}
	return prefix + "timeline:" + raw, true
}

func redisScanKeys(value any) (string, []string, bool) {
	items, ok := value.([]any)
	if !ok || len(items) != 2 {
		return "", nil, false
	}
	cursor, ok := items[0].(string)
	if !ok {
		return "", nil, false
	}
	rawKeys, ok := items[1].([]any)
	if !ok {
		return "", nil, false
	}
	keys := make([]string, 0, len(rawKeys))
	for _, raw := range rawKeys {
		key, ok := raw.(string)
		if ok {
			keys = append(keys, key)
		}
	}
	return cursor, keys, true
}

func (s *Server) publishToSubscribedHomeTimelines(event string, payload any) {
	if s == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cfg := redisConfig(s.cfg)
	message := announcementStreamPayload(event, payload)
	cursor := "0"
	seen := map[string]struct{}{}
	for {
		value, err := s.redisCommand(ctx, "SCAN", cursor, "MATCH", cfg.prefix+"subscribed:timeline:*", "COUNT", "100")
		if err != nil {
			return
		}
		next, keys, ok := redisScanKeys(value)
		if !ok {
			return
		}
		for _, key := range keys {
			channel, ok := timelineChannelFromSubscribedKey(s.cfg, key)
			if !ok {
				continue
			}
			if _, ok := seen[channel]; ok {
				continue
			}
			seen[channel] = struct{}{}
			_, _ = s.redisCommand(ctx, "PUBLISH", channel, message)
		}
		cursor = next
		if cursor == "0" {
			return
		}
	}
}

func (s *Server) broadcastAnnouncement(announcement models.Announcement) {
	if s == nil || s.db == nil || !announcement.Published {
		return
	}
	announcement = s.refreshAnnouncementStatusIDs(announcement)
	_ = s.hydrateAnnouncementReferences(&announcement)
	statuses, err := s.announcementStatuses(announcement)
	if err != nil {
		statuses = []models.Status{}
	}
	reactions, err := s.announcementReactions(0, announcement.ID)
	if err != nil {
		reactions = []serializer.ReactionSource{}
	}
	payload := serializer.AnnouncementFromModel(s.cfg, announcement, nil, statuses, reactions)
	s.publishToSubscribedHomeTimelines("announcement", payload)
}

func (s *Server) broadcastAnnouncementDelete(announcementID int64) {
	if announcementID == 0 {
		return
	}
	s.publishToSubscribedHomeTimelines("announcement.delete", strconv.FormatInt(announcementID, 10))
}

func (s *Server) broadcastAnnouncementReaction(announcementID int64, name string) {
	if s == nil || s.db == nil || announcementID == 0 || strings.TrimSpace(name) == "" {
		return
	}
	payload, err := s.announcementReactionStreamPayload(announcementID, name)
	if err != nil {
		return
	}
	s.publishToSubscribedHomeTimelines("announcement.reaction", payload)
}

func (s *Server) announcementReactionStreamPayload(announcementID int64, name string) (map[string]any, error) {
	var row announcementReactionRow
	err := s.db.Model(&models.AnnouncementReaction{}).
		Select("name, custom_emoji_id, COUNT(*) AS count").
		Where("announcement_id = ? AND name = ?", announcementID, name).
		Group("name, custom_emoji_id").
		Order("COUNT(*) DESC").
		Limit(1).
		Scan(&row).Error
	if err != nil {
		return nil, err
	}
	payload := map[string]any{
		"announcement_id": strconv.FormatInt(announcementID, 10),
		"name":            name,
		"count":           row.Count,
	}
	if row.Count == 0 || !row.CustomEmojiID.Valid {
		return payload, nil
	}
	emojis, err := s.announcementReactionCustomEmojis([]announcementReactionRow{row})
	if err != nil {
		return nil, err
	}
	if emoji, ok := emojis[row.CustomEmojiID.Int64]; ok {
		serialized := serializer.CustomEmojiFromModel(s.cfg, emoji)
		payload["url"] = serialized.URL
		payload["static_url"] = serialized.StaticURL
	}
	return payload, nil
}

func (s *Server) refreshAnnouncementStatusIDs(announcement models.Announcement) models.Announcement {
	if s == nil || s.db == nil {
		return announcement
	}
	ids := s.statusIDsFromAnnouncementText(announcement.Text)
	if int64ArraysEqual(ids, announcement.StatusIDs) {
		return announcement
	}
	announcement.StatusIDs = ids
	_ = s.db.Model(&models.Announcement{}).Where("id = ?", announcement.ID).Update("status_ids", ids).Error
	return announcement
}

func (s *Server) statusIDsFromAnnouncementText(text string) models.Int64Array {
	seen := map[int64]struct{}{}
	ids := models.Int64Array{}
	for _, raw := range announcementStatusURLPattern.FindAllString(text, -1) {
		unescaped, err := url.QueryUnescape(strings.TrimRight(raw, ".,;:)"))
		if err != nil {
			unescaped = strings.TrimRight(raw, ".,;:)")
		}
		id, err := strconv.ParseInt(statusIDFromLocalURL(s.cfg.BaseURL(), unescaped), 10, 64)
		if err != nil || id == 0 {
			status, err := s.announcementRemoteStatusFromTextURL(unescaped)
			if err != nil || status == nil || !activityPubStatusDistributable(*status) {
				continue
			}
			id = status.ID
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids
}

func (s *Server) announcementRemoteStatusFromTextURL(raw string) (*models.Status, error) {
	if s == nil || s.db == nil || strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	if s.localActivityURI(raw) {
		return nil, nil
	}
	return s.fetchRemoteStatusFromResolvableURL(raw)
}

func int64ArraysEqual(left models.Int64Array, right models.Int64Array) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
