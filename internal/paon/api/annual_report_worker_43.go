package api

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const annualReportSchemaVersion = 1

type annualReportCountItem struct {
	Name  string `json:"name,omitempty" gorm:"column:name"`
	Count int64  `json:"count" gorm:"column:count"`
}

type annualReportAccountCountItem struct {
	AccountID int64 `json:"account_id" gorm:"column:account_id"`
	Count     int64 `json:"count" gorm:"column:count"`
}

type annualReportMonth struct {
	Month     int   `json:"month" gorm:"column:month"`
	Statuses  int64 `json:"statuses"`
	Following int64 `json:"following"`
	Followers int64 `json:"followers"`
}

func (s *Server) generateAnnualReport(ctx context.Context, accountID int64, year int) error {
	if s == nil || s.db == nil || accountID == 0 {
		return nil
	}
	var account models.Account
	if err := s.db.WithContext(ctx).Select("id").Where("id = ?", accountID).First(&account).Error; err != nil {
		return workerLookupError("annual report account lookup", err)
	}
	var existing int64
	if err := s.db.WithContext(ctx).Model(&models.GeneratedAnnualReport{}).Where("account_id = ? AND year = ?", accountID, year).Count(&existing).Error; err != nil || existing > 0 {
		return err
	}
	start := time.Date(year, time.January, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(1, 0, 0)
	data, err := s.annualReportData(ctx, accountID, start, end)
	if err != nil {
		return err
	}
	body, err := json.Marshal(data)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	report := models.GeneratedAnnualReport{AccountID: accountID, Year: year, Data: models.JSONValue(body), SchemaVersion: annualReportSchemaVersion, CreatedAt: now, UpdatedAt: now}
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "account_id"}, {Name: "year"}}, DoNothing: true}).Create(&report).Error
}

func (s *Server) annualReportData(ctx context.Context, accountID int64, start time.Time, end time.Time) (map[string]any, error) {
	db := s.db.WithContext(ctx)
	base := db.Table("statuses AS annual_statuses").Where("annual_statuses.account_id = ? AND annual_statuses.created_at >= ? AND annual_statuses.created_at < ? AND annual_statuses.deleted_at IS NULL", accountID, start, end)
	count := func(condition string, args ...any) (int64, error) {
		var value int64
		query := base
		if condition != "" {
			query = query.Where(condition, args...)
		}
		return value, query.Count(&value).Error
	}
	total, err := count("")
	if err != nil {
		return nil, err
	}
	reblogs, err := count("annual_statuses.reblog_of_id IS NOT NULL")
	if err != nil {
		return nil, err
	}
	replies, err := count("annual_statuses.in_reply_to_id IS NOT NULL AND annual_statuses.in_reply_to_account_id <> ?", accountID)
	if err != nil {
		return nil, err
	}
	standalone, err := count("annual_statuses.in_reply_to_id IS NULL AND annual_statuses.reblog_of_id IS NULL")
	if err != nil {
		return nil, err
	}
	polls, err := count("annual_statuses.poll_id IS NOT NULL")
	if err != nil {
		return nil, err
	}

	topStatuses, err := annualReportTopStatuses(base)
	if err != nil {
		return nil, err
	}
	mostUsedApps := []annualReportCountItem{}
	if err := base.Select("oauth_applications.name AS name, COUNT(*) AS count").
		Joins("JOIN oauth_applications ON oauth_applications.id = annual_statuses.application_id").
		Group("oauth_applications.name").Order("count DESC").Limit(10).Scan(&mostUsedApps).Error; err != nil {
		return nil, err
	}
	commonlyInteracted := []annualReportAccountCountItem{}
	if err := base.Select("annual_statuses.in_reply_to_account_id AS account_id, COUNT(*) AS count").
		Where("annual_statuses.in_reply_to_account_id IS NOT NULL AND annual_statuses.in_reply_to_account_id <> ?", accountID).
		Group("annual_statuses.in_reply_to_account_id").Having("COUNT(*) > 1").Order("count DESC").Limit(40).Scan(&commonlyInteracted).Error; err != nil {
		return nil, err
	}
	topHashtags := []annualReportCountItem{}
	if err := db.Table("tags").Select("COALESCE(tags.display_name, tags.name) AS name, COUNT(*) AS count").
		Joins("JOIN statuses_tags ON statuses_tags.tag_id = tags.id").
		Joins("JOIN statuses annual_statuses ON annual_statuses.id = statuses_tags.status_id").
		Where("annual_statuses.account_id = ? AND annual_statuses.created_at >= ? AND annual_statuses.created_at < ? AND annual_statuses.deleted_at IS NULL", accountID, start, end).
		Group("tags.id").Having("COUNT(*) > 1").Order("count DESC").Limit(40).Scan(&topHashtags).Error; err != nil {
		return nil, err
	}
	mostReblogged := []annualReportAccountCountItem{}
	if err := base.Select("reblogged.account_id AS account_id, COUNT(*) AS count").
		Joins("JOIN statuses reblogged ON reblogged.id = annual_statuses.reblog_of_id").
		Group("reblogged.account_id").Having("COUNT(*) > 1").Order("count DESC").Limit(10).Scan(&mostReblogged).Error; err != nil {
		return nil, err
	}
	series, err := annualReportTimeSeries(db, accountID, start, end)
	if err != nil {
		return nil, err
	}
	percentiles, err := annualReportPercentiles(db, accountID, start, end, total)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"archetype":                         annualReportArchetype(standalone, replies, reblogs, polls),
		"type_distribution":                 map[string]int64{"total": total, "reblogs": reblogs, "replies": replies, "standalone": standalone},
		"top_statuses":                      topStatuses,
		"most_used_apps":                    mostUsedApps,
		"commonly_interacted_with_accounts": commonlyInteracted,
		"time_series":                       series,
		"top_hashtags":                      topHashtags,
		"most_reblogged_accounts":           mostReblogged,
		"percentiles":                       percentiles,
	}, nil
}

func annualReportArchetype(standalone int64, replies int64, reblogs int64, polls int64) string {
	if standalone+replies+reblogs < 113 {
		return "lurker"
	}
	if reblogs > standalone*2 {
		return "booster"
	}
	if float64(polls) > float64(standalone)*0.1 {
		return "pollster"
	}
	if replies > standalone*2 {
		return "replier"
	}
	return "oracle"
}

func annualReportTopStatuses(base *gorm.DB) (map[string]any, error) {
	out := map[string]any{"by_reblogs": nil, "by_favourites": nil, "by_replies": nil}
	excluded := []int64{}
	for _, item := range []struct{ key, column string }{{"by_reblogs", "reblogs_count"}, {"by_favourites", "favourites_count"}, {"by_replies", "replies_count"}} {
		var row struct{ ID int64 }
		query := base.Select("annual_statuses.id").Joins("JOIN status_stats ON status_stats.status_id = annual_statuses.id").Where("annual_statuses.visibility = ?", 0)
		if len(excluded) > 0 {
			query = query.Where("annual_statuses.id NOT IN ?", excluded)
		}
		err := query.Order("status_stats." + item.column + " DESC").First(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out[item.key] = row.ID
		excluded = append(excluded, row.ID)
	}
	return out, nil
}

func annualReportTimeSeries(db *gorm.DB, accountID int64, start time.Time, end time.Time) ([]annualReportMonth, error) {
	series := make([]annualReportMonth, 12)
	for i := range series {
		series[i].Month = i + 1
	}
	apply := func(rows []struct {
		Month int
		Count int64
	}, field func(*annualReportMonth, int64)) {
		for _, row := range rows {
			if row.Month >= 1 && row.Month <= 12 {
				field(&series[row.Month-1], row.Count)
			}
		}
	}
	var statuses []struct {
		Month int
		Count int64
	}
	if err := db.Table("statuses").Select("EXTRACT(MONTH FROM created_at)::int AS month, COUNT(*) AS count").Where("account_id = ? AND created_at >= ? AND created_at < ? AND deleted_at IS NULL", accountID, start, end).Group("month").Scan(&statuses).Error; err != nil {
		return nil, err
	}
	apply(statuses, func(m *annualReportMonth, count int64) { m.Statuses = count })
	var following []struct {
		Month int
		Count int64
	}
	if err := db.Table("follows").Select("EXTRACT(MONTH FROM created_at)::int AS month, COUNT(*) AS count").Where("account_id = ? AND created_at >= ? AND created_at < ?", accountID, start, end).Group("month").Scan(&following).Error; err != nil {
		return nil, err
	}
	apply(following, func(m *annualReportMonth, count int64) { m.Following = count })
	var followers []struct {
		Month int
		Count int64
	}
	if err := db.Table("follows").Select("EXTRACT(MONTH FROM created_at)::int AS month, COUNT(*) AS count").Where("target_account_id = ? AND created_at >= ? AND created_at < ?", accountID, start, end).Group("month").Scan(&followers).Error; err != nil {
		return nil, err
	}
	apply(followers, func(m *annualReportMonth, count int64) { m.Followers = count })
	return series, nil
}

func annualReportPercentiles(db *gorm.DB, accountID int64, start time.Time, end time.Time, statusesCreated int64) (map[string]float64, error) {
	var followersGained int64
	if err := db.Table("follows").Where("target_account_id = ? AND created_at >= ? AND created_at < ?", accountID, start, end).Count(&followersGained).Error; err != nil {
		return nil, err
	}
	var followerTotals struct{ Fewer, Any int64 }
	if err := db.Raw(`WITH totals AS (
		SELECT follows.target_account_id, COUNT(*) AS total
		FROM follows JOIN accounts ON accounts.id = follows.target_account_id
		WHERE follows.created_at >= ? AND follows.created_at < ? AND (accounts.domain IS NULL OR accounts.domain = '')
		GROUP BY follows.target_account_id
	) SELECT COUNT(*) FILTER (WHERE total < ?) AS fewer, COUNT(*) AS any FROM totals`, start, end, followersGained).Scan(&followerTotals).Error; err != nil {
		return nil, err
	}
	var statusTotals struct{ Fewer, Any int64 }
	if err := db.Raw(`WITH totals AS (
		SELECT statuses.account_id, COUNT(*) AS total
		FROM statuses JOIN accounts ON accounts.id = statuses.account_id
		WHERE statuses.created_at >= ? AND statuses.created_at < ? AND statuses.deleted_at IS NULL AND (accounts.domain IS NULL OR accounts.domain = '')
		GROUP BY statuses.account_id
	) SELECT COUNT(*) FILTER (WHERE total < ?) AS fewer, COUNT(*) AS any FROM totals`, start, end, statusesCreated).Scan(&statusTotals).Error; err != nil {
		return nil, err
	}
	return map[string]float64{
		"followers": float64(followerTotals.Fewer) / float64(followerTotals.Any+1) * 100,
		"statuses":  float64(statusTotals.Fewer) / float64(statusTotals.Any+1) * 100,
	}, nil
}
