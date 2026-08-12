package api

import (
	"context"
	"database/sql"
	"html"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"github.com/mstdn-plusminus-io/paon/internal/paon/serializer"
	"gorm.io/gorm"
)

const adminRailsDefaultPageSize = 40

func (s *Server) adminTrendsTagsPage(c *echo.Context) error {
	user, handled, err := s.requireAdminTrendsWebUser(c)
	if handled || err != nil {
		return err
	}
	tags, err := s.adminTrendsTagWebRecords(c)
	if err != nil {
		return err
	}
	return c.HTML(http.StatusOK, adminTrendsTagsHTML(tags, c.QueryParam("notice"), c.QueryParam("error"), adminTrendsTagStatus(c), adminTrendsPageValue(c), s.webLocale(c, user)))
}

func (s *Server) batchAdminTrendsTags(c *echo.Context) error {
	user, handled, err := s.requireAdminTrendsWebUser(c)
	if handled || err != nil {
		return err
	}
	ids := parseAdminTrendsTagIDs(c)
	loc := s.webLocale(c, user)
	if len(ids) == 0 {
		return c.Redirect(http.StatusFound, "/admin/trends/tags?"+adminTrendsTagRedirectQuery(c, "error", adminT(loc, "admin.trends.tags.no_tag_selected", "No tags were changed as none were selected")))
	}
	action := adminTrendsTagBatchAction(c)
	if action == "" {
		return c.Redirect(http.StatusFound, "/admin/trends/tags?"+adminTrendsTagRedirectQuery(c, "", ""))
	}
	if err := s.applyAdminTrendsTagBatch(c.Request().Context(), user.AccountID, ids, action); err != nil {
		return err
	}
	return c.Redirect(http.StatusFound, "/admin/trends/tags?"+adminTrendsTagRedirectQuery(c, "", ""))
}

func (s *Server) requireAdminTrendsWebUser(c *echo.Context) (*models.User, bool, error) {
	user, _, handled, err := s.requireFunctionalWebUser(c)
	if handled || err != nil {
		return nil, handled, err
	}
	if !s.userCan(user, rolePermissionManageTaxonomies) {
		locale := s.webLocale(c, user)
		return nil, true, c.HTML(http.StatusForbidden, authPageHTML(adminT(locale, "admin.trends.tags.title", "Trend tags"), "", adminT(locale, "admin.trends.not_permitted", "You are not allowed to review trends."), "", locale))
	}
	return user, false, nil
}

func (s *Server) adminTrendsTagWebRecords(c *echo.Context) ([]models.Tag, error) {
	if s.db == nil {
		return []models.Tag{}, nil
	}
	query := s.db.Model(&models.Tag{}).
		Joins("JOIN tag_trends ON tag_trends.tag_id = tags.id")
	status, apply, err := adminTrendsTagStatusFilter(c)
	if err != nil {
		return nil, err
	}
	if apply {
		switch status {
		case "approved":
			query = query.Where("tags.trendable = TRUE")
		case "rejected":
			query = query.Where("tags.trendable = FALSE")
		case "pending_review":
			query = query.Where("tags.reviewed_at IS NULL").Where("tags.requested_review_at IS NOT NULL")
		}
	}
	var tags []models.Tag
	err = query.
		Order("CASE WHEN tags.reviewed_at IS NULL THEN 0 ELSE 1 END ASC").
		Order("tags.requested_review_at DESC NULLS LAST").
		Order("tags.last_status_at DESC NULLS LAST").
		Order("tags.id DESC").
		Offset(adminRailsPageOffset(c)).
		Limit(adminRailsDefaultPageSize).
		Find(&tags).Error
	return tags, err
}

func (s *Server) applyAdminTrendsTagBatch(ctx context.Context, actorAccountID int64, ids []int64, action string) error {
	if s.db == nil {
		return echo.NewHTTPError(http.StatusUnprocessableEntity, "DATABASE_URL is not set")
	}
	now := time.Now().UTC()
	updates := map[string]any{
		"reviewed_at": now,
		"updated_at":  now,
	}
	switch action {
	case "approve":
		updates["trendable"] = true
	case "reject":
		updates["trendable"] = false
	default:
		return nil
	}
	var tags []models.Tag
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("id IN ?", ids).Find(&tags).Error; err != nil {
			return err
		}
		if err := tx.Model(&models.Tag{}).Where("id IN ?", ids).Updates(updates).Error; err != nil {
			return err
		}
		for _, tag := range tags {
			if err := logAdminAction(tx, actorAccountID, action, tagAuditLogTarget(tag), now); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	s.meiliIndexTagsBestEffort(ctx, ids)
	return nil
}

func parseAdminTrendsTagIDs(c *echo.Context) []int64 {
	req := c.Request()
	_ = req.ParseForm()
	values := req.Form["trends_tag_batch[tag_ids][]"]
	out := make([]int64, 0, len(values))
	seen := map[int64]struct{}{}
	for _, raw := range values {
		id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err == nil && id > 0 {
			if _, ok := seen[id]; !ok {
				seen[id] = struct{}{}
				out = append(out, id)
			}
		}
	}
	return out
}

func adminTrendsTagBatchAction(c *echo.Context) string {
	if adminBatchFormParamExists(c, "approve") {
		return "approve"
	}
	if adminBatchFormParamExists(c, "reject") {
		return "reject"
	}
	return ""
}

func adminTrendsTagStatus(c *echo.Context) string {
	status := strings.TrimSpace(firstNonEmpty(c.QueryParam("status"), c.FormValue("status")))
	switch status {
	case "approved", "rejected", "pending_review":
		return status
	default:
		return ""
	}
}

func adminTrendsTagStatusFilter(c *echo.Context) (string, bool, error) {
	status := strings.TrimSpace(firstNonEmpty(c.QueryParam("status"), c.FormValue("status")))
	if status == "" {
		return "", false, nil
	}
	switch status {
	case "approved", "rejected", "pending_review":
		return status, true, nil
	default:
		return "", false, echo.NewHTTPError(http.StatusBadRequest, "Unknown status: "+status)
	}
}

func adminTrendsTagRedirectQuery(c *echo.Context, key string, value string) string {
	params := url.Values{}
	if page := strings.TrimSpace(firstNonEmpty(c.QueryParam("page"), c.FormValue("page"))); page != "" {
		params.Set("page", page)
	}
	if status := adminTrendsTagStatus(c); status != "" {
		params.Set("status", status)
	}
	if key != "" && value != "" {
		params.Set(key, value)
	}
	return params.Encode()
}

func adminTrendsPageValue(c *echo.Context) string {
	page := strings.TrimSpace(firstNonEmpty(c.QueryParam("page"), c.FormValue("page")))
	if page == "" {
		return "1"
	}
	return page
}

func adminRailsPageOffset(c *echo.Context) int {
	return adminPageOffset(c, adminRailsDefaultPageSize)
}

func adminPageOffset(c *echo.Context, pageSize int) int {
	if pageSize <= 0 {
		return 0
	}
	page, err := strconv.Atoi(adminTrendsPageValue(c))
	if err != nil || page <= 1 {
		return 0
	}
	return (page - 1) * pageSize
}

func adminTrendsTagsHTML(tags []models.Tag, notice string, errorText string, status string, page string, locale ...string) string {
	loc := settingsLocaleArgOrEnglish(locale...)
	if strings.TrimSpace(page) == "" {
		page = "1"
	}
	var body strings.Builder
	body.WriteString(`<p>` + adminT(loc, "admin.trends.tags.description_html", "Review hashtags before they appear in trends.") + `</p><hr class="spacer"><div class="filters">`)
	statusFilters := adminTrendsFilterValues("status", status)
	body.WriteString(relationshipFilterSubsetHTML(adminT(loc, "admin.tags.review", "Review status"), []relationshipFilterLink{
		{Label: adminT(loc, "generic.all", "All"), Href: adminTrendsWebFilterHref("/admin/trends/tags", statusFilters, "status", ""), Active: status == ""},
		{Label: adminT(loc, "admin.trends.approved", "Approved"), Href: adminTrendsWebFilterHref("/admin/trends/tags", statusFilters, "status", "approved"), Active: status == "approved"},
		{Label: adminT(loc, "admin.trends.rejected", "Rejected"), Href: adminTrendsWebFilterHref("/admin/trends/tags", statusFilters, "status", "rejected"), Active: status == "rejected"},
		{Label: adminT(loc, "admin.accounts.moderation.pending", "Pending review"), Href: adminTrendsWebFilterHref("/admin/trends/tags", statusFilters, "status", "pending_review"), Active: status == "pending_review"},
	}))
	body.WriteString(`</div><form method="post" action="/admin/trends/tags/batch" class="new_trends_tag_batch"><input type="hidden" name="page" value="` + html.EscapeString(page) + `">`)
	if status != "" {
		body.WriteString(`<input type="hidden" name="status" value="` + html.EscapeString(status) + `">`)
	}
	confirm := html.EscapeString(adminT(loc, "admin.reports.are_you_sure", "Are you sure?"))
	body.WriteString(`<div class="batch-table"><div class="batch-table__toolbar"><label class="batch-table__toolbar__select batch-checkbox-all"><input type="checkbox" name="batch_checkbox_all"></label><div class="batch-table__toolbar__actions"><button class="table-action-link" name="approve" value="1" type="submit" data-confirm="` + confirm + `"><i class="fa fa-check"></i> ` + html.EscapeString(adminT(loc, "admin.trends.allow", "Allow")) + `</button><button class="table-action-link" name="reject" value="1" type="submit" data-confirm="` + confirm + `"><i class="fa fa-times"></i> ` + html.EscapeString(adminT(loc, "admin.trends.disallow", "Disallow")) + `</button></div></div><div class="batch-table__body">`)
	if len(tags) == 0 {
		body.WriteString(adminNothingHereHTML(loc, "nothing-here--under-tabs"))
	} else {
		for _, tag := range tags {
			body.WriteString(adminTrendsTagRowHTML(tag, loc))
		}
	}
	body.WriteString(`</div></div></form>`)
	body.WriteString(adminRailsPaginationHTML(loc, "/admin/trends/tags", page, adminTrendsFilterValues("status", status), len(tags)))
	return authPageHTML(adminT(loc, "admin.trends.tags.title", "Trend tags"), notice, errorText, body.String(), loc)
}

func adminTrendsWebFilterHref(path string, filters url.Values, key string, value string) string {
	values := url.Values{}
	for currentKey, currentValues := range filters {
		for _, currentValue := range currentValues {
			values.Add(currentKey, currentValue)
		}
	}
	values.Del("page")
	if value == "" {
		values.Del(key)
	} else {
		values.Set(key, value)
	}
	if query := values.Encode(); query != "" {
		return path + "?" + query
	}
	return path
}

func adminTrendsTagRowHTML(tag models.Tag, locale ...string) string {
	loc := settingsLocaleArgOrEnglish(locale...)
	name := tag.DisplayNameValue()
	status := adminTrendsTagReviewStatus(tag)
	publicName := tag.Name
	if publicName == "" {
		publicName = name
	}
	var meta []string
	meta = append(meta, adminTrendsTagReviewStatusLabel(tag, loc))
	if tag.RequestedReviewAt.Valid {
		meta = append(meta, adminT(loc, "admin.trends.requested_review", "Requested review")+": "+nullTimeRFC3339(tag.RequestedReviewAt))
	}
	if tag.LastStatusAt.Valid {
		meta = append(meta, adminT(loc, "admin.trends.last_used", "Last used")+": "+nullTimeRFC3339(tag.LastStatusAt))
	}
	return `<div class="batch-table__row` + adminTrendReviewRowClass(status) + `"><label class="batch-table__row__select batch-table__row__select--aligned batch-checkbox"><input type="checkbox" name="trends_tag_batch[tag_ids][]" value="` + strconv.FormatInt(tag.ID, 10) + `"></label><div class="batch-table__row__content pending-account"><div class="pending-account__header"><a href="/admin/tags/` + strconv.FormatInt(tag.ID, 10) + `">#` + html.EscapeString(name) + `</a><br><a target="_blank" href="/tags/` + url.PathEscape(publicName) + `">` + html.EscapeString(strings.Join(meta, " · ")) + `</a></div></div></div>`
}

func adminTrendsTagReviewStatus(tag models.Tag) string {
	if !tag.ReviewedAt.Valid {
		return "pending_review"
	}
	if tag.Trendable.Valid && tag.Trendable.Bool {
		return "approved"
	}
	if tag.Trendable.Valid && !tag.Trendable.Bool {
		return "rejected"
	}
	return "reviewed"
}

func adminTrendsTagReviewStatusLabel(tag models.Tag, locale string) string {
	switch adminTrendsTagReviewStatus(tag) {
	case "pending_review":
		return adminT(locale, "admin.trends.pending_review", "Pending review")
	case "approved":
		return adminT(locale, "admin.trends.approved", "Approved")
	case "rejected":
		return adminT(locale, "admin.trends.rejected", "Rejected")
	default:
		return adminT(locale, "admin.trends.reviewed", "Reviewed")
	}
}

func nullTimeRFC3339(value sql.NullTime) string {
	if !value.Valid {
		return "-"
	}
	return value.Time.UTC().Format(time.RFC3339)
}

func (s *Server) adminTrendsStatusesPage(c *echo.Context) error {
	user, handled, err := s.requireAdminTrendsWebUser(c)
	if handled || err != nil {
		return err
	}
	statuses, err := s.adminTrendsStatusWebRecords(c)
	if err != nil {
		return err
	}
	return c.HTML(http.StatusOK, adminTrendsStatusesHTML(s.cfg.BaseURL(), statuses, c.QueryParam("notice"), c.QueryParam("error"), adminTrendsStatusTrending(c), strings.TrimSpace(c.QueryParam("locale")), adminTrendsPageValue(c), s.webLocale(c, user)))
}

func (s *Server) batchAdminTrendsStatuses(c *echo.Context) error {
	user, handled, err := s.requireAdminTrendsWebUser(c)
	if handled || err != nil {
		return err
	}
	ids := parseAdminTrendsStatusIDs(c)
	loc := s.webLocale(c, user)
	if len(ids) == 0 {
		return c.Redirect(http.StatusFound, "/admin/trends/statuses?"+adminTrendsStatusRedirectQuery(c, "error", adminT(loc, "admin.trends.statuses.no_status_selected", "No trending posts were changed as none were selected")))
	}
	action := adminTrendsStatusBatchAction(c)
	if action == "" {
		return c.Redirect(http.StatusFound, "/admin/trends/statuses?"+adminTrendsStatusRedirectQuery(c, "", ""))
	}
	if err := s.applyAdminTrendsStatusBatch(c.Request().Context(), user.AccountID, ids, action); err != nil {
		return err
	}
	return c.Redirect(http.StatusFound, "/admin/trends/statuses?"+adminTrendsStatusRedirectQuery(c, "", ""))
}

func (s *Server) adminTrendsStatusWebRecords(c *echo.Context) ([]models.Status, error) {
	if s.db == nil {
		return []models.Status{}, nil
	}
	query := s.statusQuery().
		Joins("LEFT JOIN status_stats admin_trend_web_status_stats ON admin_trend_web_status_stats.status_id = statuses.id").
		Joins("JOIN status_trends ON status_trends.status_id = statuses.id").
		Where("statuses.deleted_at IS NULL").
		Where("statuses.visibility IN ?", []int{0, 1}).
		Where("statuses.reblog_of_id IS NULL")
	if locale := strings.TrimSpace(c.QueryParam("locale")); locale != "" {
		query = query.Where("status_trends.language = ?", locale)
	}
	if adminTrendsStatusTrending(c) == "allowed" {
		query = query.
			Joins("INNER JOIN (SELECT account_id, MAX(score) AS max_score FROM status_trends GROUP BY account_id) AS grouped_status_trends ON status_trends.account_id = grouped_status_trends.account_id AND status_trends.score = grouped_status_trends.max_score").
			Where("status_trends.allowed = TRUE")
	}
	var statuses []models.Status
	err := query.
		Order("CASE WHEN statuses.trendable IS NULL THEN 0 ELSE 1 END ASC").
		Order("status_trends.score DESC").
		Order("status_trends.rank ASC").
		Order("statuses.id DESC").
		Offset(adminRailsPageOffset(c)).
		Limit(adminRailsDefaultPageSize).
		Find(&statuses).Error
	return statuses, err
}

func (s *Server) applyAdminTrendsStatusBatch(ctx context.Context, actorAccountID int64, ids []int64, action string) error {
	if s.db == nil {
		return echo.NewHTTPError(http.StatusUnprocessableEntity, "DATABASE_URL is not set")
	}
	now := time.Now().UTC()
	switch action {
	case "approve":
		return s.updateTrendStatusesWithAudit(ctx, actorAccountID, ids, true, "approve", now)
	case "reject":
		return s.updateTrendStatusesWithAudit(ctx, actorAccountID, ids, false, "reject", now)
	case "approve_accounts", "reject_accounts":
		trendable := action == "approve_accounts"
		auditAction := "reject"
		if trendable {
			auditAction = "approve"
		}
		var accountIDs []int64
		if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var statuses []models.Status
			if err := tx.Select("id, account_id").Where("id IN ?", ids).Find(&statuses).Error; err != nil {
				return err
			}
			accountIDs = make([]int64, 0, len(statuses))
			seen := map[int64]struct{}{}
			for _, status := range statuses {
				if _, ok := seen[status.AccountID]; ok {
					continue
				}
				seen[status.AccountID] = struct{}{}
				accountIDs = append(accountIDs, status.AccountID)
			}
			var accounts []models.Account
			if len(accountIDs) > 0 {
				if err := tx.Where("id IN ?", accountIDs).Find(&accounts).Error; err != nil {
					return err
				}
				if err := tx.Model(&models.Account{}).Where("id IN ?", accountIDs).Updates(map[string]any{
					"trendable":   trendable,
					"reviewed_at": now,
					"updated_at":  now,
				}).Error; err != nil {
					return err
				}
			}
			if err := tx.Model(&models.Status{}).Where("id IN ?", ids).Updates(map[string]any{"trendable": nil, "updated_at": now}).Error; err != nil {
				return err
			}
			for _, account := range accounts {
				if err := logAdminAction(tx, actorAccountID, auditAction, accountAuditLogTarget(account), now); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return err
		}
		s.refreshAdminTrendStatusReviewSideEffects(ctx, ids, accountIDs)
		for _, accountID := range uniqueInt64s(accountIDs) {
			s.triggerAccountWebhook("account.updated", accountID)
			_ = s.enqueueFASPAccountLifecycleByID(ctx, accountID, "update")
		}
		return nil
	default:
		return nil
	}
}

func (s *Server) updateTrendStatusesWithAudit(ctx context.Context, actorAccountID int64, ids []int64, trendable bool, action string, now time.Time) error {
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var statuses []models.Status
		if err := tx.Where("id IN ?", ids).Find(&statuses).Error; err != nil {
			return err
		}
		if err := tx.Model(&models.Status{}).Where("id IN ?", ids).Updates(map[string]any{"trendable": trendable, "updated_at": now}).Error; err != nil {
			return err
		}
		for _, status := range statuses {
			if err := logAdminAction(tx, actorAccountID, action, statusAuditLogTarget(status), now); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	s.refreshAdminTrendStatusReviewSideEffects(ctx, ids, nil)
	return nil
}

func (s *Server) refreshAdminTrendStatusReviewSideEffects(ctx context.Context, statusIDs []int64, accountIDs []int64) {
	if s == nil {
		return
	}
	for _, id := range uniqueInt64s(statusIDs) {
		s.invalidateStatusCache(ctx, id)
		s.meiliIndexStatusBestEffort(ctx, id)
	}
	for _, id := range uniqueInt64s(accountIDs) {
		s.meiliIndexAccountBestEffort(ctx, id)
	}
}

func parseAdminTrendsStatusIDs(c *echo.Context) []int64 {
	req := c.Request()
	_ = req.ParseForm()
	values := req.Form["trends_status_batch[status_ids][]"]
	out := make([]int64, 0, len(values))
	seen := map[int64]struct{}{}
	for _, raw := range values {
		id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err == nil && id > 0 {
			if _, ok := seen[id]; !ok {
				seen[id] = struct{}{}
				out = append(out, id)
			}
		}
	}
	return out
}

func adminTrendsStatusBatchAction(c *echo.Context) string {
	if adminBatchFormParamExists(c, "approve") {
		return "approve"
	}
	if adminBatchFormParamExists(c, "approve_accounts") {
		return "approve_accounts"
	}
	if adminBatchFormParamExists(c, "reject") {
		return "reject"
	}
	if adminBatchFormParamExists(c, "reject_accounts") {
		return "reject_accounts"
	}
	return ""
}

func adminTrendsStatusTrending(c *echo.Context) string {
	trending := strings.TrimSpace(firstNonEmpty(c.QueryParam("trending"), c.FormValue("trending")))
	if trending == "allowed" {
		return "allowed"
	}
	return ""
}

func adminTrendsStatusRedirectQuery(c *echo.Context, key string, value string) string {
	params := url.Values{}
	if page := strings.TrimSpace(firstNonEmpty(c.QueryParam("page"), c.FormValue("page"))); page != "" {
		params.Set("page", page)
	}
	if trending := adminTrendsStatusTrending(c); trending != "" {
		params.Set("trending", trending)
	}
	if locale := strings.TrimSpace(firstNonEmpty(c.QueryParam("locale"), c.FormValue("locale"))); locale != "" {
		params.Set("locale", locale)
	}
	if key != "" && value != "" {
		params.Set(key, value)
	}
	return params.Encode()
}

func adminTrendsLocaleSelectHTML(locales []string, selected string) string {
	var b strings.Builder
	b.WriteString(`<select name="locale"><option value=""></option>`)
	for _, locale := range locales {
		b.WriteString(`<option value="` + html.EscapeString(locale) + `"`)
		if locale == selected {
			b.WriteString(` selected`)
		}
		b.WriteString(`>` + html.EscapeString(railsStandardLocaleName(locale)) + `</option>`)
	}
	b.WriteString(`</select>`)
	return b.String()
}

func adminTrendsStatusLocales(statuses []models.Status, selected string) []string {
	locales := make([]string, 0, len(statuses)+1)
	if selected != "" {
		locales = append(locales, selected)
	}
	for _, status := range statuses {
		if status.Language.Valid {
			locales = append(locales, status.Language.String)
		}
	}
	return uniqueSortedNonEmptyStrings(locales)
}

func adminTrendsLinkLocales(cards []models.PreviewCard, selected string) []string {
	locales := make([]string, 0, len(cards)+1)
	if selected != "" {
		locales = append(locales, selected)
	}
	for _, card := range cards {
		if card.Language.Valid {
			locales = append(locales, card.Language.String)
		}
	}
	return uniqueSortedNonEmptyStrings(locales)
}

func uniqueSortedNonEmptyStrings(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func adminTrendsStatusesHTML(baseURL string, statuses []models.Status, notice string, errorText string, trending string, locale string, page string, uiLocale ...string) string {
	loc := settingsLocaleArgOrEnglish(uiLocale...)
	if len(uiLocale) == 0 {
		loc = settingsLocaleArgOrEnglish(locale)
	}
	if strings.TrimSpace(page) == "" {
		page = "1"
	}
	var body strings.Builder
	body.WriteString(`<p>` + adminT(loc, "admin.trends.statuses.description_html", "Review statuses before they appear in trends.") + `</p><hr class="spacer">`)
	statusFilters := adminTrendsFilterValues("trending", trending, "locale", locale)
	body.WriteString(`<form method="get" action="/admin/trends/statuses" class="simple_form">`)
	if trending != "" {
		body.WriteString(`<input type="hidden" name="trending" value="` + html.EscapeString(trending) + `">`)
	}
	body.WriteString(`<div class="filters"><div class="filter-subset filter-subset--with-select"><strong>` + html.EscapeString(adminT(loc, "admin.follow_recommendations.language", "Language")) + `</strong><div class="input select optional">` + adminTrendsLocaleSelectHTML(adminTrendsStatusLocales(statuses, locale), locale) + `</div></div>`)
	body.WriteString(relationshipFilterSubsetHTML(adminT(loc, "admin.trends.trending", "Trending"), []relationshipFilterLink{
		{Label: adminT(loc, "generic.all", "All"), Href: adminTrendsWebFilterHref("/admin/trends/statuses", statusFilters, "trending", ""), Active: trending == ""},
		{Label: adminT(loc, "admin.trends.only_allowed", "Only allowed"), Href: adminTrendsWebFilterHref("/admin/trends/statuses", statusFilters, "trending", "allowed"), Active: trending == "allowed"},
	}))
	body.WriteString(`</div></form><form method="post" action="/admin/trends/statuses/batch" class="new_trends_status_batch"><input type="hidden" name="page" value="` + html.EscapeString(page) + `">`)
	if trending != "" {
		body.WriteString(`<input type="hidden" name="trending" value="` + html.EscapeString(trending) + `">`)
	}
	if locale != "" {
		body.WriteString(`<input type="hidden" name="locale" value="` + html.EscapeString(locale) + `">`)
	}
	confirm := html.EscapeString(adminT(loc, "admin.reports.are_you_sure", "Are you sure?"))
	body.WriteString(`<div class="batch-table"><div class="batch-table__toolbar"><label class="batch-table__toolbar__select batch-checkbox-all"><input type="checkbox" name="batch_checkbox_all"></label><div class="batch-table__toolbar__actions"><button class="table-action-link" name="approve" value="1" type="submit" data-confirm="` + confirm + `"><i class="fa fa-check"></i> ` + html.EscapeString(adminT(loc, "admin.trends.statuses.allow", "Allow status")) + `</button><button class="table-action-link" name="approve_accounts" value="1" type="submit" data-confirm="` + confirm + `"><i class="fa fa-check"></i> ` + html.EscapeString(adminT(loc, "admin.trends.statuses.allow_account", "Allow account")) + `</button><button class="table-action-link" name="reject" value="1" type="submit" data-confirm="` + confirm + `"><i class="fa fa-times"></i> ` + html.EscapeString(adminT(loc, "admin.trends.statuses.disallow", "Disallow status")) + `</button><button class="table-action-link" name="reject_accounts" value="1" type="submit" data-confirm="` + confirm + `"><i class="fa fa-times"></i> ` + html.EscapeString(adminT(loc, "admin.trends.statuses.disallow_account", "Disallow account")) + `</button></div></div><div class="batch-table__body">`)
	if len(statuses) == 0 {
		body.WriteString(adminNothingHereHTML(loc, "nothing-here--under-tabs"))
	} else {
		for _, status := range statuses {
			body.WriteString(adminTrendsStatusRowHTML(baseURL, status, loc))
		}
	}
	body.WriteString(`</div></div></form>`)
	body.WriteString(adminRailsPaginationHTML(loc, "/admin/trends/statuses", page, adminTrendsFilterValues("trending", trending, "locale", locale), len(statuses)))
	return authPageHTML(adminT(loc, "admin.trends.statuses.title", "Trend statuses"), notice, errorText, body.String(), loc)
}

func adminTrendsStatusRowHTML(baseURL string, status models.Status, locale ...string) string {
	loc := settingsLocaleArgOrEnglish(locale...)
	preview := trimForTable(firstNonEmpty(status.SpoilerText, status.Text, "#"+strconv.FormatInt(status.ID, 10)))
	language := "-"
	if status.Language.Valid && status.Language.String != "" {
		language = railsStandardLocaleName(status.Language.String)
	}
	interactions := status.StatusStat.ReblogsCount + status.StatusStat.FavouritesCount
	var body strings.Builder
	body.WriteString(`<div class="batch-table__row` + adminTrendReviewRowClass(adminTrendsStatusReviewStatus(status)) + `">`)
	body.WriteString(`<label class="batch-table__row__select batch-table__row__select--aligned batch-checkbox"><input type="checkbox" name="trends_status_batch[status_ids][]" value="` + strconv.FormatInt(status.ID, 10) + `"></label>`)
	body.WriteString(`<div class="batch-table__row__content pending-account__header"><div class="one-liner">`)
	body.WriteString(`<a href="/admin/accounts/` + strconv.FormatInt(status.AccountID, 10) + `">` + html.EscapeString(adminReportAccountLabel(status.Account)) + `</a> `)
	body.WriteString(`<a target="_blank" class="emojify" rel="noopener noreferrer" href="` + html.EscapeString(adminTrendsStatusURL(baseURL, status)) + `">` + html.EscapeString(preview))
	for _, media := range status.MediaAttachments {
		name := media.FileFileName.String
		if name == "" {
			name = media.RemoteURL
		}
		if name == "" {
			continue
		}
		title := media.Description.String
		body.WriteString(` <abbr title="` + html.EscapeString(title) + `">` + html.EscapeString(name) + `</abbr>`)
	}
	body.WriteString(`</a></div>`)
	body.WriteString(html.EscapeString(adminTVars(loc, "admin.trends.statuses.shared_by.other", "Shared and favorited %{friendly_count} times", map[string]string{
		"count":          strconv.FormatInt(interactions, 10),
		"friendly_count": strconv.FormatInt(interactions, 10),
	})))
	if status.Account.Domain.Valid && status.Account.Domain.String != "" {
		body.WriteString(` &middot; ` + html.EscapeString(status.Account.Domain.String))
	}
	if language != "-" {
		body.WriteString(` &middot; ` + html.EscapeString(language))
	}
	body.WriteString(` &middot; ` + html.EscapeString(adminTrendsStatusReviewStatusLabel(status, loc)))
	body.WriteString(`</div></div>`)
	return body.String()
}

func adminTrendsStatusURL(baseURL string, status models.Status) string {
	if status.URL.Valid && status.URL.String != "" {
		return status.URL.String
	}
	if status.Account.Username != "" {
		return strings.TrimRight(baseURL, "/") + "/@" + url.PathEscape(status.Account.Username) + "/" + strconv.FormatInt(status.ID, 10)
	}
	return "/admin/accounts/" + strconv.FormatInt(status.AccountID, 10) + "/statuses/" + strconv.FormatInt(status.ID, 10)
}

func adminTrendsStatusReviewStatus(status models.Status) string {
	if !status.Trendable.Valid {
		return "pending_review"
	}
	if status.Trendable.Bool {
		return "approved"
	}
	return "rejected"
}

func adminTrendsStatusReviewStatusLabel(status models.Status, locale string) string {
	switch adminTrendsStatusReviewStatus(status) {
	case "pending_review":
		return adminT(locale, "admin.trends.pending_review", "Pending review")
	case "approved":
		return adminT(locale, "admin.trends.allowed", "Allowed")
	case "rejected":
		return adminT(locale, "admin.trends.rejected", "Rejected")
	default:
		return adminT(locale, "admin.trends.reviewed", "Reviewed")
	}
}

func (s *Server) adminTrendsLinksPage(c *echo.Context) error {
	user, handled, err := s.requireAdminTrendsWebUser(c)
	if handled || err != nil {
		return err
	}
	cards, err := s.adminTrendsLinkWebRecords(c)
	if err != nil {
		return err
	}
	return c.HTML(http.StatusOK, adminTrendsLinksHTML(cards, c.QueryParam("notice"), c.QueryParam("error"), adminTrendsLinkTrending(c), strings.TrimSpace(c.QueryParam("locale")), adminTrendsPageValue(c), s.webLocale(c, user)))
}

func (s *Server) batchAdminTrendsLinks(c *echo.Context) error {
	user, handled, err := s.requireAdminTrendsWebUser(c)
	if handled || err != nil {
		return err
	}
	ids := parseAdminTrendsLinkIDs(c)
	loc := s.webLocale(c, user)
	if len(ids) == 0 {
		return c.Redirect(http.StatusFound, "/admin/trends/links?"+adminTrendsLinkRedirectQuery(c, "error", adminT(loc, "admin.trends.links.no_link_selected", "No links were changed as none were selected")))
	}
	action := adminTrendsLinkBatchAction(c)
	if action == "" {
		return c.Redirect(http.StatusFound, "/admin/trends/links?"+adminTrendsLinkRedirectQuery(c, "", ""))
	}
	if err := s.applyAdminTrendsLinkBatch(c.Request().Context(), user.AccountID, ids, action); err != nil {
		return err
	}
	return c.Redirect(http.StatusFound, "/admin/trends/links?"+adminTrendsLinkRedirectQuery(c, "", ""))
}

func (s *Server) adminTrendsLinkWebRecords(c *echo.Context) ([]models.PreviewCard, error) {
	if s.db == nil {
		return []models.PreviewCard{}, nil
	}
	query := s.db.Model(&models.PreviewCard{}).
		Joins("JOIN preview_card_trends ON preview_card_trends.preview_card_id = preview_cards.id").
		Where("preview_cards.title <> ''")
	if locale := strings.TrimSpace(c.QueryParam("locale")); locale != "" {
		query = query.Where("preview_card_trends.language = ?", locale)
	}
	if adminTrendsLinkTrending(c) == "allowed" {
		query = query.Where("preview_card_trends.allowed = TRUE")
	}
	var cards []models.PreviewCard
	err := query.
		Order("CASE WHEN preview_cards.trendable IS NULL THEN 0 ELSE 1 END ASC").
		Order("preview_card_trends.score DESC").
		Order("preview_card_trends.rank ASC").
		Order("preview_cards.id DESC").
		Offset(adminRailsPageOffset(c)).
		Limit(adminRailsDefaultPageSize).
		Find(&cards).Error
	return cards, err
}

func (s *Server) applyAdminTrendsLinkBatch(ctx context.Context, actorAccountID int64, ids []int64, action string) error {
	if s.db == nil {
		return echo.NewHTTPError(http.StatusUnprocessableEntity, "DATABASE_URL is not set")
	}
	now := time.Now().UTC()
	switch action {
	case "approve":
		return s.updateTrendPreviewCardsWithAudit(ctx, actorAccountID, ids, true, "approve", now)
	case "reject":
		return s.updateTrendPreviewCardsWithAudit(ctx, actorAccountID, ids, false, "reject", now)
	case "approve_providers", "reject_providers":
		trendable := action == "approve_providers"
		auditAction := "reject"
		if trendable {
			auditAction = "approve"
		}
		return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var cards []models.PreviewCard
			if err := tx.Where("id IN ?", ids).Find(&cards).Error; err != nil {
				return err
			}
			seen := map[string]struct{}{}
			for _, card := range cards {
				domain := previewCardHost(card.URL)
				if domain == "" {
					continue
				}
				if _, ok := seen[domain]; ok {
					continue
				}
				seen[domain] = struct{}{}
				provider := models.PreviewCardProvider{Domain: domain, CreatedAt: now, UpdatedAt: now}
				if err := tx.Where("domain = ?", domain).FirstOrCreate(&provider).Error; err != nil {
					return err
				}
				if err := tx.Model(&models.PreviewCardProvider{}).Where("id = ?", provider.ID).Updates(map[string]any{
					"trendable":   trendable,
					"reviewed_at": now,
					"updated_at":  now,
				}).Error; err != nil {
					return err
				}
				if err := logAdminAction(tx, actorAccountID, auditAction, previewCardProviderAuditLogTarget(provider), now); err != nil {
					return err
				}
			}
			return tx.Model(&models.PreviewCard{}).Where("id IN ?", ids).Updates(map[string]any{"trendable": nil, "updated_at": now}).Error
		})
	default:
		return nil
	}
}

func (s *Server) updateTrendPreviewCardsWithAudit(ctx context.Context, actorAccountID int64, ids []int64, trendable bool, action string, now time.Time) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var cards []models.PreviewCard
		if err := tx.Where("id IN ?", ids).Find(&cards).Error; err != nil {
			return err
		}
		if err := tx.Model(&models.PreviewCard{}).Where("id IN ?", ids).Updates(map[string]any{"trendable": trendable, "updated_at": now}).Error; err != nil {
			return err
		}
		for _, card := range cards {
			if err := logAdminAction(tx, actorAccountID, action, previewCardAuditLogTarget(card), now); err != nil {
				return err
			}
		}
		return nil
	})
}

func parseAdminTrendsLinkIDs(c *echo.Context) []int64 {
	req := c.Request()
	_ = req.ParseForm()
	values := req.Form["trends_preview_card_batch[preview_card_ids][]"]
	out := make([]int64, 0, len(values))
	seen := map[int64]struct{}{}
	for _, raw := range values {
		id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err == nil && id > 0 {
			if _, ok := seen[id]; !ok {
				seen[id] = struct{}{}
				out = append(out, id)
			}
		}
	}
	return out
}

func adminTrendsLinkBatchAction(c *echo.Context) string {
	if adminBatchFormParamExists(c, "approve") {
		return "approve"
	}
	if adminBatchFormParamExists(c, "approve_providers") {
		return "approve_providers"
	}
	if adminBatchFormParamExists(c, "reject") {
		return "reject"
	}
	if adminBatchFormParamExists(c, "reject_providers") {
		return "reject_providers"
	}
	return ""
}

func adminTrendsLinkTrending(c *echo.Context) string {
	trending := strings.TrimSpace(firstNonEmpty(c.QueryParam("trending"), c.FormValue("trending")))
	if trending == "allowed" {
		return "allowed"
	}
	return ""
}

func adminTrendsLinkRedirectQuery(c *echo.Context, key string, value string) string {
	params := url.Values{}
	if page := strings.TrimSpace(firstNonEmpty(c.QueryParam("page"), c.FormValue("page"))); page != "" {
		params.Set("page", page)
	}
	if trending := adminTrendsLinkTrending(c); trending != "" {
		params.Set("trending", trending)
	}
	if locale := strings.TrimSpace(firstNonEmpty(c.QueryParam("locale"), c.FormValue("locale"))); locale != "" {
		params.Set("locale", locale)
	}
	if key != "" && value != "" {
		params.Set(key, value)
	}
	return params.Encode()
}

func adminTrendsLinksHTML(cards []models.PreviewCard, notice string, errorText string, trending string, locale string, page string, uiLocale ...string) string {
	loc := settingsLocaleArgOrEnglish(uiLocale...)
	if len(uiLocale) == 0 {
		loc = settingsLocaleArgOrEnglish(locale)
	}
	if strings.TrimSpace(page) == "" {
		page = "1"
	}
	var body strings.Builder
	body.WriteString(`<p>` + adminT(loc, "admin.trends.links.description_html", "Review link cards before they appear in trends.") + `</p><hr class="spacer">`)
	linkFilters := adminTrendsFilterValues("trending", trending, "locale", locale)
	body.WriteString(`<form method="get" action="/admin/trends/links" class="simple_form">`)
	if trending != "" {
		body.WriteString(`<input type="hidden" name="trending" value="` + html.EscapeString(trending) + `">`)
	}
	body.WriteString(`<div class="filters"><div class="filter-subset filter-subset--with-select"><strong>` + html.EscapeString(adminT(loc, "admin.follow_recommendations.language", "Language")) + `</strong><div class="input select optional">` + adminTrendsLocaleSelectHTML(adminTrendsLinkLocales(cards, locale), locale) + `</div></div>`)
	body.WriteString(relationshipFilterSubsetHTML(adminT(loc, "admin.trends.trending", "Trending"), []relationshipFilterLink{
		{Label: adminT(loc, "generic.all", "All"), Href: adminTrendsWebFilterHref("/admin/trends/links", linkFilters, "trending", ""), Active: trending == ""},
		{Label: adminT(loc, "admin.trends.only_allowed", "Only allowed"), Href: adminTrendsWebFilterHref("/admin/trends/links", linkFilters, "trending", "allowed"), Active: trending == "allowed"},
	}))
	body.WriteString(`<div class="back-link"><a href="/admin/trends/links/publishers">` + html.EscapeString(adminT(loc, "admin.trends.preview_card_providers.title", "Publishers")) + ` <i class="fa fa-chevron-right fa-fw"></i></a></div></div></form><form method="post" action="/admin/trends/links/batch" class="new_trends_link_batch"><input type="hidden" name="page" value="` + html.EscapeString(page) + `">`)
	if trending != "" {
		body.WriteString(`<input type="hidden" name="trending" value="` + html.EscapeString(trending) + `">`)
	}
	if locale != "" {
		body.WriteString(`<input type="hidden" name="locale" value="` + html.EscapeString(locale) + `">`)
	}
	confirm := html.EscapeString(adminT(loc, "admin.reports.are_you_sure", "Are you sure?"))
	body.WriteString(`<div class="batch-table"><div class="batch-table__toolbar"><label class="batch-table__toolbar__select batch-checkbox-all"><input type="checkbox" name="batch_checkbox_all"></label><div class="batch-table__toolbar__actions"><button class="table-action-link" name="approve" value="1" type="submit" data-confirm="` + confirm + `"><i class="fa fa-check"></i> ` + html.EscapeString(adminT(loc, "admin.trends.links.allow", "Allow link")) + `</button><button class="table-action-link" name="approve_providers" value="1" type="submit" data-confirm="` + confirm + `"><i class="fa fa-check"></i> ` + html.EscapeString(adminT(loc, "admin.trends.links.allow_provider", "Allow publisher")) + `</button><button class="table-action-link" name="reject" value="1" type="submit" data-confirm="` + confirm + `"><i class="fa fa-times"></i> ` + html.EscapeString(adminT(loc, "admin.trends.links.disallow", "Disallow link")) + `</button><button class="table-action-link" name="reject_providers" value="1" type="submit" data-confirm="` + confirm + `"><i class="fa fa-times"></i> ` + html.EscapeString(adminT(loc, "admin.trends.links.disallow_provider", "Disallow publisher")) + `</button></div></div><div class="batch-table__body">`)
	if len(cards) == 0 {
		body.WriteString(adminNothingHereHTML(loc, "nothing-here--under-tabs"))
	} else {
		for _, card := range cards {
			body.WriteString(adminTrendsLinkRowHTML(card, loc))
		}
	}
	body.WriteString(`</div></div></form>`)
	body.WriteString(adminRailsPaginationHTML(loc, "/admin/trends/links", page, adminTrendsFilterValues("trending", trending, "locale", locale), len(cards)))
	return authPageHTML(adminT(loc, "admin.trends.links.title", "Trend links"), notice, errorText, body.String(), loc)
}

func adminTrendsLinkRowHTML(card models.PreviewCard, locale ...string) string {
	loc := settingsLocaleArgOrEnglish(locale...)
	title := firstNonEmpty(card.Title, card.URL)
	language := "-"
	if card.Language.Valid && card.Language.String != "" {
		language = railsStandardLocaleName(card.Language.String)
	}
	var meta []string
	if card.ProviderName != "" {
		meta = append(meta, card.ProviderName)
	}
	if language != "-" {
		meta = append(meta, language)
	}
	meta = append(meta, adminTrendsLinkReviewStatusLabel(card, loc))
	return `<div class="batch-table__row` + adminTrendReviewRowClass(adminTrendsLinkReviewStatus(card)) + `"><label class="batch-table__row__select batch-table__row__select--aligned batch-checkbox"><input type="checkbox" name="trends_preview_card_batch[preview_card_ids][]" value="` + strconv.FormatInt(card.ID, 10) + `"></label><div class="batch-table__row__content pending-account"><div class="pending-account__header"><a href="` + html.EscapeString(card.URL) + `">` + html.EscapeString(title) + `</a><br>` + html.EscapeString(strings.Join(meta, " · ")) + `</div></div></div>`
}

func adminTrendsLinkReviewStatus(card models.PreviewCard) string {
	if !card.Trendable.Valid {
		return "pending_review"
	}
	if card.Trendable.Bool {
		return "approved"
	}
	return "rejected"
}

func adminTrendsLinkReviewStatusLabel(card models.PreviewCard, locale string) string {
	switch adminTrendsLinkReviewStatus(card) {
	case "pending_review":
		return adminT(locale, "admin.trends.pending_review", "Pending review")
	case "approved":
		return adminT(locale, "admin.trends.allowed", "Allowed")
	case "rejected":
		return adminT(locale, "admin.trends.rejected", "Rejected")
	default:
		return adminT(locale, "admin.trends.reviewed", "Reviewed")
	}
}

func adminTrendReviewRowClass(status string) string {
	switch status {
	case "pending_review":
		return " batch-table__row--attention"
	case "rejected":
		return " batch-table__row--muted"
	default:
		return ""
	}
}

func (s *Server) adminTrendsLinkPublishersPage(c *echo.Context) error {
	user, handled, err := s.requireAdminTrendsWebUser(c)
	if handled || err != nil {
		return err
	}
	providers, err := s.adminTrendsLinkPublisherRecords(c)
	if err != nil {
		return err
	}
	return c.HTML(http.StatusOK, adminTrendsLinkPublishersHTML(providers, c.QueryParam("notice"), c.QueryParam("error"), adminTrendsLinkPublisherStatus(c), adminTrendsPageValue(c), s.webLocale(c, user)))
}

func (s *Server) batchAdminTrendsLinkPublishers(c *echo.Context) error {
	user, handled, err := s.requireAdminTrendsWebUser(c)
	if handled || err != nil {
		return err
	}
	ids := parseAdminTrendsLinkPublisherIDs(c)
	loc := s.webLocale(c, user)
	if len(ids) == 0 {
		return c.Redirect(http.StatusFound, "/admin/trends/links/publishers?"+adminTrendsLinkPublisherRedirectQuery(c, "error", adminT(loc, "admin.trends.links.publishers.no_publisher_selected", "No publishers were changed as none were selected")))
	}
	action := adminTrendsLinkPublisherBatchAction(c)
	if action == "" {
		return c.Redirect(http.StatusFound, "/admin/trends/links/publishers?"+adminTrendsLinkPublisherRedirectQuery(c, "", ""))
	}
	if err := s.applyAdminTrendsLinkPublisherBatch(c.Request().Context(), user.AccountID, ids, action); err != nil {
		return err
	}
	return c.Redirect(http.StatusFound, "/admin/trends/links/publishers?"+adminTrendsLinkPublisherRedirectQuery(c, "", ""))
}

func (s *Server) adminTrendsLinkPublisherRecords(c *echo.Context) ([]models.PreviewCardProvider, error) {
	if s.db == nil {
		return []models.PreviewCardProvider{}, nil
	}
	query := s.db.Model(&models.PreviewCardProvider{})
	status, apply, err := adminTrendsLinkPublisherStatusFilter(c)
	if err != nil {
		return nil, err
	}
	if apply {
		switch status {
		case "approved":
			query = query.Where("preview_card_providers.trendable = TRUE")
		case "rejected":
			query = query.Where("preview_card_providers.trendable = FALSE")
		case "pending_review":
			query = query.Where("preview_card_providers.reviewed_at IS NULL")
		}
	}
	var providers []models.PreviewCardProvider
	err = query.Order("preview_card_providers.domain ASC").Offset(adminRailsPageOffset(c)).Limit(adminRailsDefaultPageSize).Find(&providers).Error
	return providers, err
}

func (s *Server) applyAdminTrendsLinkPublisherBatch(ctx context.Context, actorAccountID int64, ids []int64, action string) error {
	if s.db == nil {
		return echo.NewHTTPError(http.StatusUnprocessableEntity, "DATABASE_URL is not set")
	}
	now := time.Now().UTC()
	updates := map[string]any{
		"reviewed_at": now,
		"updated_at":  now,
	}
	switch action {
	case "approve":
		updates["trendable"] = true
	case "reject":
		updates["trendable"] = false
	default:
		return nil
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var providers []models.PreviewCardProvider
		if err := tx.Where("id IN ?", ids).Find(&providers).Error; err != nil {
			return err
		}
		if err := tx.Model(&models.PreviewCardProvider{}).Where("id IN ?", ids).Updates(updates).Error; err != nil {
			return err
		}
		for _, provider := range providers {
			if err := logAdminAction(tx, actorAccountID, action, previewCardProviderAuditLogTarget(provider), now); err != nil {
				return err
			}
		}
		return nil
	})
}

func parseAdminTrendsLinkPublisherIDs(c *echo.Context) []int64 {
	req := c.Request()
	_ = req.ParseForm()
	values := req.Form["trends_preview_card_provider_batch[preview_card_provider_ids][]"]
	out := make([]int64, 0, len(values))
	seen := map[int64]struct{}{}
	for _, raw := range values {
		id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err == nil && id > 0 {
			if _, ok := seen[id]; !ok {
				seen[id] = struct{}{}
				out = append(out, id)
			}
		}
	}
	return out
}

func adminTrendsLinkPublisherBatchAction(c *echo.Context) string {
	if adminBatchFormParamExists(c, "approve") {
		return "approve"
	}
	if adminBatchFormParamExists(c, "reject") {
		return "reject"
	}
	return ""
}

func adminTrendsLinkPublisherStatus(c *echo.Context) string {
	status := strings.TrimSpace(firstNonEmpty(c.QueryParam("status"), c.FormValue("status")))
	switch status {
	case "approved", "rejected", "pending_review":
		return status
	default:
		return ""
	}
}

func adminTrendsLinkPublisherStatusFilter(c *echo.Context) (string, bool, error) {
	status := strings.TrimSpace(firstNonEmpty(c.QueryParam("status"), c.FormValue("status")))
	if status == "" {
		return "", false, nil
	}
	switch status {
	case "approved", "rejected", "pending_review":
		return status, true, nil
	default:
		return "", false, echo.NewHTTPError(http.StatusBadRequest, "Unknown status: "+status)
	}
}

func adminTrendsLinkPublisherRedirectQuery(c *echo.Context, key string, value string) string {
	params := url.Values{}
	if page := strings.TrimSpace(firstNonEmpty(c.QueryParam("page"), c.FormValue("page"))); page != "" {
		params.Set("page", page)
	}
	if status := adminTrendsLinkPublisherStatus(c); status != "" {
		params.Set("status", status)
	}
	if key != "" && value != "" {
		params.Set(key, value)
	}
	return params.Encode()
}

func adminTrendsLinkPublishersHTML(providers []models.PreviewCardProvider, notice string, errorText string, status string, page string, locale ...string) string {
	loc := settingsLocaleArgOrEnglish(locale...)
	if strings.TrimSpace(page) == "" {
		page = "1"
	}
	var body strings.Builder
	body.WriteString(`<p>` + html.EscapeString(adminT(loc, "admin.trends.publishers.review_preamble", "Review link publishers before their cards appear in trends.")) + `</p>`)
	body.WriteString(`<hr class="spacer"><div class="filters">` + relationshipFilterSubsetHTML(adminT(loc, "admin.tags.review", "Review"), []relationshipFilterLink{
		{Label: adminT(loc, "generic.all", "All"), Href: "/admin/trends/links/publishers", Active: status == ""},
		{Label: adminT(loc, "admin.trends.approved", "Approved"), Href: "/admin/trends/links/publishers?status=approved", Active: status == "approved"},
		{Label: adminT(loc, "admin.trends.rejected", "Rejected"), Href: "/admin/trends/links/publishers?status=rejected", Active: status == "rejected"},
		{Label: adminT(loc, "admin.accounts.moderation.pending", "Pending"), Href: "/admin/trends/links/publishers?status=pending_review", Active: status == "pending_review"},
	}) + `<div class="back-link"><a href="/admin/trends/links"><i class="fa fa-chevron-left fa-fw"></i> ` + html.EscapeString(adminT(loc, "admin.trends.links.title", "Trend links")) + `</a></div></div><hr class="spacer">`)
	body.WriteString(`<form method="post" action="/admin/trends/links/publishers/batch" class="new_form_preview_card_provider_batch"><input type="hidden" name="page" value="` + html.EscapeString(page) + `">`)
	if status != "" {
		body.WriteString(`<input type="hidden" name="status" value="` + html.EscapeString(status) + `">`)
	}
	confirm := html.EscapeString(adminT(loc, "admin.reports.are_you_sure", "Are you sure?"))
	body.WriteString(`<div class="batch-table"><div class="batch-table__toolbar"><label class="batch-table__toolbar__select batch-checkbox-all"><input type="checkbox" name="batch_checkbox_all"></label><div class="batch-table__toolbar__actions"><button class="table-action-link" name="approve" value="1" type="submit" data-confirm="` + confirm + `"><i class="fa fa-check"></i> ` + html.EscapeString(adminT(loc, "admin.trends.allow", "Allow")) + `</button><button class="table-action-link" name="reject" value="1" type="submit" data-confirm="` + confirm + `"><i class="fa fa-times"></i> ` + html.EscapeString(adminT(loc, "admin.trends.disallow", "Disallow")) + `</button></div></div><div class="batch-table__body">`)
	if len(providers) == 0 {
		body.WriteString(adminNothingHereHTML(loc, "nothing-here--under-tabs"))
	} else {
		for _, provider := range providers {
			body.WriteString(adminTrendsLinkPublisherRowHTML(provider, loc))
		}
	}
	body.WriteString(`</div></div>`)
	body.WriteString(`</form>`)
	body.WriteString(adminRailsPaginationHTML(loc, "/admin/trends/links/publishers", page, adminTrendsFilterValues("status", status), len(providers)))
	return authPageHTML(adminT(loc, "admin.trends.publishers.title", "Trend publishers"), notice, errorText, body.String(), loc)
}

func adminTrendsFilterValues(items ...string) url.Values {
	values := url.Values{}
	for i := 0; i+1 < len(items); i += 2 {
		if value := strings.TrimSpace(items[i+1]); value != "" {
			values.Set(items[i], value)
		}
	}
	return values
}

func adminRailsPaginationHTML(locale string, path string, page string, filters url.Values, rowCount int) string {
	current, err := strconv.Atoi(strings.TrimSpace(page))
	if err != nil || current <= 0 {
		current = 1
	}
	var links []string
	if current > 1 {
		links = append(links, `<a rel="prev" href="`+html.EscapeString(adminRailsPaginationURL(path, filters, current-1))+`">`+html.EscapeString(adminT(locale, "pagination.prev", "Previous"))+`</a>`)
	}
	if rowCount >= adminRailsDefaultPageSize {
		links = append(links, `<a rel="next" href="`+html.EscapeString(adminRailsPaginationURL(path, filters, current+1))+`">`+html.EscapeString(adminT(locale, "pagination.next", "Next"))+`</a>`)
	}
	if len(links) == 0 {
		return ""
	}
	return `<nav class="pagination">` + strings.Join(links, " ") + `</nav>`
}

func adminRailsPaginationURL(path string, filters url.Values, page int) string {
	values := url.Values{}
	for key, existing := range filters {
		for _, value := range existing {
			if strings.TrimSpace(value) != "" {
				values.Add(key, value)
			}
		}
	}
	if page > 1 {
		values.Set("page", strconv.Itoa(page))
	}
	if encoded := values.Encode(); encoded != "" {
		return path + "?" + encoded
	}
	return path
}

func adminTrendsLinkPublisherRowHTML(provider models.PreviewCardProvider, locale ...string) string {
	loc := settingsLocaleArgOrEnglish(locale...)
	classes := []string{"batch-table__row"}
	if !provider.ReviewedAt.Valid {
		classes = append(classes, "batch-table__row--attention")
	} else if !(provider.Trendable.Valid && provider.Trendable.Bool) {
		classes = append(classes, "batch-table__row--muted")
	}
	return `<div class="` + html.EscapeString(strings.Join(classes, " ")) + `"><label class="batch-table__row__select batch-table__row__select--aligned batch-checkbox"><input type="checkbox" name="trends_preview_card_provider_batch[preview_card_provider_ids][]" value="` + strconv.FormatInt(provider.ID, 10) + `"></label><div class="batch-table__row__content pending-account"><div class="pending-account__header"><strong>` + html.EscapeString(provider.Domain) + `</strong><br>` + html.EscapeString(adminTrendsLinkPublisherReviewStatusLabel(provider, loc)) + `</div><div class="pending-account__body"><span>` + html.EscapeString(adminT(loc, "admin.trends.requested_review", "Requested review")) + `: ` + html.EscapeString(nullTimeRFC3339(provider.RequestedReviewAt)) + `</span><br><span>` + html.EscapeString(adminT(loc, "admin.trends.reviewed", "Reviewed")) + `: ` + html.EscapeString(nullTimeRFC3339(provider.ReviewedAt)) + `</span></div></div></div>`
}

func adminTrendsLinkPublisherReviewStatus(provider models.PreviewCardProvider) string {
	if !provider.ReviewedAt.Valid {
		return "pending_review"
	}
	if provider.Trendable.Valid && provider.Trendable.Bool {
		return "approved"
	}
	if provider.Trendable.Valid && !provider.Trendable.Bool {
		return "rejected"
	}
	return "reviewed"
}

func adminTrendsLinkPublisherReviewStatusLabel(provider models.PreviewCardProvider, locale string) string {
	switch adminTrendsLinkPublisherReviewStatus(provider) {
	case "pending_review":
		return adminT(locale, "admin.trends.pending_review", "Pending review")
	case "approved":
		return adminT(locale, "admin.trends.preview_card_providers.allowed", "Allowed")
	case "rejected":
		return adminT(locale, "admin.trends.preview_card_providers.rejected", "Rejected")
	default:
		return adminT(locale, "admin.trends.reviewed", "Reviewed")
	}
}

func (s *Server) adminTrendTags(c *echo.Context) error {
	user, err := s.requireAdminReadToken(c)
	if err != nil {
		return err
	}
	if !s.userCan(user, rolePermissionManageTaxonomies) {
		return withPublicTrendPath(c, "/api/v1/trends/tags", s.trendingTags)
	}
	if s.db == nil {
		return c.JSON(http.StatusOK, []any{})
	}
	var tags []models.Tag
	limitValue := limit(c, 100, 200)
	offsetValue := offset(c)
	query := s.db.WithContext(c.Request().Context()).Model(&models.Tag{}).
		Joins("JOIN tag_trends ON tag_trends.tag_id = tags.id").
		Order("tag_trends.score DESC").
		Offset(offsetValue).
		Limit(limitValue)
	if err := query.Find(&tags).Error; err != nil {
		return err
	}
	if len(tags) > 0 {
		setPaginationLinkHeader(c, offsetPaginationLinkWithPathAndAllowedParams(c, "/api/v1/trends/tags", offsetValue, limitValue, len(tags), adminLimitPaginationParams))
	}
	out := make([]serializer.AdminTag, 0, len(tags))
	for _, tag := range tags {
		out = append(out, s.adminTagFromModel(c, tag))
	}
	return c.JSON(http.StatusOK, out)
}

func withPublicTrendPath(c *echo.Context, path string, handler echo.HandlerFunc) error {
	req := c.Request()
	originalPath := req.URL.Path
	req.URL.Path = path
	defer func() {
		req.URL.Path = originalPath
	}()
	return handler(c)
}

func (s *Server) adminTrendLinks(c *echo.Context) error {
	user, err := s.requireAdminReadToken(c)
	if err != nil {
		return err
	}
	if !s.userCan(user, rolePermissionManageTaxonomies) {
		return withPublicTrendPath(c, "/api/v1/trends/links", s.trendingLinks)
	}
	if s.db == nil {
		return c.JSON(http.StatusOK, []any{})
	}
	var cards []models.PreviewCard
	limitValue := limit(c, 100, 200)
	offsetValue := offset(c)
	refs, err := s.adminTrendingPreviewCardRefs(limitValue, offsetValue, time.Now().UTC())
	if err != nil {
		return err
	}
	if len(refs) > 0 {
		ids := make([]int64, 0, len(refs))
		order := map[int64]int{}
		for _, ref := range refs {
			ids = append(ids, ref.ID)
			order[ref.ID] = len(ids) - 1
		}
		if err := s.db.Where("id IN ?", ids).Find(&cards).Error; err != nil {
			return err
		}
		sort.SliceStable(cards, func(i, j int) bool { return order[cards[i].ID] < order[cards[j].ID] })
	}
	if len(cards) > 0 {
		setPaginationLinkHeader(c, offsetPaginationLinkWithPathAndAllowedParams(c, "/api/v1/trends/links", offsetValue, limitValue, len(cards), adminLimitPaginationParams))
	}
	return c.JSON(http.StatusOK, s.serializeAdminTrendLinks(c, cards, time.Now().UTC()))
}

func (s *Server) approveAdminTrendTag(c *echo.Context) error {
	return s.reviewAdminTrendTag(c, true)
}

func (s *Server) rejectAdminTrendTag(c *echo.Context) error {
	return s.reviewAdminTrendTag(c, false)
}

func (s *Server) reviewAdminTrendTag(c *echo.Context, trendable bool) error {
	user, err := s.requireAdminWriteWithPermissions(c, nil, rolePermissionManageTaxonomies)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	action := "reject"
	if trendable {
		action = "approve"
	}
	if err := s.db.WithContext(c.Request().Context()).Transaction(func(tx *gorm.DB) error {
		var tag models.Tag
		if err := tx.Where("id = ?", id).First(&tag).Error; err != nil {
			return apiError(c, http.StatusNotFound, "Record not found")
		}
		if err := tx.Model(&models.Tag{}).Where("id = ?", id).Updates(map[string]any{
			"trendable":   trendable,
			"reviewed_at": now,
			"updated_at":  now,
		}).Error; err != nil {
			return err
		}
		return logAdminAction(tx, user.AccountID, action, tagAuditLogTarget(tag), now)
	}); err != nil {
		return err
	}
	s.meiliIndexTagsBestEffort(c.Request().Context(), []int64{id})
	var tag models.Tag
	if err := s.db.Where("id = ?", id).First(&tag).Error; err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	return c.JSON(http.StatusOK, s.adminTagFromModel(c, tag))
}

func (s *Server) approveAdminTrendLink(c *echo.Context) error {
	return s.reviewAdminTrendLink(c, true)
}

func (s *Server) rejectAdminTrendLink(c *echo.Context) error {
	return s.reviewAdminTrendLink(c, false)
}

func (s *Server) reviewAdminTrendLink(c *echo.Context, trendable bool) error {
	user, err := s.requireAdminWriteWithPermissions(c, nil, rolePermissionManageTaxonomies)
	if err != nil {
		return err
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	now := time.Now().UTC()
	action := "reject"
	if trendable {
		action = "approve"
	}
	if err := s.db.WithContext(c.Request().Context()).Transaction(func(tx *gorm.DB) error {
		var card models.PreviewCard
		if err := tx.Where("id = ?", id).First(&card).Error; err != nil {
			return apiError(c, http.StatusNotFound, "Record not found")
		}
		if err := tx.Model(&models.PreviewCard{}).Where("id = ?", id).Updates(map[string]any{
			"trendable":  trendable,
			"updated_at": now,
		}).Error; err != nil {
			return err
		}
		return logAdminAction(tx, user.AccountID, action, previewCardAuditLogTarget(card), now)
	}); err != nil {
		return err
	}
	var card models.PreviewCard
	if err := s.db.Where("id = ?", id).First(&card).Error; err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	links := s.serializeAdminTrendLinks(c, []models.PreviewCard{card}, time.Now().UTC())
	if len(links) == 0 {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	return c.JSON(http.StatusOK, links[0])
}

func (s *Server) adminTrendingPreviewCardRefs(limitValue int, offsetValue int, now time.Time) ([]trendPreviewCardRef, error) {
	var refs []trendPreviewCardRef
	since := now.AddDate(0, 0, -7)
	err := s.db.Table("preview_card_trends").
		Select("preview_cards.id, COUNT(statuses.id) AS uses, COUNT(DISTINCT statuses.account_id) AS accounts").
		Joins("JOIN preview_cards ON preview_cards.id = preview_card_trends.preview_card_id").
		Joins("LEFT JOIN preview_cards_statuses ON preview_cards_statuses.preview_card_id = preview_cards.id").
		Joins("LEFT JOIN statuses ON statuses.id = preview_cards_statuses.status_id AND statuses.deleted_at IS NULL AND statuses.visibility IN ? AND statuses.created_at >= ?", []int{0, 1}, since).
		Where("preview_cards.title <> ''").
		Group("preview_cards.id, preview_card_trends.score, preview_card_trends.rank").
		Order("preview_card_trends.score DESC").
		Order("preview_card_trends.rank ASC").
		Order("preview_cards.id ASC").
		Offset(offsetValue).
		Limit(limitValue).
		Scan(&refs).Error
	return refs, err
}

func (s *Server) adminTrendStatuses(c *echo.Context) error {
	user, err := s.requireAdminReadToken(c)
	if err != nil {
		return err
	}
	if !s.userCan(user, rolePermissionManageTaxonomies) {
		return withPublicTrendPath(c, "/api/v1/trends/statuses", s.trendingStatuses)
	}
	if s.db == nil {
		return c.JSON(http.StatusOK, []any{})
	}
	offsetValue := offset(c)
	query := s.statusQuery().
		Joins("LEFT JOIN status_stats admin_trend_status_stats ON admin_trend_status_stats.status_id = statuses.id").
		Joins("JOIN status_trends ON status_trends.status_id = statuses.id").
		Where("statuses.deleted_at IS NULL").
		Where("statuses.visibility IN ?", []int{0, 1}).
		Where("statuses.reblog_of_id IS NULL").
		Order("status_trends.score DESC").
		Order("status_trends.rank ASC").
		Order("statuses.id DESC").
		Offset(offsetValue)
	limitValue := limit(c, 100, 200)
	query = query.Limit(limitValue)
	var statuses []models.Status
	if err := query.Find(&statuses).Error; err != nil {
		return err
	}
	if len(statuses) > 0 {
		setPaginationLinkHeader(c, offsetPaginationLinkWithPathAndAllowedParams(c, "/api/v1/trends/statuses", offsetValue, limitValue, len(statuses), adminLimitPaginationParams))
	}
	account, _, _ := s.currentAccount(c)
	if err := s.hydrateStatusRelationships(statuses, account); err != nil {
		return err
	}
	filters := s.accountFilters(account)
	out := make([]serializer.AdminTrendStatus, 0, len(statuses))
	for _, status := range statuses {
		item := serializer.AdminTrendStatusFromModel(s.cfg, status, account)
		item.Status = statusWithAllFilterContexts(s.cfg, status, account, filters)
		out = append(out, item)
	}
	return c.JSON(http.StatusOK, out)
}

func (s *Server) approveAdminTrendStatus(c *echo.Context) error {
	return s.reviewAdminTrendStatus(c, true)
}

func (s *Server) rejectAdminTrendStatus(c *echo.Context) error {
	return s.reviewAdminTrendStatus(c, false)
}

func (s *Server) reviewAdminTrendStatus(c *echo.Context, trendable bool) error {
	user, err := s.requireAdminWriteWithPermissions(c, nil, rolePermissionManageTaxonomies)
	if err != nil {
		return err
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	now := time.Now().UTC()
	action := "reject"
	if trendable {
		action = "approve"
	}
	if err := s.db.WithContext(c.Request().Context()).Transaction(func(tx *gorm.DB) error {
		var status models.Status
		if err := tx.Where("id = ?", id).First(&status).Error; err != nil {
			return apiError(c, http.StatusNotFound, "Record not found")
		}
		if err := tx.Model(&models.Status{}).Where("id = ?", id).Updates(map[string]any{
			"trendable":  trendable,
			"updated_at": now,
		}).Error; err != nil {
			return err
		}
		return logAdminAction(tx, user.AccountID, action, statusAuditLogTarget(status), now)
	}); err != nil {
		return err
	}
	s.refreshAdminTrendStatusReviewSideEffects(c.Request().Context(), []int64{id}, nil)
	s.triggerStatusWebhook("status.updated", id)
	var status models.Status
	if err := s.statusQuery().Where("statuses.id = ?", id).First(&status).Error; err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	_ = s.enqueueFASPContentLifecycle(c.Request().Context(), status, "update")
	account, _, _ := s.currentAccount(c)
	statuses := []models.Status{status}
	if err := s.hydrateStatusRelationships(statuses, account); err != nil {
		return err
	}
	item := serializer.AdminTrendStatusFromModel(s.cfg, statuses[0], account)
	item.Status = statusWithAllFilterContexts(s.cfg, statuses[0], account, s.accountFilters(account))
	return c.JSON(http.StatusOK, item)
}

func (s *Server) adminPreviewCardProviders(c *echo.Context) error {
	if _, err := s.requireAdminReadWithPermissions(c, nil, rolePermissionManageTaxonomies); err != nil {
		return err
	}
	if s.db == nil {
		return c.JSON(http.StatusOK, []any{})
	}
	var providers []models.PreviewCardProvider
	limitValue := limit(c, 100, 200)
	query := applyIDPagination(c, s.db.Model(&models.PreviewCardProvider{}), "preview_card_providers.id").
		Order("preview_card_providers.id DESC").
		Limit(limitValue)
	if err := query.Find(&providers).Error; err != nil {
		return err
	}
	if c.QueryParam("min_id") != "" {
		reverseRows(providers)
	}
	if len(providers) > 0 {
		c.Response().Header().Set("Link", paginationLinkWithAllowedParams(c, providers[0].ID, providers[len(providers)-1].ID, "min_id", len(providers) == limitValue, true, adminLimitPaginationParams))
	}
	out := make([]serializer.AdminPreviewCardProvider, 0, len(providers))
	for _, provider := range providers {
		out = append(out, serializer.AdminPreviewCardProviderFromModel(provider))
	}
	return c.JSON(http.StatusOK, out)
}

func (s *Server) approveAdminPreviewCardProvider(c *echo.Context) error {
	return s.reviewAdminPreviewCardProvider(c, true)
}

func (s *Server) rejectAdminPreviewCardProvider(c *echo.Context) error {
	return s.reviewAdminPreviewCardProvider(c, false)
}

func (s *Server) reviewAdminPreviewCardProvider(c *echo.Context, trendable bool) error {
	user, err := s.requireAdminWriteWithPermissions(c, nil, rolePermissionManageTaxonomies)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	action := "reject"
	if trendable {
		action = "approve"
	}
	if err := s.db.WithContext(c.Request().Context()).Transaction(func(tx *gorm.DB) error {
		var provider models.PreviewCardProvider
		if err := tx.Where("id = ?", id).First(&provider).Error; err != nil {
			return apiError(c, http.StatusNotFound, "Record not found")
		}
		if err := tx.Model(&models.PreviewCardProvider{}).Where("id = ?", id).Updates(map[string]any{
			"trendable":   trendable,
			"reviewed_at": now,
			"updated_at":  now,
		}).Error; err != nil {
			return err
		}
		return logAdminAction(tx, user.AccountID, action, previewCardProviderAuditLogTarget(provider), now)
	}); err != nil {
		return err
	}
	var provider models.PreviewCardProvider
	if err := s.db.Where("id = ?", id).First(&provider).Error; err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	return c.JSON(http.StatusOK, serializer.AdminPreviewCardProviderFromModel(provider))
}

func (s *Server) serializeAdminTrendLinks(c *echo.Context, cards []models.PreviewCard, now time.Time) []serializer.AdminTrendLink {
	out := make([]serializer.AdminTrendLink, 0, len(cards))
	for _, card := range cards {
		out = append(out, serializer.AdminTrendLinkFromModelWithHistory(s.cfg, card, s.linkHistory((*c).Request().Context(), card.ID, now), s.previewCardRequiresReview(card)))
	}
	return out
}

func (s *Server) linkHistory(ctx context.Context, previewCardID int64, now time.Time) []any {
	out := make([]any, 0, 7)
	for i := 0; i < 7; i++ {
		day := dayStart(now.AddDate(0, 0, -i))
		uses, accounts := s.linkHistoryDay(ctx, previewCardID, day)
		out = append(out, map[string]string{
			"day":      strconv.FormatInt(day.Unix(), 10),
			"uses":     strconv.FormatInt(uses, 10),
			"accounts": strconv.FormatInt(accounts, 10),
		})
	}
	return out
}

func (s *Server) linkHistoryDay(ctx context.Context, previewCardID int64, day time.Time) (int64, int64) {
	usesCtx, cancelUses := context.WithTimeout(ctx, 150*time.Millisecond)
	usesValue, usesErr := s.redisCommand(usesCtx, "GET", linkHistoryRedisKey(s.cfg.RedisNamespace, previewCardID, day, false))
	cancelUses()
	accountsCtx, cancelAccounts := context.WithTimeout(ctx, 150*time.Millisecond)
	accountsValue, accountsErr := s.redisCommand(accountsCtx, "PFCOUNT", linkHistoryRedisKey(s.cfg.RedisNamespace, previewCardID, day, true))
	cancelAccounts()
	var uses int64
	if usesErr == nil {
		uses = redisInt(usesValue)
	}
	var accounts int64
	if accountsErr == nil {
		accounts = redisInt(accountsValue)
	}
	return uses, accounts
}

func (s *Server) previewCardTrendCounts(cards []models.PreviewCard, now time.Time) map[int64]trendPreviewCardRef {
	out := make(map[int64]trendPreviewCardRef, len(cards))
	ids := make([]int64, 0, len(cards))
	for _, card := range cards {
		ids = append(ids, card.ID)
		out[card.ID] = trendPreviewCardRef{ID: card.ID}
	}
	if len(ids) == 0 || s.db == nil {
		return out
	}
	var rows []trendPreviewCardRef
	err := s.db.Table("preview_cards").
		Select("preview_cards.id, COUNT(statuses.id) AS uses, COUNT(DISTINCT statuses.account_id) AS accounts").
		Joins("LEFT JOIN preview_cards_statuses ON preview_cards_statuses.preview_card_id = preview_cards.id").
		Joins("LEFT JOIN statuses ON statuses.id = preview_cards_statuses.status_id AND statuses.deleted_at IS NULL AND statuses.visibility IN ? AND statuses.created_at >= ?", []int{0, 1}, now.AddDate(0, 0, -7)).
		Where("preview_cards.id IN ?", ids).
		Group("preview_cards.id").
		Scan(&rows).Error
	if err != nil {
		return out
	}
	for _, row := range rows {
		out[row.ID] = row
	}
	return out
}

func (s *Server) previewCardRequiresReview(card models.PreviewCard) bool {
	if card.Trendable.Valid {
		return false
	}
	provider, ok := s.previewCardProviderForURL(card.URL)
	return !ok || !provider.ReviewedAt.Valid
}

func (s *Server) previewCardProviderForURL(rawURL string) (models.PreviewCardProvider, bool) {
	host := previewCardHost(rawURL)
	if host == "" || s.db == nil {
		return models.PreviewCardProvider{}, false
	}
	suffixes := domainSuffixes(host)
	var providers []models.PreviewCardProvider
	if err := s.db.Where("lower(domain) IN ?", suffixes).Find(&providers).Error; err != nil {
		return models.PreviewCardProvider{}, false
	}
	byDomain := map[string]models.PreviewCardProvider{}
	for _, provider := range providers {
		byDomain[strings.ToLower(provider.Domain)] = provider
	}
	for _, suffix := range suffixes {
		if provider, ok := byDomain[suffix]; ok {
			return provider, true
		}
	}
	return models.PreviewCardProvider{}, false
}

func previewCardHost(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(normalizeDomain(parsed.Hostname()), "www.")
}

func domainSuffixes(host string) []string {
	parts := strings.Split(host, ".")
	out := make([]string, 0, len(parts))
	for i := 0; i < len(parts); i++ {
		suffix := strings.Join(parts[i:], ".")
		if suffix != "" {
			out = append(out, suffix)
		}
	}
	return out
}
