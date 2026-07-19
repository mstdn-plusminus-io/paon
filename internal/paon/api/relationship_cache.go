package api

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func relationshipCacheKey(accountID int64, targetID int64) string {
	return "relationship/" + strconv.FormatInt(accountID, 10) + "/" + strconv.FormatInt(targetID, 10)
}

func excludeAccountIDsCacheKey(accountID int64) string {
	return "exclude_account_ids_for:" + strconv.FormatInt(accountID, 10)
}

func followersHashCacheKey(targetID int64, synchronizationURIPrefix string) string {
	return "followers_hash:" + strconv.FormatInt(targetID, 10) + ":" + synchronizationURIPrefix
}

func accountSynchronizationURIPrefix(account models.Account) string {
	if account.Local() {
		return "local"
	}
	parsed, err := url.Parse(strings.TrimSpace(account.URI))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	return parsed.Scheme + "://" + parsed.Host + "/"
}

func relationshipCacheRedisKeys(cfg config.Config, accountID int64, targetID int64, excludeAccountIDs []int64) []string {
	raw := []string{
		relationshipCacheKey(accountID, targetID),
		relationshipCacheKey(targetID, accountID),
	}
	for _, id := range excludeAccountIDs {
		if id != 0 {
			raw = append(raw, excludeAccountIDsCacheKey(id))
		}
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(raw)*4)
	for _, key := range raw {
		for _, candidate := range railsCacheRedisKeyCandidates(cfg, key) {
			if _, ok := seen[candidate]; ok {
				continue
			}
			seen[candidate] = struct{}{}
			out = append(out, candidate)
		}
	}
	return out
}

func followRelationshipCacheRedisKeys(cfg config.Config, source models.Account, targetID int64) []string {
	raw := []string{
		relationshipCacheKey(source.ID, targetID),
		relationshipCacheKey(targetID, source.ID),
	}
	if prefix := accountSynchronizationURIPrefix(source); prefix != "" {
		raw = append(raw, followersHashCacheKey(targetID, prefix))
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(raw)*4)
	for _, key := range raw {
		for _, candidate := range railsCacheRedisKeyCandidates(cfg, key) {
			if _, ok := seen[candidate]; ok {
				continue
			}
			seen[candidate] = struct{}{}
			out = append(out, candidate)
		}
	}
	return out
}

func (s *Server) invalidateRelationshipCaches(ctx context.Context, accountID int64, targetID int64, excludeAccountIDs ...int64) {
	if s == nil || accountID == 0 || targetID == 0 {
		return
	}
	keys := relationshipCacheRedisKeys(s.cfg, accountID, targetID, excludeAccountIDs)
	if len(keys) == 0 {
		return
	}
	cacheCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	_, _ = s.cacheRedisCommand(cacheCtx, append([]string{"DEL"}, keys...)...)
}

func (s *Server) invalidateFollowRelationshipCaches(ctx context.Context, source models.Account, targetID int64) {
	if s == nil || source.ID == 0 || targetID == 0 {
		return
	}
	keys := followRelationshipCacheRedisKeys(s.cfg, source, targetID)
	if len(keys) == 0 {
		return
	}
	cacheCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	_, _ = s.cacheRedisCommand(cacheCtx, append([]string{"DEL"}, keys...)...)
}

func (s *Server) invalidateBlockRelationshipCaches(ctx context.Context, accountID int64, targetID int64) {
	s.invalidateRelationshipCaches(ctx, accountID, targetID, accountID, targetID)
}

func (s *Server) invalidateMuteRelationshipCaches(ctx context.Context, accountID int64, targetID int64) {
	s.invalidateRelationshipCaches(ctx, accountID, targetID, accountID)
}
