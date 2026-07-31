package api

import (
	"context"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

const pgheroSpaceStatsWorkerInterval = 24 * time.Hour

func (s *Server) runPgHeroSpaceStatsWorker(ctx context.Context) {
	ticker := time.NewTicker(pgheroSpaceStatsWorkerInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runSchedulerWithRedisLock(ctx, "pghero_scheduler", 24*time.Hour, func() {
				s.capturePgHeroSpaceStats(ctx)
			})
		}
	}
}

func (s *Server) capturePgHeroSpaceStats(ctx context.Context) int64 {
	if s == nil || s.db == nil || s.db.Dialector == nil || s.db.Dialector.Name() != "postgres" {
		return 0
	}
	total := int64(0)
	if statsDB := s.pgHeroStatsDatabase(); statsDB != s.db {
		rows, err := s.pgHeroSpaceStatRows(ctx, s.db)
		if err != nil || len(rows) == 0 {
			return s.capturePgHeroOtherSpaceStats(ctx, statsDB, total)
		}
		if tx := statsDB.WithContext(ctx).CreateInBatches(rows, 100); tx.Error != nil {
			return s.capturePgHeroOtherSpaceStats(ctx, statsDB, total)
		}
		total += int64(len(rows))
		return s.capturePgHeroOtherSpaceStats(ctx, statsDB, total)
	}
	tx := s.db.WithContext(ctx).Exec(pgheroSpaceStatsInsertSQL)
	if tx.Error == nil {
		total += tx.RowsAffected
	}
	return s.capturePgHeroOtherSpaceStats(ctx, s.db, total)
}

func (s *Server) capturePgHeroOtherSpaceStats(ctx context.Context, statsDB *gorm.DB, total int64) int64 {
	if s == nil || s.pgHeroOtherDB == nil || s.pgHeroOtherDB.Dialector == nil || s.pgHeroOtherDB.Dialector.Name() != "postgres" || statsDB == nil {
		return total
	}
	rows, err := s.pgHeroSpaceStatRows(ctx, s.pgHeroOtherDB)
	if err != nil || len(rows) == 0 {
		return total
	}
	if tx := statsDB.WithContext(ctx).CreateInBatches(rows, 100); tx.Error != nil {
		return total
	}
	return total + int64(len(rows))
}

func (s *Server) pgHeroStatsDatabase() *gorm.DB {
	if s != nil && s.pgHeroStatsDB != nil {
		return s.pgHeroStatsDB
	}
	if s == nil {
		return nil
	}
	return s.db
}

func (s *Server) pgHeroSpaceStatRows(ctx context.Context, database *gorm.DB) ([]models.PgHeroSpaceStat, error) {
	if database == nil {
		return nil, nil
	}
	var rows []models.PgHeroSpaceStat
	if err := database.WithContext(ctx).Raw(pgheroSpaceStatsSelectSQL).Scan(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

const pgheroSpaceStatsInsertSQL = `
INSERT INTO pghero_space_stats (database, schema, relation, size, captured_at)
` + pgheroSpaceStatsSelectSQL

const pgheroSpaceStatsSelectSQL = `
SELECT
  current_database() AS database,
  pg_namespace.nspname AS schema,
  pg_class.relname AS relation,
  pg_total_relation_size(pg_class.oid) AS size,
  NOW() AS captured_at
FROM pg_class
INNER JOIN pg_namespace ON pg_namespace.oid = pg_class.relnamespace
WHERE pg_class.relkind IN ('r', 'm')
  AND pg_namespace.nspname NOT IN ('pg_catalog', 'information_schema')
  AND pg_namespace.nspname NOT LIKE 'pg_toast%'
ORDER BY pg_namespace.nspname, pg_class.relname`
