package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"github.com/mstdn-plusminus-io/paon/internal/paon/serializer"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type trendPreviewCardRef struct {
	ID       int64
	Uses     int64
	Accounts int64
}

func (s *Server) trendingLinks(c *echo.Context) error {
	c.Response().Header().Set("Vary", "Authorization, Accept-Language")
	publicRESTCacheIfUnauthenticated(c, 15)
	if s.db == nil || !s.trendsEnabled() {
		return c.JSON(http.StatusOK, []any{})
	}
	now := time.Now().UTC()
	offsetValue := offset(c)
	limitValue := limit(c, 10, 20)
	refs, err := s.trendingPreviewCardRefs(limitValue, offsetValue, now, s.trendPreferredLanguages(c))
	if err != nil {
		return err
	}
	if len(refs) == 0 {
		return c.JSON(http.StatusOK, []serializer.PreviewCardTrendLink{})
	}

	ids := make([]int64, 0, len(refs))
	for _, ref := range refs {
		ids = append(ids, ref.ID)
	}
	var cards []models.PreviewCard
	if err := s.db.Where("id IN ?", ids).Find(&cards).Error; err != nil {
		return err
	}
	byID := make(map[int64]models.PreviewCard, len(cards))
	for _, card := range cards {
		byID[card.ID] = card
	}

	out := make([]serializer.PreviewCardTrendLink, 0, len(refs))
	for _, ref := range refs {
		card, ok := byID[ref.ID]
		if !ok {
			continue
		}
		out = append(out, serializer.PreviewCardTrendLinkFromModelWithHistory(s.cfg, card, s.linkHistory((*c).Request().Context(), card.ID, now)))
	}
	if len(out) > 0 {
		setPaginationLinkHeader(c, offsetPaginationLinkWithAllowedParams(c, offsetValue, limitValue, len(out), []string{"limit"}))
	}
	return c.JSON(http.StatusOK, out)
}

func (s *Server) trendingStatuses(c *echo.Context) error {
	c.Response().Header().Set("Vary", "Authorization, Accept-Language")
	publicRESTCacheIfUnauthenticated(c, 15)
	if s.db == nil || !s.trendsEnabled() {
		return c.JSON(http.StatusOK, []any{})
	}
	account, _, _ := s.currentAccount(c)
	query := s.statusQuery().
		Joins("JOIN status_trends ON status_trends.status_id = statuses.id").
		Joins("JOIN (SELECT account_id, MAX(score) AS max_score FROM status_trends GROUP BY account_id) grouped_status_trends ON status_trends.account_id = grouped_status_trends.account_id AND status_trends.score = grouped_status_trends.max_score").
		Where("statuses.deleted_at IS NULL").
		Where("status_trends.allowed = ?", true)
	query = applyTrendStatusAccountFilters(query, account)
	preferredLanguages := s.trendPreferredLanguages(c)
	offsetValue := offset(c)
	query = applyTrendLanguageOrder(query, "status_trends.language", preferredLanguages)
	query = query.Order("status_trends.score DESC").
		Offset(offsetValue)
	limitValue := limit(c, 20, 40)
	query = query.Limit(limitValue)
	var statuses []models.Status
	if err := query.Find(&statuses).Error; err != nil {
		return err
	}
	if len(statuses) > 0 {
		setPaginationLinkHeader(c, offsetPaginationLinkWithAllowedParams(c, offsetValue, limitValue, len(statuses), []string{"limit"}))
	}
	if err := s.hydrateStatusRelationships(statuses, account); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, serializeStatusesWithFilterContext(s.cfg, statuses, account, s.accountFilters(account), statusListFilterContext(c)))
}

func applyTrendStatusAccountFilters(query *gorm.DB, account *models.Account) *gorm.DB {
	if account == nil || account.ID == 0 {
		return query
	}
	return query.
		Where(`NOT EXISTS (
			SELECT 1 FROM blocks trend_status_blocks
			WHERE trend_status_blocks.account_id = ?
			  AND trend_status_blocks.target_account_id = statuses.account_id
		)`, account.ID).
		Where(`NOT EXISTS (
			SELECT 1 FROM blocks trend_status_blocked_by
			WHERE trend_status_blocked_by.target_account_id = ?
			  AND trend_status_blocked_by.account_id = statuses.account_id
		)`, account.ID).
		Where(`NOT EXISTS (
			SELECT 1 FROM mutes trend_status_mutes
			WHERE trend_status_mutes.account_id = ?
			  AND trend_status_mutes.target_account_id = statuses.account_id
		)`, account.ID).
		Where(`NOT EXISTS (
			SELECT 1
			FROM account_domain_blocks trend_status_domain_blocks
			JOIN accounts trend_status_accounts ON trend_status_accounts.id = statuses.account_id
			WHERE trend_status_domain_blocks.account_id = ?
			  AND trend_status_accounts.domain IS NOT NULL
			  AND lower(trend_status_domain_blocks.domain) = lower(trend_status_accounts.domain)
		)`, account.ID)
}

func (s *Server) trendingPreviewCardRefs(limitValue int, offsetValue int, now time.Time, preferredLanguages []string) ([]trendPreviewCardRef, error) {
	var refs []trendPreviewCardRef
	since := now.AddDate(0, 0, -7)
	query := s.db.Table("preview_card_trends").
		Select("preview_cards.id, COUNT(statuses.id) AS uses, COUNT(DISTINCT statuses.account_id) AS accounts").
		Joins("JOIN preview_cards ON preview_cards.id = preview_card_trends.preview_card_id").
		Joins("LEFT JOIN preview_cards_statuses ON preview_cards_statuses.preview_card_id = preview_cards.id").
		Joins("LEFT JOIN statuses ON statuses.id = preview_cards_statuses.status_id AND statuses.deleted_at IS NULL AND statuses.visibility IN ? AND statuses.created_at >= ?", []int{0, 1}, since).
		Where("preview_card_trends.allowed = ?", true).
		Group("preview_cards.id, preview_card_trends.score")
	query = applyTrendLanguageOrder(query, "preview_card_trends.language", preferredLanguages)
	err := query.
		Order("preview_card_trends.score DESC").
		Offset(offsetValue).
		Limit(limitValue).
		Scan(&refs).Error
	return refs, err
}

func (s *Server) trendPreferredLanguages(c *echo.Context) []string {
	if s != nil && s.db != nil && requestToken(c) != "" {
		if user, _, err := s.currentUser(c); err == nil {
			if languages := normalizeTrendLanguages([]string(user.ChosenLanguages)); len(languages) > 0 {
				return languages
			}
		}
	}
	return normalizeTrendLanguages([]string{acceptLanguageCandidate((*c).Request().Header.Get("Accept-Language"))})
}

func normalizeTrendLanguages(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		language := normalizeContentLocale(value)
		if language == "" {
			continue
		}
		if _, ok := seen[language]; ok {
			continue
		}
		seen[language] = struct{}{}
		out = append(out, language)
	}
	return out
}

func applyTrendLanguageOrder(query *gorm.DB, column string, preferredLanguages []string) *gorm.DB {
	column = strings.TrimSpace(column)
	if query == nil || column == "" || len(preferredLanguages) == 0 {
		return query
	}
	return query.Order(clause.Expr{
		SQL:  "CASE WHEN " + column + " IN (?) THEN 1 ELSE 0 END DESC",
		Vars: []any{preferredLanguages},
	})
}

func (s *Server) trendsEnabled() bool {
	return s.settingBoolValue("trends", true)
}
