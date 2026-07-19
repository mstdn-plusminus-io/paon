package api

import (
	"os"
	"testing"
	"time"
)

func TestMediaVacuumConstantsMatchRailsCadence(t *testing.T) {
	if mediaVacuumWorkerInterval != 24*time.Hour {
		t.Fatalf("mediaVacuumWorkerInterval = %s", mediaVacuumWorkerInterval)
	}
	if mediaVacuumBatchSize != 1000 {
		t.Fatalf("mediaVacuumBatchSize = %d", mediaVacuumBatchSize)
	}
	if orphanedMediaAttachmentTTL != 24*time.Hour {
		t.Fatalf("orphanedMediaAttachmentTTL = %s", orphanedMediaAttachmentTTL)
	}
}

func TestMediaVacuumWorkerUsesRailsMediaAttachmentsVacuumShape(t *testing.T) {
	src, err := os.ReadFile("media_vacuum_worker.go")
	if err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		functionName string
		want         string
	}{
		{"runMediaVacuumWorker", `s.vacuumMediaAttachments(ctx, now.UTC())`},
		{"vacuumMediaAttachments", `s.vacuumOrphanedMediaAttachments(ctx, now.Add(-orphanedMediaAttachmentTTL))`},
		{"vacuumMediaAttachments", `s.vacuumCachedRemoteMediaAttachments(ctx, now)`},
		{"vacuumCachedRemoteMediaAttachments", `days, ok := s.mediaCacheRetentionDays()`},
		{"vacuumCachedRemoteMediaAttachments", `Where("remote_url <> ''")`},
		{"vacuumCachedRemoteMediaAttachments", `Where("file_file_name IS NOT NULL")`},
		{"vacuumCachedRemoteMediaAttachments", `Where("created_at < ? AND updated_at < ?", cutoff, cutoff)`},
		{"vacuumCachedRemoteMediaAttachments", `Updates(clearMediaAttachmentFileUpdates(now))`},
		{"vacuumOrphanedMediaAttachments", `Where("status_id IS NULL AND scheduled_status_id IS NULL")`},
		{"vacuumOrphanedMediaAttachments", `Where("created_at < ?", cutoff)`},
		{"vacuumOrphanedMediaAttachments", `Delete(&models.MediaAttachment{}, attachment.ID)`},
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
	if !functionBodyContains(t, startup, "StartBackgroundWorkers", "workers.Go(ctx, s.runMediaVacuumWorker)") {
		t.Fatal("StartBackgroundWorkers does not start media vacuum worker")
	}
}
