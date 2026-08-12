package api

import (
	"database/sql"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const maxUntrustedStatusCount int64 = 100_000_000

func activityPubStatusStatFromObject(statusID int64, object activityObject) models.StatusStat {
	return models.StatusStat{
		StatusID:                 statusID,
		UntrustedFavouritesCount: clampUntrustedStatusCount(object.LikesTotalItems),
		UntrustedReblogsCount:    clampUntrustedStatusCount(object.SharesTotalItems),
	}
}

func clampUntrustedStatusCount(count sql.NullInt64) sql.NullInt64 {
	if !count.Valid {
		return sql.NullInt64{}
	}
	if count.Int64 < 0 {
		count.Int64 = 0
	} else if count.Int64 > maxUntrustedStatusCount {
		count.Int64 = maxUntrustedStatusCount
	}
	return count
}

func activityPubUntrustedStatusCountUpdates(object activityObject, now time.Time) map[string]any {
	updates := map[string]any{}
	if count := clampUntrustedStatusCount(object.LikesTotalItems); count.Valid {
		updates["untrusted_favourites_count"] = count.Int64
	}
	if count := clampUntrustedStatusCount(object.SharesTotalItems); count.Valid {
		updates["untrusted_reblogs_count"] = count.Int64
	}
	if len(updates) > 0 {
		updates["updated_at"] = now
	}
	return updates
}

func updateActivityPubStatusUntrustedCounts(tx *gorm.DB, statusID int64, object activityObject, now time.Time) error {
	if tx == nil || statusID == 0 {
		return nil
	}
	updates := activityPubUntrustedStatusCountUpdates(object, now)
	if len(updates) == 0 {
		return nil
	}
	statusStat := activityPubStatusStatFromObject(statusID, object)
	return tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "status_id"}},
		DoUpdates: clause.Assignments(updates),
	}).Create(&statusStat).Error
}
