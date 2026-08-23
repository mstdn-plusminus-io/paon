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
	"github.com/mstdn-plusminus-io/paon/internal/paon/serializer"
	"gorm.io/gorm"
)

type adminBrandingSettings struct {
	SiteTitle            string
	SiteContactUsername  string
	SiteContactEmail     string
	SiteShortDescription string
	ThumbnailUploadID    int64
	ThumbnailURL         string
	FaviconUploadID      int64
	FaviconURL           string
	AppIconUploadID      int64
	AppIconURL           string
	LandingPage          string
}

type adminExistingUsernameEntry struct {
	Raw      string
	Username string
	Domain   string
}

type existingUsernameValidationError struct {
	Multiple  bool
	Usernames []string
}

func (e existingUsernameValidationError) Error() string {
	if e.Multiple {
		return "could not find " + strings.Join(e.Usernames, ", ")
	}
	return "could not find a local user with that username"
}

func (s *Server) adminSettingsBrandingPage(c *echo.Context) error {
	user, _, handled, err := s.requireAdminSettingsWebUser(c)
	if handled || err != nil {
		return err
	}
	locale := s.webLocale(c, user)
	theme := settingsWebTheme(decodeUserSettings(user.Settings.String))
	return c.HTML(http.StatusOK, adminSettingsBrandingHTML(s.adminBrandingSettings(), c.QueryParam("notice"), c.QueryParam("error"), locale, theme))
}

func (s *Server) updateAdminSettingsBranding(c *echo.Context) error {
	user, _, handled, err := s.requireAdminSettingsWebUser(c)
	if handled || err != nil {
		return err
	}
	locale := s.webLocale(c, user)
	theme := settingsWebTheme(decodeUserSettings(user.Settings.String))
	if c.Request().Method == http.MethodPost && !methodOverrideIs(c, "put", "patch") {
		return c.Redirect(http.StatusFound, "/admin/settings/branding")
	}
	settings, err := parseAdminBrandingSettings(c)
	if err != nil {
		if adminSettingsParamsMissing(err) {
			return apiError(c, http.StatusBadRequest, "Malformed request")
		}
		return c.HTML(http.StatusOK, adminSettingsBrandingHTML(s.adminBrandingSettings(), "", adminSettingsInvalidMessage(locale, "branding"), locale, theme))
	}
	if err := validateAdminBrandingSettings(settings); err != nil {
		return c.HTML(http.StatusOK, adminSettingsBrandingHTML(s.withAdminBrandingSiteUpload(settings), "", adminSettingErrorText(locale, err), locale, theme))
	}
	if s.db == nil {
		return c.HTML(http.StatusOK, adminSettingsBrandingHTML(settings, "", adminSettingsDatabaseUnavailableMessage(locale), locale, theme))
	}
	if err := s.validateExistingAdminSettingUsernames(settings.SiteContactUsername, false); err != nil {
		return c.HTML(http.StatusOK, adminSettingsBrandingHTML(s.withAdminBrandingSiteUpload(settings), "", adminSettingErrorText(locale, err), locale, theme))
	}
	for _, name := range []string{"thumbnail", "favicon", "app_icon"} {
		if err := validateAdminSiteUploadFromForm(c, name); err != nil {
			return c.HTML(http.StatusOK, adminSettingsBrandingHTML(s.withAdminBrandingSiteUpload(settings), "", adminSettingErrorText(locale, err), locale, theme))
		}
	}
	if err := s.updateAdminBrandingSettings(settings); err != nil {
		return err
	}
	for _, name := range []string{"thumbnail", "favicon", "app_icon"} {
		if err := s.storeAdminSiteUploadFromForm(c, name); err != nil {
			return c.HTML(http.StatusOK, adminSettingsBrandingHTML(s.withAdminBrandingSiteUpload(settings), "", adminSettingErrorText(locale, err), locale, theme))
		}
	}
	return c.Redirect(http.StatusFound, "/admin/settings/branding?notice="+url.QueryEscape(adminSettingsSavedMessage(locale, "branding")))
}

func (s *Server) requireAdminSettingsWebUser(c *echo.Context) (*models.User, string, bool, error) {
	user, token, handled, err := s.requireFunctionalWebUser(c)
	if handled || err != nil {
		return nil, "", handled, err
	}
	if !s.userCan(user, rolePermissionManageSettings) {
		locale := s.webLocale(c, user)
		return nil, "", true, c.HTML(http.StatusForbidden, authPageHTML(adminT(locale, "admin.settings.title", "Admin settings"), "", adminT(locale, "admin.settings.not_permitted", "You are not allowed to manage settings."), "", locale))
	}
	return user, token, false, nil
}

func (s *Server) adminBrandingSettings() adminBrandingSettings {
	return s.withAdminBrandingSiteUpload(adminBrandingSettings{
		SiteTitle:            s.settingStringValue("site_title", s.cfg.Title),
		SiteContactUsername:  s.settingStringValue("site_contact_username", ""),
		SiteContactEmail:     s.settingStringValue("site_contact_email", ""),
		SiteShortDescription: s.settingStringValue("site_short_description", ""),
		LandingPage:          normalizeLandingPage(s.settingStringValue("landing_page", "trends")),
	})
}

func (s *Server) withAdminBrandingSiteUpload(settings adminBrandingSettings) adminBrandingSettings {
	if upload, _ := s.instanceSiteUpload("thumbnail"); upload != nil {
		settings.ThumbnailUploadID = upload.ID
		settings.ThumbnailURL = serializer.SiteUploadFileURL(s.cfg, *upload, "@1x")
	}
	if upload, _ := s.instanceSiteUpload("favicon"); upload != nil {
		settings.FaviconUploadID = upload.ID
		settings.FaviconURL = serializer.SiteUploadFileURL(s.cfg, *upload, "48")
	}
	if upload, _ := s.instanceSiteUpload("app_icon"); upload != nil {
		settings.AppIconUploadID = upload.ID
		settings.AppIconURL = serializer.SiteUploadFileURL(s.cfg, *upload, "48")
	}
	return settings
}

func parseAdminBrandingSettings(c *echo.Context) (adminBrandingSettings, error) {
	req := c.Request()
	if err := parseAdminSettingsFormRoot(req, 8<<20); err != nil {
		return adminBrandingSettings{}, err
	}
	return adminBrandingSettings{
		SiteTitle:            lastFormValue(req.Form, "form_admin_settings[site_title]"),
		SiteContactUsername:  lastFormValue(req.Form, "form_admin_settings[site_contact_username]"),
		SiteContactEmail:     lastFormValue(req.Form, "form_admin_settings[site_contact_email]"),
		SiteShortDescription: lastFormValue(req.Form, "form_admin_settings[site_short_description]"),
		LandingPage:          lastFormValue(req.Form, "form_admin_settings[landing_page]"),
	}, nil
}

var errAdminSettingsParamsMissing = errors.New("admin settings root parameter is missing")

func adminSettingsParamsMissing(err error) bool {
	return errors.Is(err, errAdminSettingsParamsMissing)
}

func parseAdminSettingsFormRoot(req *http.Request, maxMemory int64) error {
	rootPresent, err := requestHasNestedFormOrFilePrefix(req, "form_admin_settings", maxMemory)
	if err != nil {
		return err
	}
	if !rootPresent {
		return errAdminSettingsParamsMissing
	}
	return nil
}

func validateAdminBrandingSettings(settings adminBrandingSettings) error {
	if strings.TrimSpace(settings.SiteTitle) == "" {
		return errAdminSetting("Site title can't be blank")
	}
	if strings.TrimSpace(settings.SiteContactUsername) == "" {
		return errAdminSetting("Contact username can't be blank")
	}
	if strings.TrimSpace(settings.SiteContactEmail) == "" {
		return errAdminSetting("Contact e-mail can't be blank")
	}
	if len([]rune(settings.SiteShortDescription)) > 200 {
		return errAdminSetting("Short description is too long")
	}
	if settings.LandingPage != "" && normalizeLandingPage(settings.LandingPage) != strings.TrimSpace(settings.LandingPage) {
		return errAdminSetting("Landing page is invalid")
	}
	return nil
}

func (s *Server) validateExistingAdminSettingUsernames(value string, multiple bool) error {
	entries := s.adminExistingUsernameEntries(value)
	if len(entries) == 0 {
		return nil
	}
	missing := make([]string, 0)
	for _, entry := range entries {
		var count int64
		query := s.db.Model(&models.Account{}).Where("lower(username) = ?", strings.ToLower(entry.Username))
		if entry.Domain == "" {
			query = query.Where("domain IS NULL")
		} else {
			query = query.Where("lower(domain) = ?", strings.ToLower(entry.Domain))
		}
		if err := query.Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			missing = append(missing, entry.Raw)
		}
	}
	if multiple {
		if len(missing) > 0 {
			return existingUsernameValidationError{Multiple: true, Usernames: missing}
		}
		return nil
	}
	if len(missing) > 0 || len(entries) > 1 {
		return existingUsernameValidationError{}
	}
	return nil
}

func (s *Server) adminExistingUsernameEntries(value string) []adminExistingUsernameEntry {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	entries := make([]adminExistingUsernameEntry, 0)
	for _, raw := range strings.Split(value, ",") {
		username, domain, _ := strings.Cut(strings.TrimPrefix(strings.TrimSpace(raw), "@"), "@")
		if strings.EqualFold(domain, s.cfg.LocalDomain) {
			domain = ""
		}
		if strings.TrimSpace(username) == "" {
			continue
		}
		entries = append(entries, adminExistingUsernameEntry{Raw: raw, Username: username, Domain: domain})
	}
	return entries
}

func (s *Server) updateAdminBrandingSettings(settings adminBrandingSettings) error {
	values := map[string]string{
		"site_title":             settings.SiteTitle,
		"site_contact_username":  settings.SiteContactUsername,
		"site_contact_email":     settings.SiteContactEmail,
		"site_short_description": settings.SiteShortDescription,
		"landing_page":           normalizeLandingPage(settings.LandingPage),
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

func upsertGlobalSetting(tx *gorm.DB, name string, value string) error {
	now := time.Now().UTC()
	var existing models.Setting
	err := tx.Where("var = ?", name).First(&existing).Error
	if err == nil {
		return tx.Model(&models.Setting{}).
			Where("id = ?", existing.ID).
			Updates(map[string]any{
				"value":      sql.NullString{String: value, Valid: true},
				"updated_at": sql.NullTime{Time: now, Valid: true},
			}).Error
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	setting := models.Setting{
		Var:       name,
		Value:     sql.NullString{String: value, Valid: true},
		CreatedAt: sql.NullTime{Time: now, Valid: true},
		UpdatedAt: sql.NullTime{Time: now, Valid: true},
	}
	return tx.Create(&setting).Error
}

type adminSettingError string

func (e adminSettingError) Error() string { return string(e) }

func errAdminSetting(message string) error { return adminSettingError(message) }

func adminSettingsSectionTitle(locale string, section string) string {
	fallbacks := map[string]string{
		"about":             "About",
		"appearance":        "Appearance",
		"branding":          "Branding",
		"content_retention": "Content retention",
		"discovery":         "Discovery",
		"registrations":     "Registrations",
	}
	return adminT(locale, "admin.settings."+section+".title", firstNonEmpty(fallbacks[section], section))
}

func adminSettingsSavedMessage(locale string, section string) string {
	return adminTVars(locale, "admin.settings.messages.saved", "%{section} settings saved", map[string]string{"section": adminSettingsSectionTitle(locale, section)})
}

func adminSettingsInvalidMessage(locale string, section string) string {
	return adminTVars(locale, "admin.settings.messages.invalid", "%{section} settings are invalid", map[string]string{"section": adminSettingsSectionTitle(locale, section)})
}

func adminSettingsDatabaseUnavailableMessage(locale string) string {
	return adminT(locale, "admin.settings.messages.database_unavailable", "DATABASE_URL is not set")
}

func adminSettingErrorText(locale string, err error) string {
	if err == nil {
		return ""
	}
	if existing, ok := err.(existingUsernameValidationError); ok {
		if existing.Multiple {
			return settingsTVars(locale, "existing_username_validator.not_found_multiple", "could not find %{usernames}", map[string]string{"usernames": strings.Join(existing.Usernames, ", ")})
		}
		return settingsT(locale, "existing_username_validator.not_found", "could not find a local user with that username")
	}
	keys := map[string]string{
		"Backups retention period must be an integer":       "backups_retention_period_integer",
		"Contact e-mail can't be blank":                     "contact_email_blank",
		"Contact username can't be blank":                   "contact_username_blank",
		"Content cache retention period must be an integer": "content_cache_retention_period_integer",
		"Custom CSS is too long":                            "custom_css_too_long",
		"Domain block rationale visibility is invalid":      "domain_block_rationale_visibility_invalid",
		"Domain block visibility is invalid":                "domain_block_visibility_invalid",
		"Media cache retention period must be an integer":   "media_cache_retention_period_integer",
		"Minimum age must be an integer":                    "minimum_age_integer",
		"Registration mode is invalid":                      "registration_mode_invalid",
		"Short description is too long":                     "short_description_too_long",
		"Site title can't be blank":                         "site_title_blank",
		"Site upload must be an image":                      "site_upload_image",
		"Site upload must be a readable image":              "site_upload_readable_image",
		"Status page URL is invalid":                        "status_page_url_invalid",
		"Theme is invalid":                                  "theme_invalid",
	}
	text := err.Error()
	if key := keys[text]; key != "" {
		return adminT(locale, "admin.settings.errors."+key, text)
	}
	return text
}

func adminSettingsNavHTML(active string, locale ...string) string {
	loc := settingsLocaleArgOrEnglish(locale...)
	items := []struct {
		key   string
		icon  string
		label string
		href  string
	}{
		{key: "branding", icon: "pencil", label: adminT(loc, "admin.settings.branding.title", "Branding"), href: "/admin/settings/branding"},
		{key: "about", icon: "file-text", label: adminT(loc, "admin.settings.about.title", "About"), href: "/admin/settings/about"},
		{key: "registrations", icon: "users", label: adminT(loc, "admin.settings.registrations.title", "Registrations"), href: "/admin/settings/registrations"},
		{key: "discovery", icon: "search", label: adminT(loc, "admin.settings.discovery.title", "Discovery"), href: "/admin/settings/discovery"},
		{key: "content_retention", icon: "history", label: adminT(loc, "admin.settings.content_retention.title", "Content retention"), href: "/admin/settings/content_retention"},
		{key: "appearance", icon: "desktop", label: adminT(loc, "admin.settings.appearance.title", "Appearance"), href: "/admin/settings/appearance"},
	}
	var b strings.Builder
	b.WriteString(`<nav class="content__heading__tabs"><div>`)
	for _, item := range items {
		class := ""
		if item.key == active {
			class = ` class="selected simple-navigation-active-leaf"`
		}
		b.WriteString(`<a id="` + item.key + `"` + class + ` href="` + item.href + `"><i class="fa fa-` + item.icon + ` fa-fw"></i>` + html.EscapeString(item.label) + `</a>`)
	}
	b.WriteString(`</div></nav>`)
	return b.String()
}

func adminSettingsPageHTML(title string, active string, notice string, errorText string, body string, localeAndTheme ...string) string {
	locale := settingsLocaleArgOrEnglish(localeAndTheme...)
	return authPageHTML(title, notice, errorText, adminSettingsNavHTML(active, locale)+body, localeAndTheme...)
}

func adminSettingsLabel(locale string, key string, fallback string) string {
	return settingsT(locale, "simple_form.labels.form_admin_settings."+key, fallback)
}

func adminSettingsHint(locale string, key string, fallback string) string {
	return settingsT(locale, "simple_form.hints.form_admin_settings."+key, fallback)
}

func adminSettingsBrandingHTML(settings adminBrandingSettings, notice string, errorText string, localeAndTheme ...string) string {
	loc := settingsLocaleArgOrEnglish(localeAndTheme...)
	theme := settingsThemeArg(localeAndTheme...)
	title := adminT(loc, "admin.settings.branding.title", "Branding")
	landingPage := normalizeLandingPage(settings.LandingPage)
	var landingOptions strings.Builder
	for _, page := range []string{"trends", "about", "local_feed"} {
		selected := ""
		if page == landingPage {
			selected = " selected"
		}
		label := map[string]string{
			"trends":     adminT(loc, "admin.settings.landing_page.values.trends", "Trending content"),
			"about":      adminT(loc, "admin.settings.landing_page.values.about", "About this server"),
			"local_feed": adminT(loc, "admin.settings.landing_page.values.local_feed", "Local feed"),
		}[page]
		landingOptions.WriteString(`<option value="` + page + `"` + selected + `>` + html.EscapeString(label) + `</option>`)
	}
	body := `<form class="simple_form new_form_admin_settings" id="new_form_admin_settings" method="post" action="/admin/settings/branding" enctype="multipart/form-data" novalidate="novalidate">
  <input type="hidden" name="_method" value="patch">
  <p class="lead">` + html.EscapeString(adminT(loc, "admin.settings.branding.preamble", "Manage the public identity shown on your instance profile and about pages.")) + `</p>
  <div class="fields-group">` + adminSettingsWithLabelInput(loc, "site_title", "Server name", "string", "text", settings.SiteTitle) + `</div>
  <div class="fields-row"><div class="fields-row__column fields-row__column-6 fields-group">` + adminSettingsWithLabelInput(loc, "site_contact_username", "Contact username", "string", "text", settings.SiteContactUsername) + `</div>
  <div class="fields-row__column fields-row__column-6 fields-group">` + adminSettingsWithLabelInput(loc, "site_contact_email", "Contact e-mail", "email", "email", settings.SiteContactEmail) + `</div></div>
  <div class="fields-group">` + adminSettingsBlockTextarea(loc, "site_short_description", "Server description", settings.SiteShortDescription, 2, 200) + `</div>
  <div class="fields-row"><div class="fields-row__column fields-row__column-6 fields-group">` + adminSettingsBlockFileInput(loc, "thumbnail", "Server thumbnail") + `</div>
  <div class="fields-row__column fields-row__column-6 fields-group">` + adminSiteUploadPreviewDeleteHTML(settings.ThumbnailUploadID, settings.ThumbnailURL, loc) + `</div></div>
  <div class="fields-row"><div class="fields-row__column fields-row__column-6 fields-group">` + adminSettingsBlockFileInputWithAccept(loc, "favicon", "Favicon", "image/jpeg,image/png,image/gif,image/webp") + `</div>
  <div class="fields-row__column fields-row__column-6 fields-group">` + adminSiteUploadPreviewDeleteHTML(settings.FaviconUploadID, settings.FaviconURL, loc) + `</div></div>
  <div class="fields-row"><div class="fields-row__column fields-row__column-6 fields-group">` + adminSettingsBlockFileInputWithAccept(loc, "app_icon", "App icon", "image/jpeg,image/png,image/gif,image/webp") + `</div>
  <div class="fields-row__column fields-row__column-6 fields-group">` + adminSiteUploadPreviewDeleteHTML(settings.AppIconUploadID, settings.AppIconURL, loc) + `</div></div>
  <div class="fields-group"><label>` + html.EscapeString(adminSettingsLabel(loc, "landing_page", "Landing page for new visitors")) + ` <select name="form_admin_settings[landing_page]">` + landingOptions.String() + `</select></label></div>
  <div class="actions"><button name="button" type="submit" class="btn">` + html.EscapeString(settingsT(loc, "generic.save_changes", "Save changes")) + `</button></div>
</form>`
	return adminSettingsPageHTML(title, "branding", notice, errorText, body, loc, theme)
}

func adminSettingsWithLabelInput(locale string, key string, fallback string, fieldClass string, inputType string, value string) string {
	id := "form_admin_settings_" + key
	label := html.EscapeString(adminSettingsLabel(locale, key, fallback))
	hint := html.EscapeString(adminSettingsHint(locale, key, ""))
	inputClass := "string " + fieldClass + " optional"
	if fieldClass == "string" {
		inputClass = "string optional"
	}
	return `<div class="input with_label ` + fieldClass + ` optional ` + id + ` field_with_hint"><div class="label_input"><label class="` + fieldClass + ` optional" for="` + id + `">` + label + `</label><div class="label_input__wrapper"><input class="` + inputClass + `" type="` + inputType + `" value="` + html.EscapeString(value) + `" name="form_admin_settings[` + key + `]" id="` + id + `"></div></div><span class="hint">` + hint + `</span></div>`
}

func adminSettingsBlockTextarea(locale string, key string, fallback string, value string, rows int, maxlength int) string {
	id := "form_admin_settings_" + key
	return `<div class="input with_block_label text optional ` + id + ` field_with_hint"><label class="text optional" for="` + id + `">` + html.EscapeString(adminSettingsLabel(locale, key, fallback)) + `</label><span class="hint">` + html.EscapeString(adminSettingsHint(locale, key, "")) + `</span><div class="label_input"><textarea rows="` + strconv.Itoa(rows) + `" maxlength="` + strconv.Itoa(maxlength) + `" class="text optional" name="form_admin_settings[` + key + `]" id="` + id + `">` + html.EscapeString(value) + `</textarea></div></div>`
}

func adminSettingsBlockFileInput(locale string, key string, fallback string) string {
	return adminSettingsBlockFileInputWithAccept(locale, key, fallback, "")
}

func adminSettingsBlockFileInputWithAccept(locale string, key string, fallback string, accept string) string {
	id := "form_admin_settings_" + key
	acceptAttribute := ""
	if strings.TrimSpace(accept) != "" {
		acceptAttribute = ` accept="` + html.EscapeString(accept) + `"`
	}
	return `<div class="input with_block_label file optional ` + id + ` field_with_hint"><label class="file optional" for="` + id + `">` + html.EscapeString(adminSettingsLabel(locale, key, fallback)) + `</label><span class="hint">` + html.EscapeString(adminSettingsHint(locale, key, "")) + `</span><div class="label_input"><input class="file optional" type="file" name="form_admin_settings[` + key + `]" id="` + id + `"` + acceptAttribute + `></div></div>`
}

func adminSiteUploadPreviewDeleteHTML(id int64, imageURL string, locale string) string {
	if id == 0 || imageURL == "" {
		return ""
	}
	return `<img src="` + html.EscapeString(imageURL) + `" class="fields-group__thumbnail" alt="">
    <form method="post" action="/admin/site_uploads/` + strconv.FormatInt(id, 10) + `" class="inline-form">
      <input type="hidden" name="_method" value="delete">
      <button type="submit" class="link-button link-button--destructive">` + html.EscapeString(adminT(locale, "admin.site_uploads.delete", "Delete")) + `</button>
    </form>`
}
