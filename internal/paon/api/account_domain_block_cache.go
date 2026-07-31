package api

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
)

func accountDomainBlockAggregateCacheKey(accountID int64) string {
	return "exclude_domains_for:" + strconv.FormatInt(accountID, 10)
}

func accountDomainBlockEntryCacheKey(accountID int64, domain string) string {
	return "exclude_domains/" + strconv.FormatInt(accountID, 10) + "/" + strings.ToLower(strings.TrimSpace(domain))
}

func accountDomainBlockCacheRedisKeys(cfg config.Config, accountID int64, domains []string) []string {
	candidates := railsCacheRedisKeyCandidates(cfg, accountDomainBlockAggregateCacheKey(accountID))
	seen := map[string]struct{}{}
	out := make([]string, 0, len(candidates)+len(domains)*4)
	for _, key := range candidates {
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, key)
	}
	for _, domain := range domains {
		domain = strings.ToLower(strings.TrimSpace(domain))
		if domain == "" {
			continue
		}
		for _, key := range railsCacheRedisKeyCandidates(cfg, accountDomainBlockEntryCacheKey(accountID, domain)) {
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, key)
		}
	}
	return out
}

func (s *Server) invalidateAccountDomainBlockCaches(ctx context.Context, accountID int64, domains []string) {
	if s == nil || accountID == 0 {
		return
	}
	keys := accountDomainBlockCacheRedisKeys(s.cfg, accountID, domains)
	if len(keys) == 0 {
		return
	}
	cacheCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	_, _ = s.cacheRedisCommand(cacheCtx, append([]string{"DEL"}, keys...)...)
}
