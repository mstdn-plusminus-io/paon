package api

import (
	"html"
	"net/http"
	"net/url"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/serializer"
	"gorm.io/gorm"
)

type adminAppearanceSettings struct {
	Theme          string
	CustomCSS      string
	MascotUploadID int64
	MascotURL      string
}

func (s *Server) adminSettingsAppearancePage(c *echo.Context) error {
	user, _, handled, err := s.requireAdminSettingsWebUser(c)
	if handled || err != nil {
		return err
	}
	locale := s.webLocale(c, user)
	theme := settingsWebTheme(decodeUserSettings(user.Settings.String))
	return c.HTML(http.StatusOK, adminSettingsAppearanceHTML(s.adminAppearanceSettings(), c.QueryParam("notice"), c.QueryParam("error"), locale, theme))
}

func (s *Server) updateAdminSettingsAppearance(c *echo.Context) error {
	user, _, handled, err := s.requireAdminSettingsWebUser(c)
	if handled || err != nil {
		return err
	}
	locale := s.webLocale(c, user)
	theme := settingsWebTheme(decodeUserSettings(user.Settings.String))
	if c.Request().Method == http.MethodPost && !methodOverrideIs(c, "put", "patch") {
		return c.Redirect(http.StatusFound, "/admin/settings/appearance")
	}
	settings, err := parseAdminAppearanceSettings(c)
	if err != nil {
		if adminSettingsParamsMissing(err) {
			return apiError(c, http.StatusBadRequest, "Malformed request")
		}
		return c.HTML(http.StatusOK, adminSettingsAppearanceHTML(s.adminAppearanceSettings(), "", adminSettingsInvalidMessage(locale, "appearance"), locale, theme))
	}
	if err := validateAdminAppearanceSettings(settings); err != nil {
		return c.HTML(http.StatusOK, adminSettingsAppearanceHTML(s.withAdminAppearanceSiteUpload(settings), "", adminSettingErrorText(locale, err), locale, theme))
	}
	if s.db == nil {
		return c.HTML(http.StatusOK, adminSettingsAppearanceHTML(settings, "", adminSettingsDatabaseUnavailableMessage(locale), locale, theme))
	}
	if err := validateAdminSiteUploadFromForm(c, "mascot"); err != nil {
		return c.HTML(http.StatusOK, adminSettingsAppearanceHTML(s.withAdminAppearanceSiteUpload(settings), "", adminSettingErrorText(locale, err), locale, theme))
	}
	if err := s.updateAdminAppearanceSettings(settings); err != nil {
		return err
	}
	if err := s.storeAdminSiteUploadFromForm(c, "mascot"); err != nil {
		return c.HTML(http.StatusOK, adminSettingsAppearanceHTML(s.withAdminAppearanceSiteUpload(settings), "", adminSettingErrorText(locale, err), locale, theme))
	}
	return c.Redirect(http.StatusFound, "/admin/settings/appearance?notice="+url.QueryEscape(adminSettingsSavedMessage(locale, "appearance")))
}

func (s *Server) adminAppearanceSettings() adminAppearanceSettings {
	return s.withAdminAppearanceSiteUpload(adminAppearanceSettings{
		Theme:     adminThemeSetting(s.settingStringValue("theme", "system")),
		CustomCSS: s.settingValue("custom_css", ""),
	})
}

func (s *Server) withAdminAppearanceSiteUpload(settings adminAppearanceSettings) adminAppearanceSettings {
	if upload, _ := s.instanceSiteUpload("mascot"); upload != nil {
		settings.MascotUploadID = upload.ID
		settings.MascotURL = serializer.SiteUploadFileURL(s.cfg, *upload, "original")
	}
	return settings
}

func parseAdminAppearanceSettings(c *echo.Context) (adminAppearanceSettings, error) {
	req := c.Request()
	if err := parseAdminSettingsFormRoot(req, 8<<20); err != nil {
		return adminAppearanceSettings{}, err
	}
	return adminAppearanceSettings{
		Theme:     lastFormValue(req.Form, "form_admin_settings[theme]"),
		CustomCSS: lastFormValue(req.Form, "form_admin_settings[custom_css]"),
	}, nil
}

func validateAdminAppearanceSettings(settings adminAppearanceSettings) error {
	if !adminThemeAllowed(settings.Theme) {
		return errAdminSetting("Theme is invalid")
	}
	return nil
}

func (s *Server) updateAdminAppearanceSettings(settings adminAppearanceSettings) error {
	values := map[string]string{
		"theme":      settings.Theme,
		"custom_css": settings.CustomCSS,
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

func adminThemeSetting(value string) string {
	value = normalizeSettingScalar(value)
	if adminThemeAllowed(value) {
		return value
	}
	return "system"
}

func adminThemeAllowed(value string) bool {
	switch value {
	case "system", "default", "contrast", "mastodon-light", "single-column-chat-dark":
		return true
	default:
		return false
	}
}

func adminSettingsAppearanceHTML(settings adminAppearanceSettings, notice string, errorText string, localeAndTheme ...string) string {
	loc := settingsLocaleArgOrEnglish(localeAndTheme...)
	theme := settingsThemeArg(localeAndTheme...)
	option := func(value string, label string) string {
		selected := ""
		if settings.Theme == value {
			selected = ` selected`
		}
		return `<option value="` + value + `"` + selected + `>` + html.EscapeString(label) + `</option>`
	}
	title := adminT(loc, "admin.settings.appearance.title", "Appearance")
	body := `<p class="lead">` + html.EscapeString(adminT(loc, "admin.settings.appearance.preamble", "Configure the default visual theme, custom CSS, and optional mascot assets.")) + `</p>
<form class="simple_form" method="post" action="/admin/settings/appearance" enctype="multipart/form-data">
	  <input type="hidden" name="_method" value="patch">
	  <div class="fields-group"><label>` + html.EscapeString(adminSettingsLabel(loc, "theme", "Default theme")) + ` <select name="form_admin_settings[theme]">` +
		option("system", settingsT(loc, "themes.system", "Automatic (use system theme)")) +
		option("default", "Default") +
		option("contrast", "High contrast") +
		option("mastodon-light", "Mastodon light") +
		option("single-column-chat-dark", "Single column chat dark") +
		`</select></label>
  </div><div class="fields-group"><label>` + html.EscapeString(adminSettingsLabel(loc, "custom_css", "Custom CSS")) + ` <textarea name="form_admin_settings[custom_css]" rows="8">` + html.EscapeString(settings.CustomCSS) + `</textarea></label></div>
  <div class="fields-row"><div class="fields-row__column fields-row__column-6 fields-group"><label>` + html.EscapeString(adminSettingsLabel(loc, "mascot", "Custom mascot (legacy)")) + ` <input type="file" name="form_admin_settings[mascot]" accept="image/*"></label></div>
  <div class="fields-row__column fields-row__column-6 fields-group">` + adminSiteUploadPreviewDeleteHTML(settings.MascotUploadID, settings.MascotURL, loc) + `</div></div>
  <div class="actions"><button type="submit">` + html.EscapeString(settingsT(loc, "generic.save_changes", "Save changes")) + `</button></div>
</form>`
	return adminSettingsPageHTML(title, "appearance", notice, errorText, body, loc, theme)
}
