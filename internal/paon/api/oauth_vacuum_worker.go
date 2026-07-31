package api

import (
	"context"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

const (
	oauthVacuumWorkerInterval = 24 * time.Hour
	oauthVacuumBatchSize      = 1000
)

const (
	expiredOAuthAccessTokensSQL = `(expires_in IS NOT NULL AND created_at + make_interval(secs => expires_in) < ?) OR (revoked_at IS NOT NULL AND revoked_at < ?)`
	expiredOAuthAccessGrantsSQL = `(expires_in IS NOT NULL AND created_at + make_interval(secs => expires_in) < ?) OR (revoked_at IS NOT NULL AND revoked_at < ?)`
)

func (s *Server) runOAuthVacuumWorker(ctx context.Context) {
	ticker := time.NewTicker(oauthVacuumWorkerInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			s.runSchedulerWithRedisLock(ctx, "vacuum_scheduler:access_tokens_vacuum", 24*time.Hour, func() {
				s.vacuumExpiredOAuth(ctx, now.UTC())
			})
		}
	}
}

func (s *Server) vacuumExpiredOAuth(ctx context.Context, now time.Time) int {
	if s == nil || s.db == nil {
		return 0
	}
	deleted := s.vacuumExpiredOAuthAccessTokens(ctx, now)
	deleted += s.vacuumExpiredOAuthAccessGrants(ctx, now)
	return deleted
}

func (s *Server) vacuumExpiredOAuthAccessTokens(ctx context.Context, now time.Time) int {
	deleted := 0
	for {
		var ids []int64
		if err := s.db.WithContext(ctx).
			Model(&models.OAuthAccessToken{}).
			Where(expiredOAuthAccessTokensSQL, now, now).
			Order("id ASC").
			Limit(oauthVacuumBatchSize).
			Pluck("id", &ids).Error; err != nil {
			return deleted
		}
		if len(ids) == 0 {
			return deleted
		}
		if err := s.db.WithContext(ctx).Delete(&models.OAuthAccessToken{}, ids).Error; err != nil {
			return deleted
		}
		deleted += len(ids)
	}
}

func (s *Server) vacuumExpiredOAuthAccessGrants(ctx context.Context, now time.Time) int {
	deleted := 0
	for {
		var ids []int64
		if err := s.db.WithContext(ctx).
			Model(&models.OAuthAccessGrant{}).
			Where(expiredOAuthAccessGrantsSQL, now, now).
			Order("id ASC").
			Limit(oauthVacuumBatchSize).
			Pluck("id", &ids).Error; err != nil {
			return deleted
		}
		if len(ids) == 0 {
			return deleted
		}
		if err := s.db.WithContext(ctx).Delete(&models.OAuthAccessGrant{}, ids).Error; err != nil {
			return deleted
		}
		deleted += len(ids)
	}
}
