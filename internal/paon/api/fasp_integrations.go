package api

import (
	"context"
	"strings"

	"github.com/labstack/echo/v5"
)

func (s *Server) beginFASPAsyncRefresh(ctx context.Context, key string, countResults bool) (string, bool) {
	if !s.faspEnabled() || strings.TrimSpace(key) == "" {
		return "", false
	}
	if value, err := s.redisCommand(ctx, "HGET", key, "status"); err == nil {
		if status, ok := value.(string); ok && status == "running" {
			return "", false
		}
	}
	id, err := s.createAsyncRefresh(ctx, key, countResults)
	return id, err == nil && id != ""
}

func (s *Server) scheduleFASPAccountSearch(c *echo.Context, query string) {
	if c == nil || !s.faspEnabled() || strings.TrimSpace(query) == "" || strings.TrimSpace(c.Request().Header.Get("Mastodon-Async-Refresh-Id")) != "" {
		return
	}
	key := faspAccountSearchRefreshKey(query)
	id, started := s.beginFASPAsyncRefresh(c.Request().Context(), key, false)
	if !started {
		return
	}
	if err := s.enqueueFASPAccountSearch(c.Request().Context(), query); err != nil {
		_ = s.finishAsyncRefresh(c.Request().Context(), key)
		return
	}
	s.setAsyncRefreshHeader(c, id, 3)
}

func (s *Server) scheduleFASPFollowRecommendations(c *echo.Context, accountID int64) {
	if c == nil || !s.faspEnabled() || accountID <= 0 || strings.TrimSpace(c.Request().Header.Get("Mastodon-Async-Refresh-Id")) != "" {
		return
	}
	key := faspFollowRecommendationRefreshKey(accountID)
	id, started := s.beginFASPAsyncRefresh(c.Request().Context(), key, false)
	if !started {
		return
	}
	if err := s.enqueueFASPFollowRecommendation(c.Request().Context(), accountID); err != nil {
		_ = s.finishAsyncRefresh(c.Request().Context(), key)
		return
	}
	s.setAsyncRefreshHeader(c, id, 3)
}
