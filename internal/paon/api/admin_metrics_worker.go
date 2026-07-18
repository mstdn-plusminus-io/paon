package api

import (
	"context"
	"time"
)

const adminMetricsPrewarmWorkerInterval = adminMetricsCacheTTL

var adminDashboardPrewarmMeasures = []string{
	"new_users",
	"active_users",
	"interactions",
	"opened_reports",
	"resolved_reports",
}

type adminDashboardPrewarmDimension struct {
	Key   string
	Limit int
}

var adminDashboardPrewarmDimensions = []adminDashboardPrewarmDimension{
	{Key: "sources", Limit: 8},
	{Key: "languages", Limit: 8},
	{Key: "servers", Limit: 8},
	{Key: "software_versions", Limit: 4},
	{Key: "space_usage", Limit: 3},
}

func (s *Server) runAdminMetricsPrewarmWorker(ctx context.Context) {
	s.runSchedulerWithRedisLock(ctx, "admin_metrics_prewarm_scheduler", adminMetricsPrewarmWorkerInterval, func() {
		s.prewarmAdminDashboardMetrics(time.Now().UTC())
	})
	ticker := time.NewTicker(adminMetricsPrewarmWorkerInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			s.runSchedulerWithRedisLock(ctx, "admin_metrics_prewarm_scheduler", adminMetricsPrewarmWorkerInterval, func() {
				s.prewarmAdminDashboardMetrics(now.UTC())
			})
		}
	}
}

func (s *Server) prewarmAdminDashboardMetrics(now time.Time) {
	if s == nil {
		return
	}
	startValue := now.AddDate(0, 0, -29).Format("2006-01-02")
	endValue := now.Format("2006-01-02")
	start, end := adminMetricsRange(startValue, endValue)
	for _, key := range adminDashboardPrewarmMeasures {
		s.cachedAdminMeasure(key, start, end, adminMetricKeyParam{})
	}
	for _, dimension := range adminDashboardPrewarmDimensions {
		s.cachedAdminDimension(dimension.Key, dimension.Limit, adminMetricKeyParam{}, start, end)
	}
	retentionStart, _ := adminMetricsRange(now.AddDate(0, -6, 0).Format("2006-01-02"), endValue)
	s.cachedAdminRetentionCohorts(retentionStart, endValue, "month")
}
