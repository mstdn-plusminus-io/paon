package api

import (
	"errors"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func createModerationWarningNotification(tx *gorm.DB, warning models.AccountWarning, now time.Time) error {
	if tx == nil || warning.ID == 0 || !warning.TargetAccountID.Valid || warning.TargetAccountID.Int64 == 0 {
		return nil
	}
	// The notification table deliberately has no polymorphic uniqueness
	// constraint. Serialize retries on the durable warning row so two workers
	// cannot both pass the existence check and create duplicate notifications.
	var lockedWarning models.AccountWarning
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id").Where("id = ?", warning.ID).First(&lockedWarning).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	var user models.User
	if err := tx.Select("id").Where("account_id = ?", warning.TargetAccountID.Int64).First(&user).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	var count int64
	if err := tx.Model(&models.Notification{}).Where("account_id = ? AND activity_type = ? AND activity_id = ? AND type = ?", warning.TargetAccountID.Int64, "AccountWarning", warning.ID, "moderation_warning").Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	fromAccountID := warning.TargetAccountID.Int64
	if warning.AccountID.Valid && warning.AccountID.Int64 > 0 {
		fromAccountID = warning.AccountID.Int64
	}
	notification := models.Notification{
		ActivityID: warning.ID, ActivityType: "AccountWarning", AccountID: warning.TargetAccountID.Int64,
		FromAccountID: fromAccountID, Type: "moderation_warning", CreatedAt: now, UpdatedAt: now,
	}
	return tx.Create(&notification).Error
}

func (s *Server) publishModerationWarningNotification(warningID int64) {
	if s == nil || s.db == nil || warningID == 0 {
		return
	}
	var ids []int64
	if err := s.db.Model(&models.Notification{}).Where("activity_type = ? AND activity_id = ? AND type = ?", "AccountWarning", warningID, "moderation_warning").Pluck("id", &ids).Error; err == nil {
		s.publishNotificationIDs(ids)
	}
}
