package api

import (
	"context"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

const (
	importVacuumWorkerInterval = 24 * time.Hour
	importVacuumBatchSize      = 1000
	unconfirmedImportTTL       = 10 * time.Minute
	oldImportTTL               = 7 * 24 * time.Hour
)

func (s *Server) runImportVacuumWorker(ctx context.Context) {
	ticker := time.NewTicker(importVacuumWorkerInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			s.runSchedulerWithRedisLock(ctx, "vacuum_scheduler:imports_vacuum", 24*time.Hour, func() {
				s.vacuumExpiredImports(ctx, now.UTC())
			})
		}
	}
}

func (s *Server) vacuumExpiredImports(ctx context.Context, now time.Time) int {
	if s == nil || s.db == nil {
		return 0
	}
	deleted := s.vacuumBulkImports(ctx, "state = ? AND created_at <= ?", []any{bulkImportStateUnconfirmed, now.Add(-unconfirmedImportTTL)})
	deleted += s.vacuumBulkImports(ctx, "created_at <= ?", []any{now.Add(-oldImportTTL)})
	return deleted
}

func (s *Server) vacuumBulkImports(ctx context.Context, condition string, args []any) int {
	deleted := 0
	for {
		var ids []int64
		query := s.db.WithContext(ctx).
			Model(&models.BulkImport{}).
			Where(condition, args...).
			Order("id ASC").
			Limit(importVacuumBatchSize)
		if err := query.Pluck("id", &ids).Error; err != nil {
			return deleted
		}
		if len(ids) == 0 {
			return deleted
		}
		if err := s.db.WithContext(ctx).Delete(&models.BulkImport{}, ids).Error; err != nil {
			return deleted
		}
		deleted += len(ids)
	}
}
