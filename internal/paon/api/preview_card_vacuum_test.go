package api

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestPreviewCardVacuumConstantsMatchRailsCadence(t *testing.T) {
	if previewCardVacuumWorkerInterval != 24*time.Hour {
		t.Fatalf("previewCardVacuumWorkerInterval = %s", previewCardVacuumWorkerInterval)
	}
	if previewCardVacuumBatchSize != 1000 {
		t.Fatalf("previewCardVacuumBatchSize = %d", previewCardVacuumBatchSize)
	}
}

func TestClearPreviewCardImageUpdatesNullsPaperclipColumns(t *testing.T) {
	now := time.Date(2026, 6, 20, 8, 15, 0, 0, time.UTC)
	updates := clearPreviewCardImageUpdates(now)
	if updates["image_file_name"] != (sql.NullString{}) || updates["image_content_type"] != (sql.NullString{}) {
		t.Fatalf("image strings not nulled: %#v", updates)
	}
	if updates["image_file_size"] != (sql.NullInt64{}) || updates["image_storage_schema_version"] != (sql.NullInt64{}) {
		t.Fatalf("image integers not nulled: %#v", updates)
	}
	if updates["image_updated_at"] != (sql.NullTime{}) {
		t.Fatalf("image_updated_at = %#v", updates["image_updated_at"])
	}
	if updates["blurhash"] != (sql.NullString{}) {
		t.Fatalf("blurhash = %#v", updates["blurhash"])
	}
	if updates["updated_at"] != now {
		t.Fatalf("updated_at = %#v", updates["updated_at"])
	}
}

func TestRemovePreviewCardImageFilesRemovesPaperclipDirectories(t *testing.T) {
	root := t.TempDir()
	s := &Server{cfg: config.Config{PublicDir: root}}
	card := models.PreviewCard{ID: 42, ImageFileName: sql.NullString{String: "card.png", Valid: true}}
	for _, base := range []string{
		filepath.Join(root, "system", "preview_cards", "images"),
		filepath.Join(root, "system", "cache", "preview_cards", "images"),
	} {
		path := filepath.Join(base, "000", "000", "042", "original", "card.png")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("image"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if err := s.removePreviewCardImageFiles(card); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "system", "preview_cards", "images", "000", "000", "042")); !os.IsNotExist(err) {
		t.Fatalf("preview card image directory still exists err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "system", "cache", "preview_cards", "images", "000", "000", "042")); !os.IsNotExist(err) {
		t.Fatalf("cached preview card image directory still exists err=%v", err)
	}
}

func TestPreviewCardVacuumWorkerUsesRailsPreviewCardsVacuumShape(t *testing.T) {
	src, err := os.ReadFile("preview_card_vacuum_worker.go")
	if err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		functionName string
		want         string
	}{
		{"runPreviewCardVacuumWorker", `s.vacuumCachedPreviewCardImages(ctx, now.UTC())`},
		{"vacuumCachedPreviewCardImages", `days, ok := s.mediaCacheRetentionDays()`},
		{"vacuumCachedPreviewCardImages", `cutoff := now.Add(-time.Duration(days) * 24 * time.Hour)`},
		{"vacuumCachedPreviewCardImages", `Where("image_file_name IS NOT NULL AND image_file_name <> ''")`},
		{"vacuumCachedPreviewCardImages", `Where("updated_at < ?", cutoff)`},
		{"vacuumCachedPreviewCardImages", `Limit(previewCardVacuumBatchSize)`},
		{"vacuumCachedPreviewCardImages", `s.removePreviewCardImageFiles(card)`},
		{"vacuumCachedPreviewCardImages", `Updates(clearPreviewCardImageUpdates(now))`},
		{"removePreviewCardImageFiles", `s.deletePaperclipObject(context.Background(), previewCardImageObjectKey(card.ID, card.ImageFileName.String))`},
	}
	for _, check := range checks {
		if !functionBodyContains(t, src, check.functionName, check.want) {
			t.Fatalf("%s missing %q", check.functionName, check.want)
		}
	}
	startup, err := os.ReadFile("activitypub_retry.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, startup, "StartBackgroundWorkers", "workers.Go(ctx, s.runPreviewCardVacuumWorker)") {
		t.Fatal("StartBackgroundWorkers does not start preview card vacuum worker")
	}
}
