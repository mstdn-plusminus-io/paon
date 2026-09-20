package api

import (
	"database/sql"
	"errors"
	"html"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

const adminTagsPageSize = 20

type adminTagIndexFilters struct {
	Status string
	Name   string
	Order  string
	Page   int
}

func (s *Server) adminTagsPage(c *echo.Context) error {
	user, handled, err := s.requireAdminTagsWebUser(c)
	if handled || err != nil {
		return err
	}
	filters, err := adminTagIndexFiltersFromContext(c)
	if err != nil {
		return err
	}
	trendableByDefault := s.settingBoolValue("trendable_by_default", false)
	tags, hasMore, err := s.adminTagIndexRecords(c, filters, trendableByDefault)
	if err != nil {
		return err
	}
	return c.HTML(http.StatusOK, adminTagsHTML(tags, filters, hasMore, trendableByDefault, s.webLocale(c, user)))
}

func adminTagIndexFiltersFromContext(c *echo.Context) (adminTagIndexFilters, error) {
	filters := adminTagIndexFilters{
		Status: strings.TrimSpace(c.QueryParam("status")),
		Name:   strings.TrimSpace(c.QueryParam("name")),
		Order:  strings.TrimSpace(c.QueryParam("order")),
		Page:   1,
	}
	if filters.Order == "" {
		filters.Order = "newest"
	}
	switch filters.Status {
	case "", "reviewed", "review_requested", "unreviewed", "trendable", "not_trendable", "usable", "not_usable":
	default:
		return filters, echo.NewHTTPError(http.StatusBadRequest, "Unknown status: "+filters.Status)
	}
	switch filters.Order {
	case "newest", "oldest":
	default:
		return filters, echo.NewHTTPError(http.StatusBadRequest, "Unknown order: "+filters.Order)
	}
	if page, err := strconv.Atoi(strings.TrimSpace(c.QueryParam("page"))); err == nil && page > 0 {
		filters.Page = page
	}
	return filters, nil
}

func (s *Server) adminTagIndexRecords(c *echo.Context, filters adminTagIndexFilters, trendableByDefault bool) ([]models.Tag, bool, error) {
	if s.db == nil {
		return []models.Tag{}, false, nil
	}
	query := s.db.Model(&models.Tag{})
	switch filters.Status {
	case "reviewed":
		query = query.Where("tags.reviewed_at IS NOT NULL")
	case "review_requested":
		query = query.Where("tags.reviewed_at IS NULL").Where("tags.requested_review_at IS NOT NULL")
	case "unreviewed":
		query = query.Where("tags.reviewed_at IS NULL")
	case "trendable":
		if trendableByDefault {
			query = query.Where("tags.trendable = TRUE OR tags.trendable IS NULL")
		} else {
			query = query.Where("tags.trendable = TRUE")
		}
	case "not_trendable":
		query = query.Where("tags.trendable = FALSE")
	case "usable":
		query = query.Where("tags.usable = TRUE OR tags.usable IS NULL")
	case "not_usable":
		query = query.Where("tags.usable = FALSE")
	}
	if filters.Name != "" {
		query = query.Where("LOWER(tags.name) LIKE ?", adminTagSearchPattern(filters.Name))
	}
	if filters.Order == "oldest" {
		query = query.Order("tags.created_at ASC").Order("tags.id ASC")
	} else {
		query = query.Order("tags.created_at DESC").Order("tags.id DESC")
	}
	var tags []models.Tag
	if err := query.Offset((filters.Page - 1) * adminTagsPageSize).Limit(adminTagsPageSize + 1).Find(&tags).Error; err != nil {
		return nil, false, err
	}
	hasMore := len(tags) > adminTagsPageSize
	if hasMore {
		tags = tags[:adminTagsPageSize]
	}
	return tags, hasMore, nil
}

func adminTagSearchPattern(value string) string {
	normalized := railsNormalizeHashtagName(strings.TrimSpace(value))
	normalized = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(normalized)
	return normalized + "%"
}

func adminTagsHTML(tags []models.Tag, filters adminTagIndexFilters, hasMore bool, trendableByDefault bool, locale ...string) string {
	loc := settingsLocaleArgOrEnglish(locale...)
	var body strings.Builder
	body.WriteString(`<form action="/admin/tags" method="get" class="simple_form"><div class="filters">`)
	body.WriteString(adminTagIndexSelectHTML("status", adminT(loc, "admin.tags.moderation.title", "Moderation status"), filters.Status, []relationshipFilterLink{
		{Label: adminT(loc, "generic.all", "All"), Href: ""},
		{Label: adminT(loc, "admin.tags.moderation.reviewed", "Reviewed"), Href: "reviewed"},
		{Label: adminT(loc, "admin.tags.moderation.review_requested", "Review requested"), Href: "review_requested"},
		{Label: adminT(loc, "admin.tags.moderation.unreviewed", "Unreviewed"), Href: "unreviewed"},
		{Label: adminT(loc, "admin.tags.moderation.trendable", "Trendable"), Href: "trendable"},
		{Label: adminT(loc, "admin.tags.moderation.not_trendable", "Not trendable"), Href: "not_trendable"},
		{Label: adminT(loc, "admin.tags.moderation.usable", "Usable"), Href: "usable"},
		{Label: adminT(loc, "admin.tags.moderation.not_usable", "Not usable"), Href: "not_usable"},
	}))
	body.WriteString(adminTagIndexSelectHTML("order", adminT(loc, "generic.order_by", "Order by"), filters.Order, []relationshipFilterLink{
		{Label: adminT(loc, "admin.tags.newest", "Newest"), Href: "newest"},
		{Label: adminT(loc, "admin.tags.oldest", "Oldest"), Href: "oldest"},
	}))
	body.WriteString(`</div><div class="fields-group"><div class="input string optional"><input class="string optional" type="text" name="name" value="` + html.EscapeString(filters.Name) + `" placeholder="` + html.EscapeString(adminT(loc, "admin.tags.name", "Name")) + `"></div></div><div class="actions"><button class="button" type="submit">` + html.EscapeString(adminT(loc, "admin.tags.search", "Search")) + `</button> <a class="button negative" href="/admin/tags">` + html.EscapeString(adminT(loc, "admin.tags.reset", "Reset")) + `</a></div></form><hr class="spacer"><div class="batch-table"><div class="batch-table__body">`)
	if len(tags) == 0 {
		body.WriteString(adminNothingHereHTML(loc, "nothing-here--under-tabs nothing-here--no-toolbar"))
	} else {
		for _, tag := range tags {
			body.WriteString(adminTagIndexRowHTML(tag, trendableByDefault, loc))
		}
	}
	body.WriteString(`</div></div>`)
	body.WriteString(adminTagIndexPaginationHTML(loc, filters, hasMore))
	return authPageHTML(adminT(loc, "admin.tags.title", "Hashtags"), "", "", body.String(), loc)
}

func adminTagIndexSelectHTML(name string, label string, selected string, options []relationshipFilterLink) string {
	var body strings.Builder
	body.WriteString(`<div class="filter-subset filter-subset--with-select"><strong>` + html.EscapeString(label) + `</strong><div class="input select optional"><select name="` + html.EscapeString(name) + `">`)
	for _, option := range options {
		selectedAttr := ""
		if option.Href == selected {
			selectedAttr = ` selected`
		}
		body.WriteString(`<option value="` + html.EscapeString(option.Href) + `"` + selectedAttr + `>` + html.EscapeString(option.Label) + `</option>`)
	}
	body.WriteString(`</select></div></div>`)
	return body.String()
}

func adminTagIndexFilterValues(filters adminTagIndexFilters) url.Values {
	values := url.Values{}
	if filters.Status != "" {
		values.Set("status", filters.Status)
	}
	if filters.Name != "" {
		values.Set("name", filters.Name)
	}
	if filters.Order != "" && filters.Order != "newest" {
		values.Set("order", filters.Order)
	}
	return values
}

func adminTagIndexPaginationHTML(locale string, filters adminTagIndexFilters, hasMore bool) string {
	var links []string
	values := adminTagIndexFilterValues(filters)
	if filters.Page > 1 {
		links = append(links, `<a rel="prev" href="`+html.EscapeString(adminRailsPaginationURL("/admin/tags", values, filters.Page-1))+`">`+html.EscapeString(adminT(locale, "pagination.prev", "Previous"))+`</a>`)
	}
	if hasMore {
		links = append(links, `<a rel="next" href="`+html.EscapeString(adminRailsPaginationURL("/admin/tags", values, filters.Page+1))+`">`+html.EscapeString(adminT(locale, "pagination.next", "Next"))+`</a>`)
	}
	if len(links) == 0 {
		return ""
	}
	return `<nav class="pagination">` + strings.Join(links, " ") + `</nav>`
}

func adminTagIndexRowHTML(tag models.Tag, trendableByDefault bool, locale string) string {
	usable := nullBoolDefaultAPI(tag.Usable, true)
	trendable := nullBoolDefaultAPI(tag.Trendable, trendableByDefault)
	classes := []string{"batch-table__row"}
	if tag.ReviewedAt.Valid && !usable {
		classes = append(classes, "batch-table__row--muted")
	}
	usableKey, usableLabel := "admin.tags.moderation.not_usable", "Not usable"
	if usable {
		usableKey, usableLabel = "admin.tags.moderation.usable", "Usable"
	}
	trendableKey, trendableLabel := "admin.tags.moderation.not_trendable", "Not trendable"
	if trendable {
		trendableKey, trendableLabel = "admin.tags.moderation.trendable", "Trendable"
	}
	meta := []string{
		html.EscapeString(adminT(locale, usableKey, usableLabel)),
		html.EscapeString(adminT(locale, trendableKey, trendableLabel)),
	}
	if tag.RequestedReviewAt.Valid {
		meta = append(meta, `<span class="negative-hint">`+html.EscapeString(adminT(locale, "admin.tags.moderation.review_requested", "Review requested"))+`</span>`)
	} else if !tag.ReviewedAt.Valid {
		meta = append(meta, `<span class="warning-hint">`+html.EscapeString(adminT(locale, "admin.tags.moderation.pending_review", "Pending review"))+`</span>`)
	}
	return `<div class="` + html.EscapeString(strings.Join(classes, " ")) + `"><div class="batch-table__row__content batch-table__row__content--padded pending-account"><div class="pending-account__header"><strong><a href="/admin/tags/` + strconv.FormatInt(tag.ID, 10) + `">#` + html.EscapeString(adminTagDisplayName(tag)) + `</a></strong><br>` + strings.Join(meta, " · ") + `</div></div></div>`
}

func (s *Server) adminTagPage(c *echo.Context) error {
	user, handled, err := s.requireAdminTagsWebUser(c)
	if handled || err != nil {
		return err
	}
	locale := s.webLocale(c, user)
	tag, err := s.findAdminTag(c.Param("id"))
	if err != nil {
		return c.HTML(http.StatusNotFound, authPageHTML(adminT(locale, "admin.tags.tag", "Tag"), "", adminT(locale, "admin.tags.not_found", "Tag not found."), "", locale))
	}
	return c.HTML(http.StatusOK, adminTagHTML(*tag, c.QueryParam("notice"), c.QueryParam("error"), adminTagHTMLOptions{Locale: locale, ShowDashboard: s.userCan(user, rolePermissionViewDashboard), TrendableByDefault: s.settingBoolValue("trendable_by_default", false)}))
}

func (s *Server) updateAdminTagWeb(c *echo.Context) error {
	user, handled, err := s.requireAdminTagsWebUser(c)
	if handled || err != nil {
		return err
	}
	locale := s.webLocale(c, user)
	tag, err := s.findAdminTag(c.Param("id"))
	if err != nil {
		return c.HTML(http.StatusNotFound, authPageHTML(adminT(locale, "admin.tags.tag", "Tag"), "", adminT(locale, "admin.tags.not_found", "Tag not found."), "", locale))
	}
	updates, _, err := adminTagUpdatesFromForm(c, *tag, locale)
	if errors.Is(err, errAdminTagParamsMissing) {
		return apiError(c, http.StatusBadRequest, "Malformed request")
	}
	if err != nil {
		return c.HTML(http.StatusOK, adminTagHTML(*tag, "", err.Error(), adminTagHTMLOptions{Locale: locale, TrendableByDefault: s.settingBoolValue("trendable_by_default", false)}))
	}
	if err := s.db.Model(&models.Tag{}).Where("id = ?", tag.ID).Updates(updates).Error; err != nil {
		return err
	}
	s.meiliIndexTagsBestEffort(c.Request().Context(), []int64{tag.ID})
	return c.Redirect(http.StatusFound, "/admin/tags/"+strconv.FormatInt(tag.ID, 10)+"?notice="+url.QueryEscape(adminT(locale, "admin.tags.updated_msg", "Tag updated")))
}

func (s *Server) requireAdminTagsWebUser(c *echo.Context) (*models.User, bool, error) {
	user, _, handled, err := s.requireFunctionalWebUser(c)
	if handled || err != nil {
		return nil, handled, err
	}
	if !s.userCan(user, rolePermissionManageTaxonomies) {
		locale := s.webLocale(c, user)
		return nil, true, c.HTML(http.StatusForbidden, authPageHTML(adminT(locale, "admin.tags.tag", "Tag"), "", adminT(locale, "admin.tags.not_permitted", "You are not allowed to manage tags."), "", locale))
	}
	return user, false, nil
}

func adminTagUpdatesFromForm(c *echo.Context, tag models.Tag, locale ...string) (map[string]any, models.Tag, error) {
	if err := c.Request().ParseForm(); err != nil {
		return nil, tag, err
	}
	const prefix = "tag"
	if !formHasNestedPrefix(c.Request().Form, prefix) {
		return nil, tag, errAdminTagParamsMissing
	}
	loc := settingsLocaleArgOrEnglish(locale...)
	now := time.Now().UTC()
	updates := map[string]any{
		"reviewed_at": now,
		"updated_at":  now,
	}
	if value, ok := c.Request().Form["tag[name]"]; ok {
		name := value[len(value)-1]
		if name == "" || !railsValidTagName(name) {
			return nil, tag, errAdminSetting(adminT(loc, "admin.tags.invalid_name", "Name is invalid"))
		}
		normalized := railsNormalizeHashtagName(name)
		if normalized != strings.ToLower(tag.Name) {
			return nil, tag, errAdminSetting(adminTagPreviousNameError(loc))
		}
		updates["name"] = name
		tag.Name = name
	}
	if value, ok := c.Request().Form["tag[display_name]"]; ok {
		displayName := value[len(value)-1]
		if displayName != "" {
			if !railsValidTagName(displayName) {
				return nil, tag, errAdminSetting(adminT(loc, "admin.tags.invalid_display_name", "Display name is invalid"))
			}
			normalized := railsNormalizeHashtagName(displayName)
			if normalized != strings.ToLower(tag.Name) {
				return nil, tag, errAdminSetting(adminTagPreviousNameError(loc))
			}
		}
		updates["display_name"] = sql.NullString{String: displayName, Valid: displayName != ""}
		tag.DisplayName = sql.NullString{String: displayName, Valid: displayName != ""}
	}
	if value, ok := adminTagNestedBool(c, "tag[usable]"); ok {
		updates["usable"] = sql.NullBool{Bool: value, Valid: true}
		tag.Usable = sql.NullBool{Bool: value, Valid: true}
	}
	if value, ok := adminTagNestedBool(c, "tag[trendable]"); ok {
		updates["trendable"] = sql.NullBool{Bool: value, Valid: true}
		tag.Trendable = sql.NullBool{Bool: value, Valid: true}
	}
	if value, ok := adminTagNestedBool(c, "tag[listable]"); ok {
		updates["listable"] = sql.NullBool{Bool: value, Valid: true}
		tag.Listable = sql.NullBool{Bool: value, Valid: true}
	}
	tag.ReviewedAt = sql.NullTime{Time: now, Valid: true}
	tag.UpdatedAt = now
	return updates, tag, nil
}

var errAdminTagParamsMissing = errors.New("admin tag root parameter is missing")

func adminTagPreviousNameError(locale string) string {
	return settingsT(locale, "tags.does_not_match_previous_name", "does not match the previous name")
}

func adminTagNestedBool(c *echo.Context, key string) (bool, bool) {
	if _, ok := c.Request().Form[key]; !ok {
		return false, false
	}
	return truthy(lastFormValue(c.Request().Form, key)), true
}

func adminTagHTML(tag models.Tag, notice string, errorText string, args ...any) string {
	options := adminTagHTMLArgs(args...)
	loc := options.Locale
	name := adminTagDisplayName(tag)
	trendable := nullBoolDefaultAPI(tag.Trendable, options.TrendableByDefault)
	var body strings.Builder
	if options.ShowDashboard {
		body.WriteString(adminTagDashboardHTML(tag, loc, options.TrendableByDefault))
	}
	body.WriteString(`<form method="post" action="/admin/tags/` + strconv.FormatInt(tag.ID, 10) + `" class="simple_form edit_tag" id="edit_tag_` + strconv.FormatInt(tag.ID, 10) + `" novalidate><input type="hidden" name="_method" value="patch"><div class="fields-group"><div class="input string optional tag_display_name with_block_label"><div class="label_input"><label class="string optional" for="tag_display_name">` + html.EscapeString(adminT(loc, "admin.accounts.display_name", "Display name")) + `</label><input class="string optional" type="text" name="tag[display_name]" id="tag_display_name" value="` + html.EscapeString(name) + `"></div></div></div><div class="fields-group">`)
	body.WriteString(adminTagCheckboxHTML("tag[usable]", adminT(loc, "admin.trends.tags.usable", "Usable"), nullBoolDefaultAPI(tag.Usable, true)))
	body.WriteString(adminTagCheckboxHTML("tag[trendable]", adminT(loc, "admin.trends.tags.trendable", "Trendable"), trendable))
	body.WriteString(adminTagCheckboxHTML("tag[listable]", adminT(loc, "admin.trends.tags.listable", "Listable"), nullBoolDefaultAPI(tag.Listable, true)))
	body.WriteString(`</div><div class="actions"><button class="button" type="submit">` + html.EscapeString(adminT(loc, "generic.save_changes", "Save changes")) + `</button></div></form>`)
	return authPageHTML("#"+name, notice, errorText, body.String(), loc)
}

type adminTagHTMLOptions struct {
	Locale             string
	ShowDashboard      bool
	TrendableByDefault bool
}

func adminTagHTMLArgs(args ...any) adminTagHTMLOptions {
	options := adminTagHTMLOptions{Locale: "en"}
	for _, arg := range args {
		switch value := arg.(type) {
		case string:
			if strings.TrimSpace(value) != "" {
				options.Locale = value
			}
		case bool:
			options.ShowDashboard = value
		case adminTagHTMLOptions:
			if strings.TrimSpace(value.Locale) != "" {
				options.Locale = value.Locale
			}
			options.ShowDashboard = value.ShowDashboard
			options.TrendableByDefault = value.TrendableByDefault
		}
	}
	options.Locale = settingsLocaleArgOrEnglish(options.Locale)
	return options
}

func adminTagDashboardHTML(tag models.Tag, locale string, trendableByDefault ...bool) string {
	startAt, endAt := adminTagDashboardDateRange(time.Now().UTC())
	params := map[string]any{"id": strconv.FormatInt(tag.ID, 10)}
	trendableDefault := false
	if len(trendableByDefault) > 0 {
		trendableDefault = trendableByDefault[0]
	}
	var body strings.Builder
	body.WriteString(`<div class="content__heading__actions"><span>` + html.EscapeString(startAt) + ` - ` + html.EscapeString(endAt) + `</span></div>`)
	body.WriteString(`<div class="dashboard">`)
	body.WriteString(`<div class="dashboard__item">` + adminDashboardReactComponent("counter", map[string]any{"measure": "tag_accounts", "start_at": startAt, "end_at": endAt, "params": params, "label": adminT(locale, "admin.trends.tags.dashboard.tag_accounts_measure", "unique uses"), "href": "/tags/" + url.PathEscape(tag.Name), "target": "_blank"}) + `</div>`)
	body.WriteString(`<div class="dashboard__item">` + adminDashboardReactComponent("counter", map[string]any{"measure": "tag_uses", "start_at": startAt, "end_at": endAt, "params": params, "label": adminT(locale, "admin.trends.tags.dashboard.tag_uses_measure", "total uses")}) + `</div>`)
	body.WriteString(`<div class="dashboard__item">` + adminDashboardReactComponent("counter", map[string]any{"measure": "tag_servers", "start_at": startAt, "end_at": endAt, "params": params, "label": adminT(locale, "admin.trends.tags.dashboard.tag_servers_measure", "different servers")}) + `</div>`)
	body.WriteString(`<div class="dashboard__item">` + adminDashboardReactComponent("dimension", map[string]any{"dimension": "tag_servers", "start_at": startAt, "end_at": endAt, "params": params, "limit": 8, "label": adminT(locale, "admin.trends.tags.dashboard.tag_servers_dimension", "Top servers")}) + `</div>`)
	body.WriteString(`<div class="dashboard__item">` + adminDashboardReactComponent("dimension", map[string]any{"dimension": "tag_languages", "start_at": startAt, "end_at": endAt, "params": params, "limit": 8, "label": adminT(locale, "admin.trends.tags.dashboard.tag_languages_dimension", "Top languages")}) + `</div>`)
	body.WriteString(`<div class="dashboard__item">`)
	body.WriteString(adminTagDashboardQuickAccess(tag.ID, nullBoolDefaultAPI(tag.Usable, true), adminT(locale, "admin.trends.tags.usable", "Can be used"), adminT(locale, "admin.trends.tags.not_usable", "Cannot be used")))
	body.WriteString(adminTagDashboardQuickAccess(tag.ID, nullBoolDefaultAPI(tag.Trendable, trendableDefault), adminT(locale, "admin.trends.tags.trendable", "Can appear under trends"), adminT(locale, "admin.trends.tags.not_trendable", "Won't appear under trends")))
	body.WriteString(adminTagDashboardQuickAccess(tag.ID, nullBoolDefaultAPI(tag.Listable, true), adminT(locale, "admin.trends.tags.listable", "Can be suggested"), adminT(locale, "admin.trends.tags.not_listable", "Won't be suggested")))
	body.WriteString(`</div></div><hr class="spacer">`)
	return body.String()
}

func adminTagDashboardDateRange(now time.Time) (string, string) {
	date := time.Date(now.UTC().Year(), now.UTC().Month(), now.UTC().Day(), 0, 0, 0, 0, time.UTC)
	return date.AddDate(0, 0, -6).Format("2006-01-02"), date.AddDate(0, 0, -1).Format("2006-01-02")
}

func adminTagDashboardQuickAccess(tagID int64, positive bool, positiveLabel string, negativeLabel string) string {
	className := "dashboard__quick-access negative"
	label := negativeLabel
	if positive {
		className = "dashboard__quick-access positive"
		label = positiveLabel
	}
	return `<a href="/admin/tags/` + strconv.FormatInt(tagID, 10) + `" class="` + className + `"><span>` + html.EscapeString(label) + `</span></a>`
}

func adminTagDisplayName(tag models.Tag) string {
	return tag.DisplayNameValue()
}

func adminTagCheckboxHTML(name string, label string, checked bool) string {
	checkedAttr := ""
	if checked {
		checkedAttr = ` checked`
	}
	id := strings.NewReplacer("[", "_", "]", "").Replace(name)
	return `<div class="input boolean optional ` + html.EscapeString(id) + `"><div class="label_input"><label class="boolean optional" for="` + html.EscapeString(id) + `"><input type="hidden" name="` + html.EscapeString(name) + `" value="0"><input class="boolean optional" type="checkbox" name="` + html.EscapeString(name) + `" id="` + html.EscapeString(id) + `" value="1"` + checkedAttr + `> ` + html.EscapeString(label) + `</label></div></div>`
}

func adminBoolLabel(locale string, value bool) string {
	if value {
		return adminT(locale, "generic.enabled", "Enabled")
	}
	return adminT(locale, "generic.disabled", "Disabled")
}

func nullBoolDefaultAPI(value sql.NullBool, fallback bool) bool {
	if value.Valid {
		return value.Bool
	}
	return fallback
}
