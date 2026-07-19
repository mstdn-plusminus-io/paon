package api

import (
	"database/sql"
	"errors"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

var errSafeReblogTargetUnavailable = errors.New("reblog target unavailable")

func safeInsertReblogStatus(tx *gorm.DB, reblog *models.Status) error {
	if tx == nil || reblog == nil {
		return gorm.ErrInvalidData
	}
	if !reblog.ReblogOfID.Valid {
		return tx.Create(reblog).Error
	}
	row := tx.Raw(`
INSERT INTO statuses (uri, text, created_at, updated_at, account_id, local, visibility, reblog_of_id, conversation_id)
SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?
FROM statuses AS reblog_target
WHERE reblog_target.id = ? AND reblog_target.deleted_at IS NULL
RETURNING id`,
		nullableStringValue(reblog.URI),
		reblog.Text,
		reblog.CreatedAt,
		reblog.UpdatedAt,
		reblog.AccountID,
		nullableBoolValue(reblog.Local),
		reblog.Visibility,
		reblog.ReblogOfID.Int64,
		nullableInt64Value(reblog.ConversationID),
		reblog.ReblogOfID.Int64,
	).Row()
	if err := row.Scan(&reblog.ID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errSafeReblogTargetUnavailable
		}
		return err
	}
	return nil
}

func nullableStringValue(value sql.NullString) any {
	if !value.Valid {
		return nil
	}
	return value.String
}

func nullableBoolValue(value sql.NullBool) any {
	if !value.Valid {
		return nil
	}
	return value.Bool
}

func nullableInt64Value(value sql.NullInt64) any {
	if !value.Valid {
		return nil
	}
	return value.Int64
}
