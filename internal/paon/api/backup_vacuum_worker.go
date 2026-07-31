package api

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

const (
	backupVacuumWorkerInterval = 24 * time.Hour
	backupVacuumBatchSize      = 100
)

func (s *Server) runBackupVacuumWorker(ctx context.Context) {
	ticker := time.NewTicker(backupVacuumWorkerInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			s.runSchedulerWithRedisLock(ctx, "vacuum_scheduler:backups_vacuum", 24*time.Hour, func() {
				s.vacuumExpiredBackups(ctx, now.UTC())
			})
		}
	}
}

func (s *Server) vacuumExpiredBackups(ctx context.Context, now time.Time) int {
	if s == nil || s.db == nil {
		return 0
	}
	days, ok := s.backupRetentionDays()
	if !ok {
		return 0
	}
	cutoff := now.Add(-time.Duration(days) * 24 * time.Hour)
	deleted := 0
	for {
		var backups []models.Backup
		if err := s.db.WithContext(ctx).
			Where("created_at < ?", cutoff).
			Order("id ASC").
			Limit(backupVacuumBatchSize).
			Find(&backups).Error; err != nil {
			return deleted
		}
		if len(backups) == 0 {
			return deleted
		}
		for _, backup := range backups {
			_ = s.removeBackupDumpFiles(backup)
			if err := s.db.WithContext(ctx).Delete(&models.Backup{}, backup.ID).Error; err == nil {
				deleted++
			}
		}
	}
}

func (s *Server) backupRetentionDays() (int, bool) {
	if s == nil {
		return 0, false
	}
	return parsePositiveRetentionDays(s.settingValue("backups_retention_period", "7"))
}

func parsePositiveRetentionDays(raw string) (int, bool) {
	value := normalizeSettingScalar(raw)
	days, err := strconv.Atoi(value)
	if err != nil || days <= 0 {
		return 0, false
	}
	return days, true
}

func (s *Server) removeBackupDumpFiles(backup models.Backup) error {
	if s == nil || backup.ID <= 0 {
		return nil
	}
	if backup.DumpFileName.Valid && backup.DumpFileName.String != "" {
		s.deletePaperclipObject(context.Background(), backupDumpObjectKey(backup.ID, backup.DumpFileName.String))
	}
	dir := s.cfg.SystemAssetPath("backups", "dumps", mediaPaperclipIDPartition(backup.ID))
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	return removeEmptyPaperclipParents(filepath.Dir(dir), s.cfg.SystemAssetPath("backups", "dumps"))
}
