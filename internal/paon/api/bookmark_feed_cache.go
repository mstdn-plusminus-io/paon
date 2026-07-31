package api

import (
	"context"
	"strconv"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

const (
	bookmarkFeedSmallTTL     = 30 * 24 * time.Hour
	bookmarkFeedMediumTTL    = 7 * 24 * time.Hour
	bookmarkFeedLargeTTL     = 24 * time.Hour
	bookmarkFeedVeryLargeTTL = 6 * time.Hour
	bookmarkFeedRedisTimeout = 250 * time.Millisecond
	bookmarkFeedMediumCutoff = 100
	bookmarkFeedLargeCutoff  = 1000
	bookmarkFeedHugeCutoff   = 10000
)

func bookmarkFeedRedisKey(namespace string, accountID int64) string {
	return namespace + "feed:bookmark:" + strconv.FormatInt(accountID, 10)
}

func bookmarkFeedTTL(count int64) time.Duration {
	switch {
	case count <= bookmarkFeedMediumCutoff:
		return bookmarkFeedSmallTTL
	case count <= bookmarkFeedLargeCutoff:
		return bookmarkFeedMediumTTL
	case count <= bookmarkFeedHugeCutoff:
		return bookmarkFeedLargeTTL
	default:
		return bookmarkFeedVeryLargeTTL
	}
}

func (s *Server) addBookmarkToFeedCache(ctx context.Context, bookmark models.Bookmark) {
	if bookmark.ID == 0 || bookmark.AccountID == 0 || bookmark.StatusID == 0 {
		return
	}
	key := bookmarkFeedRedisKey(s.cfg.RedisNamespace, bookmark.AccountID)
	cacheCtx, cancel := context.WithTimeout(ctx, bookmarkFeedRedisTimeout)
	defer cancel()
	_, _ = s.redisCommand(cacheCtx, "ZADD", key, strconv.FormatInt(bookmark.ID, 10), strconv.FormatInt(bookmark.StatusID, 10))
	value, err := s.redisCommand(cacheCtx, "ZCARD", key)
	if err != nil {
		return
	}
	count := int64(0)
	switch typed := value.(type) {
	case int64:
		count = typed
	case string:
		count, _ = strconv.ParseInt(typed, 10, 64)
	}
	_, _ = s.redisCommand(cacheCtx, "EXPIRE", key, strconv.FormatInt(int64(bookmarkFeedTTL(count)/time.Second), 10))
}

func (s *Server) removeBookmarkFromFeedCache(ctx context.Context, accountID int64, statusID int64) {
	if accountID == 0 || statusID == 0 {
		return
	}
	cacheCtx, cancel := context.WithTimeout(ctx, bookmarkFeedRedisTimeout)
	defer cancel()
	_, _ = s.redisCommand(cacheCtx, "ZREM", bookmarkFeedRedisKey(s.cfg.RedisNamespace, accountID), strconv.FormatInt(statusID, 10))
}

func (s *Server) runBookmarkDestroyedSideEffects(ctx context.Context, bookmark models.Bookmark) {
	if bookmark.AccountID == 0 || bookmark.StatusID == 0 {
		return
	}
	s.removeBookmarkFromFeedCache(ctx, bookmark.AccountID, bookmark.StatusID)
	s.invalidateDestroyedBookmarkCleanupInfo(ctx, bookmark)
}

func (s *Server) invalidateDestroyedBookmarkCleanupInfo(ctx context.Context, bookmark models.Bookmark) {
	if s == nil || s.db == nil || bookmark.AccountID == 0 || bookmark.StatusID == 0 {
		return
	}
	statusAccountID := bookmark.Status.AccountID
	if statusAccountID == 0 {
		var status models.Status
		if err := s.db.WithContext(ctx).Select("account_id").Where("id = ?", bookmark.StatusID).First(&status).Error; err != nil {
			return
		}
		statusAccountID = status.AccountID
	}
	if statusAccountID != bookmark.AccountID {
		return
	}
	var account models.Account
	if err := s.db.WithContext(ctx).Select("id, domain").Where("id = ?", bookmark.AccountID).First(&account).Error; err != nil {
		return
	}
	if !account.Local() {
		return
	}
	s.invalidateStatusesCleanupLastInspected(ctx, bookmark.AccountID, bookmark.StatusID, "unbookmark")
}
