package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"github.com/mstdn-plusminus-io/paon/internal/paon/serializer"
	"gorm.io/gorm"
)

const (
	accountsByIDLimit = 40
	statusesByIDLimit = 20
)

func (s *Server) accountsByID(c *echo.Context) error {
	c.Response().Header().Set("Vary", "Authorization")
	if err := s.authorizeTokenScopeIfPresent(c, "read", "read:accounts"); err != nil {
		return err
	}
	ids := uniquePositiveQueryIDs(c, "id[]", "id")
	if len(ids) > accountsByIDLimit {
		return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Ids is too long")
	}
	if s.db == nil || len(ids) == 0 {
		return c.JSON(http.StatusOK, []serializer.Account{})
	}
	var accounts []models.Account
	if err := accountSerializerPreloads(s.db).Where("accounts.id IN ?", ids).Find(&accounts).Error; err != nil {
		return err
	}
	out := make([]serializer.Account, 0, len(accounts))
	for i := range accounts {
		if accountHiddenFromAccountsShow(&accounts[i]) {
			continue
		}
		if err := s.hydrateAccountCustomEmojis(&accounts[i]); err != nil {
			return err
		}
		out = append(out, serializer.AccountFromModel(s.cfg, accounts[i]))
	}
	return c.JSON(http.StatusOK, out)
}

func (s *Server) statusesByID(c *echo.Context) error {
	c.Response().Header().Set("Vary", "Authorization")
	if err := s.authorizeTokenScopeIfPresent(c, "read", "read:statuses"); err != nil {
		return err
	}
	ids := uniquePositiveQueryIDs(c, "id[]", "id")
	if len(ids) > statusesByIDLimit {
		return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Ids is too long")
	}
	if s.db == nil || len(ids) == 0 {
		return c.JSON(http.StatusOK, []serializer.Status{})
	}
	current, err := s.currentAccountForOptionalRequestToken(c)
	if err != nil {
		return err
	}
	var statuses []models.Status
	if err := s.visibleStatusQuery(current).Where("statuses.id IN ?", ids).Find(&statuses).Error; err != nil {
		return err
	}
	if err := s.hydrateStatusRelationships(statuses, current); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, serializeStatusesWithFilterContext(s.cfg, statuses, current, s.accountFilters(current), "thread"))
}

func uniquePositiveQueryIDs(c *echo.Context, keys ...string) []int64 {
	seen := map[int64]struct{}{}
	out := []int64{}
	for _, key := range keys {
		for _, raw := range c.QueryParams()[key] {
			id := railsToInt64(raw)
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
	return out
}

func (s *Server) domainBlockPreview(c *echo.Context) error {
	account, _, err := s.requireAccountScope(c, "follow", "write", "write:blocks")
	if err != nil {
		return err
	}
	domain := normalizeDomain(c.QueryParam("domain"))
	if domain == "" {
		return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Domain is invalid")
	}
	var followingCount int64
	if err := s.db.Model(&models.Follow{}).
		Joins("JOIN accounts domain_block_preview_following ON domain_block_preview_following.id = follows.target_account_id").
		Where("follows.account_id = ? AND lower(domain_block_preview_following.domain) = ?", account.ID, domain).
		Count(&followingCount).Error; err != nil {
		return err
	}
	var followersCount int64
	if err := s.db.Model(&models.Follow{}).
		Joins("JOIN accounts domain_block_preview_followers ON domain_block_preview_followers.id = follows.account_id").
		Where("follows.target_account_id = ? AND lower(domain_block_preview_followers.domain) = ?", account.ID, domain).
		Count(&followersCount).Error; err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]int64{
		"following_count": followingCount,
		"followers_count": followersCount,
	})
}

func (s *Server) linkTimeline(c *echo.Context) error {
	c.Response().Header().Set("Vary", "Authorization")
	publicRESTCacheIfUnauthenticated(c, 15)
	if err := s.authorizeTokenScopeIfPresent(c, "read", "read:statuses"); err != nil {
		return err
	}
	if err := s.requireTimelinePreviewAccess(c); err != nil {
		return err
	}
	current, err := s.currentAccountForOptionalRequestToken(c)
	if err != nil {
		return err
	}
	var card models.PreviewCard
	if err := s.db.Joins("JOIN preview_card_trends ON preview_card_trends.preview_card_id = preview_cards.id AND preview_card_trends.allowed = true").
		Where("preview_cards.url = ?", c.QueryParam("url")).First(&card).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apiError(c, http.StatusNotFound, "Record not found")
		}
		return err
	}
	query := s.publicTimelineStatusQuery().
		Where("COALESCE(timeline_accounts.discoverable, FALSE) = TRUE").
		Joins("JOIN preview_cards_statuses ON preview_cards_statuses.status_id = statuses.id").
		Where("preview_cards_statuses.preview_card_id = ?", card.ID)
	query = applyPublicTimelineAccountFilters(query, current, false)
	return s.statusList(c, query)
}

type annualReportEntity struct {
	Year          int             `json:"year"`
	Data          json.RawMessage `json:"data"`
	SchemaVersion int             `json:"schema_version"`
}

func (s *Server) annualReports(c *echo.Context) error {
	account, _, err := s.requireAccountScope(c, "read", "read:accounts")
	if err != nil {
		return err
	}
	var reports []models.GeneratedAnnualReport
	if err := s.db.Where("account_id = ? AND viewed_at IS NULL", account.ID).Find(&reports).Error; err != nil {
		return err
	}
	accountIDs, statusIDs := annualReportReferencedIDs(reports)
	accounts, err := s.annualReportAccounts(accountIDs)
	if err != nil {
		return err
	}
	statuses, err := s.annualReportStatuses(statusIDs, account)
	if err != nil {
		return err
	}
	reportEntities := make([]annualReportEntity, 0, len(reports))
	for _, report := range reports {
		data := json.RawMessage(report.Data)
		if !json.Valid(data) {
			data = json.RawMessage(`{}`)
		}
		reportEntities = append(reportEntities, annualReportEntity{Year: report.Year, Data: data, SchemaVersion: report.SchemaVersion})
	}
	return c.JSON(http.StatusOK, map[string]any{
		"annual_reports": reportEntities,
		"accounts":       accounts,
		"statuses":       statuses,
	})
}

func (s *Server) readAnnualReport(c *echo.Context) error {
	account, _, err := s.requireAccountScope(c, "write", "write:accounts")
	if err != nil {
		return err
	}
	year, err := strconv.Atoi(c.Param("year"))
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	result := s.db.Model(&models.GeneratedAnnualReport{}).
		Where("account_id = ? AND year = ?", account.ID, year).
		Update("viewed_at", time.Now().UTC())
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	return renderEmpty(c)
}

func annualReportReferencedIDs(reports []models.GeneratedAnnualReport) ([]int64, []int64) {
	accountIDs := []int64{}
	statusIDs := []int64{}
	for _, report := range reports {
		var data map[string]any
		if json.Unmarshal(report.Data, &data) != nil {
			continue
		}
		for _, key := range []string{"most_reblogged_accounts", "commonly_interacted_with_accounts"} {
			for _, item := range anySlice(data[key]) {
				if row, ok := item.(map[string]any); ok {
					if id := anyPositiveInt64(row["account_id"]); id > 0 {
						accountIDs = append(accountIDs, id)
					}
				}
			}
		}
		if top, ok := data["top_statuses"].(map[string]any); ok {
			for _, value := range top {
				if id := anyPositiveInt64(value); id > 0 {
					statusIDs = append(statusIDs, id)
				}
			}
		}
	}
	return uniqueInt64s(accountIDs), uniqueInt64s(statusIDs)
}

func anySlice(value any) []any {
	if out, ok := value.([]any); ok {
		return out
	}
	return nil
}

func anyPositiveInt64(value any) int64 {
	var out int64
	switch typed := value.(type) {
	case string:
		out = railsToInt64(typed)
	case float64:
		out = int64(typed)
	case json.Number:
		out, _ = typed.Int64()
	}
	if out <= 0 {
		return 0
	}
	return out
}

func (s *Server) annualReportAccounts(ids []int64) ([]serializer.Account, error) {
	if len(ids) == 0 {
		return []serializer.Account{}, nil
	}
	var rows []models.Account
	if err := accountSerializerPreloads(s.db).Where("accounts.id IN ?", ids).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]serializer.Account, 0, len(rows))
	for i := range rows {
		if err := s.hydrateAccountCustomEmojis(&rows[i]); err != nil {
			return nil, err
		}
		out = append(out, serializer.AccountFromModel(s.cfg, rows[i]))
	}
	return out, nil
}

func (s *Server) annualReportStatuses(ids []int64, current *models.Account) ([]serializer.Status, error) {
	if len(ids) == 0 {
		return []serializer.Status{}, nil
	}
	var rows []models.Status
	if err := s.statusQuery().Where("statuses.id IN ? AND statuses.deleted_at IS NULL", ids).Find(&rows).Error; err != nil {
		return nil, err
	}
	if err := s.hydrateStatusRelationships(rows, current); err != nil {
		return nil, err
	}
	return serializeStatusesWithFilterContext(s.cfg, rows, current, s.accountFilters(current), "thread"), nil
}

func inviteWantsJSON(c *echo.Context) bool {
	if strings.EqualFold(c.Param("format"), "json") || strings.HasSuffix(strings.ToLower(c.Request().URL.Path), ".json") {
		return true
	}
	return strings.Contains(strings.ToLower(c.Request().Header.Get("Accept")), "application/json")
}
