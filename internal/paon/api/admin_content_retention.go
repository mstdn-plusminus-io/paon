package api

import (
	"html"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/labstack/echo/v5"
	"gorm.io/gorm"
)

type adminContentRetentionSettings struct {
	MediaCacheRetentionPeriod   string
	ContentCacheRetentionPeriod string
	BackupsRetentionPeriod      string
}

func (s *Server) adminSettingsContentRetentionPage(c *echo.Context) error {
	user, _, handled, err := s.requireAdminSettingsWebUser(c)
	if handled || err != nil {
		return err
	}
	locale := s.webLocale(c, user)
	theme := settingsWebTheme(decodeUserSettings(user.Settings.String))
	return c.HTML(http.StatusOK, adminSettingsContentRetentionHTML(s.adminContentRetentionSettings(), c.QueryParam("notice"), c.QueryParam("error"), locale, theme))
}

func (s *Server) updateAdminSettingsContentRetention(c *echo.Context) error {
	user, _, handled, err := s.requireAdminSettingsWebUser(c)
	if handled || err != nil {
		return err
	}
	locale := s.webLocale(c, user)
	theme := settingsWebTheme(decodeUserSettings(user.Settings.String))
	if c.Request().Method == http.MethodPost && !methodOverrideIs(c, "put", "patch") {
		return c.Redirect(http.StatusFound, "/admin/settings/content_retention")
	}
	settings, err := parseAdminContentRetentionSettings(c)
	if err != nil {
		if adminSettingsParamsMissing(err) {
			return apiError(c, http.StatusBadRequest, "Malformed request")
		}
		return c.HTML(http.StatusOK, adminSettingsContentRetentionHTML(s.adminContentRetentionSettings(), "", adminSettingsInvalidMessage(locale, "content_retention"), locale, theme))
	}
	if err := validateAdminContentRetentionSettings(settings); err != nil {
		return c.HTML(http.StatusOK, adminSettingsContentRetentionHTML(settings, "", adminSettingErrorText(locale, err), locale, theme))
	}
	if s.db == nil {
		return c.HTML(http.StatusOK, adminSettingsContentRetentionHTML(settings, "", adminSettingsDatabaseUnavailableMessage(locale), locale, theme))
	}
	if err := s.updateAdminContentRetentionSettings(settings); err != nil {
		return err
	}
	return c.Redirect(http.StatusFound, "/admin/settings/content_retention?notice="+url.QueryEscape(adminSettingsSavedMessage(locale, "content_retention")))
}

func (s *Server) adminContentRetentionSettings() adminContentRetentionSettings {
	return adminContentRetentionSettings{
		MediaCacheRetentionPeriod:   adminRetentionIntegerSetting(s.settingValue("media_cache_retention_period", "")),
		ContentCacheRetentionPeriod: adminRetentionIntegerSetting(s.settingValue("content_cache_retention_period", "")),
		BackupsRetentionPeriod:      adminRetentionIntegerSetting(s.settingValue("backups_retention_period", "7")),
	}
}

func parseAdminContentRetentionSettings(c *echo.Context) (adminContentRetentionSettings, error) {
	req := c.Request()
	if err := parseAdminSettingsFormRoot(req, 8<<20); err != nil {
		return adminContentRetentionSettings{}, err
	}
	return adminContentRetentionSettings{
		MediaCacheRetentionPeriod:   strings.TrimSpace(lastFormValue(req.Form, "form_admin_settings[media_cache_retention_period]")),
		ContentCacheRetentionPeriod: strings.TrimSpace(lastFormValue(req.Form, "form_admin_settings[content_cache_retention_period]")),
		BackupsRetentionPeriod:      strings.TrimSpace(lastFormValue(req.Form, "form_admin_settings[backups_retention_period]")),
	}, nil
}

func validateAdminContentRetentionSettings(settings adminContentRetentionSettings) error {
	if !adminRetentionIntegerAllowed(settings.MediaCacheRetentionPeriod) {
		return errAdminSetting("Media cache retention period must be an integer")
	}
	if !adminRetentionIntegerAllowed(settings.ContentCacheRetentionPeriod) {
		return errAdminSetting("Content cache retention period must be an integer")
	}
	if !adminRetentionIntegerAllowed(settings.BackupsRetentionPeriod) {
		return errAdminSetting("Backups retention period must be an integer")
	}
	return nil
}

func (s *Server) updateAdminContentRetentionSettings(settings adminContentRetentionSettings) error {
	values := map[string]string{
		"media_cache_retention_period":   settings.MediaCacheRetentionPeriod,
		"content_cache_retention_period": settings.ContentCacheRetentionPeriod,
		"backups_retention_period":       settings.BackupsRetentionPeriod,
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		for name, value := range values {
			if err := upsertGlobalSetting(tx, name, value); err != nil {
				return err
			}
		}
		return nil
	})
}

func adminRetentionIntegerSetting(value string) string {
	value = normalizeSettingScalar(value)
	if adminRetentionIntegerAllowed(value) {
		return value
	}
	return ""
}

func adminRetentionIntegerAllowed(value string) bool {
	if value == "" {
		return true
	}
	_, err := strconv.ParseInt(value, 10, 64)
	return err == nil
}

func adminSettingsContentRetentionHTML(settings adminContentRetentionSettings, notice string, errorText string, localeAndTheme ...string) string {
	loc := settingsLocaleArgOrEnglish(localeAndTheme...)
	theme := settingsThemeArg(localeAndTheme...)
	title := adminT(loc, "admin.settings.content_retention.title", "Content retention")
	body := `<p class="lead">` + html.EscapeString(adminT(loc, "admin.settings.content_retention.preamble", "Configure the same cache and backup retention settings used by Rails background maintenance tasks.")) + `</p>
<form class="simple_form" method="post" action="/admin/settings/content_retention">
  <input type="hidden" name="_method" value="patch">
  <div class="fields-group"><label>` + html.EscapeString(adminSettingsLabel(loc, "media_cache_retention_period", "Media cache retention period")) + ` <input name="form_admin_settings[media_cache_retention_period]" pattern="[0-9]+" value="` + html.EscapeString(settings.MediaCacheRetentionPeriod) + `"></label></div>
  <div class="fields-group"><label>` + html.EscapeString(adminSettingsLabel(loc, "content_cache_retention_period", "Content cache retention period")) + ` <input name="form_admin_settings[content_cache_retention_period]" pattern="[0-9]+" value="` + html.EscapeString(settings.ContentCacheRetentionPeriod) + `"></label></div>
  <div class="fields-group"><label>` + html.EscapeString(adminSettingsLabel(loc, "backups_retention_period", "User archive retention period")) + ` <input name="form_admin_settings[backups_retention_period]" pattern="[0-9]+" value="` + html.EscapeString(settings.BackupsRetentionPeriod) + `"></label></div>
  <div class="actions"><button type="submit">` + html.EscapeString(settingsT(loc, "generic.save_changes", "Save changes")) + `</button></div>
</form>`
	return adminSettingsPageHTML(title, "content_retention", notice, errorText, body, loc, theme)
}
