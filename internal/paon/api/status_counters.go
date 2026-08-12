package api

import (
	"fmt"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
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
	if untrustedCounter := statusStatUntrustedCounter(counter); untrustedCounter != "" {
		return tx.Exec(`
			INSERT INTO status_stats (status_id, `+counter+`, created_at, updated_at)
			VALUES (?, ?, now(), now())
			ON CONFLICT (status_id)
			DO UPDATE SET
				`+counter+` = GREATEST(status_stats.`+counter+`, 0) + ?,
				`+untrustedCounter+` = CASE
					WHEN status_stats.`+untrustedCounter+` IS NULL OR EXISTS (
						SELECT 1 FROM statuses
						WHERE statuses.id = status_stats.status_id
						AND (statuses.local IS TRUE OR statuses.uri IS NULL)
					) THEN status_stats.`+untrustedCounter+`
					ELSE LEAST(?, GREATEST(status_stats.`+untrustedCounter+`, 0) + ?)
				END,
				updated_at = now()
		`, statusID, value, value, maxUntrustedStatusCount, value).Error
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
	if untrustedCounter := statusStatUntrustedCounter(counter); untrustedCounter != "" {
		return tx.Exec(`
			INSERT INTO status_stats (status_id, `+counter+`, created_at, updated_at)
			VALUES (?, 0, now(), now())
			ON CONFLICT (status_id)
			DO UPDATE SET
				`+counter+` = GREATEST(status_stats.`+counter+` - ?, 0),
				`+untrustedCounter+` = CASE
					WHEN status_stats.`+untrustedCounter+` IS NULL OR EXISTS (
						SELECT 1 FROM statuses
						WHERE statuses.id = status_stats.status_id
						AND (statuses.local IS TRUE OR statuses.uri IS NULL)
					) THEN status_stats.`+untrustedCounter+`
					ELSE GREATEST(LEAST(status_stats.`+untrustedCounter+`, ?) - ?, 0)
				END,
				updated_at = now()
		`, statusID, value, maxUntrustedStatusCount, value).Error
	}
	return tx.Exec(`
		INSERT INTO status_stats (status_id, `+counter+`, created_at, updated_at)
		VALUES (?, 0, now(), now())
		ON CONFLICT (status_id)
		DO UPDATE SET `+counter+` = GREATEST(status_stats.`+counter+` - ?, 0), updated_at = now()
	`, statusID, value).Error
}

func statusStatUntrustedCounter(counter string) string {
	switch counter {
	case statusStatCounterReblogs:
		return "untrusted_reblogs_count"
	case statusStatCounterFavourites:
		return "untrusted_favourites_count"
	default:
		return ""
	}
}

func decrementLoadedStatusStatCounter(status *models.Status, counter string, value int64) {
	if status == nil || value <= 0 {
		return
	}
	switch counter {
	case statusStatCounterReblogs:
		status.StatusStat.ReblogsCount = maxInt64(status.StatusStat.ReblogsCount-value, 0)
		if !statusLocalLikeRails(*status) && status.StatusStat.UntrustedReblogsCount.Valid {
			status.StatusStat.UntrustedReblogsCount.Int64 = maxInt64(status.StatusStat.UntrustedReblogsCount.Int64-value, 0)
		}
	case statusStatCounterFavourites:
		status.StatusStat.FavouritesCount = maxInt64(status.StatusStat.FavouritesCount-value, 0)
		if !statusLocalLikeRails(*status) && status.StatusStat.UntrustedFavouritesCount.Valid {
			status.StatusStat.UntrustedFavouritesCount.Int64 = maxInt64(status.StatusStat.UntrustedFavouritesCount.Int64-value, 0)
		}
	}
}

func statusStatCounterAllowed(counter string) bool {
	switch counter {
	case statusStatCounterReplies, statusStatCounterReblogs, statusStatCounterFavourites:
		return true
	default:
		return false
	}
}
