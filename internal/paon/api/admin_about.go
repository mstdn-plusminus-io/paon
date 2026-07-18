package api

import (
	"html"
	"net/http"
	"net/url"
	"strings"

	"github.com/labstack/echo/v5"
	"gorm.io/gorm"
)

type adminAboutSettings struct {
	SiteExtendedDescription string
	ShowDomainBlocks        string
	ShowDomainBlocksReason  string
	StatusPageURL           string
	SiteTerms               string
}

func (s *Server) adminSettingsAboutPage(c *echo.Context) error {
	user, _, handled, err := s.requireAdminSettingsWebUser(c)
	if handled || err != nil {
		return err
	}
	locale := s.webLocale(c, user)
	theme := settingsWebTheme(decodeUserSettings(user.Settings.String))
	return c.HTML(http.StatusOK, adminSettingsAboutHTML(s.adminAboutSettings(), c.QueryParam("notice"), c.QueryParam("error"), locale, theme))
}

func (s *Server) updateAdminSettingsAbout(c *echo.Context) error {
	user, _, handled, err := s.requireAdminSettingsWebUser(c)
	if handled || err != nil {
		return err
	}
	locale := s.webLocale(c, user)
	theme := settingsWebTheme(decodeUserSettings(user.Settings.String))
	if c.Request().Method == http.MethodPost && !methodOverrideIs(c, "put", "patch") {
		return c.Redirect(http.StatusFound, "/admin/settings/about")
	}
	settings, err := parseAdminAboutSettings(c)
	if err != nil {
		if adminSettingsParamsMissing(err) {
			return apiError(c, http.StatusBadRequest, "Malformed request")
		}
		return c.HTML(http.StatusOK, adminSettingsAboutHTML(s.adminAboutSettings(), "", adminSettingsInvalidMessage(locale, "about"), locale, theme))
	}
	if err := validateAdminAboutSettings(settings); err != nil {
		return c.HTML(http.StatusOK, adminSettingsAboutHTML(settings, "", adminSettingErrorText(locale, err), locale, theme))
	}
	if s.db == nil {
		return c.HTML(http.StatusOK, adminSettingsAboutHTML(settings, "", adminSettingsDatabaseUnavailableMessage(locale), locale, theme))
	}
	if err := s.updateAdminAboutSettings(settings); err != nil {
		return err
	}
	return c.Redirect(http.StatusFound, "/admin/settings/about?notice="+url.QueryEscape(adminSettingsSavedMessage(locale, "about")))
}

func (s *Server) adminAboutSettings() adminAboutSettings {
	return adminAboutSettings{
		SiteExtendedDescription: s.settingStringValue("site_extended_description", ""),
		ShowDomainBlocks:        domainBlockVisibilitySetting(s.settingValue("show_domain_blocks", "disabled")),
		ShowDomainBlocksReason:  domainBlockVisibilitySetting(s.settingValue("show_domain_blocks_rationale", "disabled")),
		StatusPageURL:           s.settingStringValue("status_page_url", ""),
		SiteTerms:               s.settingStringValue("site_terms", ""),
	}
}

func parseAdminAboutSettings(c *echo.Context) (adminAboutSettings, error) {
	req := c.Request()
	if err := parseAdminSettingsFormRoot(req, 8<<20); err != nil {
		return adminAboutSettings{}, err
	}
	return adminAboutSettings{
		SiteExtendedDescription: lastFormValue(req.Form, "form_admin_settings[site_extended_description]"),
		ShowDomainBlocks:        lastFormValue(req.Form, "form_admin_settings[show_domain_blocks]"),
		ShowDomainBlocksReason:  lastFormValue(req.Form, "form_admin_settings[show_domain_blocks_rationale]"),
		StatusPageURL:           lastFormValue(req.Form, "form_admin_settings[status_page_url]"),
		SiteTerms:               lastFormValue(req.Form, "form_admin_settings[site_terms]"),
	}, nil
}

func validateAdminAboutSettings(settings adminAboutSettings) error {
	if !domainBlockVisibilityAllowed(settings.ShowDomainBlocks) {
		return errAdminSetting("Domain block visibility is invalid")
	}
	if !domainBlockVisibilityAllowed(settings.ShowDomainBlocksReason) {
		return errAdminSetting("Domain block rationale visibility is invalid")
	}
	if strings.TrimSpace(settings.StatusPageURL) != "" {
		parsed, err := url.Parse(settings.StatusPageURL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return errAdminSetting("Status page URL is invalid")
		}
	}
	return nil
}

func (s *Server) updateAdminAboutSettings(settings adminAboutSettings) error {
	values := map[string]string{
		"site_extended_description":    settings.SiteExtendedDescription,
		"show_domain_blocks":           settings.ShowDomainBlocks,
		"show_domain_blocks_rationale": settings.ShowDomainBlocksReason,
		"status_page_url":              settings.StatusPageURL,
		"site_terms":                   settings.SiteTerms,
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

func domainBlockVisibilitySetting(value string) string {
	value = normalizeSettingScalar(value)
	if domainBlockVisibilityAllowed(value) {
		return value
	}
	return "disabled"
}

func domainBlockVisibilityAllowed(value string) bool {
	switch value {
	case "disabled", "users", "all":
		return true
	default:
		return false
	}
}

func adminSettingsAboutHTML(settings adminAboutSettings, notice string, errorText string, localeAndTheme ...string) string {
	loc := settingsLocaleArgOrEnglish(localeAndTheme...)
	theme := settingsThemeArg(localeAndTheme...)
	option := func(current string, value string, label string) string {
		selected := ""
		if current == value {
			selected = ` selected`
		}
		return `<option value="` + value + `"` + selected + `>` + html.EscapeString(label) + `</option>`
	}
	visibilitySelect := func(name string, current string) string {
		return `<select name="form_admin_settings[` + name + `]">` +
			option(current, "disabled", adminT(loc, "admin.settings.domain_blocks.disabled", "Disabled")) +
			option(current, "users", adminT(loc, "admin.settings.domain_blocks.users", "Authenticated users")) +
			option(current, "all", adminT(loc, "admin.settings.domain_blocks.all", "Everyone")) +
			`</select>`
	}
	title := adminT(loc, "admin.settings.about.title", "About")
	body := `<p class="lead">` + html.EscapeString(adminT(loc, "admin.settings.about.preamble", "Edit long-form public instance information, moderation transparency, and policy text.")) + `</p>
<form class="simple_form" method="post" action="/admin/settings/about">
  <input type="hidden" name="_method" value="patch">
  <div class="fields-group"><label>` + html.EscapeString(adminSettingsLabel(loc, "site_extended_description", "Extended description")) + ` <textarea name="form_admin_settings[site_extended_description]" rows="8">` + html.EscapeString(settings.SiteExtendedDescription) + `</textarea></label></div>
  <div class="fields-group"><label>` + html.EscapeString(adminSettingsLabel(loc, "show_domain_blocks", "Show domain blocks")) + ` ` + visibilitySelect("show_domain_blocks", settings.ShowDomainBlocks) + `</label></div>
  <div class="fields-group"><label>` + html.EscapeString(adminSettingsLabel(loc, "show_domain_blocks_rationale", "Show why domains were blocked")) + ` ` + visibilitySelect("show_domain_blocks_rationale", settings.ShowDomainBlocksReason) + `</label></div>
  <div class="fields-group"><label>` + html.EscapeString(adminSettingsLabel(loc, "status_page_url", "Status page URL")) + ` <input name="form_admin_settings[status_page_url]" value="` + html.EscapeString(settings.StatusPageURL) + `"></label></div>
  <div class="fields-group"><label>` + html.EscapeString(adminSettingsLabel(loc, "site_terms", "Privacy Policy")) + ` <textarea name="form_admin_settings[site_terms]" rows="8">` + html.EscapeString(settings.SiteTerms) + `</textarea></label></div>
  <div class="actions"><button type="submit">` + html.EscapeString(settingsT(loc, "generic.save_changes", "Save changes")) + `</button></div>
</form>`
	return adminSettingsPageHTML(title, "about", notice, errorText, body, loc, theme)
}
