package api

import (
	"context"
	"database/sql"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

const (
	previewCardVacuumWorkerInterval = 24 * time.Hour
	previewCardVacuumBatchSize      = 1000
)

func (s *Server) runPreviewCardVacuumWorker(ctx context.Context) {
	ticker := time.NewTicker(previewCardVacuumWorkerInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			s.runSchedulerWithRedisLock(ctx, "vacuum_scheduler:preview_cards_vacuum", 24*time.Hour, func() {
				s.vacuumCachedPreviewCardImages(ctx, now.UTC())
			})
		}
	}
}

func (s *Server) vacuumCachedPreviewCardImages(ctx context.Context, now time.Time) int {
	if s == nil || s.db == nil {
		return 0
	}
	days, ok := s.mediaCacheRetentionDays()
	if !ok {
		return 0
	}
	cutoff := now.Add(-time.Duration(days) * 24 * time.Hour)
	cleaned := 0
	lastID := int64(0)
	for {
		var cards []models.PreviewCard
		if err := s.db.WithContext(ctx).
			Where("id > ?", lastID).
			Where("image_file_name IS NOT NULL AND image_file_name <> ''").
			Where("updated_at < ?", cutoff).
			Order("id ASC").
			Limit(previewCardVacuumBatchSize).
			Find(&cards).Error; err != nil {
			return cleaned
		}
		if len(cards) == 0 {
			return cleaned
		}
		// Cursor past the selected batch even when its cleanup fails, matching
		// Mastodon 4.5's behavior of rescuing one batch and continuing with the
		// next instead of aborting the entire vacuum run.
		lastID = cards[len(cards)-1].ID
		for _, card := range cards {
			if err := s.removePreviewCardImageFiles(card); err != nil {
				log.Printf("preview-card vacuum skipping batch after id=%d: %v", card.ID, err)
				break
			}
			if err := s.db.WithContext(ctx).Model(&models.PreviewCard{}).Where("id = ?", card.ID).Updates(clearPreviewCardImageUpdates(now)).Error; err != nil {
				log.Printf("preview-card vacuum skipping batch after id=%d: %v", card.ID, err)
				break
			}
			cleaned++
		}
	}
}

func (s *Server) mediaCacheRetentionDays() (int, bool) {
	if s == nil {
		return 0, false
	}
	return parsePositiveRetentionDays(s.settingValue("media_cache_retention_period", ""))
}

func clearPreviewCardImageUpdates(now time.Time) map[string]any {
	return map[string]any{
		"image_file_name":              sql.NullString{},
		"image_content_type":           sql.NullString{},
		"image_file_size":              sql.NullInt64{},
		"image_updated_at":             sql.NullTime{},
		"image_storage_schema_version": sql.NullInt64{},
		"blurhash":                     sql.NullString{},
		"updated_at":                   now,
	}
}

func (s *Server) removePreviewCardImageFiles(card models.PreviewCard) error {
	if s == nil || s.cfg.PublicDir == "" || card.ID <= 0 {
		return nil
	}
	if card.ImageFileName.Valid && card.ImageFileName.String != "" {
		if s.cfg.CacheBusterEnabled {
			s.bustCacheURL(s.cacheBusterPreviewCardImageURL(card.ID, card.ImageFileName.String))
		}
		s.deletePaperclipObject(context.Background(), previewCardImageObjectKey(card.ID, card.ImageFileName.String))
	}
	for _, base := range []string{
		s.cfg.SystemAssetPath("preview_cards", "images"),
		s.cfg.SystemAssetPath("cache", "preview_cards", "images"),
	} {
		dir := filepath.Join(base, mediaPaperclipIDPartition(card.ID))
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
		if err := removeEmptyPaperclipParents(filepath.Dir(dir), base); err != nil {
			return err
		}
	}
	return nil
}
