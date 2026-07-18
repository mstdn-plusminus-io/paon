package api

import (
	"context"
	"strconv"
	"strings"
	"time"
)

const schedulerRedisLockReleaseScript = `if redis.call("GET", KEYS[1]) == ARGV[1] then return redis.call("DEL", KEYS[1]) else return 0 end`
const schedulerRedisLockRenewScript = `if redis.call("GET", KEYS[1]) == ARGV[1] then return redis.call("EXPIRE", KEYS[1], ARGV[2]) else return 0 end`

func (s *Server) runSchedulerWithRedisLock(ctx context.Context, name string, ttl time.Duration, fn func()) bool {
	if fn == nil {
		return false
	}
	if s == nil || ttl <= 0 || strings.TrimSpace(name) == "" {
		fn()
		return true
	}
	key := redisConfig(s.cfg).prefix + "scheduler:" + name + ":lock"
	markerKey := redisConfig(s.cfg).prefix + "scheduler:" + name + ":cadence"
	token := randomHex(16)
	value, err := s.redisCommand(ctx, "SET", key, token, "NX", "EX", formatRedisSeconds(ttl))
	if err != nil {
		return false
	}
	if !redisOK(value) {
		return false
	}
	defer s.releaseSchedulerRedisLock(ctx, key, token)
	marker, err := s.redisCommand(ctx, "SET", markerKey, token, "NX", "EX", formatRedisSeconds(schedulerCadence(name, ttl)))
	if err != nil || !redisOK(marker) {
		return false
	}
	stopRenewal := s.renewSchedulerRedisLock(ctx, key, token, ttl)
	defer stopRenewal()
	fn()
	return true
}

func schedulerCadence(name string, fallback time.Duration) time.Duration {
	switch name {
	case "accounts_statuses_cleanup_scheduler", "suspended_user_cleanup_scheduler":
		return time.Minute
	case "indexing_scheduler":
		return indexingWorkerInterval
	case "scheduled_statuses_scheduler":
		return scheduledStatusPublishWorkerInterval
	case "trends_refresh_scheduler":
		return trendsRefreshWorkerInterval
	case "trends_review_scheduler":
		return trendsReviewWorkerInterval
	case "software_update_check_scheduler":
		return softwareUpdateCheckWorkerInterval
	case "auto_close_registrations_scheduler":
		return autoCloseRegistrationsWorkerInterval
	case "instance_refresh_scheduler":
		return instanceRefreshWorkerInterval
	case "admin_metrics_prewarm_scheduler":
		return adminMetricsPrewarmWorkerInterval
	case "featured_tag_refresh_scheduler":
		return featuredTagRefreshWorkerInterval
	case "profile_verification_scheduler":
		return profileVerificationWorkerInterval
	case "meili_index_definition_scheduler":
		return 30 * time.Minute
	default:
		return fallback
	}
}

func (s *Server) renewSchedulerRedisLock(ctx context.Context, key string, token string, ttl time.Duration) func() {
	if s == nil || key == "" || token == "" || ttl <= 0 {
		return func() {}
	}
	interval := ttl / 3
	if interval < time.Second {
		interval = time.Second
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-stop:
				return
			case <-ticker.C:
				result, err := s.redisCommand(ctx, "EVAL", schedulerRedisLockRenewScript, "1", key, token, formatRedisSeconds(ttl))
				if err != nil || redisInteger(result) != 1 {
					return
				}
			}
		}
	}()
	return func() {
		close(stop)
		<-done
	}
}

func (s *Server) releaseSchedulerRedisLock(ctx context.Context, key string, token string) {
	if s == nil || key == "" || token == "" {
		return
	}
	_, _ = s.redisCommand(ctx, "EVAL", schedulerRedisLockReleaseScript, "1", key, token)
}

func redisOK(value any) bool {
	raw, ok := value.(string)
	return ok && strings.EqualFold(raw, "OK")
}

func formatRedisSeconds(ttl time.Duration) string {
	seconds := int64(ttl / time.Second)
	if seconds <= 0 {
		seconds = 1
	}
	return strconv.FormatInt(seconds, 10)
}
