package api

import (
	"context"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
)

const unavailableDomainsRailsCacheKey = "unavailable_domains"

func unavailableDomainsCacheRedisKeys(cfg config.Config) []string {
	return railsCacheRedisKeyCandidates(cfg, unavailableDomainsRailsCacheKey)
}

func (s *Server) invalidateUnavailableDomainsCache(ctx context.Context) {
	if s == nil {
		return
	}
	keys := unavailableDomainsCacheRedisKeys(s.cfg)
	if len(keys) == 0 {
		return
	}
	cacheCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	_, _ = s.cacheRedisCommand(cacheCtx, append([]string{"DEL"}, keys...)...)
}
