package api

import (
	"html"
	"net/http"
	"net/url"

	"github.com/labstack/echo/v5"
	"gorm.io/gorm"
)

type adminRegistrationsSettings struct {
	RegistrationsMode          string
	RequireInviteText          bool
	CaptchaEnabled             bool
	ClosedRegistrationsMessage string
}

func (s *Server) adminSettingsRegistrationsPage(c *echo.Context) error {
	user, _, handled, err := s.requireAdminSettingsWebUser(c)
	if handled || err != nil {
		return err
	}
	locale := s.webLocale(c, user)
	theme := settingsWebTheme(decodeUserSettings(user.Settings.String))
	return c.HTML(http.StatusOK, adminSettingsRegistrationsHTML(s.adminRegistrationsSettings(), c.QueryParam("notice"), c.QueryParam("error"), locale, theme))
}

func (s *Server) updateAdminSettingsRegistrations(c *echo.Context) error {
	user, _, handled, err := s.requireAdminSettingsWebUser(c)
	if handled || err != nil {
		return err
	}
	locale := s.webLocale(c, user)
	theme := settingsWebTheme(decodeUserSettings(user.Settings.String))
	if c.Request().Method == http.MethodPost && !methodOverrideIs(c, "put", "patch") {
		return c.Redirect(http.StatusFound, "/admin/settings/registrations")
	}
	settings, err := parseAdminRegistrationsSettings(c)
	if err != nil {
		if adminSettingsParamsMissing(err) {
			return apiError(c, http.StatusBadRequest, "Malformed request")
		}
		return c.HTML(http.StatusOK, adminSettingsRegistrationsHTML(s.adminRegistrationsSettings(), "", adminSettingsInvalidMessage(locale, "registrations"), locale, theme))
	}
	if err := validateAdminRegistrationsSettings(settings); err != nil {
		return c.HTML(http.StatusOK, adminSettingsRegistrationsHTML(settings, "", adminSettingErrorText(locale, err), locale, theme))
	}
	if s.db == nil {
		return c.HTML(http.StatusOK, adminSettingsRegistrationsHTML(settings, "", adminSettingsDatabaseUnavailableMessage(locale), locale, theme))
	}
	if err := s.updateAdminRegistrationsSettings(settings); err != nil {
		return err
	}
	return c.Redirect(http.StatusFound, "/admin/settings/registrations?notice="+url.QueryEscape(adminSettingsSavedMessage(locale, "registrations")))
}

func (s *Server) adminRegistrationsSettings() adminRegistrationsSettings {
	return adminRegistrationsSettings{
		RegistrationsMode:          normalizeRegistrationsMode(s.settingValue("registrations_mode", "none")),
		RequireInviteText:          s.settingBoolValue("require_invite_text", false),
		CaptchaEnabled:             s.settingBoolValue("captcha_enabled", false),
		ClosedRegistrationsMessage: s.settingStringValue("closed_registrations_message", ""),
	}
}

func parseAdminRegistrationsSettings(c *echo.Context) (adminRegistrationsSettings, error) {
	req := c.Request()
	if err := parseAdminSettingsFormRoot(req, 8<<20); err != nil {
		return adminRegistrationsSettings{}, err
	}
	return adminRegistrationsSettings{
		RegistrationsMode:          lastFormValue(req.Form, "form_admin_settings[registrations_mode]"),
		RequireInviteText:          adminSettingsCheckbox(req.Form, "form_admin_settings[require_invite_text]"),
		CaptchaEnabled:             adminSettingsCheckbox(req.Form, "form_admin_settings[captcha_enabled]"),
		ClosedRegistrationsMessage: lastFormValue(req.Form, "form_admin_settings[closed_registrations_message]"),
	}, nil
}

func validateAdminRegistrationsSettings(settings adminRegistrationsSettings) error {
	switch settings.RegistrationsMode {
	case "open", "approved", "none":
		return nil
	default:
		return errAdminSetting("Registration mode is invalid")
	}
}

func (s *Server) updateAdminRegistrationsSettings(settings adminRegistrationsSettings) error {
	values := map[string]string{
		"registrations_mode":           settings.RegistrationsMode,
		"require_invite_text":          boolSettingValue(settings.RequireInviteText),
		"captcha_enabled":              boolSettingValue(settings.CaptchaEnabled),
		"closed_registrations_message": settings.ClosedRegistrationsMessage,
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

func boolSettingValue(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func adminSettingsCheckbox(values map[string][]string, key string) bool {
	raw, ok := values[key]
	if !ok || len(raw) == 0 {
		return false
	}
	return truthy(raw[len(raw)-1])
}

func adminSettingsRegistrationsHTML(settings adminRegistrationsSettings, notice string, errorText string, localeAndTheme ...string) string {
	loc := settingsLocaleArgOrEnglish(localeAndTheme...)
	theme := settingsThemeArg(localeAndTheme...)
	option := func(value string, label string) string {
		selected := ""
		if settings.RegistrationsMode == value {
			selected = ` selected`
		}
		return `<option value="` + value + `"` + selected + `>` + label + `</option>`
	}
	checkedInvite := ""
	if settings.RequireInviteText {
		checkedInvite = " checked"
	}
	checkedCaptcha := ""
	if settings.CaptchaEnabled {
		checkedCaptcha = " checked"
	}
	title := adminT(loc, "admin.settings.registrations.title", "Registrations")
	body := `<p class="lead">` + html.EscapeString(adminT(loc, "admin.settings.registrations.preamble", "Control who can create accounts and what extra steps are required during sign-up.")) + `</p>
<p class="flash-message">` + html.EscapeString(adminT(loc, "admin.settings.registrations.moderation_recommandation", "Approval mode is recommended when moderators are not actively reviewing reports.")) + `</p>
<form class="simple_form" method="post" action="/admin/settings/registrations">
  <input type="hidden" name="_method" value="patch">
  <div class="fields-group"><label>` + html.EscapeString(adminSettingsLabel(loc, "registrations_mode", "Who can sign-up")) + ` <select name="form_admin_settings[registrations_mode]">` +
		option("open", html.EscapeString(adminT(loc, "admin.settings.registrations_mode.modes.open", "Anyone can sign up"))) +
		option("approved", html.EscapeString(adminT(loc, "admin.settings.registrations_mode.modes.approved", "Approval required for sign up"))) +
		option("none", html.EscapeString(adminT(loc, "admin.settings.registrations_mode.modes.none", "Nobody can sign up"))) + `</select></label>
  </div><input type="hidden" name="form_admin_settings[require_invite_text]" value="0">
  <div class="fields-group"><label><input type="checkbox" name="form_admin_settings[require_invite_text]" value="1"` + checkedInvite + `> ` + html.EscapeString(adminSettingsLabel(loc, "require_invite_text", "Require a reason to join")) + `</label></div>
  <input type="hidden" name="form_admin_settings[captcha_enabled]" value="0">
  <div class="fields-group"><label><input type="checkbox" name="form_admin_settings[captcha_enabled]" value="1"` + checkedCaptcha + `> ` + html.EscapeString(adminT(loc, "admin.settings.captcha_enabled.title", "Require new users to solve a CAPTCHA to confirm their account")) + `</label></div>
  <div class="fields-group"><label>` + html.EscapeString(adminSettingsLabel(loc, "closed_registrations_message", "Custom message when sign-ups are not available")) + ` <textarea name="form_admin_settings[closed_registrations_message]" rows="3">` + html.EscapeString(settings.ClosedRegistrationsMessage) + `</textarea></label></div>
  <div class="actions"><button type="submit">` + html.EscapeString(settingsT(loc, "generic.save_changes", "Save changes")) + `</button></div>
</form>`
	return adminSettingsPageHTML(title, "registrations", notice, errorText, body, loc, theme)
}
