package api

import (
	"context"
	"strconv"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func statusCacheKey(statusID int64) string {
	return "v3:statuses/" + strconv.FormatInt(statusID, 10)
}

func statusCacheKeys(statusID int64) []string {
	id := strconv.FormatInt(statusID, 10)
	return []string{statusCacheKey(statusID), "statuses/" + id}
}

func (s *Server) invalidateStatusCache(ctx context.Context, statusID int64) {
	if statusID == 0 {
		return
	}
	keys := make([]string, 0, 8)
	seen := map[string]struct{}{}
	for _, key := range statusCacheKeys(statusID) {
		for _, candidate := range railsCacheRedisKeyCandidates(s.cfg, key) {
			if _, ok := seen[candidate]; ok {
				continue
			}
			seen[candidate] = struct{}{}
			keys = append(keys, candidate)
		}
	}
	if len(keys) == 0 {
		return
	}
	cacheCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	_, _ = s.cacheRedisCommand(cacheCtx, append([]string{"DEL"}, keys...)...)
}

func (s *Server) invalidateMediaAttachmentParentStatusCache(ctx context.Context, attachment models.MediaAttachment) {
	if !attachment.StatusID.Valid {
		return
	}
	s.invalidateStatusCache(ctx, attachment.StatusID.Int64)
}

func (s *Server) invalidateMediaAttachmentParentStatusCaches(ctx context.Context, attachments []models.MediaAttachment) {
	seen := make(map[int64]struct{}, len(attachments))
	for _, attachment := range attachments {
		if !attachment.StatusID.Valid || attachment.StatusID.Int64 == 0 {
			continue
		}
		if _, ok := seen[attachment.StatusID.Int64]; ok {
			continue
		}
		seen[attachment.StatusID.Int64] = struct{}{}
		s.invalidateStatusCache(ctx, attachment.StatusID.Int64)
	}
}
