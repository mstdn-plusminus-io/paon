package api

import (
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

type adminWarningPresetForm struct {
	Title string
	Text  string
}

var errAdminWarningPresetParamsMissing = errors.New("admin warning preset root parameter is missing")

func (s *Server) adminWarningPresetsPage(c *echo.Context) error {
	user, _, handled, err := s.requireAdminSettingsWebUser(c)
	if handled || err != nil {
		return err
	}
	presets, err := s.adminWarningPresetModels()
	if err != nil {
		return err
	}
	return c.HTML(http.StatusOK, adminWarningPresetsIndexHTML(presets, adminWarningPresetForm{}, c.QueryParam("notice"), c.QueryParam("error"), s.webLocale(c, user)))
}

func (s *Server) createAdminWarningPreset(c *echo.Context) error {
	user, _, handled, err := s.requireAdminSettingsWebUser(c)
	if handled || err != nil {
		return err
	}
	locale := s.webLocale(c, user)
	form, err := parseAdminWarningPresetForm(c)
	if err != nil {
		if errors.Is(err, errAdminWarningPresetParamsMissing) {
			return apiError(c, http.StatusBadRequest, "Malformed request")
		}
		return s.renderAdminWarningPresetsIndexError(c, adminWarningPresetForm{}, adminWarningPresetMessage(locale, "errors.invalid", "Warning preset is invalid"), locale)
	}
	if err := validateAdminWarningPresetForm(form); err != nil {
		return s.renderAdminWarningPresetsIndexError(c, form, adminWarningPresetErrorText(locale, err), locale)
	}
	if s.db == nil {
		return s.renderAdminWarningPresetsIndexError(c, form, adminWarningPresetMessage(locale, "errors.database_unavailable", "DATABASE_URL is not set"), locale)
	}
	if err := s.insertAdminWarningPreset(form); err != nil {
		return err
	}
	return c.Redirect(http.StatusFound, "/admin/warning_presets")
}

func (s *Server) renderAdminWarningPresetsIndexError(c *echo.Context, form adminWarningPresetForm, errorText string, locale string) error {
	presets, err := s.adminWarningPresetModels()
	if err != nil {
		return err
	}
	return c.HTML(http.StatusOK, adminWarningPresetsIndexHTML(presets, form, "", errorText, locale))
}

func (s *Server) editAdminWarningPresetPage(c *echo.Context) error {
	user, _, handled, err := s.requireAdminSettingsWebUser(c)
	if handled || err != nil {
		return err
	}
	preset, err := s.findAdminWarningPreset(c.Param("id"))
	if err != nil {
		return err
	}
	return c.HTML(http.StatusOK, adminWarningPresetEditHTML(preset, c.QueryParam("notice"), c.QueryParam("error"), s.webLocale(c, user)))
}

func (s *Server) updateAdminWarningPreset(c *echo.Context) error {
	user, _, handled, err := s.requireAdminSettingsWebUser(c)
	if handled || err != nil {
		return err
	}
	if c.Request().Method == http.MethodPost && !methodOverrideIs(c, "put", "patch") {
		return c.Redirect(http.StatusFound, "/admin/warning_presets")
	}
	preset, err := s.findAdminWarningPreset(c.Param("id"))
	if err != nil {
		return err
	}
	locale := s.webLocale(c, user)
	form, err := parseAdminWarningPresetForm(c)
	if err != nil {
		if errors.Is(err, errAdminWarningPresetParamsMissing) {
			return apiError(c, http.StatusBadRequest, "Malformed request")
		}
		return c.HTML(http.StatusOK, adminWarningPresetEditHTML(preset, "", adminWarningPresetMessage(locale, "errors.invalid", "Warning preset is invalid"), locale))
	}
	if err := validateAdminWarningPresetForm(form); err != nil {
		return c.HTML(http.StatusOK, adminWarningPresetEditHTML(adminWarningPresetWithForm(preset, form), "", adminWarningPresetErrorText(locale, err), locale))
	}
	if s.db == nil {
		return c.HTML(http.StatusOK, adminWarningPresetEditHTML(adminWarningPresetWithForm(preset, form), "", adminWarningPresetMessage(locale, "errors.database_unavailable", "DATABASE_URL is not set"), locale))
	}
	if err := s.updateAdminWarningPresetModel(preset.ID, form); err != nil {
		return err
	}
	return c.Redirect(http.StatusFound, "/admin/warning_presets")
}

func (s *Server) adminWarningPresetMemberAction(c *echo.Context) error {
	if methodOverrideIs(c, "delete") {
		return s.destroyAdminWarningPreset(c)
	}
	if methodOverrideIs(c, "put", "patch") {
		return s.updateAdminWarningPreset(c)
	}
	return c.Redirect(http.StatusFound, "/admin/warning_presets")
}

func (s *Server) destroyAdminWarningPreset(c *echo.Context) error {
	user, _, handled, err := s.requireAdminSettingsWebUser(c)
	if handled || err != nil {
		return err
	}
	preset, err := s.findAdminWarningPreset(c.Param("id"))
	if err != nil {
		return err
	}
	locale := s.webLocale(c, user)
	if s.db == nil {
		return c.Redirect(http.StatusFound, "/admin/warning_presets?error="+url.QueryEscape(adminWarningPresetMessage(locale, "errors.database_unavailable", "DATABASE_URL is not set")))
	}
	if err := s.db.Delete(&models.AccountWarningPreset{}, preset.ID).Error; err != nil {
		return err
	}
	return c.Redirect(http.StatusFound, "/admin/warning_presets")
}

func adminWarningPresetMessage(locale string, key string, fallback string) string {
	return adminT(locale, "admin.warning_presets."+key, fallback)
}

func adminWarningPresetErrorText(locale string, err error) string {
	if err == nil {
		return ""
	}
	text := err.Error()
	keys := map[string]string{
		"Warning text can't be blank": "text_blank",
	}
	if key := keys[text]; key != "" {
		return adminWarningPresetMessage(locale, "errors."+key, text)
	}
	return text
}

func (s *Server) adminWarningPresetModels() ([]models.AccountWarningPreset, error) {
	if s.db == nil {
		return []models.AccountWarningPreset{}, nil
	}
	var presets []models.AccountWarningPreset
	err := s.db.Order("title ASC, text ASC").Find(&presets).Error
	return presets, err
}

func (s *Server) findAdminWarningPreset(rawID string) (models.AccountWarningPreset, error) {
	id, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil {
		return models.AccountWarningPreset{}, echo.NewHTTPError(http.StatusNotFound, "warning preset not found")
	}
	var preset models.AccountWarningPreset
	if s.db == nil {
		return preset, echo.NewHTTPError(http.StatusNotFound, "warning preset not found")
	}
	if err := s.db.Where("id = ?", id).First(&preset).Error; err != nil {
		return preset, echo.NewHTTPError(http.StatusNotFound, "warning preset not found")
	}
	return preset, nil
}

func (s *Server) insertAdminWarningPreset(form adminWarningPresetForm) error {
	now := time.Now().UTC()
	preset := models.AccountWarningPreset{
		Title:     form.Title,
		Text:      form.Text,
		CreatedAt: now,
		UpdatedAt: now,
	}
	return s.db.Create(&preset).Error
}

func (s *Server) updateAdminWarningPresetModel(id int64, form adminWarningPresetForm) error {
	return s.db.Model(&models.AccountWarningPreset{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"title":      form.Title,
			"text":       form.Text,
			"updated_at": time.Now().UTC(),
		}).Error
}

func adminWarningPresetWithForm(preset models.AccountWarningPreset, form adminWarningPresetForm) models.AccountWarningPreset {
	preset.Title = form.Title
	preset.Text = form.Text
	return preset
}

func parseAdminWarningPresetForm(c *echo.Context) (adminWarningPresetForm, error) {
	req := c.Request()
	if err := req.ParseForm(); err != nil {
		return adminWarningPresetForm{}, err
	}
	prefix := "account_warning_preset"
	if !formHasNestedPrefix(req.Form, prefix) {
		return adminWarningPresetForm{}, errAdminWarningPresetParamsMissing
	}
	return adminWarningPresetForm{
		Title: lastFormValue(req.Form, "account_warning_preset[title]"),
		Text:  lastFormValue(req.Form, "account_warning_preset[text]"),
	}, nil
}

func validateAdminWarningPresetForm(form adminWarningPresetForm) error {
	if strings.TrimSpace(form.Text) == "" {
		return errAdminSetting("Warning text can't be blank")
	}
	return nil
}

func adminWarningPresetsIndexHTML(presets []models.AccountWarningPreset, form adminWarningPresetForm, notice string, errorText string, locale ...string) string {
	loc := settingsLocaleArg(locale...)
	var rows strings.Builder
	if len(presets) == 0 {
		rows.WriteString(`<div class="muted-hint center-text">` + html.EscapeString(adminT(loc, "admin.warning_presets.empty", "No warning presets have been configured.")) + `</div>`)
	} else {
		rows.WriteString(`<div class="announcements-list">`)
		for _, preset := range presets {
			id := strconv.FormatInt(preset.ID, 10)
			title := strings.TrimSpace(preset.Title)
			if title == "" {
				title = preset.Text
			}
			rows.WriteString(`<div class="announcements-list__item"><a class="announcements-list__item__title" href="/admin/warning_presets/` + id + `/edit">` + html.EscapeString(title) + `</a><div class="announcements-list__item__action-bar"><div class="announcements-list__item__meta">` + html.EscapeString(preset.Text) + `</div><div><a class="table-action-link" data-method="delete" data-confirm="` + html.EscapeString(adminT(loc, "admin.accounts.are_you_sure", "Are you sure?")) + `" href="/admin/warning_presets/` + id + `"><i class="fa fa-trash fa-fw"></i> ` + html.EscapeString(adminT(loc, "admin.warning_presets.delete", "Delete")) + `</a></div></div></div>`)
		}
		rows.WriteString(`</div>`)
	}
	body := simpleFormOpen("/admin/warning_presets", "post") +
		simpleTextInput(adminT(loc, "simple_form.labels.account_warning_preset.title", "Title"), "account_warning_preset[title]", form.Title, "text", "") +
		`<div class="fields-group"><div class="input with_block_label"><div class="label_input"><label>` + html.EscapeString(adminT(loc, "simple_form.labels.account_warning_preset.text", "Warning text")) + `</label><textarea name="account_warning_preset[text]" rows="5" required>` + html.EscapeString(form.Text) + `</textarea></div></div></div>` +
		simpleSubmit(adminT(loc, "admin.warning_presets.add_new", "Add preset")) +
		simpleFormClose() + `<hr class="spacer">` +
		rows.String()
	return authPageHTML(adminT(loc, "admin.warning_presets.title", "Warning presets"), notice, errorText, body, loc)
}

func adminWarningPresetEditHTML(preset models.AccountWarningPreset, notice string, errorText string, locale ...string) string {
	loc := settingsLocaleArg(locale...)
	id := strconv.FormatInt(preset.ID, 10)
	body := simpleFormOpen("/admin/warning_presets/"+id, "patch") +
		simpleTextInput(adminT(loc, "simple_form.labels.account_warning_preset.title", "Title"), "account_warning_preset[title]", preset.Title, "text", "") +
		`<div class="fields-group"><div class="input with_block_label"><div class="label_input"><label>` + html.EscapeString(adminT(loc, "simple_form.labels.account_warning_preset.text", "Warning text")) + `</label><textarea name="account_warning_preset[text]" rows="5" required>` + html.EscapeString(preset.Text) + `</textarea></div></div></div>` +
		simpleSubmit(adminT(loc, "generic.save_changes", "Save changes")) +
		simpleFormClose()
	return authPageHTML(adminT(loc, "admin.warning_presets.edit_preset", "Edit warning preset"), notice, errorText, body, loc)
}
