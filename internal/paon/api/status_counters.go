package api

import (
	"fmt"

	"gorm.io/gorm"
)

const (
	statusStatCounterReplies    = "replies_count"
	statusStatCounterReblogs    = "reblogs_count"
	statusStatCounterFavourites = "favourites_count"
)

func incrementStatusStatCounter(tx *gorm.DB, statusID int64, counter string, value int64) error {
	if statusID == 0 || value <= 0 {
		return nil
	}
	if !statusStatCounterAllowed(counter) {
		return fmt.Errorf("invalid status_stats counter %q", counter)
	}
	return tx.Exec(`
		INSERT INTO status_stats (status_id, `+counter+`, created_at, updated_at)
		VALUES (?, ?, now(), now())
		ON CONFLICT (status_id)
		DO UPDATE SET `+counter+` = GREATEST(status_stats.`+counter+`, 0) + ?, updated_at = now()
	`, statusID, value, value).Error
}

func decrementStatusStatCounter(tx *gorm.DB, statusID int64, counter string, value int64) error {
	if statusID == 0 || value <= 0 {
		return nil
	}
	if !statusStatCounterAllowed(counter) {
		return fmt.Errorf("invalid status_stats counter %q", counter)
	}
	return tx.Exec(`
		INSERT INTO status_stats (status_id, `+counter+`, created_at, updated_at)
		VALUES (?, 0, now(), now())
		ON CONFLICT (status_id)
		DO UPDATE SET `+counter+` = GREATEST(status_stats.`+counter+` - ?, 0), updated_at = now()
	`, statusID, value).Error
}

func statusStatCounterAllowed(counter string) bool {
	switch counter {
	case statusStatCounterReplies, statusStatCounterReblogs, statusStatCounterFavourites:
		return true
	default:
		return false
	}
}
