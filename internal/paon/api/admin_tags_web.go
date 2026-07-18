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
