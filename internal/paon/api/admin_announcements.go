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
	"gorm.io/gorm"
)

type adminAnnouncementForm struct {
	Text        string
	AllDay      bool
	ScheduledAt sql.NullTime
	StartsAt    sql.NullTime
	EndsAt      sql.NullTime
}

func (s *Server) adminAnnouncementsPage(c *echo.Context) error {
	user, handled, err := s.requireAdminAnnouncementsWebUser(c)
	if handled || err != nil {
		return err
	}
	announcements, err := s.adminAnnouncementModels(c)
	if err != nil {
		return err
	}
	filters := adminAnnouncementFilters{
		Page:        adminTrendsPageValue(c),
		Published:   c.QueryParam("published"),
		Unpublished: c.QueryParam("unpublished"),
	}
	return c.HTML(http.StatusOK, adminAnnouncementsIndexHTML(announcements, c.QueryParam("notice"), c.QueryParam("error"), filters, s.webLocale(c, user)))
}

func (s *Server) newAdminAnnouncementPage(c *echo.Context) error {
	user, handled, err := s.requireAdminAnnouncementsWebUser(c)
	if handled || err != nil {
		return err
	}
	locale := s.webLocale(c, user)
	return c.HTML(http.StatusOK, adminAnnouncementFormHTML(adminT(locale, "admin.announcements.new.title", "New announcement"), "/admin/announcements", "", adminAnnouncementForm{}, false, adminT(locale, "admin.announcements.new.create", "Create announcement"), c.QueryParam("error"), locale))
}

func (s *Server) createAdminAnnouncement(c *echo.Context) error {
	user, handled, err := s.requireAdminAnnouncementsWebUser(c)
	if handled || err != nil {
		return err
	}
	locale := s.webLocale(c, user)
	form, err := parseAdminAnnouncementForm(c)
	if errors.Is(err, errAdminAnnouncementParamsMissing) {
		return apiError(c, http.StatusBadRequest, "Malformed request")
	}
	if err != nil {
		return c.HTML(http.StatusOK, adminAnnouncementNewHTML(adminAnnouncementForm{}, adminAnnouncementMessage(locale, "errors.invalid", "Announcement is invalid"), locale))
	}
	if err := validateAdminAnnouncementForm(form); err != nil {
		return c.HTML(http.StatusOK, adminAnnouncementNewHTML(form, adminAnnouncementErrorText(locale, err), locale))
	}
	if s.db == nil {
		return c.HTML(http.StatusOK, adminAnnouncementNewHTML(form, adminAnnouncementMessage(locale, "errors.database_unavailable", "DATABASE_URL is not set"), locale))
	}
	announcement := adminAnnouncementFromForm(form, time.Now().UTC())
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&announcement).Error; err != nil {
			return err
		}
		return logAdminAction(tx, user.AccountID, "create", announcementAuditLogTarget(announcement), announcement.UpdatedAt)
	}); err != nil {
		return err
	}
	notice := adminT(locale, "admin.announcements.scheduled_msg", "Announcement scheduled for publication!")
	if announcement.Published {
		notice = adminT(locale, "admin.announcements.published_msg", "Announcement successfully published!")
		if !s.enqueuePublishAnnouncementTask(announcement.ID, time.Time{}) {
			s.broadcastAnnouncement(announcement)
		}
	}
	return c.Redirect(http.StatusFound, "/admin/announcements?notice="+url.QueryEscape(notice))
}

func (s *Server) editAdminAnnouncementPage(c *echo.Context) error {
	user, handled, err := s.requireAdminAnnouncementsWebUser(c)
	if handled || err != nil {
		return err
	}
	announcement, err := s.findAdminAnnouncement(c.Param("id"))
	if err != nil {
		return err
	}
	return c.HTML(http.StatusOK, adminAnnouncementEditHTML(announcement, c.QueryParam("notice"), c.QueryParam("error"), s.webLocale(c, user)))
}

func (s *Server) updateAdminAnnouncement(c *echo.Context) error {
	user, handled, err := s.requireAdminAnnouncementsWebUser(c)
	if handled || err != nil {
		return err
	}
	locale := s.webLocale(c, user)
	if c.Request().Method == http.MethodPost && !methodOverrideIs(c, "put", "patch") {
		return c.Redirect(http.StatusFound, "/admin/announcements")
	}
	announcement, err := s.findAdminAnnouncement(c.Param("id"))
	if err != nil {
		return err
	}
	form, err := parseAdminAnnouncementForm(c)
	if errors.Is(err, errAdminAnnouncementParamsMissing) {
		return apiError(c, http.StatusBadRequest, "Malformed request")
	}
	if err != nil {
		return c.HTML(http.StatusOK, adminAnnouncementEditFormHTML(announcement, adminAnnouncementFormFromModel(announcement), adminAnnouncementMessage(locale, "errors.invalid", "Announcement is invalid"), locale))
	}
	if err := validateAdminAnnouncementForm(form); err != nil {
		return c.HTML(http.StatusOK, adminAnnouncementEditFormHTML(announcement, form, adminAnnouncementErrorText(locale, err), locale))
	}
	if s.db == nil {
		return c.HTML(http.StatusOK, adminAnnouncementEditFormHTML(announcement, form, adminAnnouncementMessage(locale, "errors.database_unavailable", "DATABASE_URL is not set"), locale))
	}
	if err := s.updateAdminAnnouncementModel(user.AccountID, announcement, form); err != nil {
		return err
	}
	if updated, err := s.findAdminAnnouncement(c.Param("id")); err == nil && updated.Published {
		if !s.enqueuePublishAnnouncementTask(updated.ID, time.Time{}) {
			s.broadcastAnnouncement(updated)
		}
	}
	return c.Redirect(http.StatusFound, "/admin/announcements?notice="+url.QueryEscape(adminT(locale, "admin.announcements.updated_msg", "Announcement successfully updated!")))
}

func (s *Server) publishAdminAnnouncement(c *echo.Context) error {
	user, handled, err := s.requireAdminAnnouncementsWebUser(c)
	if handled || err != nil {
		return err
	}
	announcement, err := s.findAdminAnnouncement(c.Param("id"))
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	announcement.Published = true
	announcement.PublishedAt = sql.NullTime{Time: now, Valid: true}
	announcement.ScheduledAt = sql.NullTime{}
	announcement.UpdatedAt = now
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.Announcement{}).
			Where("id = ?", announcement.ID).
			Updates(map[string]any{
				"published":    true,
				"published_at": announcement.PublishedAt,
				"scheduled_at": announcement.ScheduledAt,
				"updated_at":   now,
			}).Error; err != nil {
			return err
		}
		return logAdminAction(tx, user.AccountID, "update", announcementAuditLogTarget(announcement), now)
	}); err != nil {
		return err
	}
	if !s.enqueuePublishAnnouncementTask(announcement.ID, time.Time{}) {
		s.broadcastAnnouncement(announcement)
	}
	return c.Redirect(http.StatusFound, "/admin/announcements?notice="+url.QueryEscape(adminT(s.webLocale(c, user), "admin.announcements.published_msg", "Announcement successfully published!")))
}

func (s *Server) unpublishAdminAnnouncement(c *echo.Context) error {
	user, handled, err := s.requireAdminAnnouncementsWebUser(c)
	if handled || err != nil {
		return err
	}
	announcement, err := s.findAdminAnnouncement(c.Param("id"))
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	announcement.Published = false
	announcement.ScheduledAt = sql.NullTime{}
	announcement.UpdatedAt = now
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.Announcement{}).
			Where("id = ?", announcement.ID).
			Updates(map[string]any{
				"published":    false,
				"scheduled_at": announcement.ScheduledAt,
				"updated_at":   now,
			}).Error; err != nil {
			return err
		}
		return logAdminAction(tx, user.AccountID, "update", announcementAuditLogTarget(announcement), now)
	}); err != nil {
		return err
	}
	if !s.enqueueUnpublishAnnouncementTask(announcement.ID) {
		s.broadcastAnnouncementDelete(announcement.ID)
	}
	return c.Redirect(http.StatusFound, "/admin/announcements?notice="+url.QueryEscape(adminT(s.webLocale(c, user), "admin.announcements.unpublished_msg", "Announcement successfully unpublished!")))
}

func (s *Server) adminAnnouncementMemberAction(c *echo.Context) error {
	if methodOverrideIs(c, "delete") {
		return s.destroyAdminAnnouncement(c)
	}
	if methodOverrideIs(c, "put", "patch") {
		return s.updateAdminAnnouncement(c)
	}
	return c.Redirect(http.StatusFound, "/admin/announcements")
}

func (s *Server) destroyAdminAnnouncement(c *echo.Context) error {
	user, handled, err := s.requireAdminAnnouncementsWebUser(c)
	if handled || err != nil {
		return err
	}
	announcement, err := s.findAdminAnnouncement(c.Param("id"))
	if err != nil {
		return err
	}
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("announcement_id = ?", announcement.ID).Delete(&models.AnnouncementMute{}).Error; err != nil {
			return err
		}
		if err := tx.Where("announcement_id = ?", announcement.ID).Delete(&models.AnnouncementReaction{}).Error; err != nil {
			return err
		}
		if err := tx.Delete(&models.Announcement{}, announcement.ID).Error; err != nil {
			return err
		}
		return logAdminAction(tx, user.AccountID, "destroy", announcementAuditLogTarget(announcement), time.Now().UTC())
	}); err != nil {
		return err
	}
	if announcement.Published {
		if !s.enqueueUnpublishAnnouncementTask(announcement.ID) {
			s.broadcastAnnouncementDelete(announcement.ID)
		}
	}
	return c.Redirect(http.StatusFound, "/admin/announcements?notice="+url.QueryEscape(adminT(s.webLocale(c, user), "admin.announcements.destroyed_msg", "Announcement successfully deleted!")))
}

func (s *Server) requireAdminAnnouncementsWebUser(c *echo.Context) (*models.User, bool, error) {
	user, _, handled, err := s.requireFunctionalWebUser(c)
	if handled || err != nil {
		return nil, handled, err
	}
	if !s.userCan(user, rolePermissionManageAnnouncements) {
		locale := s.webLocale(c, user)
		return nil, true, c.HTML(http.StatusForbidden, authPageHTML(adminT(locale, "admin.announcements.title", "Announcements"), "", adminT(locale, "admin.announcements.not_permitted", "You are not allowed to manage announcements."), "", locale))
	}
	return user, false, nil
}

func (s *Server) adminAnnouncementModels(c *echo.Context) ([]models.Announcement, error) {
	if s.db == nil {
		return []models.Announcement{}, nil
	}
	query := s.db.Model(&models.Announcement{})
	if strings.TrimSpace(c.QueryParam("published")) != "" {
		query = query.Where("published = ?", true)
	}
	if strings.TrimSpace(c.QueryParam("unpublished")) != "" {
		query = query.Where("published = ?", false)
	}
	var announcements []models.Announcement
	err := query.Order("COALESCE(starts_at, scheduled_at, published_at, created_at) DESC").
		Offset(adminRailsPageOffset(c)).
		Limit(adminRailsDefaultPageSize).
		Find(&announcements).Error
	return announcements, err
}

func (s *Server) findAdminAnnouncement(rawID string) (models.Announcement, error) {
	id, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil {
		return models.Announcement{}, echo.NewHTTPError(http.StatusNotFound, "announcement not found")
	}
	var announcement models.Announcement
	if s.db == nil {
		return announcement, echo.NewHTTPError(http.StatusNotFound, "announcement not found")
	}
	if err := s.db.Where("id = ?", id).First(&announcement).Error; err != nil {
		return announcement, echo.NewHTTPError(http.StatusNotFound, "announcement not found")
	}
	return announcement, nil
}

func parseAdminAnnouncementForm(c *echo.Context) (adminAnnouncementForm, error) {
	req := c.Request()
	if err := req.ParseForm(); err != nil {
		return adminAnnouncementForm{}, err
	}
	const prefix = "announcement"
	if !formHasNestedPrefix(req.Form, prefix) {
		return adminAnnouncementForm{}, errAdminAnnouncementParamsMissing
	}
	startsAt, err := parseAdminDateTimeLocal(lastFormValue(req.Form, "announcement[starts_at]"))
	if err != nil {
		return adminAnnouncementForm{}, err
	}
	endsAt, err := parseAdminDateTimeLocal(lastFormValue(req.Form, "announcement[ends_at]"))
	if err != nil {
		return adminAnnouncementForm{}, err
	}
	scheduledAt, err := parseAdminDateTimeLocal(lastFormValue(req.Form, "announcement[scheduled_at]"))
	if err != nil {
		return adminAnnouncementForm{}, err
	}
	return adminAnnouncementForm{
		Text:        lastFormValue(req.Form, "announcement[text]"),
		AllDay:      adminSettingsCheckbox(req.Form, "announcement[all_day]"),
		ScheduledAt: scheduledAt,
		StartsAt:    startsAt,
		EndsAt:      endsAt,
	}, nil
}

var errAdminAnnouncementParamsMissing = errors.New("admin announcement root parameter is missing")

func parseAdminDateTimeLocal(value string) (sql.NullTime, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return sql.NullTime{}, nil
	}
	for _, layout := range []string{"2006-01-02T15:04:05", "2006-01-02T15:04"} {
		parsed, err := time.ParseInLocation(layout, value, time.Local)
		if err == nil {
			return sql.NullTime{Time: parsed.UTC(), Valid: true}, nil
		}
	}
	return sql.NullTime{}, errAdminSetting("datetime is invalid")
}

func validateAdminAnnouncementForm(form adminAnnouncementForm) error {
	if strings.TrimSpace(form.Text) == "" {
		return errAdminSetting("Announcement text can't be blank")
	}
	if form.StartsAt.Valid != form.EndsAt.Valid {
		return errAdminSetting("Announcement start and end must both be present")
	}
	return nil
}

func adminAnnouncementMessage(locale, key, fallback string) string {
	return adminT(locale, "admin.announcements."+key, fallback)
}

func adminAnnouncementErrorText(locale string, err error) string {
	if err == nil {
		return ""
	}
	if settingErr, ok := err.(adminSettingError); ok {
		switch string(settingErr) {
		case "datetime is invalid":
			return adminAnnouncementMessage(locale, "errors.datetime_invalid", string(settingErr))
		case "Announcement text can't be blank":
			return adminAnnouncementMessage(locale, "errors.text_blank", string(settingErr))
		case "Announcement start and end must both be present":
			return adminAnnouncementMessage(locale, "errors.start_and_end_required", string(settingErr))
		}
	}
	return err.Error()
}

func adminAnnouncementFromForm(form adminAnnouncementForm, now time.Time) models.Announcement {
	published := !form.ScheduledAt.Valid || !form.ScheduledAt.Time.After(now)
	announcement := models.Announcement{
		Text:        form.Text,
		AllDay:      form.AllDay,
		ScheduledAt: form.ScheduledAt,
		StartsAt:    form.StartsAt,
		EndsAt:      form.EndsAt,
		Published:   published,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if published {
		announcement.PublishedAt = sql.NullTime{Time: now, Valid: true}
		announcement.ScheduledAt = sql.NullTime{}
	}
	return announcement
}

func (s *Server) updateAdminAnnouncementModel(actorAccountID int64, announcement models.Announcement, form adminAnnouncementForm) error {
	now := time.Now().UTC()
	updates := map[string]any{
		"text":         form.Text,
		"all_day":      form.AllDay,
		"starts_at":    form.StartsAt,
		"ends_at":      form.EndsAt,
		"scheduled_at": form.ScheduledAt,
		"updated_at":   now,
	}
	if announcement.Published {
		updates["scheduled_at"] = sql.NullTime{}
	}
	announcement.Text = form.Text
	announcement.AllDay = form.AllDay
	announcement.StartsAt = form.StartsAt
	announcement.EndsAt = form.EndsAt
	announcement.ScheduledAt = updates["scheduled_at"].(sql.NullTime)
	announcement.UpdatedAt = now
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.Announcement{}).Where("id = ?", announcement.ID).Updates(updates).Error; err != nil {
			return err
		}
		return logAdminAction(tx, actorAccountID, "update", announcementAuditLogTarget(announcement), now)
	})
}

type adminAnnouncementFilters struct {
	Page        string
	Published   string
	Unpublished string
}

func adminAnnouncementsIndexHTML(announcements []models.Announcement, notice string, errorText string, filters adminAnnouncementFilters, locale ...string) string {
	loc := settingsLocaleArgOrEnglish(locale...)
	var rows strings.Builder
	if len(announcements) == 0 {
		rows.WriteString(`<div class="muted-hint center-text">` + html.EscapeString(adminT(loc, "admin.announcements.empty", "No announcements found.")) + `</div>`)
	} else {
		rows.WriteString(`<div class="announcements-list">`)
		for _, announcement := range announcements {
			id := strconv.FormatInt(announcement.ID, 10)
			rows.WriteString(`<div class="announcements-list__item"><a class="announcements-list__item__title" href="/admin/announcements/` + id + `/edit">` + html.EscapeString(announcement.Text) + `</a><div class="announcements-list__item__action-bar"><div class="announcements-list__item__meta">` + html.EscapeString(adminAnnouncementMeta(announcement, loc)) + `</div><div>`)
			if announcement.Published {
				rows.WriteString(`<a class="table-action-link" data-method="post" data-confirm="` + html.EscapeString(adminT(loc, "admin.accounts.are_you_sure", "Are you sure?")) + `" href="/admin/announcements/` + id + `/unpublish"><i class="fa fa-toggle-off fa-fw"></i> ` + html.EscapeString(adminT(loc, "admin.announcements.unpublish", "Unpublish")) + `</a>`)
			} else {
				rows.WriteString(`<a class="table-action-link" data-method="post" data-confirm="` + html.EscapeString(adminT(loc, "admin.accounts.are_you_sure", "Are you sure?")) + `" href="/admin/announcements/` + id + `/publish"><i class="fa fa-toggle-on fa-fw"></i> ` + html.EscapeString(adminT(loc, "admin.announcements.publish", "Publish")) + `</a>`)
			}
			rows.WriteString(` <a class="table-action-link" data-method="delete" data-confirm="` + html.EscapeString(adminT(loc, "admin.accounts.are_you_sure", "Are you sure?")) + `" href="/admin/announcements/` + id + `"><i class="fa fa-trash fa-fw"></i> ` + html.EscapeString(adminT(loc, "generic.delete", "Delete")) + `</a></div></div></div>`)
		}
		rows.WriteString(`</div>`)
		rows.WriteString(adminAnnouncementsPaginationHTML(filters, len(announcements) == adminRailsDefaultPageSize, loc))
	}
	filterValues := url.Values{}
	body := `<div class="content__heading__actions"><a class="button" href="/admin/announcements/new">` + html.EscapeString(adminT(loc, "admin.announcements.new.title", "New announcement")) + `</a></div><div class="filters">` + relationshipFilterSubsetHTML(adminT(loc, "admin.relays.status", "Status"), []relationshipFilterLink{{Label: adminT(loc, "generic.all", "All"), Href: adminTrendsWebFilterHref("/admin/announcements", filterValues, "published", ""), Active: filters.Published == ""}, {Label: adminT(loc, "admin.announcements.live", "Live"), Href: adminTrendsWebFilterHref("/admin/announcements", filterValues, "published", "1"), Active: filters.Published == "1"}}) + `</div>` + rows.String()
	return authPageHTML(adminT(loc, "admin.announcements.title", "Announcements"), notice, errorText, body, loc)
}

func adminAnnouncementsPaginationHTML(filters adminAnnouncementFilters, hasNext bool, locale ...string) string {
	loc := settingsLocaleArgOrEnglish(locale...)
	pageNum, err := strconv.Atoi(strings.TrimSpace(filters.Page))
	if err != nil || pageNum < 1 {
		pageNum = 1
	}
	var links []string
	if pageNum > 1 {
		params := adminAnnouncementPageParams(filters, pageNum-1)
		links = append(links, `<a href="/admin/announcements?`+html.EscapeString(params.Encode())+`">`+html.EscapeString(settingsT(loc, "pagination.prev", "Previous"))+`</a>`)
	}
	if hasNext {
		params := adminAnnouncementPageParams(filters, pageNum+1)
		links = append(links, `<a href="/admin/announcements?`+html.EscapeString(params.Encode())+`">`+html.EscapeString(settingsT(loc, "pagination.next", "Next"))+`</a>`)
	}
	if len(links) == 0 {
		return ""
	}
	return `<nav class="pagination">` + strings.Join(links, " ") + `</nav>`
}

func adminAnnouncementPageParams(filters adminAnnouncementFilters, page int) url.Values {
	params := url.Values{"page": []string{strconv.Itoa(page)}}
	if strings.TrimSpace(filters.Published) != "" {
		params.Set("published", strings.TrimSpace(filters.Published))
	}
	if strings.TrimSpace(filters.Unpublished) != "" {
		params.Set("unpublished", strings.TrimSpace(filters.Unpublished))
	}
	return params
}

func adminAnnouncementEditHTML(announcement models.Announcement, notice string, errorText string, locale ...string) string {
	loc := settingsLocaleArgOrEnglish(locale...)
	return authPageHTML(adminT(loc, "admin.announcements.edit.title", "Edit announcement"), notice, errorText, adminAnnouncementFormBody("/admin/announcements/"+strconv.FormatInt(announcement.ID, 10), "patch", adminAnnouncementFormFromModel(announcement), announcement.Published, adminT(loc, "generic.save_changes", "Save changes"), loc), loc)
}

func adminAnnouncementFormFromModel(announcement models.Announcement) adminAnnouncementForm {
	return adminAnnouncementForm{
		Text:        announcement.Text,
		AllDay:      announcement.AllDay,
		ScheduledAt: announcement.ScheduledAt,
		StartsAt:    announcement.StartsAt,
		EndsAt:      announcement.EndsAt,
	}
}

func adminAnnouncementNewHTML(form adminAnnouncementForm, errorText string, locale ...string) string {
	loc := settingsLocaleArgOrEnglish(locale...)
	return adminAnnouncementFormHTML(adminT(loc, "admin.announcements.new.title", "New announcement"), "/admin/announcements", "", form, false, adminT(loc, "admin.announcements.new.create", "Create announcement"), errorText, loc)
}

func adminAnnouncementEditFormHTML(announcement models.Announcement, form adminAnnouncementForm, errorText string, locale ...string) string {
	loc := settingsLocaleArgOrEnglish(locale...)
	return adminAnnouncementFormHTML(adminT(loc, "admin.announcements.edit.title", "Edit announcement"), "/admin/announcements/"+strconv.FormatInt(announcement.ID, 10), "patch", form, announcement.Published, adminT(loc, "generic.save_changes", "Save changes"), errorText, loc)
}

func adminAnnouncementFormHTML(title string, action string, methodOverride string, form adminAnnouncementForm, published bool, submitLabel string, errorText string, locale ...string) string {
	loc := settingsLocaleArgOrEnglish(locale...)
	return authPageHTML(title, "", errorText, adminAnnouncementFormBody(action, methodOverride, form, published, submitLabel, loc), loc)
}

func adminAnnouncementFormBody(action string, methodOverride string, form adminAnnouncementForm, published bool, submitLabel string, locale ...string) string {
	loc := settingsLocaleArgOrEnglish(locale...)
	if strings.TrimSpace(submitLabel) == "" {
		submitLabel = adminT(loc, "generic.save_changes", "Save changes")
	}
	scheduledField := ""
	if !published {
		scheduledField = simpleTextInput(adminT(loc, "simple_form.labels.announcement.scheduled_at", "Schedule publication"), "announcement[scheduled_at]", adminDateTimeLocalValue(form.ScheduledAt), "datetime-local", "")
	}
	return simpleFormOpen(action, methodOverride) +
		simpleTextInput(adminT(loc, "simple_form.labels.announcement.starts_at", "Start of event"), "announcement[starts_at]", adminDateTimeLocalValue(form.StartsAt), "datetime-local", "") +
		simpleTextInput(adminT(loc, "simple_form.labels.announcement.ends_at", "End of event"), "announcement[ends_at]", adminDateTimeLocalValue(form.EndsAt), "datetime-local", "") +
		simpleCheckbox(adminT(loc, "simple_form.labels.announcement.all_day", "All-day event"), "announcement[all_day]", form.AllDay) +
		`<div class="fields-group"><div class="input with_block_label"><div class="label_input"><label>` + html.EscapeString(adminT(loc, "simple_form.labels.announcement.text", "Announcement")) + `</label><textarea name="announcement[text]" rows="8" required>` + html.EscapeString(form.Text) + `</textarea></div></div></div>` +
		scheduledField +
		simpleSubmit(submitLabel) +
		simpleFormClose()
}

func adminAnnouncementMeta(announcement models.Announcement, locale ...string) string {
	loc := settingsLocaleArgOrEnglish(locale...)
	if announcement.ScheduledAt.Valid {
		return adminTVars(loc, "admin.announcements.scheduled_for", "Scheduled for %{time}", map[string]string{"time": announcement.ScheduledAt.Time.Format(time.RFC3339)})
	}
	if announcement.Published {
		return adminT(loc, "admin.announcements.live", "Live")
	}
	return adminT(loc, "admin.announcements.unpublished", "Unpublished")
}

func adminDateTimeLocalValue(value sql.NullTime) string {
	if !value.Valid {
		return ""
	}
	return value.Time.Local().Format("2006-01-02T15:04")
}
