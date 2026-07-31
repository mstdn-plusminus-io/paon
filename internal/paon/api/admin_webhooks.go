package api

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
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

var adminWebhookEvents = []string{
	"account.approved",
	"account.created",
	"account.updated",
	"report.created",
	"report.updated",
	"status.created",
	"status.updated",
}

var errAdminWebhookParamsMissing = errors.New("admin webhook root parameter is missing")

type adminWebhookForm struct {
	URL      string
	Events   []string
	Template string
}

type adminWebhookRetryRow struct {
	Attempts  int
	CreatedAt time.Time
	Event     string
	Body      string
}

type adminWebhookDeliveryHistoryRow struct {
	DeliveredAt time.Time
	Status      string
	Event       string
	HTTPStatus  int
	Error       string
	Body        string
}

func (s *Server) adminWebhooksPage(c *echo.Context) error {
	user, handled, err := s.requireAdminWebhooksWebUser(c)
	if handled || err != nil {
		return err
	}
	webhooks, err := s.adminWebhookModels(c)
	if err != nil {
		return err
	}
	return c.HTML(http.StatusOK, adminWebhooksIndexHTML(webhooks, c.QueryParam("notice"), c.QueryParam("error"), adminTrendsPageValue(c), s.webLocale(c, user)))
}

func (s *Server) newAdminWebhookPage(c *echo.Context) error {
	user, handled, err := s.requireAdminWebhooksWebUser(c)
	if handled || err != nil {
		return err
	}
	locale := s.webLocale(c, user)
	return c.HTML(http.StatusOK, adminWebhookFormHTML(adminT(locale, "admin.webhooks.new", "New webhook"), "/admin/webhooks", "", adminWebhookForm{}, s.adminWebhookEventsForUser(user), adminT(locale, "admin.webhooks.add_new", "Add endpoint"), c.QueryParam("error"), locale))
}

func (s *Server) showAdminWebhookPage(c *echo.Context) error {
	user, handled, err := s.requireAdminWebhooksWebUser(c)
	if handled || err != nil {
		return err
	}
	webhook, err := s.findAdminWebhook(c.Param("id"))
	if err != nil {
		return err
	}
	return c.HTML(http.StatusOK, adminWebhookShowHTML(webhook, nil, nil, c.QueryParam("notice"), c.QueryParam("error"), s.webLocale(c, user)))
}

func (s *Server) editAdminWebhookPage(c *echo.Context) error {
	user, handled, err := s.requireAdminWebhooksWebUser(c)
	if handled || err != nil {
		return err
	}
	webhook, err := s.findAdminWebhook(c.Param("id"))
	if err != nil {
		return err
	}
	form := adminWebhookForm{URL: webhook.URL, Events: []string(webhook.Events), Template: webhook.Template.String}
	locale := s.webLocale(c, user)
	return c.HTML(http.StatusOK, adminWebhookFormHTML(adminT(locale, "admin.webhooks.edit", "Edit webhook"), "/admin/webhooks/"+strconv.FormatInt(webhook.ID, 10), "patch", form, s.adminWebhookEventsForUser(user), adminT(locale, "generic.save_changes", "Save changes"), c.QueryParam("error"), locale))
}

func (s *Server) createAdminWebhook(c *echo.Context) error {
	user, handled, err := s.requireAdminWebhooksWebUser(c)
	if handled || err != nil {
		return err
	}
	locale := s.webLocale(c, user)
	form, err := parseAdminWebhookForm(c)
	if err != nil {
		if errors.Is(err, errAdminWebhookParamsMissing) {
			return apiError(c, http.StatusBadRequest, "Malformed request")
		}
		return c.HTML(http.StatusOK, adminWebhookFormHTML(adminT(locale, "admin.webhooks.new", "New webhook"), "/admin/webhooks", "", form, s.adminWebhookEventsForUser(user), adminT(locale, "admin.webhooks.add_new", "Add endpoint"), adminWebhookMessage(locale, "errors.invalid", "Webhook is invalid"), locale))
	}
	if err := s.validateAdminWebhookForm(user, form); err != nil {
		return c.HTML(http.StatusOK, adminWebhookFormHTML(adminT(locale, "admin.webhooks.new", "New webhook"), "/admin/webhooks", "", form, s.adminWebhookEventsForUser(user), adminT(locale, "admin.webhooks.add_new", "Add endpoint"), adminWebhookErrorText(locale, err), locale))
	}
	if s.db == nil {
		return c.HTML(http.StatusOK, adminWebhookFormHTML(adminT(locale, "admin.webhooks.new", "New webhook"), "/admin/webhooks", "", form, s.adminWebhookEventsForUser(user), adminT(locale, "admin.webhooks.add_new", "Add endpoint"), adminWebhookMessage(locale, "errors.database_unavailable", "DATABASE_URL is not set"), locale))
	}
	secret, err := newAdminWebhookSecret()
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	webhook := models.Webhook{
		URL:       form.URL,
		Events:    models.StringArray(form.Events),
		Secret:    secret,
		Enabled:   true,
		CreatedAt: now,
		UpdatedAt: now,
		Template:  sql.NullString{String: form.Template, Valid: true},
	}
	if err := s.db.Create(&webhook).Error; err != nil {
		return err
	}
	return c.Redirect(http.StatusFound, "/admin/webhooks/"+strconv.FormatInt(webhook.ID, 10))
}

func (s *Server) updateAdminWebhook(c *echo.Context) error {
	user, handled, err := s.requireAdminWebhooksWebUser(c)
	if handled || err != nil {
		return err
	}
	if c.Request().Method == http.MethodPost && !methodOverrideIs(c, "put", "patch") {
		return c.Redirect(http.StatusFound, "/admin/webhooks")
	}
	webhook, err := s.findAdminWebhook(c.Param("id"))
	if err != nil {
		return err
	}
	locale := s.webLocale(c, user)
	if err := s.requireWebhookEventPermissions(user, []string(webhook.Events)); err != nil {
		return c.Redirect(http.StatusFound, "/admin/webhooks/"+strconv.FormatInt(webhook.ID, 10)+"?error="+url.QueryEscape(adminWebhookErrorText(locale, err)))
	}
	form, err := parseAdminWebhookForm(c)
	if err != nil {
		if errors.Is(err, errAdminWebhookParamsMissing) {
			return apiError(c, http.StatusBadRequest, "Malformed request")
		}
		return c.HTML(http.StatusOK, adminWebhookFormHTML(adminT(locale, "admin.webhooks.edit", "Edit webhook"), "/admin/webhooks/"+strconv.FormatInt(webhook.ID, 10), "patch", adminWebhookForm{URL: webhook.URL, Events: []string(webhook.Events), Template: webhook.Template.String}, s.adminWebhookEventsForUser(user), adminT(locale, "generic.save_changes", "Save changes"), adminWebhookMessage(locale, "errors.invalid", "Webhook is invalid"), locale))
	}
	if err := s.validateAdminWebhookForm(user, form); err != nil {
		return c.HTML(http.StatusOK, adminWebhookFormHTML(adminT(locale, "admin.webhooks.edit", "Edit webhook"), "/admin/webhooks/"+strconv.FormatInt(webhook.ID, 10), "patch", form, s.adminWebhookEventsForUser(user), adminT(locale, "generic.save_changes", "Save changes"), adminWebhookErrorText(locale, err), locale))
	}
	if err := s.db.Model(&models.Webhook{}).Where("id = ?", webhook.ID).Updates(map[string]any{
		"url":        form.URL,
		"events":     models.StringArray(form.Events),
		"template":   sql.NullString{String: form.Template, Valid: true},
		"updated_at": time.Now().UTC(),
	}).Error; err != nil {
		return err
	}
	return c.Redirect(http.StatusFound, "/admin/webhooks/"+strconv.FormatInt(webhook.ID, 10))
}

func (s *Server) enableAdminWebhook(c *echo.Context) error {
	return s.setAdminWebhookEnabled(c, true)
}

func (s *Server) disableAdminWebhook(c *echo.Context) error {
	return s.setAdminWebhookEnabled(c, false)
}

func (s *Server) rotateAdminWebhookSecret(c *echo.Context) error {
	user, handled, err := s.requireAdminWebhooksWebUser(c)
	if handled || err != nil {
		return err
	}
	webhook, err := s.findAdminWebhook(c.Param("webhook_id"))
	if err != nil {
		return err
	}
	secret, err := newAdminWebhookSecret()
	if err != nil {
		return err
	}
	if err := s.db.Model(&models.Webhook{}).Where("id = ?", webhook.ID).Updates(map[string]any{
		"secret":     secret,
		"updated_at": time.Now().UTC(),
	}).Error; err != nil {
		return err
	}
	locale := s.webLocale(c, user)
	return c.Redirect(http.StatusFound, "/admin/webhooks/"+strconv.FormatInt(webhook.ID, 10)+"?notice="+url.QueryEscape(adminWebhookMessage(locale, "secret_rotated_msg", "Webhook secret rotated")))
}

func (s *Server) destroyAdminWebhook(c *echo.Context) error {
	user, handled, err := s.requireAdminWebhooksWebUser(c)
	if handled || err != nil {
		return err
	}
	webhook, err := s.findAdminWebhook(c.Param("id"))
	if err != nil {
		return err
	}
	locale := s.webLocale(c, user)
	if err := s.requireWebhookEventPermissions(user, []string(webhook.Events)); err != nil {
		return c.Redirect(http.StatusFound, "/admin/webhooks/"+strconv.FormatInt(webhook.ID, 10)+"?error="+url.QueryEscape(adminWebhookErrorText(locale, err)))
	}
	if err := s.db.Delete(&models.Webhook{}, webhook.ID).Error; err != nil {
		return err
	}
	return c.Redirect(http.StatusFound, "/admin/webhooks")
}

func (s *Server) adminWebhookMemberAction(c *echo.Context) error {
	if methodOverrideIs(c, "delete") {
		return s.destroyAdminWebhook(c)
	}
	if methodOverrideIs(c, "put", "patch") {
		return s.updateAdminWebhook(c)
	}
	return c.Redirect(http.StatusFound, "/admin/webhooks")
}

func (s *Server) setAdminWebhookEnabled(c *echo.Context, enabled bool) error {
	_, handled, err := s.requireAdminWebhooksWebUser(c)
	if handled || err != nil {
		return err
	}
	webhook, err := s.findAdminWebhook(c.Param("id"))
	if err != nil {
		return err
	}
	if err := s.db.Model(&models.Webhook{}).Where("id = ?", webhook.ID).Updates(map[string]any{
		"enabled":    enabled,
		"updated_at": time.Now().UTC(),
	}).Error; err != nil {
		return err
	}
	return c.Redirect(http.StatusFound, "/admin/webhooks/"+strconv.FormatInt(webhook.ID, 10))
}

func adminWebhookMessage(locale string, key string, fallback string) string {
	return adminT(locale, "admin.webhooks."+key, fallback)
}

func adminWebhookErrorText(locale string, err error) string {
	if err == nil {
		return ""
	}
	text := err.Error()
	keys := map[string]string{
		"Webhook URL can't be blank":            "url_blank",
		"Webhook URL is invalid":                "url_invalid",
		"Webhook events can't be blank":         "events_blank",
		"Webhook events are invalid":            "events_invalid",
		"Webhook event permissions are invalid": "event_permissions_invalid",
		"Webhook template is invalid":           "template_invalid",
	}
	if key := keys[text]; key != "" {
		return adminWebhookMessage(locale, "errors."+key, text)
	}
	return text
}

func (s *Server) requireAdminWebhooksWebUser(c *echo.Context) (*models.User, bool, error) {
	user, _, handled, err := s.requireFunctionalWebUser(c)
	if handled || err != nil {
		return nil, handled, err
	}
	if !s.userCan(user, rolePermissionManageWebhooks) {
		locale := s.webLocale(c, user)
		return nil, true, c.HTML(http.StatusForbidden, authPageHTML(adminT(locale, "admin.webhooks.title", "Admin webhooks"), "", adminT(locale, "admin.webhooks.not_permitted", "You are not allowed to manage webhooks."), "", locale))
	}
	return user, false, nil
}

func (s *Server) adminWebhookModels(c *echo.Context) ([]models.Webhook, error) {
	if s.db == nil {
		return []models.Webhook{}, nil
	}
	var webhooks []models.Webhook
	err := s.db.Order("id DESC").
		Offset(adminRailsPageOffset(c)).
		Limit(adminRailsDefaultPageSize).
		Find(&webhooks).Error
	return webhooks, err
}

func (s *Server) findAdminWebhook(rawID string) (models.Webhook, error) {
	id, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil {
		return models.Webhook{}, echo.NewHTTPError(http.StatusNotFound, "webhook not found")
	}
	var webhook models.Webhook
	if s.db == nil {
		return webhook, echo.NewHTTPError(http.StatusNotFound, "webhook not found")
	}
	if err := s.db.Where("id = ?", id).First(&webhook).Error; err != nil {
		return webhook, echo.NewHTTPError(http.StatusNotFound, "webhook not found")
	}
	return webhook, nil
}

func (s *Server) adminWebhookPendingRetryRows(ctx context.Context, webhookID int64, limit int) []adminWebhookRetryRow {
	if s == nil || webhookID == 0 || limit <= 0 {
		return nil
	}
	value, err := s.redisCommand(ctx, "ZRANGE", redisConfig(s.cfg).prefix+webhookDeliveryRetryKey, "0", "-1")
	if err != nil {
		return nil
	}
	members, ok := redisStringArray(value)
	if !ok {
		return nil
	}
	return adminWebhookRetryRowsFromMembers(webhookID, members, limit)
}

func (s *Server) adminWebhookDeliveryHistoryRows(ctx context.Context, webhookID int64, limit int) []adminWebhookDeliveryHistoryRow {
	if s == nil || webhookID == 0 || limit <= 0 {
		return nil
	}
	value, err := s.redisCommand(ctx, "LRANGE", s.webhookDeliveryHistoryRedisKey(webhookID), "0", strconv.Itoa(limit-1))
	if err != nil {
		return nil
	}
	members, ok := redisStringArray(value)
	if !ok {
		return nil
	}
	return adminWebhookDeliveryHistoryRowsFromMembers(members, limit)
}

func adminWebhookDeliveryHistoryRowsFromMembers(members []string, limit int) []adminWebhookDeliveryHistoryRow {
	if limit <= 0 {
		return nil
	}
	rows := make([]adminWebhookDeliveryHistoryRow, 0, min(limit, len(members)))
	for _, member := range members {
		var item webhookDeliveryHistoryItem
		if err := json.Unmarshal([]byte(member), &item); err != nil {
			continue
		}
		rows = append(rows, adminWebhookDeliveryHistoryRow{
			DeliveredAt: time.Unix(item.DeliveredAt, 0).UTC(),
			Status:      item.Status,
			Event:       item.Event,
			HTTPStatus:  item.HTTPStatus,
			Error:       item.Error,
			Body:        webhookRetryBodyPreview(item.Body, 240),
		})
		if len(rows) >= limit {
			break
		}
	}
	return rows
}

func adminWebhookRetryRowsFromMembers(webhookID int64, members []string, limit int) []adminWebhookRetryRow {
	if limit <= 0 {
		return nil
	}
	rows := make([]adminWebhookRetryRow, 0, min(limit, len(members)))
	for _, member := range members {
		var job webhookDeliveryRetryJob
		if err := json.Unmarshal([]byte(member), &job); err != nil || job.WebhookID != webhookID {
			continue
		}
		rows = append(rows, adminWebhookRetryRow{
			Attempts:  job.Attempts,
			CreatedAt: time.Unix(job.CreatedAt, 0).UTC(),
			Event:     webhookRetryEvent(job.Body),
			Body:      webhookRetryBodyPreview(job.Body, 240),
		})
		if len(rows) >= limit {
			break
		}
	}
	return rows
}

func webhookRetryEvent(body []byte) string {
	var payload struct {
		Event string `json:"event"`
	}
	_ = json.Unmarshal(body, &payload)
	return payload.Event
}

func webhookRetryBodyPreview(body []byte, limit int) string {
	text := strings.TrimSpace(string(body))
	if limit > 0 && len(text) > limit {
		return text[:limit] + "..."
	}
	return text
}

func parseAdminWebhookForm(c *echo.Context) (adminWebhookForm, error) {
	req := c.Request()
	if err := req.ParseForm(); err != nil {
		return adminWebhookForm{}, err
	}
	prefix := "webhook"
	if !formHasNestedPrefix(req.Form, prefix) {
		return adminWebhookForm{}, errAdminWebhookParamsMissing
	}
	return adminWebhookForm{
		URL:      lastFormValue(req.Form, "webhook[url]"),
		Events:   normalizeAdminWebhookEvents(req.Form["webhook[events][]"]),
		Template: lastFormValue(req.Form, "webhook[template]"),
	}, nil
}

func normalizeAdminWebhookEvents(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
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
	return out
}

func (s *Server) validateAdminWebhookForm(user *models.User, form adminWebhookForm) error {
	if strings.TrimSpace(form.URL) == "" {
		return errAdminSetting("Webhook URL can't be blank")
	}
	parsed, err := url.Parse(form.URL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return errAdminSetting("Webhook URL is invalid")
	}
	if len(form.Events) == 0 {
		return errAdminSetting("Webhook events can't be blank")
	}
	for _, event := range form.Events {
		if !adminWebhookEventAllowed(event) {
			return errAdminSetting("Webhook events are invalid")
		}
	}
	if !webhookTemplateValid(form.Template) {
		return errAdminSetting("Webhook template is invalid")
	}
	return s.requireWebhookEventPermissions(user, form.Events)
}

func (s *Server) requireWebhookEventPermissions(user *models.User, events []string) error {
	for _, event := range events {
		permission := adminWebhookEventPermission(event)
		if permission != 0 && !s.userCan(user, permission) {
			return errAdminSetting("Webhook event permissions are invalid")
		}
	}
	return nil
}

func (s *Server) adminWebhookEventsForUser(user *models.User) []string {
	out := make([]string, 0, len(adminWebhookEvents))
	for _, event := range adminWebhookEvents {
		permission := adminWebhookEventPermission(event)
		if permission == 0 || s.userCan(user, permission) {
			out = append(out, event)
		}
	}
	return out
}

func adminWebhookEventAllowed(event string) bool {
	return adminWebhookEventPermission(event) != 0
}

func adminWebhookEventPermission(event string) int64 {
	switch event {
	case "account.approved", "account.created", "account.updated":
		return rolePermissionManageUsers
	case "report.created", "report.updated":
		return rolePermissionManageReports
	case "status.created", "status.updated":
		return rolePermissionViewDevops
	default:
		return 0
	}
}

func newAdminWebhookSecret() (string, error) {
	buf := make([]byte, 20)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func adminWebhooksIndexHTML(webhooks []models.Webhook, notice string, errorText string, page string, locale ...string) string {
	loc := settingsLocaleArgOrEnglish(locale...)
	var rows strings.Builder
	if len(webhooks) == 0 {
		rows.WriteString(`<div class="muted-hint center-text">` + html.EscapeString(adminT(loc, "admin.webhooks.empty", "No webhooks have been configured.")) + `</div>`)
	} else {
		rows.WriteString(`<div class="applications-list">`)
		for _, webhook := range webhooks {
			id := strconv.FormatInt(webhook.ID, 10)
			status := adminT(loc, "admin.webhooks.disabled", "Disabled")
			if webhook.Enabled {
				status = adminT(loc, "admin.webhooks.enabled", "Active")
			}
			statusClass := "negative-hint"
			if webhook.Enabled {
				statusClass = "positive-hint"
			}
			enabledEvents := adminTVars(loc, "admin.webhooks.enabled_events.other", "%{count} enabled events", map[string]string{"count": strconv.Itoa(len(webhook.Events))})
			rows.WriteString(`<div class="applications-list__item"><a class="announcements-list__item__title" href="/admin/webhooks/` + id + `"><i class="fa fa-inbox fa-fw"></i> ` + html.EscapeString(webhook.URL) + `</a><div class="announcements-list__item__action-bar"><div class="announcements-list__item__meta"><span class="` + statusClass + `">` + html.EscapeString(status) + `</span> &middot; <abbr title="` + html.EscapeString(strings.Join(webhook.Events, ", ")) + `">` + html.EscapeString(enabledEvents) + `</abbr></div><div><a class="table-action-link" href="/admin/webhooks/` + id + `/edit"><i class="fa fa-pencil fa-fw"></i> ` + html.EscapeString(adminT(loc, "admin.webhooks.edit", "Edit webhook")) + `</a> <a class="table-action-link" data-method="delete" data-confirm="` + html.EscapeString(adminT(loc, "admin.accounts.are_you_sure", "Are you sure?")) + `" href="/admin/webhooks/` + id + `"><i class="fa fa-trash fa-fw"></i> ` + html.EscapeString(adminT(loc, "admin.webhooks.delete", "Delete")) + `</a></div></div></div>`)
		}
		rows.WriteString(`</div>`)
		rows.WriteString(adminWebhooksPaginationHTML(page, len(webhooks) == adminRailsDefaultPageSize, loc))
	}
	body := `<div class="content__heading__actions"><a class="button" href="/admin/webhooks/new">` + html.EscapeString(adminT(loc, "admin.webhooks.add_new", "Add webhook")) + `</a></div><p>` + adminT(loc, "admin.webhooks.description_html", "Webhooks allow external applications to receive server events.") + `</p><hr class="spacer">` + rows.String()
	return authPageHTML(adminT(loc, "admin.webhooks.title", "Admin webhooks"), notice, errorText, body, loc)
}

func adminWebhooksPaginationHTML(page string, hasNext bool, locale ...string) string {
	loc := settingsLocaleArgOrEnglish(locale...)
	pageNum, err := strconv.Atoi(strings.TrimSpace(page))
	if err != nil || pageNum < 1 {
		pageNum = 1
	}
	var links []string
	if pageNum > 1 {
		params := url.Values{"page": []string{strconv.Itoa(pageNum - 1)}}
		links = append(links, `<a href="/admin/webhooks?`+html.EscapeString(params.Encode())+`">`+html.EscapeString(settingsT(loc, "pagination.prev", "Previous"))+`</a>`)
	}
	if hasNext {
		params := url.Values{"page": []string{strconv.Itoa(pageNum + 1)}}
		links = append(links, `<a href="/admin/webhooks?`+html.EscapeString(params.Encode())+`">`+html.EscapeString(settingsT(loc, "pagination.next", "Next"))+`</a>`)
	}
	if len(links) == 0 {
		return ""
	}
	return `<nav class="pagination">` + strings.Join(links, " ") + `</nav>`
}

func adminWebhookShowHTML(webhook models.Webhook, retries []adminWebhookRetryRow, history []adminWebhookDeliveryHistoryRow, notice string, errorText string, locale ...string) string {
	loc := settingsLocaleArgOrEnglish(locale...)
	id := strconv.FormatInt(webhook.ID, 10)
	_ = retries
	_ = history
	statusClass := "negative-hint"
	status := adminT(loc, "admin.webhooks.disabled", "Disabled")
	action := adminAccountTableLink("power-off", adminT(loc, "admin.webhooks.enable", "Enable"), "/admin/webhooks/"+id+"/enable", "post")
	if webhook.Enabled {
		statusClass = "positive-hint"
		status = adminT(loc, "admin.webhooks.enabled", "Active")
		action = adminAccountTableLink("power-off", adminT(loc, "admin.webhooks.disable", "Disable"), "/admin/webhooks/"+id+"/disable", "post")
	}
	events := html.EscapeString(adminTVars(loc, "admin.webhooks.enabled_events.other", "%{count} enabled events", map[string]string{"count": strconv.Itoa(len(webhook.Events))}))
	if len(webhook.Events) == 1 {
		events = html.EscapeString(adminTVars(loc, "admin.webhooks.enabled_events.one", "%{count} enabled event", map[string]string{"count": "1"}))
	}
	body := `<div class="content__heading__actions"><a class="button" href="/admin/webhooks/` + id + `/edit">` + html.EscapeString(adminT(loc, "admin.webhooks.edit", "Edit webhook")) + `</a></div><div class="table-wrapper"><table class="table horizontal-table"><tbody>
  <tr><th>` + html.EscapeString(adminT(loc, "admin.webhooks.status", "Status")) + `</th><td><span class="` + statusClass + `">` + html.EscapeString(status) + `</span> ` + action + `</td></tr>
  <tr><th>` + html.EscapeString(adminT(loc, "admin.webhooks.events", "Events")) + `</th><td><abbr title="` + html.EscapeString(strings.Join(webhook.Events, ", ")) + `">` + events + `</abbr></td></tr>
  <tr><th>` + html.EscapeString(adminT(loc, "admin.webhooks.secret", "Secret")) + `</th><td><samp>` + html.EscapeString(webhook.Secret) + `</samp> ` + adminAccountTableLink("refresh", adminT(loc, "admin.webhooks.rotate_secret", "Rotate secret"), "/admin/webhooks/"+id+"/secret/rotate", "post") + `</td></tr>
</tbody></table></div>`
	return authPageHTML(webhook.URL, notice, errorText, body, loc)
}

func adminWebhookFormHTML(title string, action string, methodOverride string, form adminWebhookForm, allowedEvents []string, submitLabel string, errorText string, locale ...string) string {
	loc := settingsLocaleArgOrEnglish(locale...)
	if strings.TrimSpace(submitLabel) == "" {
		submitLabel = adminT(loc, "generic.save_changes", "Save changes")
	}
	selected := map[string]struct{}{}
	for _, event := range form.Events {
		selected[event] = struct{}{}
	}
	var eventInputs strings.Builder
	eventInputs.WriteString(`<input type="hidden" name="webhook[events][]" value="">`)
	for _, event := range allowedEvents {
		checked := ""
		if _, ok := selected[event]; ok {
			checked = ` checked`
		}
		id := "webhook_events_" + strings.NewReplacer(".", "_", "-", "_").Replace(event)
		eventInputs.WriteString(`<li class="checkbox"><label for="` + id + `"><input class="check_boxes required" type="checkbox" name="webhook[events][]" id="` + id + `" value="` + html.EscapeString(event) + `"` + checked + `>` + html.EscapeString(event) + `</label></li>`)
	}
	body := simpleFormOpen(action, methodOverride) +
		`<div class="fields-group"><div class="input with_block_label url required webhook_url field_with_hint"><label class="url required" for="webhook_url">` + html.EscapeString(adminT(loc, "simple_form.labels.webhook.url", "URL")) + filterRequiredMarker(loc) + `</label><span class="hint">` + html.EscapeString(adminT(loc, "simple_form.hints.webhook.url", "Where events will be sent")) + `</span><div class="label_input"><input class="string url required" id="webhook_url" name="webhook[url]" value="` + html.EscapeString(form.URL) + `" type="url" placeholder="https://" required="required" aria-required="true"></div></div></div>` +
		`<div class="fields-group"><div class="input with_block_label check_boxes required webhook_events field_with_hint"><label class="check_boxes required">` + html.EscapeString(adminT(loc, "simple_form.labels.webhook.events", "Events")) + filterRequiredMarker(loc) + `</label><span class="hint">` + html.EscapeString(adminT(loc, "simple_form.hints.webhook.events", "Select events to send")) + `</span><div class="label_input"><ul>` + eventInputs.String() + `</ul></div></div></div>` +
		`<div class="fields-group"><div class="input with_block_label text optional webhook_template field_with_hint"><label class="text optional" for="webhook_template">` + html.EscapeString(adminT(loc, "simple_form.labels.webhook.template", "Template")) + `</label><span class="hint">` + html.EscapeString(adminT(loc, "simple_form.hints.webhook.template", "Use variable interpolation to compose a custom JSON payload, or leave blank to use the default JSON.")) + `</span><div class="label_input"><textarea class="text optional" id="webhook_template" name="webhook[template]" placeholder="{ &quot;content&quot;: &quot;Hello {{object.username}}&quot; }">` + html.EscapeString(form.Template) + `</textarea></div></div></div>` +
		simpleSubmit(submitLabel) +
		simpleFormClose()
	return authPageHTML(title, "", errorText, body, loc)
}
