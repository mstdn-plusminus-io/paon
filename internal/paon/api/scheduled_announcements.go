package api

import (
	"context"
	"database/sql"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

const (
	scheduledAnnouncementLookahead = 5 * time.Minute
)

func (s *Server) processDueAnnouncementSchedule(ctx context.Context, now time.Time) {
	if s == nil || s.db == nil {
		return
	}
	s.publishDueAnnouncements(ctx, now.UTC())
	s.unpublishExpiredAnnouncements(ctx, now.UTC())
}

func (s *Server) publishDueAnnouncements(ctx context.Context, now time.Time) {
	var announcements []models.Announcement
	if err := s.db.WithContext(ctx).
		Where("published = ? AND scheduled_at IS NOT NULL AND scheduled_at <= ?", false, now.Add(scheduledAnnouncementLookahead)).
		Order("scheduled_at ASC, id ASC").
		Find(&announcements).Error; err != nil {
		return
	}
	for _, announcement := range announcements {
		if s.enqueuePublishAnnouncementTask(announcement.ID, announcement.ScheduledAt.Time) {
			continue
		}
		if !announcementShouldPublish(announcement, now) {
			continue
		}
		published, updated := s.publishScheduledAnnouncement(ctx, announcement, now)
		if published {
			s.broadcastAnnouncement(updated)
		}
	}
}

func (s *Server) publishScheduledAnnouncement(ctx context.Context, announcement models.Announcement, now time.Time) (bool, models.Announcement) {
	ids := s.statusIDsFromAnnouncementText(announcement.Text)
	announcement.Published = true
	announcement.PublishedAt = sql.NullTime{Time: now, Valid: true}
	announcement.ScheduledAt = sql.NullTime{}
	announcement.StatusIDs = ids
	announcement.UpdatedAt = now
	result := s.db.WithContext(ctx).
		Model(&models.Announcement{}).
		Where("id = ? AND published = ?", announcement.ID, false).
		Updates(map[string]any{
			"published":    true,
			"published_at": announcement.PublishedAt,
			"scheduled_at": announcement.ScheduledAt,
			"status_ids":   ids,
			"updated_at":   now,
		})
	if result.Error != nil || result.RowsAffected == 0 {
		return false, announcement
	}
	return true, announcement
}

func (s *Server) publishAnnouncementWorker(ctx context.Context, announcement models.Announcement, now time.Time) (bool, models.Announcement) {
	if announcement.Published {
		updated, ok := s.refreshAnnouncementStatusIDsForWorker(ctx, announcement, now)
		return ok, updated
	}
	return s.publishScheduledAnnouncement(ctx, announcement, now)
}

func (s *Server) refreshAnnouncementStatusIDsForWorker(ctx context.Context, announcement models.Announcement, now time.Time) (models.Announcement, bool) {
	if s == nil || s.db == nil {
		return announcement, false
	}
	ids := s.statusIDsFromAnnouncementText(announcement.Text)
	if int64ArraysEqual(ids, announcement.StatusIDs) {
		return announcement, true
	}
	announcement.StatusIDs = ids
	announcement.UpdatedAt = now
	result := s.db.WithContext(ctx).
		Model(&models.Announcement{}).
		Where("id = ?", announcement.ID).
		Updates(map[string]any{
			"status_ids": ids,
			"updated_at": now,
		})
	return announcement, result.Error == nil
}

func (s *Server) unpublishExpiredAnnouncements(ctx context.Context, now time.Time) {
	var announcements []models.Announcement
	if err := s.db.WithContext(ctx).
		Where("published = ? AND ends_at IS NOT NULL AND ends_at <= ?", true, now).
		Order("ends_at ASC, id ASC").
		Find(&announcements).Error; err != nil {
		return
	}
	for _, announcement := range announcements {
		if !announcementShouldUnpublish(announcement, now) {
			continue
		}
		s.unpublishExpiredAnnouncement(ctx, announcement, now)
	}
}

func (s *Server) unpublishExpiredAnnouncement(ctx context.Context, announcement models.Announcement, now time.Time) bool {
	result := s.db.WithContext(ctx).
		Model(&models.Announcement{}).
		Where("id = ? AND published = ?", announcement.ID, true).
		Updates(map[string]any{
			"published":    false,
			"scheduled_at": sql.NullTime{},
			"updated_at":   now,
		})
	return result.Error == nil && result.RowsAffected > 0
}

func announcementShouldPublish(announcement models.Announcement, now time.Time) bool {
	return !announcement.Published && announcement.ScheduledAt.Valid && !announcement.ScheduledAt.Time.After(now)
}

func announcementShouldUnpublish(announcement models.Announcement, now time.Time) bool {
	return announcement.Published && announcement.EndsAt.Valid && !announcement.EndsAt.Time.After(now)
}
