package api

import (
	"context"
	"os"
	"strings"
	"time"
)

const (
	ipCleanupWorkerInterval        = 24 * time.Hour
	defaultIPRetentionSeconds      = 31556952
	defaultSessionRetentionSeconds = 31556952
)

func (s *Server) runIPCleanupWorker(ctx context.Context) {
	ticker := time.NewTicker(ipCleanupWorkerInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			s.runSchedulerWithRedisLock(ctx, "ip_cleanup_scheduler", 24*time.Hour, func() {
				s.cleanupIPData(ctx, now.UTC())
			})
		}
	}
}

func (s *Server) cleanupIPData(ctx context.Context, now time.Time) int64 {
	if s == nil || s.db == nil {
		return 0
	}
	cleaned := int64(0)
	cleaned += s.cleanupExpiredSessionActivations(ctx, now.Add(-sessionRetentionPeriod()))
	ipCutoff := now.Add(-ipRetentionPeriod())
	cleaned += s.nullOldSessionActivationIPs(ctx, ipCutoff)
	cleaned += s.nullOldUserSignUpIPs(ctx, ipCutoff)
	cleaned += s.deleteOldLoginActivities(ctx, ipCutoff)
	cleaned += s.nullOldOAuthAccessTokenIPs(ctx, ipCutoff)
	cleaned += s.deleteExpiredIPBlocks(ctx, now)
	return cleaned
}

func (s *Server) cleanupExpiredSessionActivations(ctx context.Context, cutoff time.Time) int64 {
	cleaned := int64(0)
	tx := s.db.WithContext(ctx).Exec(`
DELETE FROM web_push_subscriptions
WHERE id IN (
  SELECT web_push_subscription_id
  FROM session_activations
  WHERE updated_at < ? AND web_push_subscription_id IS NOT NULL
)`, cutoff)
	if tx.Error != nil {
		return cleaned
	}
	cleaned += tx.RowsAffected
	tx = s.db.WithContext(ctx).Exec(`
DELETE FROM oauth_access_tokens
WHERE id IN (
  SELECT access_token_id
  FROM session_activations
  WHERE updated_at < ? AND access_token_id IS NOT NULL
)`, cutoff)
	if tx.Error != nil {
		return cleaned
	}
	cleaned += tx.RowsAffected
	tx = s.db.WithContext(ctx).Exec("DELETE FROM session_activations WHERE updated_at < ?", cutoff)
	if tx.Error != nil {
		return cleaned
	}
	return cleaned + tx.RowsAffected
}

func (s *Server) nullOldSessionActivationIPs(ctx context.Context, cutoff time.Time) int64 {
	tx := s.db.WithContext(ctx).Exec("UPDATE session_activations SET ip = NULL WHERE updated_at < ?", cutoff)
	if tx.Error != nil {
		return 0
	}
	return tx.RowsAffected
}

func (s *Server) nullOldUserSignUpIPs(ctx context.Context, cutoff time.Time) int64 {
	tx := s.db.WithContext(ctx).Exec("UPDATE users SET sign_up_ip = NULL WHERE current_sign_in_at < ?", cutoff)
	if tx.Error != nil {
		return 0
	}
	return tx.RowsAffected
}

func (s *Server) deleteOldLoginActivities(ctx context.Context, cutoff time.Time) int64 {
	tx := s.db.WithContext(ctx).Exec("DELETE FROM login_activities WHERE created_at < ?", cutoff)
	if tx.Error != nil {
		return 0
	}
	return tx.RowsAffected
}

func (s *Server) nullOldOAuthAccessTokenIPs(ctx context.Context, cutoff time.Time) int64 {
	tx := s.db.WithContext(ctx).Exec("UPDATE oauth_access_tokens SET last_used_ip = NULL WHERE last_used_at < ?", cutoff)
	if tx.Error != nil {
		return 0
	}
	return tx.RowsAffected
}

func (s *Server) deleteExpiredIPBlocks(ctx context.Context, now time.Time) int64 {
	tx := s.db.WithContext(ctx).Exec("DELETE FROM ip_blocks WHERE expires_at IS NOT NULL AND expires_at < ?", now)
	if tx.Error != nil {
		return 0
	}
	if tx.RowsAffected > 0 {
		s.invalidateIPBlockCache(ctx)
	}
	return tx.RowsAffected
}

func ipRetentionPeriod() time.Duration {
	return retentionPeriodFromEnv("IP_RETENTION_PERIOD", defaultIPRetentionSeconds)
}

func sessionRetentionPeriod() time.Duration {
	return retentionPeriodFromEnv("SESSION_RETENTION_PERIOD", defaultSessionRetentionSeconds)
}

func retentionPeriodFromEnv(name string, defaultSeconds int64) time.Duration {
	raw, ok := os.LookupEnv(name)
	if !ok {
		return time.Duration(defaultSeconds) * time.Second
	}
	seconds := railsInt64FromString(raw)
	return time.Duration(seconds) * time.Second
}

func railsInt64FromString(raw string) int64 {
	value := strings.TrimSpace(raw)
	if value == "" {
		return 0
	}
	sign := int64(1)
	if value[0] == '+' || value[0] == '-' {
		if value[0] == '-' {
			sign = -1
		}
		value = value[1:]
	}
	i := 0
	for i < len(value) && value[i] >= '0' && value[i] <= '9' {
		i++
	}
	if i == 0 {
		return 0
	}
	var parsed int64
	for _, ch := range value[:i] {
		parsed = parsed*10 + int64(ch-'0')
	}
	return sign * parsed
}
