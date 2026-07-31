package api

import (
	"context"
	"strconv"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func streamingKillPayload() string {
	return `{"event":"kill"}`
}

func (s *Server) publishStreamingKill(accountID int64, accessTokenIDs []int64) {
	if s == nil || accountID == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	cfg := redisConfig(s.cfg)
	payload := streamingKillPayload()
	_, _ = s.redisCommand(ctx, "PUBLISH", cfg.prefix+"timeline:system:"+strconv.FormatInt(accountID, 10), payload)
	s.publishAccessTokenKillsWithContext(ctx, cfg.prefix, payload, accessTokenIDs)
}

func (s *Server) publishStreamingKillForLocalAccount(account models.Account) {
	if !account.Local() {
		return
	}
	s.publishStreamingKill(account.ID, nil)
}

func (s *Server) publishAccessTokenKills(accessTokenIDs []int64) {
	if s == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	cfg := redisConfig(s.cfg)
	s.publishAccessTokenKillsWithContext(ctx, cfg.prefix, streamingKillPayload(), accessTokenIDs)
}

func (s *Server) publishAccessTokenKillsWithContext(ctx context.Context, prefix string, payload string, accessTokenIDs []int64) {
	for _, channel := range accessTokenKillChannels(prefix, accessTokenIDs) {
		_, _ = s.redisCommand(ctx, "PUBLISH", channel, payload)
	}
}

func accessTokenKillChannels(prefix string, accessTokenIDs []int64) []string {
	seen := map[int64]struct{}{}
	channels := make([]string, 0, len(accessTokenIDs))
	for _, id := range accessTokenIDs {
		if id == 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		channels = append(channels, prefix+"timeline:access_token:"+strconv.FormatInt(id, 10))
	}
	return channels
}
