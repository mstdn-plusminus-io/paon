package api

import (
	"context"
	"strconv"
	"time"
)

func filterCacheKey(accountID int64) string {
	return "filters:v3:" + strconv.FormatInt(accountID, 10)
}

func filterChangedRedisPayload() string {
	return `{"event":"filters_changed"}`
}

func (s *Server) invalidateFilterCacheAndBroadcast(accountID int64) {
	if s == nil || accountID == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	keys := railsCacheRedisKeyCandidates(s.cfg, filterCacheKey(accountID))
	if len(keys) > 0 {
		_, _ = s.cacheRedisCommand(ctx, append([]string{"DEL"}, keys...)...)
	}
	cfg := redisConfig(s.cfg)
	payload := filterChangedRedisPayload()
	account := strconv.FormatInt(accountID, 10)
	_, _ = s.redisCommand(ctx, "PUBLISH", cfg.prefix+"timeline:"+account, payload)
	_, _ = s.redisCommand(ctx, "PUBLISH", cfg.prefix+"timeline:system:"+account, payload)
}
