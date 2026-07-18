package api

import (
	"errors"
	"html"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

func (s *Server) webFiltersPage(c *echo.Context) error {
	account, _, user, err := s.requireFunctionalAccountForWeb(c)
	if err != nil {
		return webAuthResponseError(err)
	}
	var filters []models.CustomFilter
	if err := s.db.Preload("Keywords").Preload("Statuses").Where("account_id = ?", account.ID).Order("phrase ASC").Find(&filters).Error; err != nil {
		return err
	}
	locale := s.webLocale(c, user)
	theme := settingsWebTheme(decodeUserSettings(user.Settings.String))
	renderArgs, err := s.settingsRenderArgs(c.Request().URL.Path, locale, theme, user, account)
	if err != nil {
		return err
	}
	return c.HTML(http.StatusOK, webFiltersHTML(filters, c.QueryParam("notice"), c.QueryParam("error"), renderArgs...))
}

func (s *Server) newWebFilter(c *echo.Context) error {
	account, _, user, err := s.requireFunctionalAccountForWeb(c)
	if err != nil {
		return webAuthResponseError(err)
	}
	locale := s.webLocale(c, user)
	theme := settingsWebTheme(decodeUserSettings(user.Settings.String))
	renderArgs, err := s.settingsRenderArgs(c.Request().URL.Path, locale, theme, user, account)
	if err != nil {
		return err
	}
	filter := models.CustomFilter{Action: 0}
	return c.HTML(http.StatusOK, webFilterFormHTML(settingsT(locale, "filters.new.title", "New filter"), "/filters", http.MethodPost, filter, "", "", renderArgs...))
}

func (s *Server) editWebFilter(c *echo.Context) error {
	account, _, user, err := s.requireFunctionalAccountForWeb(c)
	if err != nil {
		return webAuthResponseError(err)
	}
	locale := s.webLocale(c, user)
	theme := settingsWebTheme(decodeUserSettings(user.Settings.String))
	renderArgs, err := s.settingsRenderArgs(c.Request().URL.Path, locale, theme, user, account)
	if err != nil {
		return err
	}
	filter, err := s.findFilter(account.ID, c.Param("id"))
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	return c.HTML(http.StatusOK, webFilterFormHTML(settingsT(locale, "filters.edit.title", "Edit filter"), "/filters/"+strconv.FormatInt(filter.ID, 10), "put", *filter, c.QueryParam("notice"), c.QueryParam("error"), renderArgs...))
}

func (s *Server) createWebFilter(c *echo.Context) error {
	account, _, user, err := s.requireFunctionalAccountForWeb(c)
	if err != nil {
		return webAuthResponseError(err)
	}
	locale := s.webLocale(c, user)
	theme := settingsWebTheme(decodeUserSettings(user.Settings.String))
	renderArgs, err := s.settingsRenderArgs(c.Request().URL.Path, locale, theme, user, account)
	if err != nil {
		return err
	}
	payload, err := parseWebFilterPayload(c)
	if err != nil {
		if errors.Is(err, errMalformedWebFilterPayload) {
			return apiError(c, http.StatusBadRequest, "Malformed request")
		}
		return c.HTML(http.StatusOK, webFilterFormHTML(settingsT(locale, "filters.new.title", "New filter"), "/filters", http.MethodPost, models.CustomFilter{Action: 0}, "", settingsT(locale, "filters.errors.invalid_filter", "Filter is invalid"), renderArgs...))
	}
	payload.Context = normalizeFilterContexts(payload.Context)
	if strings.TrimSpace(payload.Title) == "" || len(payload.Context) == 0 || !validFilterContexts(payload.Context) {
		return c.HTML(http.StatusOK, webFilterFormHTML(settingsT(locale, "filters.new.title", "New filter"), "/filters", http.MethodPost, webFilterFromPayload(models.CustomFilter{Action: 0}, payload, time.Now().UTC()), "", settingsT(locale, "filters.errors.invalid_filter", "Filter is invalid"), renderArgs...))
	}
	action, ok := filterActionValue(firstNonEmpty(payload.FilterAction, "warn"))
	if !ok {
		return c.HTML(http.StatusOK, webFilterFormHTML(settingsT(locale, "filters.new.title", "New filter"), "/filters", http.MethodPost, webFilterFromPayload(models.CustomFilter{Action: 0}, payload, time.Now().UTC()), "", settingsT(locale, "filters.errors.invalid_action", "Filter action is invalid"), renderArgs...))
	}
	now := time.Now().UTC()
	filter := models.CustomFilter{
		AccountID: models.CustomFilterAccountID(account.ID),
		Phrase:    payload.Title,
		Context:   models.StringArray(payload.Context),
		Action:    action,
		ExpiresAt: expiresAtFromSeconds(payload.ExpiresIn, now),
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&filter).Error; err != nil {
			return err
		}
		return applyFilterKeywordAttributes(tx, filter.ID, payload.KeywordsAttributes, now)
	}); err != nil {
		return err
	}
	s.invalidateFilterCacheAndBroadcast(account.ID)
	return c.Redirect(http.StatusFound, "/filters")
}

func (s *Server) updateWebFilter(c *echo.Context) error {
	account, _, user, err := s.requireFunctionalAccountForWeb(c)
	if err != nil {
		return webAuthResponseError(err)
	}
	locale := s.webLocale(c, user)
	theme := settingsWebTheme(decodeUserSettings(user.Settings.String))
	renderArgs, err := s.settingsRenderArgs(c.Request().URL.Path, locale, theme, user, account)
	if err != nil {
		return err
	}
	if c.Request().Method == http.MethodPost && methodOverrideIs(c, "delete") {
		return s.destroyWebFilter(c)
	}
	if c.Request().Method == http.MethodPost && !methodOverrideIs(c, "put", "patch") {
		return c.Redirect(http.StatusFound, "/filters")
	}
	filter, err := s.findFilter(account.ID, c.Param("id"))
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	payload, err := parseWebFilterPayload(c)
	if err != nil {
		if errors.Is(err, errMalformedWebFilterPayload) {
			return apiError(c, http.StatusBadRequest, "Malformed request")
		}
		return c.HTML(http.StatusOK, webFilterFormHTML(settingsT(locale, "filters.edit.title", "Edit filter"), "/filters/"+strconv.FormatInt(filter.ID, 10), "put", *filter, "", settingsT(locale, "filters.errors.invalid_filter", "Filter is invalid"), renderArgs...))
	}
	updates := map[string]any{"updated_at": time.Now().UTC()}
	if strings.TrimSpace(payload.Title) == "" {
		return c.HTML(http.StatusOK, webFilterFormHTML(settingsT(locale, "filters.edit.title", "Edit filter"), "/filters/"+strconv.FormatInt(filter.ID, 10), "put", webFilterFromPayload(*filter, payload, time.Now().UTC()), "", settingsT(locale, "filters.errors.invalid_filter", "Filter is invalid"), renderArgs...))
	}
	updates["phrase"] = payload.Title
	contexts := normalizeFilterContexts(payload.Context)
	if len(contexts) > 0 {
		if !validFilterContexts(contexts) {
			return c.HTML(http.StatusOK, webFilterFormHTML(settingsT(locale, "filters.edit.title", "Edit filter"), "/filters/"+strconv.FormatInt(filter.ID, 10), "put", webFilterFromPayload(*filter, payload, time.Now().UTC()), "", settingsT(locale, "filters.errors.invalid_context", "Filter context is invalid"), renderArgs...))
		}
		updates["context"] = models.StringArray(contexts)
	}
	if payload.FilterAction != "" {
		action, ok := filterActionValue(payload.FilterAction)
		if !ok {
			return c.HTML(http.StatusOK, webFilterFormHTML(settingsT(locale, "filters.edit.title", "Edit filter"), "/filters/"+strconv.FormatInt(filter.ID, 10), "put", webFilterFromPayload(*filter, payload, time.Now().UTC()), "", settingsT(locale, "filters.errors.invalid_action", "Filter action is invalid"), renderArgs...))
		}
		updates["action"] = action
	}
	if payload.ExpiresInSet {
		updates["expires_at"] = expiresAtFromSeconds(payload.ExpiresIn, time.Now().UTC())
	}
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.CustomFilter{}).Where("id = ? AND account_id = ?", filter.ID, account.ID).Updates(updates).Error; err != nil {
			return err
		}
		return applyFilterKeywordAttributes(tx, filter.ID, payload.KeywordsAttributes, time.Now().UTC())
	}); err != nil {
		return err
	}
	s.invalidateFilterCacheAndBroadcast(account.ID)
	return c.Redirect(http.StatusFound, "/filters")
}

func (s *Server) destroyWebFilter(c *echo.Context) error {
	account, _, _, err := s.requireFunctionalAccountForWeb(c)
	if err != nil {
		return webAuthResponseError(err)
	}
	filter, err := s.findFilter(account.ID, c.Param("id"))
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("custom_filter_id = ?", filter.ID).Delete(&models.CustomFilterKeyword{}).Error; err != nil {
			return err
		}
		if err := tx.Where("custom_filter_id = ?", filter.ID).Delete(&models.CustomFilterStatus{}).Error; err != nil {
			return err
		}
		return tx.Delete(&models.CustomFilter{}, "id = ? AND account_id = ?", filter.ID, account.ID).Error
	}); err != nil {
		return err
	}
	s.invalidateFilterCacheAndBroadcast(account.ID)
	return c.Redirect(http.StatusFound, "/filters")
}

func (s *Server) webFilterStatusesPage(c *echo.Context) error {
	account, _, user, err := s.requireFunctionalAccountForWeb(c)
	if err != nil {
		return webAuthResponseError(err)
	}
	locale := s.webLocale(c, user)
	theme := settingsWebTheme(decodeUserSettings(user.Settings.String))
	renderArgs, err := s.settingsRenderArgs(c.Request().URL.Path, locale, theme, user, account)
	if err != nil {
		return err
	}
	filter, err := s.findFilterForStatusPage(account.ID, c.Param("filter_id"))
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	statuses, err := s.webFilterStatusModels(filter.ID, c)
	if err != nil {
		return err
	}
	filter.Statuses = statuses
	return c.HTML(http.StatusOK, webFilterStatusesHTMLWithConfig(s.cfg, *filter, "", "", adminTrendsPageValue(c), renderArgs...))
}

func (s *Server) batchWebFilterStatuses(c *echo.Context) error {
	account, _, user, err := s.requireFunctionalAccountForWeb(c)
	if err != nil {
		return webAuthResponseError(err)
	}
	locale := s.webLocale(c, user)
	filter, err := s.findFilter(account.ID, c.Param("filter_id"))
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	if !adminBatchFormRootPresent(c, "form_status_filter_batch_action") {
		return c.Redirect(http.StatusFound, webFilterEditRedirectPath(filter.ID, "error", adminT(locale, "admin.statuses.no_status_selected", "No statuses selected")))
	}
	if webFilterFormParamExists(c, "remove") {
		ids := formInt64Values(c, "form_status_filter_batch_action[status_filter_ids][]")
		if len(ids) == 0 {
			return c.Redirect(http.StatusFound, webFilterEditRedirectPath(filter.ID, "error", adminT(locale, "admin.statuses.no_status_selected", "No statuses selected")))
		}
		if err := s.db.Where("custom_filter_id = ? AND id IN ?", filter.ID, ids).Delete(&models.CustomFilterStatus{}).Error; err != nil {
			return err
		}
		s.invalidateFilterCacheAndBroadcast(account.ID)
	}
	return c.Redirect(http.StatusFound, webFilterEditRedirectPath(filter.ID, "", ""))
}

func webFilterFormParamExists(c *echo.Context, key string) bool {
	_ = c.Request().ParseForm()
	_, ok := c.Request().Form[key]
	return ok
}

func webFilterEditRedirectPath(filterID int64, messageKey string, message string) string {
	path := "/filters/" + strconv.FormatInt(filterID, 10) + "/edit"
	if messageKey == "" || message == "" {
		return path
	}
	values := url.Values{}
	values.Set(messageKey, message)
	return path + "?" + values.Encode()
}

func (s *Server) findFilterForStatusPage(accountID int64, id string) (*models.CustomFilter, error) {
	var filter models.CustomFilter
	err := s.db.Where("id = ? AND account_id = ?", id, accountID).First(&filter).Error
	return &filter, err
}

const webFilterStatusesPageSize = 20

var errMalformedWebFilterPayload = errors.New("malformed web filter payload")

func (s *Server) webFilterStatusModels(filterID int64, c *echo.Context) ([]models.CustomFilterStatus, error) {
	if s.db == nil {
		return []models.CustomFilterStatus{}, nil
	}
	var statuses []models.CustomFilterStatus
	err := s.db.Preload("Status").
		Preload("Status.Account").
		Preload("Status.MediaAttachments").
		Where("custom_filter_id = ?", filterID).
		Order("id ASC").
		Offset(adminPageOffset(c, webFilterStatusesPageSize)).
		Limit(webFilterStatusesPageSize).
		Find(&statuses).Error
	return statuses, err
}

func parseWebFilterPayload(c *echo.Context) (filterPayload, error) {
	var payload filterPayload
	req := c.Request()
	if err := req.ParseForm(); err != nil {
		return payload, err
	}
	if !railsNestedFormRootPresent(c, "custom_filter") {
		return payload, errMalformedWebFilterPayload
	}
	payload.Title = lastFormValue(req.Form, "custom_filter[title]")
	if expires, ok := filterExpiresInFromForm(req.Form, "custom_filter[expires_in]"); ok {
		payload.ExpiresInSet = true
		payload.ExpiresIn = expires
	}
	payload.Context = append(payload.Context, req.Form["custom_filter[context][]"]...)
	payload.FilterAction = lastFormValue(req.Form, "custom_filter[filter_action]")
	payload.KeywordsAttributes = webFilterKeywordsFromForm(req.Form)
	return payload, nil
}

func webFilterKeywordsFromForm(values map[string][]string) []filterKeywordPayload {
	items := map[string]*filterKeywordPayload{}
	for key, rawValues := range values {
		rest, ok := strings.CutPrefix(key, "custom_filter[keywords_attributes][")
		if !ok || len(rawValues) == 0 {
			continue
		}
		parts := strings.SplitN(rest, "][", 2)
		if len(parts) != 2 {
			continue
		}
		index := strings.TrimSuffix(parts[0], "]")
		name := strings.TrimSuffix(parts[1], "]")
		item := items[index]
		if item == nil {
			item = &filterKeywordPayload{}
			items[index] = item
		}
		value := rawValues[len(rawValues)-1]
		switch name {
		case "id":
			item.ID = value
		case "keyword":
			item.Keyword = value
			item.KeywordSet = true
		case "whole_word":
			wholeWord := truthy(value)
			item.WholeWord = &wholeWord
		case "_destroy":
			item.Destroy = truthy(value)
		}
	}
	keys := make([]string, 0, len(items))
	for key := range items {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		left, leftErr := strconv.ParseUint(keys[i], 10, 64)
		right, rightErr := strconv.ParseUint(keys[j], 10, 64)
		if leftErr == nil && rightErr == nil {
			return left < right
		}
		if leftErr == nil {
			return true
		}
		if rightErr == nil {
			return false
		}
		return keys[i] < keys[j]
	})
	out := make([]filterKeywordPayload, 0, len(keys))
	for _, key := range keys {
		out = append(out, *items[key])
	}
	return out
}

func webFilterFromPayload(base models.CustomFilter, payload filterPayload, now time.Time) models.CustomFilter {
	filter := base
	filter.Phrase = payload.Title
	filter.Context = models.StringArray(payload.Context)
	if action, ok := filterActionValue(payload.FilterAction); ok {
		filter.Action = action
	}
	if payload.ExpiresInSet {
		filter.ExpiresAt = expiresAtFromSeconds(payload.ExpiresIn, now)
	}
	filter.Keywords = webFilterKeywordsForForm(payload.KeywordsAttributes)
	return filter
}

func webFilterKeywordsForForm(attributes []filterKeywordPayload) []models.CustomFilterKeyword {
	keywords := make([]models.CustomFilterKeyword, 0, len(attributes))
	for _, attr := range attributes {
		if attr.Destroy {
			continue
		}
		keyword := models.CustomFilterKeyword{Keyword: attr.Keyword}
		if attr.ID != "" {
			if id, err := strconv.ParseInt(attr.ID, 10, 64); err == nil {
				keyword.ID = id
			}
		}
		if attr.WholeWord != nil {
			keyword.WholeWord = *attr.WholeWord
		}
		keywords = append(keywords, keyword)
	}
	return keywords
}

func webFiltersHTML(filters []models.CustomFilter, notice string, errorText string, localeAndTheme ...string) string {
	loc := settingsLocaleArgOrEnglish(localeAndTheme...)
	var rows strings.Builder
	for _, filter := range filters {
		rows.WriteString(webFilterListItemHTML(filter, loc))
	}
	listHTML := `<div class="applications-list">` + rows.String() + `</div>`
	if rows.Len() == 0 {
		listHTML = `<div class="muted-hint center-text">` + html.EscapeString(settingsT(loc, "filters.index.empty", "No filters")) + `</div>`
	}
	return authPageHTML(settingsT(loc, "filters.index.title", "Filters"), notice, errorText, `<div class="content__heading__actions"><a class="button" href="/filters/new">`+html.EscapeString(settingsT(loc, "filters.new.title", "New filter"))+`</a></div>
	    `+listHTML, localeAndTheme...)
}

func webFilterListItemHTML(filter models.CustomFilter, locale string) string {
	classes := []string{"filters-list__item"}
	if filter.ExpiresAt.Valid && filter.ExpiresAt.Time.Before(time.Now().UTC()) {
		classes = append(classes, "expired")
	}
	contexts := make([]string, 0, len(filter.Context))
	for _, context := range filter.Context {
		contexts = append(contexts, settingsT(locale, "filters.contexts."+context, context))
	}
	keywords := filterKeywordSummary(filter.Keywords)
	var permissions strings.Builder
	if len(filter.Keywords) > 0 {
		permissions.WriteString(`<li class="permissions-list__item"><div class="permissions-list__item__icon"><i class="fa fa-paragraph fa-fw"></i></div><div class="permissions-list__item__text"><div class="permissions-list__item__text__title">` + html.EscapeString(filterCountLabel(locale, "filters.index.keywords", len(filter.Keywords), "keyword")) + `</div><div class="permissions-list__item__text__type">` + html.EscapeString(keywords) + `</div></div></li>`)
	}
	if len(filter.Statuses) > 0 {
		permissions.WriteString(`<li class="permissions-list__item"><div class="permissions-list__item__icon"><i class="fa fa-comment fa-fw"></i></div><div class="permissions-list__item__text"><div class="permissions-list__item__text__title">` + html.EscapeString(filterCountLabel(locale, "filters.index.statuses", len(filter.Statuses), "status")) + `</div><div class="permissions-list__item__text__type">` + html.EscapeString(filterCountLabel(locale, "filters.index.statuses_long", len(filter.Statuses), "filtered status")) + `</div></div></li>`)
	}
	permissionHTML := `<div class="filters-list__item__permissions"><ul class="permissions-list">` + permissions.String() + `</ul></div>`
	return `<div class="` + html.EscapeString(strings.Join(classes, " ")) + `"><a class="filters-list__item__title" href="/filters/` + strconv.FormatInt(filter.ID, 10) + `/edit">` + html.EscapeString(filter.Phrase) + webFilterExpirationHTML(filter, locale) + `</a>` + permissionHTML + `<div class="announcements-list__item__action-bar"><div class="announcements-list__item__meta">` + html.EscapeString(adminTVars(locale, "filters.index.contexts", "%{contexts}", map[string]string{"contexts": strings.Join(contexts, ", ")})) + `</div><div><a class="table-action-link" href="/filters/` + strconv.FormatInt(filter.ID, 10) + `/edit"><i class="fa fa-pencil fa-fw"></i> ` + html.EscapeString(settingsT(locale, "filters.edit.title", "Edit filter")) + `</a> <a class="table-action-link" href="/filters/` + strconv.FormatInt(filter.ID, 10) + `" data-method="delete" data-confirm="` + html.EscapeString(adminT(locale, "admin.accounts.are_you_sure", "Are you sure?")) + `"><i class="fa fa-times fa-fw"></i> ` + html.EscapeString(settingsT(locale, "filters.index.delete", "Delete")) + `</a></div></div></div>`
}

func webFilterExpirationHTML(filter models.CustomFilter, locale string) string {
	if !filter.ExpiresAt.Valid {
		return ""
	}
	if filter.ExpiresAt.Time.Before(time.Now().UTC()) {
		return `<div class="expiration" title="` + html.EscapeString(filter.ExpiresAt.Time.UTC().Format(time.RFC3339)) + `">` + html.EscapeString(settingsT(locale, "invites.expired", "Expired")) + `</div>`
	}
	return `<div class="expiration" title="` + html.EscapeString(filter.ExpiresAt.Time.UTC().Format(time.RFC3339)) + `">` + html.EscapeString(filter.ExpiresAt.Time.UTC().Format(time.RFC3339)) + `</div>`
}

func filterKeywordSummary(keywords []models.CustomFilterKeyword) string {
	values := make([]string, 0, len(keywords))
	for i, keyword := range keywords {
		if i >= 5 {
			values = append(values, "...")
			break
		}
		values = append(values, keyword.Keyword)
	}
	return strings.Join(values, ", ")
}

func filterCountLabel(locale string, key string, count int, fallbackSingular string) string {
	value := webT(locale, key)
	if strings.TrimSpace(value) == "" || value == key {
		if count == 1 {
			return "1 " + fallbackSingular
		}
		return strconv.Itoa(count) + " " + fallbackSingular + "s"
	}
	value = strings.ReplaceAll(value, "%{count}", strconv.Itoa(count))
	return value
}

func webFilterFormHTML(title string, action string, method string, filter models.CustomFilter, notice string, errorText string, localeAndTheme ...string) string {
	loc := settingsLocaleArgOrEnglish(localeAndTheme...)
	methodField := ""
	if method != "" && !strings.EqualFold(method, http.MethodPost) {
		methodField = `<input type="hidden" name="_method" value="` + html.EscapeString(method) + `">`
	}
	saveLabel := settingsT(loc, "generic.save_changes", "Save filter")
	if strings.EqualFold(method, http.MethodPost) {
		saveLabel = settingsT(loc, "filters.new.save", "Save new filter")
	}
	statusesHint := ""
	if filter.ID != 0 && len(filter.Statuses) > 0 {
		statusesHint = `<hr class="spacer"><h4>` + html.EscapeString(settingsT(loc, "filters.edit.statuses", "Individual posts")) + `</h4><p class="muted-hint">` + settingsTVars(loc, "filters.edit.statuses_hint_html", "This filter applies to select individual posts regardless of whether they match the keywords below. <a href=\"%{path}\">Review or remove posts from the filter</a>.", map[string]string{"path": "/filters/" + strconv.FormatInt(filter.ID, 10) + "/statuses"}) + `</p>`
	}
	formClass := "new_custom_filter"
	formID := "new_custom_filter"
	if filter.ID != 0 || !strings.EqualFold(method, http.MethodPost) {
		formClass = "edit_custom_filter"
		formID = "edit_custom_filter_" + strconv.FormatInt(filter.ID, 10)
	}
	required := filterRequiredMarker(loc)
	body := `<form class="simple_form ` + formClass + `" id="` + formID + `" novalidate="novalidate" method="post" action="` + html.EscapeString(action) + `">` + methodField + `
      <div class="fields-row">
        <div class="fields-row__column fields-row__column-6 fields-group">
          <div class="input with_label string required custom_filter_title"><div class="label_input"><label class="string required" for="custom_filter_title">` + html.EscapeString(settingsT(loc, "simple_form.labels.defaults.title", "Title")) + required + `</label><div class="label_input__wrapper"><input class="string required" type="text" value="` + html.EscapeString(filter.Phrase) + `" name="custom_filter[title]" id="custom_filter_title"></div></div></div>
        </div>
        <div class="fields-row__column fields-row__column-6 fields-group">
          <div class="input with_label select optional custom_filter_expires_in"><div class="label_input"><label class="select optional" for="custom_filter_expires_in">` + html.EscapeString(settingsT(loc, "simple_form.labels.defaults.expires_in", "Expires after")) + `</label><div class="label_input__wrapper">` + filterExpiresInSelect(filter, loc) + `</div></div></div>
        </div>
      </div>
      <div class="fields-group"><div class="input with_block_label check_boxes required custom_filter_context field_with_hint"><label class="check_boxes required">` + html.EscapeString(settingsT(loc, "simple_form.labels.defaults.context", "Filter contexts")) + required + `</label><span class="hint">` + html.EscapeString(settingsT(loc, "simple_form.hints.defaults.context", "Where the filter should apply")) + `</span><div class="label_input">` + filterContextCheckboxes(filter.Context, loc) + `</div></div></div>
      <hr class="spacer">
      <div class="fields-group"><div class="input with_block_label radio_buttons required custom_filter_filter_action field_with_hint"><label class="radio_buttons required">` + html.EscapeString(settingsT(loc, "simple_form.labels.defaults.filter_action", "Filter action")) + required + `</label><span class="hint">` + html.EscapeString(settingsT(loc, "simple_form.hints.filters.action", "Choose how matching posts should be handled.")) + `</span><div class="label_input">` + filterActionRadios(filter.Action, loc) + `</div></div></div>
      ` + statusesHint + `
      <hr class="spacer">
      <h4>` + html.EscapeString(settingsT(loc, "filters.edit.keywords", "Keywords")) + `</h4>
      ` + filterKeywordFields(filter.Keywords, loc) + `
      <div class="actions"><button name="button" type="submit" class="btn">` + html.EscapeString(saveLabel) + `</button></div>
    </form>`
	return authPageHTML(title, notice, errorText, body, localeAndTheme...)
}

func filterRequiredMarker(locale string) string {
	return ` <abbr title="` + html.EscapeString(settingsT(locale, "simple_form.required.text", "required")) + `">` + html.EscapeString(settingsT(locale, "simple_form.required.mark", "*")) + `</abbr>`
}

func filterContextCheckboxes(contexts models.StringArray, locale ...string) string {
	loc := settingsLocaleArgOrEnglish(locale...)
	selected := map[string]struct{}{}
	for _, context := range contexts {
		selected[context] = struct{}{}
	}
	var out strings.Builder
	out.WriteString(`<ul><input type="hidden" name="custom_filter[context][]" value="">`)
	for _, context := range []string{"home", "notifications", "public", "thread", "account"} {
		_, ok := selected[context]
		id := `custom_filter_context_` + context
		out.WriteString(`<li class="checkbox"><label for="` + id + `"><input class="check_boxes required" type="checkbox" id="` + id + `" name="custom_filter[context][]" value="`)
		out.WriteString(context)
		out.WriteString(`"`)
		out.WriteString(checkedAttr(ok))
		out.WriteString(`>`)
		out.WriteString(html.EscapeString(settingsT(loc, "filters.contexts."+context, context)))
		out.WriteString(`</label></li>`)
	}
	out.WriteString(`</ul>`)
	return out.String()
}

func filterKeywordFields(keywords []models.CustomFilterKeyword, locale ...string) string {
	loc := settingsLocaleArgOrEnglish(locale...)
	var out strings.Builder
	if len(keywords) == 0 {
		keywords = []models.CustomFilterKeyword{{WholeWord: true}}
	}
	out.WriteString(`<div class="table-wrapper"><table class="table keywords-table"><thead><tr><th>` + html.EscapeString(settingsT(loc, "simple_form.labels.defaults.phrase", "Phrase")) + `</th><th>` + html.EscapeString(settingsT(loc, "simple_form.labels.defaults.whole_word", "Whole word")) + `</th><th></th></tr></thead><tbody>`)
	for i, keyword := range keywords {
		out.WriteString(filterKeywordRowHTML(keyword, strconv.Itoa(i), loc))
	}
	template := filterKeywordRowHTML(models.CustomFilterKeyword{WholeWord: true}, "new_keywords", loc)
	out.WriteString(`</tbody><tfoot><tr><td colspan="3"><a class="table-action-link add_fields" data-association-insertion-node=".keywords-table tbody" data-association-insertion-method="append" data-association="keyword" data-associations="keywords" data-association-insertion-template="` + html.EscapeString(template) + `" href="#"><i class="fa fa-plus"></i>` + html.EscapeString(settingsT(loc, "filters.edit.add_keyword", "Add keyword")) + `</a></td></tr></tfoot></table></div>`)
	return out.String()
}

func filterKeywordRowHTML(keyword models.CustomFilterKeyword, index string, locale string) string {
	prefix := `custom_filter[keywords_attributes][` + index + `]`
	idPrefix := `custom_filter_keywords_attributes_` + index
	out := `<tr class="nested-fields"><td><div class="input string required custom_filter_keywords_keyword"><input class="string required" type="text" value="` + html.EscapeString(keyword.Keyword) + `" name="` + prefix + `[keyword]" id="` + idPrefix + `_keyword"></div></td>`
	out += `<td><div class="label_input__wrapper"><input value="0" type="hidden" name="` + prefix + `[whole_word]"><label class="checkbox"><input class="boolean optional" type="checkbox" value="1" name="` + prefix + `[whole_word]" id="` + idPrefix + `_whole_word"` + checkedAttr(keyword.WholeWord) + `></label></div></td><td>`
	if keyword.ID != 0 {
		out += `<input type="hidden" name="` + prefix + `[id]" value="` + strconv.FormatInt(keyword.ID, 10) + `">`
	}
	out += `<input value="false" type="hidden" name="` + prefix + `[_destroy]" id="` + idPrefix + `__destroy"><a class="table-action-link remove_fields dynamic" href="#"><i class="fa fa-times"></i>` + html.EscapeString(settingsT(locale, "filters.index.delete", "Delete")) + `</a></td></tr>`
	return out
}

func filterExpiresInSelect(filter models.CustomFilter, locale string) string {
	options := []int64{1800, 3600, 21600, 43200, 86400, 604800}
	current := int64(0)
	if filter.ExpiresAt.Valid {
		current = int64(time.Until(filter.ExpiresAt.Time).Round(time.Second).Seconds())
	}
	out := `<select class="select optional" id="custom_filter_expires_in" name="custom_filter[expires_in]"><option value="">` + html.EscapeString(settingsT(locale, "invites.expires_in_prompt", "Never")) + `</option>`
	for _, seconds := range options {
		selected := ""
		if current > 0 && current <= seconds {
			selected = ` selected`
			current = 0
		}
		out += `<option value="` + strconv.FormatInt(seconds, 10) + `"` + selected + `>` + html.EscapeString(settingsT(locale, "invites.expires_in."+strconv.FormatInt(seconds, 10), durationFallback(seconds))) + `</option>`
	}
	return out + `</select>`
}

func filterActionRadios(action int, locale string) string {
	actions := []struct {
		value   string
		checked bool
	}{
		{"warn", action == 0},
		{"hide", action == 1},
	}
	var out strings.Builder
	out.WriteString(`<input type="hidden" name="custom_filter[filter_action]" value="">`)
	for _, item := range actions {
		id := `custom_filter_filter_action_` + item.value
		out.WriteString(`<span class="radio"><label for="` + id + `"><input class="radio_buttons required" required="required" aria-required="true" type="radio" value="` + item.value + `" name="custom_filter[filter_action]" id="` + id + `"` + checkedAttr(item.checked) + `>`)
		out.WriteString(html.EscapeString(filterActionLabel(item.value, locale)))
		out.WriteString(`<span class="hint">` + html.EscapeString(settingsT(locale, "simple_form.hints.filters.actions."+item.value, "")) + `</span>`)
		out.WriteString(`</label></span>`)
	}
	return out.String()
}

func durationFallback(seconds int64) string {
	switch seconds {
	case 1800:
		return "30 minutes"
	case 3600:
		return "1 hour"
	case 21600:
		return "6 hours"
	case 43200:
		return "12 hours"
	case 86400:
		return "1 day"
	case 604800:
		return "1 week"
	default:
		return strconv.FormatInt(seconds, 10) + " seconds"
	}
}

func webFilterStatusesHTML(filter models.CustomFilter, notice string, errorText string, page string, localeAndTheme ...string) string {
	return webFilterStatusesHTMLWithConfig(config.Config{}, filter, notice, errorText, page, localeAndTheme...)
}

func webFilterStatusesHTMLWithConfig(cfg config.Config, filter models.CustomFilter, notice string, errorText string, page string, localeAndTheme ...string) string {
	loc := settingsLocaleArgOrEnglish(localeAndTheme...)
	var rows strings.Builder
	for _, item := range filter.Statuses {
		rows.WriteString(webFilterStatusRowHTML(cfg, item, loc))
	}
	if rows.Len() == 0 {
		rows.WriteString(`<div class="nothing-here nothing-here--under-tabs">` + html.EscapeString(settingsT(loc, "filters.statuses.empty", "No filtered statuses")) + `</div>`)
	}
	pageValue := strings.TrimSpace(page)
	if pageValue == "" {
		pageValue = "1"
	}
	removeButton := ""
	if len(filter.Statuses) > 0 {
		removeButton = `<button name="remove" value="1" class="table-action-link" type="submit"><i class="fa fa-times"></i> ` + html.EscapeString(settingsT(loc, "filters.statuses.batch.remove", "Remove")) + `</button>`
	}
	body := `<div class="filters"><div class="back-link"><a href="/filters/` + strconv.FormatInt(filter.ID, 10) + `/edit"><i class="fa fa-chevron-left fa-fw"></i> ` + html.EscapeString(settingsT(loc, "filters.statuses.back_to_filter", "Back to filter")) + `</a></div></div>
    <p class="hint">` + html.EscapeString(settingsT(loc, "filters.statuses.index.hint", "Select individual posts below to remove them from this filter.")) + `</p>
    <hr class="spacer">
    <form method="post" action="/filters/` + strconv.FormatInt(filter.ID, 10) + `/statuses/batch" class="new_status_filter_batch_action">
      <input type="hidden" name="page" value="` + html.EscapeString(pageValue) + `">
      <div class="batch-table">
        <div class="batch-table__toolbar">
          <label class="batch-table__toolbar__select batch-checkbox-all"><input type="checkbox" name="batch_checkbox_all"></label>
          <div class="batch-table__toolbar__actions">` + removeButton + `</div>
        </div>
        <div class="batch-table__body">` + rows.String() + `</div>
      </div>
    </form>` + webFilterStatusesPaginationHTML(filter.ID, page, len(filter.Statuses) == webFilterStatusesPageSize, loc)
	return authPageHTML(settingsT(loc, "filters.statuses.index.title", "Filtered statuses"), notice, errorText, body, localeAndTheme...)
}

func webFilterStatusRowHTML(cfg config.Config, item models.CustomFilterStatus, locale string) string {
	status := item.Status
	content := html.EscapeString(settingsT(locale, "admin.statuses.title", "Status") + " " + strconv.FormatInt(item.StatusID, 10))
	meta := ""
	if status != nil && status.ID != 0 {
		content = webFilterStatusContentHTML(cfg, *status, locale)
		meta = webFilterStatusMetaHTML(cfg, *status, locale)
	}
	return `<div class="batch-table__row"><label class="batch-table__row__select batch-checkbox"><input type="checkbox" name="form_status_filter_batch_action[status_filter_ids][]" value="` + strconv.FormatInt(item.ID, 10) + `"></label><div class="batch-table__row__content"><div class="status__content">` + content + `</div>` + meta + `</div></div>`
}

func webFilterStatusContentHTML(cfg config.Config, status models.Status, locale string) string {
	content := statusEmbedLinkedStatusTextHTMLWithConfig(cfg, status, status.CustomEmojis)
	if content == "" {
		content = `<span class="muted">` + html.EscapeString(settingsT(locale, "statuses.no_text", "No text")) + `</span>`
	}
	spoiler := strings.TrimSpace(status.SpoilerText)
	if spoiler == "" {
		return content
	}
	summary := statusEmbedPlainTextHTMLWithConfig(cfg, spoiler, status.CustomEmojis)
	if summary == "" {
		summary = html.EscapeString(settingsT(locale, "rss.content_warning", "Content warning"))
	}
	return `<details><summary><strong>` + html.EscapeString(settingsT(locale, "rss.content_warning", "Content warning")) + `: ` + summary + `</strong></summary>` + content + `</details>`
}

func webFilterStatusMetaHTML(cfg config.Config, status models.Status, locale string) string {
	accountURL := statusEmbedAccountURL(cfg.BaseURL(), status.Account)
	statusURL := adminTrendsStatusURL(cfg.BaseURL(), status)
	avatar := statusEmbedAccountAvatarURLWithConfig(cfg, status.Account)
	acct := status.Account.Acct()
	if acct == "" {
		acct = strconv.FormatInt(status.AccountID, 10)
	}
	stamp := status.CreatedAt.UTC().Format(time.RFC3339)
	created := status.CreatedAt.UTC().Format("2006-01-02 15:04 UTC")
	meta := `<div class="detailed-status__meta"><a href="` + html.EscapeString(accountURL) + `" class="name-tag" target="_blank" rel="noopener noreferrer"><img src="` + html.EscapeString(avatar) + `" width="15" height="15" alt="` + html.EscapeString(accountDisplayName(status.Account)) + `" class="avatar"><span class="username">` + html.EscapeString(acct) + `</span></a> · <a href="` + html.EscapeString(statusURL) + `" class="detailed-status__datetime" target="_blank" rel="noopener noreferrer"><time class="formatted" datetime="` + html.EscapeString(stamp) + `" title="` + html.EscapeString(created) + `">` + html.EscapeString(created) + `</time></a>`
	if status.EditedAt.Valid {
		editedStamp := status.EditedAt.Time.UTC().Format(time.RFC3339)
		edited := status.EditedAt.Time.UTC().Format("2006-01-02 15:04 UTC")
		meta += ` · ` + settingsTVars(locale, "statuses.edited_at_html", "Edited %{date}", map[string]string{"date": `<time class="formatted" datetime="` + html.EscapeString(editedStamp) + `" title="` + html.EscapeString(edited) + `">` + html.EscapeString(edited) + `</time>`})
	}
	meta += ` · <span title="` + html.EscapeString(settingsT(locale, "admin.statuses.visibility", "Visibility")) + `">` + html.EscapeString(statusEmbedVisibilityLabel(status.Visibility, locale)) + `</span>`
	if status.Sensitive {
		meta += ` · <i class="fa fa-eye-slash fa-fw"></i> ` + html.EscapeString(settingsT(locale, "stream_entries.sensitive_content", "Sensitive content"))
	}
	meta += webFilterStatusMediaHTML(status)
	return meta + `</div>`
}

func webFilterStatusMediaHTML(status models.Status) string {
	if len(status.MediaAttachments) == 0 {
		return ""
	}
	var out strings.Builder
	for _, media := range status.MediaAttachments {
		filename := strings.TrimSpace(media.FileFileName.String)
		if filename == "" {
			continue
		}
		title := filename
		if media.Description.Valid && strings.TrimSpace(media.Description.String) != "" {
			title = strings.TrimSpace(media.Description.String)
		}
		out.WriteString(` · <abbr title="` + html.EscapeString(title) + `"><i class="fa fa-link"></i> ` + html.EscapeString(filename) + `</abbr>`)
	}
	return out.String()
}

func webFilterStatusesPaginationHTML(filterID int64, page string, hasNext bool, locale ...string) string {
	loc := settingsLocaleArgOrEnglish(locale...)
	pageNum, err := strconv.Atoi(strings.TrimSpace(page))
	if err != nil || pageNum < 1 {
		pageNum = 1
	}
	var links []string
	if pageNum > 1 {
		params := url.Values{"page": []string{strconv.Itoa(pageNum - 1)}}
		links = append(links, `<a href="/filters/`+strconv.FormatInt(filterID, 10)+`/statuses?`+html.EscapeString(params.Encode())+`">`+html.EscapeString(settingsT(loc, "pagination.previous", "Previous"))+`</a>`)
	}
	if hasNext {
		params := url.Values{"page": []string{strconv.Itoa(pageNum + 1)}}
		links = append(links, `<a href="/filters/`+strconv.FormatInt(filterID, 10)+`/statuses?`+html.EscapeString(params.Encode())+`">`+html.EscapeString(settingsT(loc, "pagination.next", "Next"))+`</a>`)
	}
	if len(links) == 0 {
		return ""
	}
	return `<nav class="pagination">` + strings.Join(links, " ") + `</nav>`
}

func filterActionLabel(action string, locale string) string {
	switch action {
	case "hide":
		return settingsT(locale, "simple_form.labels.filters.actions.hide", "Hide")
	default:
		return settingsT(locale, "simple_form.labels.filters.actions.warn", "Warn")
	}
}

func checkedAttr(value bool) string {
	if value {
		return ` checked`
	}
	return ""
}

func formInt64Values(c *echo.Context, key string) []int64 {
	_ = c.Request().ParseForm()
	values := c.Request().PostForm[key]
	out := make([]int64, 0, len(values))
	for _, value := range values {
		id, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err == nil && id > 0 {
			out = append(out, id)
		}
	}
	return out
}
