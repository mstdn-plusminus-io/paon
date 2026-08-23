package api

import (
	"context"
	"log"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

const (
	mediaVacuumWorkerInterval  = 24 * time.Hour
	mediaVacuumBatchSize       = 1000
	orphanedMediaAttachmentTTL = 24 * time.Hour
)

func (s *Server) runMediaVacuumWorker(ctx context.Context) {
	ticker := time.NewTicker(mediaVacuumWorkerInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			s.runSchedulerWithRedisLock(ctx, "vacuum_scheduler:media_attachments_vacuum", 24*time.Hour, func() {
				s.vacuumMediaAttachments(ctx, now.UTC())
			})
		}
	}
}

func (s *Server) vacuumMediaAttachments(ctx context.Context, now time.Time) int {
	if s == nil || s.db == nil {
		return 0
	}
	cleaned := s.vacuumOrphanedMediaAttachments(ctx, now.Add(-orphanedMediaAttachmentTTL))
	cleaned += s.vacuumCachedRemoteMediaAttachments(ctx, now)
	return cleaned
}

func (s *Server) vacuumCachedRemoteMediaAttachments(ctx context.Context, now time.Time) int {
	days, ok := s.mediaCacheRetentionDays()
	if !ok {
		return 0
	}
	cutoff := now.Add(-time.Duration(days) * 24 * time.Hour)
	cleaned := 0
	lastID := int64(0)
	for {
		var attachments []models.MediaAttachment
		if err := s.db.WithContext(ctx).
			Where("id > ?", lastID).
			Where("remote_url <> ''").
			Where("file_file_name IS NOT NULL").
			Where("created_at < ? AND updated_at < ?", cutoff, cutoff).
			Order("id ASC").
			Limit(mediaVacuumBatchSize).
			Find(&attachments).Error; err != nil {
			return cleaned
		}
		if len(attachments) == 0 {
			return cleaned
		}
		// Advance by the selected batch, including rows whose cleanup fails.
		// This mirrors Mastodon 4.5's per-batch rescue: one broken attachment
		// must not prevent later batches from being vacuumed.
		lastID = attachments[len(attachments)-1].ID
		for _, attachment := range attachments {
			s.removeMediaAttachmentLocalFiles(attachment)
			if err := s.db.WithContext(ctx).Model(&models.MediaAttachment{}).Where("id = ?", attachment.ID).Updates(clearMediaAttachmentFileUpdates(now)).Error; err != nil {
				log.Printf("media vacuum skipping cached attachment batch after id=%d: %v", attachment.ID, err)
				break
			}
			s.invalidateMediaAttachmentParentStatusCache(ctx, attachment)
			cleaned++
		}
	}
}

func (s *Server) vacuumOrphanedMediaAttachments(ctx context.Context, cutoff time.Time) int {
	deleted := 0
	lastID := int64(0)
	for {
		var attachments []models.MediaAttachment
		if err := s.db.WithContext(ctx).
			Where("id > ?", lastID).
			Where("status_id IS NULL AND scheduled_status_id IS NULL").
			Where("created_at < ?", cutoff).
			Order("id ASC").
			Limit(mediaVacuumBatchSize).
			Find(&attachments).Error; err != nil {
			return deleted
		}
		if len(attachments) == 0 {
			return deleted
		}
		lastID = attachments[len(attachments)-1].ID
		for _, attachment := range attachments {
			s.removeMediaAttachmentLocalFiles(attachment)
			if err := s.db.WithContext(ctx).Delete(&models.MediaAttachment{}, attachment.ID).Error; err != nil {
				log.Printf("media vacuum skipping orphaned attachment batch after id=%d: %v", attachment.ID, err)
				break
			}
			deleted++
		}
	}
}
