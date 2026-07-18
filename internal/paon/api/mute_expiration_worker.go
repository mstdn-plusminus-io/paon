package api

import (
	"context"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

const (
	muteExpirationWorkerInterval = time.Minute
	muteExpirationBatchSize      = 100
)

func (s *Server) runMuteExpirationWorker(ctx context.Context) {
	s.processExpiredMutes(ctx, time.Now().UTC(), muteExpirationBatchSize)
	ticker := time.NewTicker(muteExpirationWorkerInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			s.processExpiredMutes(ctx, now.UTC(), muteExpirationBatchSize)
		}
	}
}

func (s *Server) processExpiredMutes(ctx context.Context, now time.Time, batchSize int) int {
	if s == nil || s.db == nil || batchSize <= 0 {
		return 0
	}
	var rows []models.Mute
	if err := s.db.WithContext(ctx).
		Where("expires_at IS NOT NULL AND expires_at < ?", now).
		Order("expires_at ASC, id ASC").
		Limit(batchSize).
		Find(&rows).Error; err != nil {
		return 0
	}
	deleted := 0
	for _, mute := range rows {
		if s.enqueueDeleteMuteTask(mute.ID, time.Time{}) {
			continue
		}
		removed, err := s.deleteExpiredMute(ctx, mute.ID, now)
		if err != nil || !removed {
			continue
		}
		deleted++
	}
	return deleted
}

func (s *Server) deleteExpiredMute(ctx context.Context, muteID int64, now time.Time) (bool, error) {
	if s == nil || s.db == nil || muteID == 0 {
		return false, nil
	}
	var mute models.Mute
	if err := s.db.WithContext(ctx).Where("id = ?", muteID).First(&mute).Error; err != nil {
		return false, nil
	}
	if !mute.ExpiresAt.Valid || !mute.ExpiresAt.Time.Before(now) {
		return false, nil
	}
	tx := s.db.WithContext(ctx).
		Where("id = ? AND expires_at IS NOT NULL AND expires_at < ?", mute.ID, now).
		Delete(&models.Mute{})
	if tx.Error != nil || tx.RowsAffected == 0 {
		return false, tx.Error
	}
	s.restoreAfterUnmuteFeedCache(ctx, mute.AccountID, mute.TargetAccountID)
	s.invalidateMuteRelationshipCaches(ctx, mute.AccountID, mute.TargetAccountID)
	return true, nil
}
