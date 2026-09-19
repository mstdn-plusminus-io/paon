package api

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"time"
)

const (
	meiliSearchStoplightFailureThreshold = 10
	meiliSearchStoplightCooldown         = 5 * time.Minute
)

var errMeiliSearchStoplightOpen = errors.New("meilisearch stoplight is open")

func meiliSearchStoplightKey(prefix string) string {
	return prefix + "search:meilisearch:stoplight"
}

func (s *Server) meiliSearchStoplightOpen(ctx context.Context) bool {
	if s == nil {
		return false
	}
	value, err := s.redisCommand(ctx, "GET", meiliSearchStoplightKey(redisConfig(s.cfg).prefix))
	return err == nil && redisInt(value) >= meiliSearchStoplightFailureThreshold
}

func (s *Server) trackMeiliSearchStoplightFailure(ctx context.Context) {
	if s == nil {
		return
	}
	key := meiliSearchStoplightKey(redisConfig(s.cfg).prefix)
	if _, err := s.redisCommand(ctx, "INCR", key); err != nil {
		return
	}
	_, _ = s.redisCommand(ctx, "EXPIRE", key, strconv.FormatInt(int64(meiliSearchStoplightCooldown/time.Second), 10))
}

func (s *Server) trackMeiliSearchStoplightSuccess(ctx context.Context) {
	if s == nil {
		return
	}
	_, _ = s.redisCommand(ctx, "DEL", meiliSearchStoplightKey(redisConfig(s.cfg).prefix))
}

func meiliSearchStoplightTracksError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	var urlError *url.Error
	return errors.As(err, &urlError) || errors.Is(err, context.DeadlineExceeded)
}
