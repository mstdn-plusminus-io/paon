package api

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func encryptedMessageStreamPayload(message models.EncryptedMessage) string {
	return announcementStreamPayload("encrypted_message", cryptoEncryptedMessageResponse(message))
}

func encryptedMessageStreamingChannel(prefix string, accountID int64, deviceID string) string {
	if accountID == 0 || deviceID == "" {
		return ""
	}
	return prefix + "timeline:" + strconv.FormatInt(accountID, 10) + ":" + deviceID
}

func (s *Server) publishEncryptedMessage(message models.EncryptedMessage, device models.Device) {
	if s == nil || s.db == nil || message.ID == 0 || !device.AccountID.Valid || device.AccountID.Int64 == 0 || device.DeviceID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	if !s.encryptedMessageTimelineSubscribed(ctx, device) {
		return
	}
	if !s.enqueuePushEncryptedMessageTask(message.ID) {
		s.publishEncryptedMessageNowWithContext(ctx, message, device)
	}
}

func (s *Server) publishEncryptedMessageNowWithContext(ctx context.Context, message models.EncryptedMessage, device models.Device) {
	if s == nil || message.ID == 0 || !device.AccountID.Valid || device.AccountID.Int64 == 0 || device.DeviceID == "" {
		return
	}
	cfg := redisConfig(s.cfg)
	channel := encryptedMessageStreamingChannel(cfg.prefix, device.AccountID.Int64, device.DeviceID)
	if channel == "" {
		return
	}
	_, _ = s.redisCommand(ctx, "PUBLISH", channel, encryptedMessageStreamPayload(message))
}

func (s *Server) encryptedMessageTimelineSubscribed(ctx context.Context, device models.Device) bool {
	if s == nil || !device.AccountID.Valid || device.AccountID.Int64 == 0 || device.DeviceID == "" {
		return false
	}
	cfg := redisConfig(s.cfg)
	channel := encryptedMessageStreamingChannel(cfg.prefix, device.AccountID.Int64, device.DeviceID)
	if channel == "" {
		return false
	}
	value, err := s.redisCommand(ctx, "EXISTS", cfg.prefix+"subscribed:"+strings.TrimPrefix(channel, cfg.prefix))
	return err == nil && value == int64(1)
}

func streamingChannelIDsForSession(channel string, ids []string, session streamingSession) []string {
	if channel != "user" || session.Account == nil {
		return ids
	}
	accountID := strconv.FormatInt(session.Account.ID, 10)
	if tokenHasAnyScope(session.Scopes, "read", "read:notifications") {
		ids = appendStreamingChannelID(ids, "timeline:"+accountID+":notifications")
	}
	if session.DeviceID != "" && tokenHasAnyScope(session.Scopes, "crypto") {
		ids = appendStreamingChannelID(ids, "timeline:"+accountID+":"+session.DeviceID)
	}
	return ids
}

func appendStreamingChannelID(ids []string, channelID string) []string {
	for _, id := range ids {
		if id == channelID {
			return ids
		}
	}
	return append(ids, channelID)
}
