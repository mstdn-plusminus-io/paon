package api

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
)

const (
	followRecommendationsWorkerInterval = 24 * time.Hour
	followRecommendationsSetSize        = 100
)

type followRecommendationRef struct {
	AccountID int64
	Rank      float64
}

func (s *Server) runFollowRecommendationsWorker(ctx context.Context) {
	ticker := time.NewTicker(followRecommendationsWorkerInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runSchedulerWithRedisLock(ctx, "follow_recommendations_scheduler", 24*time.Hour, func() {
				s.refreshFollowRecommendations(ctx)
			})
		}
	}
}

func (s *Server) refreshFollowRecommendations(ctx context.Context) int {
	if s == nil || s.db == nil {
		return 0
	}
	if err := s.refreshFollowRecommendationMaterializedViews(ctx); err != nil {
		return 0
	}
	fallback, err := s.followRecommendationRefs(ctx, "", followRecommendationsSetSize)
	if err != nil {
		return 0
	}
	written := 0
	for _, locale := range railsI18nAvailableLocales {
		recommendations, err := s.followRecommendationRefs(ctx, locale, followRecommendationsSetSize)
		if err != nil {
			continue
		}
		recommendations = mergeFollowRecommendationFallbacks(recommendations, fallback, followRecommendationsSetSize)
		if s.writeFollowRecommendationRedisSet(ctx, locale, recommendations) == nil {
			written += len(recommendations)
		}
	}
	return written
}

func (s *Server) refreshFollowRecommendationMaterializedViews(ctx context.Context) error {
	if err := s.db.WithContext(ctx).Exec("REFRESH MATERIALIZED VIEW account_summaries").Error; err != nil {
		return err
	}
	return s.db.WithContext(ctx).Exec("REFRESH MATERIALIZED VIEW global_follow_recommendations").Error
}

func (s *Server) followRecommendationRefs(ctx context.Context, locale string, limitValue int) ([]followRecommendationRef, error) {
	if limitValue <= 0 {
		return []followRecommendationRef{}, nil
	}
	query := s.db.WithContext(ctx).
		Table("global_follow_recommendations").
		Select("global_follow_recommendations.account_id, global_follow_recommendations.rank").
		Order("global_follow_recommendations.rank DESC").
		Limit(limitValue)
	if strings.TrimSpace(locale) != "" {
		query = query.Joins("JOIN account_summaries ON account_summaries.account_id = global_follow_recommendations.account_id").
			Where("account_summaries.language = ?", locale)
	}
	var refs []followRecommendationRef
	err := query.Scan(&refs).Error
	return refs, err
}

func mergeFollowRecommendationFallbacks(localized []followRecommendationRef, fallback []followRecommendationRef, limitValue int) []followRecommendationRef {
	if limitValue <= 0 {
		return []followRecommendationRef{}
	}
	out := append([]followRecommendationRef{}, localized...)
	if len(out) > limitValue {
		return out[:limitValue]
	}
	missing := limitValue - len(out)
	if missing <= 0 || len(fallback) == 0 {
		return out
	}
	maxFallbackRank := fallback[0].Rank
	seen := make(map[int64]struct{}, len(out))
	for i := range out {
		out[i].Rank += maxFallbackRank
		seen[out[i].AccountID] = struct{}{}
	}
	for _, ref := range fallback {
		if _, ok := seen[ref.AccountID]; ok {
			continue
		}
		out = append(out, ref)
		seen[ref.AccountID] = struct{}{}
		missing--
		if missing <= 0 {
			break
		}
	}
	return out
}

func (s *Server) writeFollowRecommendationRedisSet(ctx context.Context, locale string, recommendations []followRecommendationRef) error {
	key := followRecommendationsRedisKey(s.cfg.RedisNamespace, locale)
	if _, err := s.redisCommand(ctx, "DEL", key); err != nil {
		return err
	}
	if len(recommendations) == 0 {
		return nil
	}
	args := []string{"ZADD", key}
	for _, recommendation := range recommendations {
		args = append(args, strconv.FormatFloat(recommendation.Rank, 'f', -1, 64), strconv.FormatInt(recommendation.AccountID, 10))
	}
	_, err := s.redisCommand(ctx, args...)
	return err
}

func requestSuggestionLocale(c *echo.Context, fallback string) string {
	locale := acceptLanguageCandidate((*c).Request().Header.Get("Accept-Language"))
	if locale == "" {
		locale = fallback
	}
	return followRecommendationLocale(locale)
}

func followRecommendationLocale(locale string) string {
	locale = strings.TrimSpace(locale)
	if locale == "" {
		return "en"
	}
	for _, sep := range []string{"-", "_"} {
		if idx := strings.Index(locale, sep); idx > 0 {
			return locale[:idx]
		}
	}
	return locale
}
