package api

import (
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestAnnouncementShouldPublishOnlyWhenScheduledTimeHasArrived(t *testing.T) {
	now := time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)
	if !announcementShouldPublish(models.Announcement{ScheduledAt: sql.NullTime{Time: now, Valid: true}}, now) {
		t.Fatal("expected due unpublished announcement to publish")
	}
	if !announcementShouldPublish(models.Announcement{ScheduledAt: sql.NullTime{Time: now.Add(-time.Second), Valid: true}}, now) {
		t.Fatal("expected past unpublished announcement to publish")
	}
	for _, announcement := range []models.Announcement{
		{Published: true, ScheduledAt: sql.NullTime{Time: now, Valid: true}},
		{ScheduledAt: sql.NullTime{Time: now.Add(time.Second), Valid: true}},
		{},
	} {
		if announcementShouldPublish(announcement, now) {
			t.Fatalf("announcement should not publish: %#v", announcement)
		}
	}
}

func mustReadTestFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestAnnouncementShouldUnpublishOnlyAfterEndTime(t *testing.T) {
	now := time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)
	if !announcementShouldUnpublish(models.Announcement{Published: true, EndsAt: sql.NullTime{Time: now, Valid: true}}, now) {
		t.Fatal("expected ended published announcement to unpublish")
	}
	if !announcementShouldUnpublish(models.Announcement{Published: true, EndsAt: sql.NullTime{Time: now.Add(-time.Second), Valid: true}}, now) {
		t.Fatal("expected expired published announcement to unpublish")
	}
	for _, announcement := range []models.Announcement{
		{Published: false, EndsAt: sql.NullTime{Time: now, Valid: true}},
		{Published: true, EndsAt: sql.NullTime{Time: now.Add(time.Second), Valid: true}},
		{Published: true},
	} {
		if announcementShouldUnpublish(announcement, now) {
			t.Fatalf("announcement should not unpublish: %#v", announcement)
		}
	}
}
