package api

import (
	"fmt"

	"gorm.io/gorm"
)

const (
	accountStatCounterStatuses  = "statuses_count"
	accountStatCounterFollowing = "following_count"
	accountStatCounterFollowers = "followers_count"
)

func incrementAccountStatCounter(tx *gorm.DB, accountID int64, counter string, value int64) error {
	if accountID == 0 || value <= 0 {
		return nil
	}
	return updateAccountStatCounter(tx, accountID, counter, value, value)
}

func decrementAccountStatCounter(tx *gorm.DB, accountID int64, counter string, value int64) error {
	if accountID == 0 || value <= 0 {
		return nil
	}
	return updateAccountStatCounter(tx, accountID, counter, 0, -value)
}

func updateAccountStatCounter(tx *gorm.DB, accountID int64, counter string, defaultValue int64, value int64) error {
	if !accountStatCounterAllowed(counter) {
		return fmt.Errorf("invalid account_stats counter %q", counter)
	}
	if counter == accountStatCounterStatuses && value > 0 {
		return tx.Exec(`
			INSERT INTO account_stats (account_id, `+counter+`, created_at, updated_at, last_status_at)
			VALUES (?, ?, now(), now(), now())
			ON CONFLICT (account_id)
			DO UPDATE SET `+counter+` = account_stats.`+counter+` + ?, last_status_at = now(), updated_at = now()
		`, accountID, defaultValue, value).Error
	}
	return tx.Exec(`
		INSERT INTO account_stats (account_id, `+counter+`, created_at, updated_at)
		VALUES (?, ?, now(), now())
		ON CONFLICT (account_id)
		DO UPDATE SET `+counter+` = account_stats.`+counter+` + ?, updated_at = now()
	`, accountID, defaultValue, value).Error
}

func accountStatCounterAllowed(counter string) bool {
	switch counter {
	case accountStatCounterStatuses, accountStatCounterFollowing, accountStatCounterFollowers:
		return true
	default:
		return false
	}
}
