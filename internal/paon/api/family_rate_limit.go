package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

type railsRateLimitFamily string

const (
	railsRateLimitFamilyFollows  railsRateLimitFamily = "follows"
	railsRateLimitFamilyReports  railsRateLimitFamily = "reports"
	railsRateLimitFamilyStatuses railsRateLimitFamily = "statuses"
)

func railsFamilyRateLimitConfig(family railsRateLimitFamily) (int, time.Duration, bool) {
	switch family {
	case railsRateLimitFamilyFollows:
		return railsFollowsFamilyLimit, railsFollowsFamilyPeriod, true
	case railsRateLimitFamilyReports:
		return railsReportsFamilyLimit, railsReportsFamilyPeriod, true
	case railsRateLimitFamilyStatuses:
		return railsStatusFamilyLimit, railsStatusFamilyPeriod, true
	default:
		return 0, 0, false
	}
}

func railsFamilyRateLimitKey(prefix string, accountID int64, family railsRateLimitFamily, now time.Time) string {
	_, period, ok := railsFamilyRateLimitConfig(family)
	if !ok {
		return ""
	}
	periodSeconds := int64(period / time.Second)
	bucket := int64(0)
	if periodSeconds > 0 {
		bucket = now.Unix() / periodSeconds
	}
	return prefix + "rate_limit:" + strconv.FormatInt(accountID, 10) + ":" + string(family) + ":" + strconv.FormatInt(bucket, 10)
}

func railsFamilyRateLimitTTLSeconds(period time.Duration, now time.Time) int64 {
	periodSeconds := int64(period / time.Second)
	if periodSeconds <= 0 {
		return 0
	}
	return periodSeconds - (now.Unix() % periodSeconds) + 1
}

func (s *Server) consumeRailsFamilyRateLimit(c *echo.Context, account models.Account, family railsRateLimitFamily, now time.Time) (bool, error) {
	remaining, recorded, err := s.recordRailsFamilyRateLimit(c.Request().Context(), account, family, now)
	if limit, period, ok := railsFamilyRateLimitConfig(family); ok {
		if err != nil {
			var apiErr apiHTTPError
			if errors.As(err, &apiErr) && apiErr.status == http.StatusTooManyRequests {
				remaining = 0
			}
		}
		setRateLimitFamilyHeaders(c, limit, period, remaining)
	}
	return recorded, err
}

func (s *Server) recordRailsFamilyRateLimit(ctx context.Context, account models.Account, family railsRateLimitFamily, now time.Time) (int, bool, error) {
	limit, period, ok := railsFamilyRateLimitConfig(family)
	if !ok {
		return 0, false, nil
	}
	if !account.Local() {
		return limit, false, nil
	}
	key := railsFamilyRateLimitKey(redisConfig(s.cfg).prefix, account.ID, family, now)
	if key == "" {
		return limit, false, nil
	}
	redisCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	count, err := s.redisCommand(redisCtx, "GET", key)
	if err != nil {
		return 0, false, err
	}
	if count == nil {
		if _, err := s.redisCommand(redisCtx, "SET", key, "0"); err != nil {
			return 0, false, err
		}
		if _, err := s.redisCommand(redisCtx, "EXPIRE", key, strconv.FormatInt(railsFamilyRateLimitTTLSeconds(period, now), 10)); err != nil {
			return 0, false, err
		}
	} else if redisInt(count) >= int64(limit) {
		return 0, false, apiHTTPError{status: http.StatusTooManyRequests, message: "Too many requests"}
	}
	value, err := s.redisCommand(redisCtx, "INCR", key)
	if err != nil {
		return 0, false, err
	}
	remaining := limit - int(redisInt(value))
	if remaining < 0 {
		remaining = 0
	}
	return remaining, true, nil
}

func (s *Server) rollbackRailsFamilyRateLimit(ctx context.Context, account models.Account, family railsRateLimitFamily, now time.Time) {
	if !account.Local() {
		return
	}
	key := railsFamilyRateLimitKey(redisConfig(s.cfg).prefix, account.ID, family, now)
	if key == "" {
		return
	}
	redisCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	_, _ = s.redisCommand(redisCtx, "DECR", key)
}
