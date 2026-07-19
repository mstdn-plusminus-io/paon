package api

import (
	"context"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
)

const ipBlockRailsCacheKey = "blocked_ips"

func ipBlockCacheRedisKeys(cfg config.Config) []string {
	return railsCacheRedisKeyCandidates(cfg, ipBlockRailsCacheKey)
}

func (s *Server) invalidateIPBlockCache(ctx context.Context) {
	if s == nil {
		return
	}
	s.ipBlockMu.Lock()
	s.noAccessIPBlocks = nil
	s.noAccessIPCached = time.Time{}
	s.ipBlockMu.Unlock()
	keys := ipBlockCacheRedisKeys(s.cfg)
	if len(keys) == 0 {
		return
	}
	cacheCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	_, _ = s.cacheRedisCommand(cacheCtx, append([]string{"DEL"}, keys...)...)
}
