package api

import (
	"context"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

var accountMergingOwnedTables = []string{
	"statuses",
	"status_pins",
	"media_attachments",
	"polls",
	"reports",
	"tombstones",
	"favourites",
	"follows",
	"follow_requests",
	"blocks",
	"mutes",
	"account_moderation_notes",
	"account_pins",
	"list_accounts",
	"poll_votes",
	"mentions",
	"account_deletion_requests",
	"account_notes",
	"follow_recommendation_suppressions",
	"appeals",
	"tag_follows",
	"notifications",
	"notification_permissions",
	"notification_requests",
}

var accountMergingTargetTables = []string{
	"follows",
	"follow_requests",
	"blocks",
	"mutes",
	"account_moderation_notes",
	"account_pins",
	"account_notes",
}

// Mastodon 4.3 notification policies introduced a second account reference
// (`from_account_id`) which must follow a merged remote account as well as the
// recipient-owned `account_id` reference above.
var accountMergingFromTables = []string{
	"notifications",
	"notification_permissions",
	"notification_requests",
}

func (s *Server) mergeDuplicateRemoteActivityPubAccounts(ctx context.Context, database *gorm.DB, account models.Account) error {
	if database == nil || account.Local() || account.URI == "" {
		return nil
	}
	var duplicates []models.Account
	if err := database.WithContext(ctx).
		Where("uri = ? AND id <> ?", account.URI, account.ID).
		Where("domain IS NOT NULL AND domain <> ''").
		Find(&duplicates).Error; err != nil {
		return err
	}
	for _, duplicate := range duplicates {
		if err := s.mergeDuplicateRemoteActivityPubAccount(ctx, database, account.ID, duplicate.ID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) enqueueOrMergeDuplicateRemoteActivityPubAccounts(ctx context.Context, database *gorm.DB, account models.Account) error {
	if s != nil && s.enqueueAccountMergingTask(account.ID) {
		return nil
	}
	return s.mergeDuplicateRemoteActivityPubAccounts(ctx, database, account)
}

func (s *Server) mergeDuplicateRemoteActivityPubAccount(ctx context.Context, database *gorm.DB, accountID int64, duplicateID int64) error {
	if accountID == 0 || duplicateID == 0 || accountID == duplicateID {
		return nil
	}
	if err := database.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, table := range accountMergingOwnedTables {
			if err := mergeAccountReferenceRows(tx, table, "account_id", accountID, duplicateID); err != nil {
				return err
			}
		}
		if err := mergeAccountStat(tx, accountID, duplicateID); err != nil {
			return err
		}
		for _, table := range accountMergingTargetTables {
			if err := mergeAccountReferenceRows(tx, table, "target_account_id", accountID, duplicateID); err != nil {
				return err
			}
		}
		for _, table := range accountMergingFromTables {
			if err := mergeAccountReferenceRows(tx, table, "from_account_id", accountID, duplicateID); err != nil {
				return err
			}
		}
		if err := mergeNullableAccountReferenceRows(tx, "canonical_email_blocks", "reference_account_id", accountID, duplicateID); err != nil {
			return err
		}
		if err := mergeAccountReferenceRows(tx, "appeals", "account_warning_id", accountID, duplicateID); err != nil {
			return err
		}
		return tx.Where("id = ?", duplicateID).Delete(&models.Account{}).Error
	}); err != nil {
		return err
	}
	s.invalidateRelationshipCaches(ctx, accountID, duplicateID)
	return nil
}

func mergeAccountReferenceRows(tx *gorm.DB, table string, column string, accountID int64, duplicateID int64) error {
	var rows []struct {
		ID int64 `gorm:"column:id"`
	}
	if err := tx.Table(table).Select("id").Where(column+" = ?", duplicateID).Find(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		err := tx.Table(table).Where("id = ?", row.ID).Update(column, accountID).Error
		if err == nil {
			continue
		}
		if !isUniqueConstraintError(err) {
			return err
		}
		if deleteErr := tx.Exec("DELETE FROM "+table+" WHERE id = ?", row.ID).Error; deleteErr != nil {
			return deleteErr
		}
	}
	return nil
}

func mergeNullableAccountReferenceRows(tx *gorm.DB, table string, column string, accountID int64, duplicateID int64) error {
	var rows []struct {
		ID int64 `gorm:"column:id"`
	}
	if err := tx.Table(table).Select("id").Where(column+" = ?", duplicateID).Find(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		err := tx.Table(table).Where("id = ?", row.ID).Update(column, accountID).Error
		if err == nil {
			continue
		}
		if !isUniqueConstraintError(err) {
			return err
		}
		if deleteErr := tx.Table(table).Where("id = ?", row.ID).Update(column, nil).Error; deleteErr != nil {
			return deleteErr
		}
	}
	return nil
}

func mergeAccountStat(tx *gorm.DB, accountID int64, duplicateID int64) error {
	err := tx.Table("account_stats").Where("account_id = ?", duplicateID).Update("account_id", accountID).Error
	if err == nil {
		return nil
	}
	if !isUniqueConstraintError(err) {
		return err
	}
	return tx.Where("account_id = ?", duplicateID).Delete(&models.AccountStat{}).Error
}
