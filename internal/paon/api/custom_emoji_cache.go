package api

import (
	"context"
	"strings"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func customEmojiEntityCacheKey(emoji models.CustomEmoji) string {
	parts := []string{strings.ToLower(emoji.Shortcode)}
	if emoji.Domain.Valid && strings.TrimSpace(emoji.Domain.String) != "" {
		parts = append(parts, strings.ToLower(emoji.Domain.String))
	}
	return "emoji:" + strings.Join(parts, ":")
}

func railsCacheRedisKeyCandidates(cfg config.Config, key string) []string {
	namespace := strings.TrimSuffix(cfg.RedisNamespace, ":")
	candidates := []string{key, "cache:" + key}
	if namespace != "" {
		candidates = append(candidates, namespace+":"+key, namespace+"_cache:"+key)
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if _, ok := seen[candidate]; ok {
			continue
		}
		seen[candidate] = struct{}{}
		out = append(out, candidate)
	}
	return out
}

func railsCacheRedisWriteKey(cfg config.Config, key string) string {
	namespace := strings.TrimSuffix(cfg.RedisNamespace, ":")
	if namespace != "" {
		return namespace + "_cache:" + key
	}
	return "cache:" + key
}

func (s *Server) invalidateCustomEmojiEntityCaches(ctx context.Context, emojis []models.CustomEmoji) {
	if len(emojis) == 0 {
		return
	}
	keys := make([]string, 0, len(emojis)*4)
	seen := map[string]struct{}{}
	for _, emoji := range emojis {
		for _, key := range railsCacheRedisKeyCandidates(s.cfg, customEmojiEntityCacheKey(emoji)) {
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return
	}
	cacheCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	_, _ = s.cacheRedisCommand(cacheCtx, append([]string{"DEL"}, keys...)...)
}
