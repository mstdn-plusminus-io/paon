package api

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	trendsRefreshWorkerInterval = 5 * time.Minute
	trendsReviewWorkerInterval  = 6 * time.Hour
	trendBatchSize              = 100
	trendStatusThreshold        = 5
	trendStatusDecayThreshold   = 0.3
	trendStatusScoreHalflife    = time.Hour
	trendLinkThreshold          = 5
	trendLinkDecayThreshold     = 1
	trendLinkMaxScoreCooldown   = 48 * time.Hour
	trendLinkMaxScoreHalflife   = 8 * time.Hour
	trendReviewThresholdRank    = 3
	trendTagThreshold           = 5
	trendTagDecayThreshold      = 1
	trendTagMaxScoreCooldown    = 48 * time.Hour
	trendTagMaxScoreHalflife    = 4 * time.Hour
	trendHistoryTTL             = 14 * 24 * time.Hour
)

type trendsReviewMailItems struct {
	Links    []trendsReviewLink
	Tags     []trendsReviewTag
	Statuses []trendsReviewStatus
}

type trendsReviewLink struct {
	Title    string
	URL      string
	Language string
	Score    float64
}

type trendsReviewTag struct {
	Name  string
	Score float64
}

type trendsReviewStatus struct {
	URL      string
	Language string
	Score    float64
}

func (s *Server) runTrendsRefreshWorker(ctx context.Context) {
	refreshTicker := time.NewTicker(trendsRefreshWorkerInterval)
	reviewTicker := time.NewTicker(trendsReviewWorkerInterval)
	defer refreshTicker.Stop()
	defer reviewTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-refreshTicker.C:
			s.runSchedulerWithRedisLock(ctx, "trends_refresh_scheduler", 30*time.Minute, func() {
				s.refreshTrends(ctx, now.UTC())
			})
		case now := <-reviewTicker.C:
			s.runSchedulerWithRedisLock(ctx, "trends_review_scheduler", trendsReviewWorkerInterval, func() {
				s.requestTrendsReview(ctx, now.UTC())
			})
		}
	}
}

func (s *Server) refreshTrends(ctx context.Context, now time.Time) int {
	if s == nil || s.db == nil || !s.trendsEnabled() {
		return 0
	}
	refreshed := s.refreshTagTrends(ctx, now)
	refreshed += s.refreshStatusTrends(ctx, now)
	refreshed += s.refreshPreviewCardTrends(ctx, now)
	return refreshed
}

func (s *Server) recordTagTrendUse(ctx context.Context, accountID int64, visibility int, tagIDs []int64, now time.Time) {
	if s == nil || s.db == nil || accountID == 0 || visibility != 0 || len(tagIDs) == 0 {
		return
	}
	var accountCount int64
	if err := s.db.WithContext(ctx).Model(&models.Account{}).
		Where("id = ? AND silenced_at IS NULL", accountID).
		Count(&accountCount).Error; err != nil || accountCount == 0 {
		return
	}
	var usableTagIDs []int64
	if err := s.db.WithContext(ctx).Model(&models.Tag{}).
		Where("id IN ?", uniqueInt64s(tagIDs)).
		Where("usable IS NULL OR usable = ?", true).
		Pluck("id", &usableTagIDs).Error; err != nil {
		return
	}
	day := truncateMetricTime(now, "day")
	usedKey := trendUsedKey(s.cfg.RedisNamespace, "trending_tags", now)
	for _, tagID := range usableTagIDs {
		if tagID <= 0 {
			continue
		}
		usesKey := tagHistoryRedisKey(s.cfg.RedisNamespace, tagID, day, false)
		accountsKey := tagHistoryRedisKey(s.cfg.RedisNamespace, tagID, day, true)
		_, _ = s.redisCommand(ctx, "INCRBY", usesKey, "1")
		_, _ = s.redisCommand(ctx, "PFADD", accountsKey, strconv.FormatInt(accountID, 10))
		_, _ = s.redisCommand(ctx, "EXPIRE", usesKey, strconv.FormatInt(int64(trendHistoryTTL/time.Second), 10))
		_, _ = s.redisCommand(ctx, "EXPIRE", accountsKey, strconv.FormatInt(int64(trendHistoryTTL/time.Second), 10))
		_, _ = s.redisCommand(ctx, "SADD", usedKey, strconv.FormatInt(tagID, 10))
	}
	_, _ = s.redisCommand(ctx, "EXPIRE", usedKey, "86400")
}

func (s *Server) recordStatusTrendUse(ctx context.Context, statusID int64, now time.Time) {
	if s == nil || statusID <= 0 {
		return
	}
	usedKey := trendUsedKey(s.cfg.RedisNamespace, "trending_statuses", now)
	_, _ = s.redisCommand(ctx, "SADD", usedKey, strconv.FormatInt(statusID, 10))
	_, _ = s.redisCommand(ctx, "EXPIRE", usedKey, "86400")
}

func (s *Server) recordPreviewCardTrendUse(ctx context.Context, accountID int64, previewCardID int64, now time.Time) {
	if s == nil || previewCardID <= 0 {
		return
	}
	day := truncateMetricTime(now, "day")
	usedKey := trendUsedKey(s.cfg.RedisNamespace, "trending_links", now)
	if accountID > 0 {
		usesKey := linkHistoryRedisKey(s.cfg.RedisNamespace, previewCardID, day, false)
		accountsKey := linkHistoryRedisKey(s.cfg.RedisNamespace, previewCardID, day, true)
		_, _ = s.redisCommand(ctx, "INCRBY", usesKey, "1")
		_, _ = s.redisCommand(ctx, "PFADD", accountsKey, strconv.FormatInt(accountID, 10))
		_, _ = s.redisCommand(ctx, "EXPIRE", usesKey, strconv.FormatInt(int64(trendHistoryTTL/time.Second), 10))
		_, _ = s.redisCommand(ctx, "EXPIRE", accountsKey, strconv.FormatInt(int64(trendHistoryTTL/time.Second), 10))
	}
	_, _ = s.redisCommand(ctx, "SADD", usedKey, strconv.FormatInt(previewCardID, 10))
	_, _ = s.redisCommand(ctx, "EXPIRE", usedKey, "86400")
}

func (s *Server) recordPreviewCardTrendUseForStatus(ctx context.Context, accountID int64, statusID int64, visibility int, now time.Time) {
	if s == nil || s.db == nil || accountID <= 0 || statusID <= 0 || visibility != 0 {
		return
	}
	var previewCardIDs []int64
	_ = s.db.WithContext(ctx).
		Table("preview_cards").
		Select("preview_cards.id").
		Joins("JOIN preview_cards_statuses ON preview_cards_statuses.preview_card_id = preview_cards.id").
		Joins("JOIN statuses ON statuses.id = preview_cards_statuses.status_id").
		Joins("JOIN accounts original_accounts ON original_accounts.id = statuses.account_id").
		Joins("JOIN accounts actor_accounts ON actor_accounts.id = ?", accountID).
		Where("statuses.id = ? AND statuses.deleted_at IS NULL AND statuses.visibility = ?", statusID, 0).
		Where("statuses.sensitive = ? AND statuses.spoiler_text = ''", false).
		Where("original_accounts.silenced_at IS NULL AND actor_accounts.silenced_at IS NULL").
		Where("preview_cards.type = ? AND preview_cards.link_type = ?", 0, 1).
		Where("preview_cards.title <> '' AND preview_cards.description <> '' AND preview_cards.provider_name <> ''").
		Where("preview_cards.image_file_name IS NOT NULL AND preview_cards.image_file_name <> ''").
		Pluck("preview_cards.id", &previewCardIDs).Error
	for _, previewCardID := range uniqueInt64s(previewCardIDs) {
		s.recordPreviewCardTrendUse(ctx, accountID, previewCardID, now)
	}
}

func (s *Server) refreshTagTrends(ctx context.Context, now time.Time) int {
	ids := s.tagTrendCandidateIDs(ctx, now)
	refreshed := 0
	for _, batch := range int64Batches(ids, trendBatchSize) {
		var tags []models.Tag
		if err := s.db.WithContext(ctx).Where("id IN ?", batch).Find(&tags).Error; err != nil {
			continue
		}
		refreshed += s.calculateTagTrendScores(ctx, tags, now)
	}
	_ = s.recalculateTagTrendRanks(ctx)
	return refreshed
}

func (s *Server) calculateTagTrendScores(ctx context.Context, tags []models.Tag, now time.Time) int {
	upserts := make([]models.TagTrend, 0, len(tags))
	deleteIDs := make([]int64, 0, len(tags))
	for _, tag := range tags {
		score := s.tagTrendScore(ctx, tag, now)
		if score < trendTagDecayThreshold {
			deleteIDs = append(deleteIDs, tag.ID)
			continue
		}
		upserts = append(upserts, models.TagTrend{
			TagID:    tag.ID,
			Score:    score,
			Allowed:  s.tagTrendAllowed(tag),
			Language: "",
		})
	}
	refreshed := 0
	if len(upserts) > 0 {
		if err := s.db.WithContext(ctx).Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "tag_id"}, {Name: "language"}},
			DoUpdates: clause.AssignmentColumns([]string{"score", "allowed"}),
		}).Create(&upserts).Error; err == nil {
			refreshed += len(upserts)
		}
	}
	if len(deleteIDs) > 0 {
		if err := s.db.WithContext(ctx).Where("tag_id IN ?", deleteIDs).Delete(&models.TagTrend{}).Error; err == nil {
			refreshed += len(deleteIDs)
		}
	}
	return refreshed
}

func (s *Server) tagTrendCandidateIDs(ctx context.Context, now time.Time) []int64 {
	seen := map[int64]struct{}{}
	out := make([]int64, 0)
	addText := func(values []string) {
		for _, value := range values {
			id, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
			if err == nil && id > 0 {
				if _, ok := seen[id]; !ok {
					seen[id] = struct{}{}
					out = append(out, id)
				}
			}
		}
	}
	addInt64 := func(values []int64) {
		for _, id := range values {
			if id > 0 {
				if _, ok := seen[id]; !ok {
					seen[id] = struct{}{}
					out = append(out, id)
				}
			}
		}
	}
	if value, err := s.redisCommand(ctx, "SMEMBERS", trendUsedKey(s.cfg.RedisNamespace, "trending_tags", now)); err == nil {
		if members, ok := redisStringArray(value); ok {
			addText(members)
		}
	}
	var existing []int64
	_ = s.db.WithContext(ctx).Model(&models.TagTrend{}).Pluck("tag_id", &existing).Error
	addInt64(existing)
	return out
}

func (s *Server) tagTrendScore(ctx context.Context, tag models.Tag, now time.Time) float64 {
	if !tagTrendUsable(tag) {
		return 0
	}
	today := truncateMetricTime(now, "day")
	yesterday := today.AddDate(0, 0, -1)
	expected := float64(s.tagTrendAccountCount(ctx, tag.ID, yesterday))
	if expected == 0 {
		expected = 1
	}
	observed := float64(s.tagTrendAccountCount(ctx, tag.ID, today))
	score := 0.0
	if observed >= trendTagThreshold && expected <= observed {
		score = math.Pow(observed-expected, 2) / expected
	}
	maxScore := tag.MaxScore.Float64
	maxTime := tag.MaxScoreAt.Time
	if !tag.MaxScoreAt.Valid || maxTime.Before(now.Add(-trendTagMaxScoreCooldown)) {
		maxScore = 0
	}
	if score > maxScore {
		maxScore = score
		maxTime = now
		if s != nil && s.db != nil {
			_ = s.db.WithContext(ctx).Model(&models.Tag{}).Where("id = ?", tag.ID).Updates(map[string]any{
				"max_score":    sql.NullFloat64{Float64: maxScore, Valid: true},
				"max_score_at": sql.NullTime{Time: maxTime, Valid: true},
			}).Error
		}
	}
	if maxScore == 0 {
		return 0
	}
	return maxScore * math.Pow(0.5, now.Sub(maxTime).Seconds()/trendTagMaxScoreHalflife.Seconds())
}

func (s *Server) tagTrendAccountCount(ctx context.Context, tagID int64, day time.Time) int64 {
	value, err := s.redisCommand(ctx, "PFCOUNT", tagHistoryRedisKey(s.cfg.RedisNamespace, tagID, day, true))
	if err != nil {
		return 0
	}
	if total, ok := value.(int64); ok {
		return total
	}
	return 0
}

func tagTrendUsable(tag models.Tag) bool {
	return !tag.Usable.Valid || tag.Usable.Bool
}

func (s *Server) tagTrendAllowed(tag models.Tag) bool {
	if tag.Trendable.Valid {
		return tag.Trendable.Bool
	}
	return s.settingBoolValue("trendable_by_default", false)
}

func trendUsedKey(redisPrefix string, prefix string, at time.Time) string {
	return redisPrefix + prefix + ":used:" + strconv.FormatInt(truncateMetricTime(at, "day").Unix(), 10)
}

func linkHistoryRedisKey(redisPrefix string, previewCardID int64, day time.Time, accounts bool) string {
	key := redisPrefix + "activity:links:" + strconv.FormatInt(previewCardID, 10) + ":" + strconv.FormatInt(truncateMetricTime(day, "day").Unix(), 10)
	if accounts {
		key += ":accounts"
	}
	return key
}

func int64sFromStrings(values []string) []int64 {
	out := make([]int64, 0, len(values))
	for _, value := range values {
		id, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err == nil && id > 0 {
			out = append(out, id)
		}
	}
	return out
}

func (s *Server) refreshStatusTrends(ctx context.Context, now time.Time) int {
	ids := s.statusTrendCandidateIDs(ctx, now)
	refreshed := 0
	for _, batch := range int64Batches(ids, trendBatchSize) {
		var statuses []models.Status
		if err := s.db.WithContext(ctx).
			Preload("StatusStat").
			Preload("Account").
			Preload("Quote.QuotedStatus.Account").
			Where("id IN ?", batch).
			Find(&statuses).Error; err != nil {
			continue
		}
		refreshed += s.calculateStatusTrendScores(ctx, statuses, now)
	}
	if refreshed > 0 {
		_ = s.recalculateStatusTrendRanks(ctx)
	}
	return refreshed
}

func (s *Server) statusTrendCandidateIDs(ctx context.Context, now time.Time) []int64 {
	seen := map[int64]struct{}{}
	out := make([]int64, 0)
	add := func(ids []int64) {
		for _, id := range ids {
			if id <= 0 {
				continue
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			out = append(out, id)
		}
	}
	var existing []int64
	_ = s.db.WithContext(ctx).Model(&models.StatusTrend{}).Pluck("status_id", &existing).Error
	add(existing)
	if value, err := s.redisCommand(ctx, "SMEMBERS", trendUsedKey(s.cfg.RedisNamespace, "trending_statuses", now)); err == nil {
		if members, ok := redisStringArray(value); ok {
			add(int64sFromStrings(members))
		}
	}
	var recent []int64
	_ = s.db.WithContext(ctx).
		Model(&models.Status{}).
		Select("statuses.id").
		Joins("JOIN status_stats ON status_stats.status_id = statuses.id").
		Joins("JOIN accounts ON accounts.id = statuses.account_id").
		Where("statuses.deleted_at IS NULL AND statuses.visibility = ? AND statuses.reblog_of_id IS NULL", 0).
		Where("statuses.created_at >= ?", now.Add(-7*24*time.Hour)).
		Where("(status_stats.reblogs_count + status_stats.favourites_count) >= ?", trendStatusThreshold).
		Where("accounts.discoverable = TRUE AND accounts.silenced_at IS NULL AND accounts.sensitized_at IS NULL").
		Limit(1000).
		Pluck("statuses.id", &recent).Error
	add(recent)
	return out
}

func (s *Server) calculateStatusTrendScores(ctx context.Context, statuses []models.Status, now time.Time) int {
	refreshed := 0
	deleteIDs := make([]int64, 0)
	upserts := make([]models.StatusTrend, 0)
	for _, status := range statuses {
		score := statusTrendScore(status, now)
		if score < trendStatusDecayThreshold {
			deleteIDs = append(deleteIDs, status.ID)
			continue
		}
		upserts = append(upserts, models.StatusTrend{
			StatusID:  status.ID,
			AccountID: status.AccountID,
			Score:     score,
			Allowed:   statusTrendAllowed(status),
			Language:  status.Language,
		})
	}
	if len(upserts) > 0 {
		if err := s.db.WithContext(ctx).Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "status_id"}},
			DoUpdates: clause.AssignmentColumns([]string{"account_id", "score", "allowed", "language"}),
		}).Create(&upserts).Error; err == nil {
			refreshed += len(upserts)
		}
	}
	if len(deleteIDs) > 0 {
		if err := s.db.WithContext(ctx).Where("status_id IN ?", deleteIDs).Delete(&models.StatusTrend{}).Error; err == nil {
			refreshed += len(deleteIDs)
		}
	}
	return refreshed
}

func statusTrendScore(status models.Status, now time.Time) float64 {
	if !statusTrendEligible(status) {
		return 0
	}
	observed := float64(status.StatusStat.ReblogsCount + status.StatusStat.FavouritesCount)
	if observed < trendStatusThreshold {
		return 0
	}
	score := math.Pow(observed-1, 2)
	age := now.Sub(status.CreatedAt)
	if age < 0 {
		age = 0
	}
	return score * math.Pow(0.5, age.Seconds()/trendStatusScoreHalflife.Seconds())
}

func statusTrendEligible(status models.Status) bool {
	return status.Visibility == 0 &&
		!status.DeletedAt.Valid &&
		!status.ReblogOfID.Valid &&
		!status.Sensitive &&
		strings.TrimSpace(status.SpoilerText) == "" &&
		!status.InReplyToID.Valid &&
		validTrendLocale(status.Language.String) &&
		status.Account.Discoverable.Valid && status.Account.Discoverable.Bool &&
		!status.Account.SilencedAt.Valid &&
		!status.Account.SensitizedAt.Valid &&
		statusTrendQuoteEligible(status.Quote)
}

func statusTrendQuoteEligible(quote *models.Quote) bool {
	if quote == nil {
		return true
	}
	if !statusQuoteAcceptable(quote) || quote.QuotedStatus == nil {
		return false
	}
	quoted := quote.QuotedStatus
	return quoted.Visibility == 0 &&
		quoted.Account.Discoverable.Valid && quoted.Account.Discoverable.Bool &&
		!quoted.Account.SilencedAt.Valid &&
		!quoted.Account.SensitizedAt.Valid &&
		!quoted.Sensitive &&
		strings.TrimSpace(quoted.SpoilerText) == ""
}

func statusTrendAllowed(status models.Status) bool {
	if status.Trendable.Valid {
		return status.Trendable.Bool
	}
	if status.Account.Trendable.Valid {
		return status.Account.Trendable.Bool
	}
	return false
}

func statusRequiresTrendReviewNotification(status models.Status) bool {
	return !status.Trendable.Valid &&
		!status.Account.ReviewedAt.Valid &&
		!status.Account.RequestedReviewAt.Valid
}

func (s *Server) refreshPreviewCardTrends(ctx context.Context, now time.Time) int {
	ids := s.previewCardTrendCandidateIDs(ctx, now)
	refreshed := 0
	for _, batch := range int64Batches(ids, trendBatchSize) {
		var cards []models.PreviewCard
		if err := s.db.WithContext(ctx).Where("id IN ?", batch).Find(&cards).Error; err != nil {
			continue
		}
		refreshed += s.calculatePreviewCardTrendScores(ctx, cards, now)
	}
	if refreshed > 0 {
		_ = s.recalculatePreviewCardTrendRanks(ctx)
	}
	return refreshed
}

func (s *Server) previewCardTrendCandidateIDs(ctx context.Context, now time.Time) []int64 {
	seen := map[int64]struct{}{}
	out := make([]int64, 0)
	add := func(ids []int64) {
		for _, id := range ids {
			if id <= 0 {
				continue
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			out = append(out, id)
		}
	}
	var existing []int64
	_ = s.db.WithContext(ctx).Model(&models.PreviewCardTrend{}).Pluck("preview_card_id", &existing).Error
	add(existing)
	if value, err := s.redisCommand(ctx, "SMEMBERS", trendUsedKey(s.cfg.RedisNamespace, "trending_links", now)); err == nil {
		if members, ok := redisStringArray(value); ok {
			add(int64sFromStrings(members))
		}
	}
	var recent []int64
	_ = s.db.WithContext(ctx).
		Table("preview_cards").
		Select("preview_cards.id").
		Joins("JOIN preview_cards_statuses ON preview_cards_statuses.preview_card_id = preview_cards.id").
		Joins("JOIN statuses ON statuses.id = preview_cards_statuses.status_id").
		Joins("JOIN accounts ON accounts.id = statuses.account_id").
		Where("statuses.deleted_at IS NULL AND statuses.visibility = ?", 0).
		Where("statuses.created_at >= ?", now.Add(-24*time.Hour)).
		Where("accounts.silenced_at IS NULL").
		Where("preview_cards.title <> ''").
		Group("preview_cards.id").
		Having("COUNT(DISTINCT statuses.account_id) >= ?", trendLinkThreshold).
		Limit(1000).
		Pluck("preview_cards.id", &recent).Error
	add(recent)
	return out
}

func (s *Server) calculatePreviewCardTrendScores(ctx context.Context, cards []models.PreviewCard, now time.Time) int {
	refreshed := 0
	deleteIDs := make([]int64, 0)
	upserts := make([]models.PreviewCardTrend, 0)
	for _, card := range cards {
		score := s.previewCardTrendScore(ctx, card, now)
		if score < trendLinkDecayThreshold {
			deleteIDs = append(deleteIDs, card.ID)
			continue
		}
		upserts = append(upserts, models.PreviewCardTrend{
			PreviewCardID: card.ID,
			Score:         score,
			Allowed:       previewCardTrendAllowed(card),
			Language:      card.Language,
		})
	}
	if len(upserts) > 0 {
		if err := s.db.WithContext(ctx).Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "preview_card_id"}},
			DoUpdates: clause.AssignmentColumns([]string{"score", "allowed", "language"}),
		}).Create(&upserts).Error; err == nil {
			refreshed += len(upserts)
		}
	}
	if len(deleteIDs) > 0 {
		if err := s.db.WithContext(ctx).Where("preview_card_id IN ?", deleteIDs).Delete(&models.PreviewCardTrend{}).Error; err == nil {
			refreshed += len(deleteIDs)
		}
	}
	return refreshed
}

func (s *Server) previewCardTrendScore(ctx context.Context, card models.PreviewCard, now time.Time) float64 {
	if strings.TrimSpace(card.Title) == "" || !validTrendLocale(card.Language.String) {
		return 0
	}
	expected, observed := s.previewCardTrendRedisCounts(ctx, card.ID, now)
	if observed == 0 {
		expected = s.previewCardTrendAccountCount(ctx, card.ID, now.Add(-24*time.Hour), now)
		observed = s.previewCardTrendAccountCount(ctx, card.ID, now, now.Add(24*time.Hour))
	}
	expectedScore := float64(expected)
	if expectedScore == 0 {
		expectedScore = 1
	}
	observedScore := float64(observed)
	score := 0.0
	if observedScore >= trendLinkThreshold && expectedScore <= observedScore {
		score = math.Pow(observedScore-expectedScore, 2) / expectedScore
	}
	maxScore := card.MaxScore.Float64
	maxTime := card.MaxScoreAt.Time
	if !card.MaxScoreAt.Valid || maxTime.Before(now.Add(-trendLinkMaxScoreCooldown)) {
		maxScore = 0
	}
	if score > maxScore {
		maxScore = score
		maxTime = now
		_ = s.db.WithContext(ctx).Model(&models.PreviewCard{}).Where("id = ?", card.ID).Updates(map[string]any{
			"max_score":    sql.NullFloat64{Float64: maxScore, Valid: true},
			"max_score_at": sql.NullTime{Time: maxTime, Valid: true},
		}).Error
	}
	if maxScore == 0 {
		return 0
	}
	return maxScore * math.Pow(0.5, now.Sub(maxTime).Seconds()/trendLinkMaxScoreHalflife.Seconds())
}

func (s *Server) previewCardTrendRedisCounts(ctx context.Context, cardID int64, now time.Time) (int64, int64) {
	expectedValue, expectedErr := s.redisCommand(ctx, "PFCOUNT", linkHistoryRedisKey(s.cfg.RedisNamespace, cardID, now.Add(-24*time.Hour), true))
	observedValue, observedErr := s.redisCommand(ctx, "PFCOUNT", linkHistoryRedisKey(s.cfg.RedisNamespace, cardID, now, true))
	if expectedErr != nil || observedErr != nil {
		return 0, 0
	}
	return redisInt(expectedValue), redisInt(observedValue)
}

func (s *Server) previewCardTrendAccountCount(ctx context.Context, cardID int64, start time.Time, end time.Time) int64 {
	var count int64
	_ = s.db.WithContext(ctx).
		Table("statuses").
		Joins("JOIN preview_cards_statuses ON preview_cards_statuses.status_id = statuses.id").
		Where("preview_cards_statuses.preview_card_id = ?", cardID).
		Where("statuses.deleted_at IS NULL AND statuses.visibility = ?", 0).
		Where("statuses.created_at >= ? AND statuses.created_at < ?", start, end).
		Distinct("statuses.account_id").
		Count(&count).Error
	return count
}

func previewCardTrendAllowed(card models.PreviewCard) bool {
	return card.Trendable.Valid && card.Trendable.Bool
}

func (s *Server) recalculateStatusTrendRanks(ctx context.Context) error {
	return s.db.WithContext(ctx).Exec(`UPDATE status_trends SET rank = t0.calculated_rank FROM (SELECT id, row_number() OVER w AS calculated_rank FROM status_trends WINDOW w AS (PARTITION BY language ORDER BY score DESC)) t0 WHERE status_trends.id = t0.id`).Error
}

func (s *Server) recalculateTagTrendRanks(ctx context.Context) error {
	return s.db.WithContext(ctx).Exec(`UPDATE tag_trends SET rank = t0.calculated_rank FROM (SELECT id, row_number() OVER w AS calculated_rank FROM tag_trends WINDOW w AS (PARTITION BY language ORDER BY score DESC)) t0 WHERE tag_trends.id = t0.id`).Error
}

func (s *Server) recalculatePreviewCardTrendRanks(ctx context.Context) error {
	return s.db.WithContext(ctx).Exec(`UPDATE preview_card_trends SET rank = t0.calculated_rank FROM (SELECT id, row_number() OVER w AS calculated_rank FROM preview_card_trends WINDOW w AS (PARTITION BY language ORDER BY score DESC)) t0 WHERE preview_card_trends.id = t0.id`).Error
}

func validTrendLocale(locale string) bool {
	return strings.TrimSpace(locale) != ""
}

func int64Batches(values []int64, size int) [][]int64 {
	if size <= 0 || len(values) == 0 {
		return nil
	}
	out := make([][]int64, 0, (len(values)+size-1)/size)
	for len(values) > 0 {
		n := size
		if len(values) < n {
			n = len(values)
		}
		out = append(out, values[:n])
		values = values[n:]
	}
	return out
}

func (s *Server) requestTrendsReview(ctx context.Context, now time.Time) int {
	if s == nil || s.db == nil || !s.trendsEnabled() || s.settingBoolValue("trendable_by_default", false) {
		return 0
	}
	items := trendsReviewMailItems{}
	items.Tags = s.requestTagTrendReviews(ctx, now)
	items.Statuses = s.requestStatusTrendReviews(ctx, now)
	items.Links = s.requestPreviewCardTrendReviews(ctx, now)
	touched := len(items.Tags) + len(items.Statuses) + len(items.Links)
	if touched > 0 {
		_ = s.sendTrendsReviewMails(items)
	}
	return touched
}

func (s *Server) requestTagTrendReviews(ctx context.Context, now time.Time) []trendsReviewTag {
	threshold := s.tagTrendReviewScore(ctx)
	var trends []models.TagTrend
	if err := s.db.WithContext(ctx).
		Preload("Tag").
		Where("allowed = ? AND score > ?", false, threshold).
		Find(&trends).Error; err != nil {
		return nil
	}
	reviewTags := make([]trendsReviewTag, 0, len(trends))
	for _, trend := range trends {
		tag := trend.Tag
		if s.tagTrendAllowed(tag) || tag.ReviewedAt.Valid || tag.RequestedReviewAt.Valid {
			continue
		}
		err := s.db.WithContext(ctx).Model(&models.Tag{}).Where("id = ?", tag.ID).Updates(map[string]any{
			"requested_review_at": now,
			"updated_at":          now,
		}).Error
		if err == nil {
			reviewTags = append(reviewTags, trendsReviewTag{Name: trendReviewTagName(tag), Score: trend.Score})
		}
	}
	return reviewTags
}

func (s *Server) tagTrendReviewScore(ctx context.Context) float64 {
	var trend models.TagTrend
	err := s.db.WithContext(ctx).
		Where("allowed = ? AND rank <= ?", true, trendReviewThresholdRank).
		Order("rank DESC").
		First(&trend).Error
	if err != nil {
		return 0
	}
	return trend.Score
}

func (s *Server) requestStatusTrendReviews(ctx context.Context, now time.Time) []trendsReviewStatus {
	var trends []models.StatusTrend
	if err := s.db.WithContext(ctx).Preload("Status.Account").Where("allowed = ?", false).Find(&trends).Error; err != nil {
		return nil
	}
	reviewStatuses := make([]trendsReviewStatus, 0, len(trends))
	for _, trend := range trends {
		if trend.Score <= s.statusTrendReviewScore(ctx, trend.Language) ||
			statusTrendAllowed(trend.Status) ||
			!statusRequiresTrendReviewNotification(trend.Status) {
			continue
		}
		if err := s.db.WithContext(ctx).Model(&models.Account{}).Where("id = ?", trend.AccountID).Update("requested_review_at", now).Error; err == nil {
			reviewStatuses = append(reviewStatuses, trendsReviewStatus{
				URL:      adminTrendsStatusURL(s.cfg.BaseURL(), trend.Status),
				Language: trendReviewLanguage(trend.Language),
				Score:    trend.Score,
			})
		}
	}
	return reviewStatuses
}

func (s *Server) statusTrendReviewScore(ctx context.Context, language sql.NullString) float64 {
	var trend models.StatusTrend
	query := s.db.WithContext(ctx).Where("allowed = ?", true).Where("rank <= ?", trendReviewThresholdRank).Order("rank DESC")
	if language.Valid {
		query = query.Where("language = ?", language.String)
	} else {
		query = query.Where("language IS NULL")
	}
	if err := query.First(&trend).Error; err != nil {
		return 0
	}
	return trend.Score
}

func (s *Server) requestPreviewCardTrendReviews(ctx context.Context, now time.Time) []trendsReviewLink {
	var trends []models.PreviewCardTrend
	if err := s.db.WithContext(ctx).Preload("PreviewCard").Where("allowed = ?", false).Find(&trends).Error; err != nil {
		return nil
	}
	reviewLinks := make([]trendsReviewLink, 0, len(trends))
	for _, trend := range trends {
		if trend.Score <= s.previewCardTrendReviewScore(ctx, trend.Language) || previewCardTrendAllowed(trend.PreviewCard) {
			continue
		}
		domain := previewCardDomain(trend.PreviewCard)
		if domain == "" {
			continue
		}
		var provider models.PreviewCardProvider
		err := s.db.WithContext(ctx).Where("domain = ?", domain).First(&provider).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			provider = models.PreviewCardProvider{Domain: domain, RequestedReviewAt: sql.NullTime{Time: now, Valid: true}, CreatedAt: now, UpdatedAt: now}
			err = s.db.WithContext(ctx).Create(&provider).Error
		} else if err == nil && !provider.ReviewedAt.Valid && !provider.RequestedReviewAt.Valid {
			err = s.db.WithContext(ctx).Model(&models.PreviewCardProvider{}).Where("id = ?", provider.ID).Updates(map[string]any{"requested_review_at": now, "updated_at": now}).Error
		} else if err == nil {
			continue
		}
		if err == nil {
			reviewLinks = append(reviewLinks, trendsReviewLink{
				Title:    trend.PreviewCard.Title,
				URL:      trend.PreviewCard.URL,
				Language: trendReviewLanguage(trend.Language),
				Score:    trend.Score,
			})
		}
	}
	return reviewLinks
}

func (s *Server) previewCardTrendReviewScore(ctx context.Context, language sql.NullString) float64 {
	var trend models.PreviewCardTrend
	query := s.db.WithContext(ctx).Where("allowed = ?", true).Where("rank <= ?", trendReviewThresholdRank).Order("rank DESC")
	if language.Valid {
		query = query.Where("language = ?", language.String)
	} else {
		query = query.Where("language IS NULL")
	}
	if err := query.First(&trend).Error; err != nil {
		return 0
	}
	return trend.Score
}

func previewCardDomain(card models.PreviewCard) string {
	for _, raw := range []string{card.ProviderURL, card.URL} {
		if domain := domainFromURL(raw); domain != "" {
			return domain
		}
	}
	return ""
}

func trendReviewTagName(tag models.Tag) string {
	if tag.DisplayName.Valid && strings.TrimSpace(tag.DisplayName.String) != "" {
		return strings.TrimSpace(tag.DisplayName.String)
	}
	return strings.TrimSpace(tag.Name)
}

func trendReviewLanguage(language sql.NullString) string {
	if !language.Valid || strings.TrimSpace(language.String) == "" {
		return ""
	}
	return railsStandardLocaleName(strings.TrimSpace(language.String))
}

func domainFromURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" {
		return ""
	}
	return normalizeDomain(parsed.Hostname())
}
