package api

import (
	"context"
	"strings"
	"time"
)

const (
	instanceRefreshWorkerInterval = time.Hour
	instanceRefreshMeiliBatchSize = 1000
)

func (s *Server) runInstanceRefreshWorker(ctx context.Context) {
	s.runSchedulerWithRedisLock(ctx, "instance_refresh_scheduler", 24*time.Hour, func() {
		s.refreshInstances(ctx)
	})
	ticker := time.NewTicker(instanceRefreshWorkerInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runSchedulerWithRedisLock(ctx, "instance_refresh_scheduler", 24*time.Hour, func() {
				s.refreshInstances(ctx)
			})
		}
	}
}

func (s *Server) refreshInstances(ctx context.Context) bool {
	if s == nil || s.db == nil {
		return false
	}
	if err := s.refreshInstancesMaterializedView(); err != nil {
		return false
	}
	if s.cfg.MeiliEnabled && strings.TrimSpace(s.cfg.MeiliHost) != "" {
		_, _ = s.deployMeiliInstances(ctx, instanceRefreshMeiliBatchSize, "", func(meiliDeployProgress) error {
			return nil
		})
	}
	return true
}
