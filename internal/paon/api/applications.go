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
	"unicode/utf8"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

const nativeOAuthRedirectURI = "urn:ietf:wg:oauth:2.0:oob"

func (s *Server) settingsApplicationsPage(c *echo.Context) error {
	account, _, user, err := s.requireFunctionalAccountForWeb(c)
	if err != nil {
		return webAuthResponseError(err)
	}
	locale := s.webLocale(c, user)
	theme := settingsWebTheme(decodeUserSettings(user.Settings.String))
	apps, err := s.userOAuthApplications(user.ID, c)
	if err != nil {
		return err
	}
	renderArgs, err := s.settingsRenderArgs(c.Request().URL.Path, locale, theme, user, account)
	if err != nil {
		return err
	}
	return c.HTML(http.StatusOK, applicationsIndexHTML(apps, c.QueryParam("notice"), c.QueryParam("error"), adminTrendsPageValue(c), renderArgs...))
}

func (s *Server) newSettingsApplication(c *echo.Context) error {
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
	return c.HTML(http.StatusOK, applicationFormHTML(settingsT(locale, "doorkeeper.applications.new.title", "New application"), "/settings/applications", http.MethodPost, oauthApplication{
		RedirectURI: nativeOAuthRedirectURI,
		Scopes:      "read write follow",
	}, "", "", renderArgs...))
}

func (s *Server) createSettingsApplication(c *echo.Context) error {
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
	if s.db == nil {
		return c.Redirect(http.StatusFound, "/settings/applications?error="+url.QueryEscape(settingsDatabaseUnavailableMessage(locale)))
	}
	if !railsNestedFormRootPresent(c, "doorkeeper_application") {
		return apiError(c, http.StatusBadRequest, "param is missing or the value is empty: doorkeeper_application")
	}
	app, err := applicationFromRequest(c)
	if err != nil {
		return c.HTML(http.StatusOK, applicationFormHTML(settingsT(locale, "doorkeeper.applications.new.title", "New application"), "/settings/applications", http.MethodPost, app, "", applicationErrorText(locale, err), renderArgs...))
	}
	now := time.Now().UTC()
	app.UID = randomHex(32)
	app.Secret = randomHex(32)
	app.CreatedAt = now
	app.UpdatedAt = now
	app.Confidential = true
	app.Superapp = false
	app.OwnerType = sql.NullString{String: "User", Valid: true}
	app.OwnerID = sql.NullInt64{Int64: user.ID, Valid: true}
	if err := s.db.Create(&app).Error; err != nil {
		return err
	}
	return c.Redirect(http.StatusFound, "/settings/applications?notice="+url.QueryEscape(settingsT(locale, "applications.created", "Application created")))
}

func (s *Server) showSettingsApplication(c *echo.Context) error {
	account, _, user, err := s.requireFunctionalAccountForWeb(c)
	if err != nil {
		return webAuthResponseError(err)
	}
	locale := s.webLocale(c, user)
	theme := settingsWebTheme(decodeUserSettings(user.Settings.String))
	app, err := s.findUserOAuthApplication(user.ID, c.Param("id"))
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	token, err := s.settingsAccessTokenForApplication(user, app)
	if err != nil {
		return err
	}
	renderArgs, err := s.settingsRenderArgs(c.Request().URL.Path, locale, theme, user, account)
	if err != nil {
		return err
	}
	return c.HTML(http.StatusOK, applicationShowHTML(*app, token.Token, c.QueryParam("notice"), c.QueryParam("error"), renderArgs...))
}

func (s *Server) updateSettingsApplication(c *echo.Context) error {
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
	app, err := s.findUserOAuthApplication(user.ID, c.Param("id"))
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	if !railsNestedFormRootPresent(c, "doorkeeper_application") {
		return apiError(c, http.StatusBadRequest, "param is missing or the value is empty: doorkeeper_application")
	}
	updated, err := applicationFromRequest(c)
	if err != nil {
		app.Name = updated.Name
		app.Website = updated.Website
		app.RedirectURI = updated.RedirectURI
		app.Scopes = updated.Scopes
		token := ""
		if existing, tokenErr := s.settingsAccessTokenForApplication(user, app); tokenErr == nil {
			token = existing.Token
		}
		return c.HTML(http.StatusOK, applicationShowHTML(*app, token, "", applicationErrorText(locale, err), renderArgs...))
	}
	scopesChanged := app.Scopes != updated.Scopes
	now := time.Now().UTC()
	if err := s.db.Model(&oauthApplication{}).
		Where("id = ? AND owner_type = ? AND owner_id = ?", app.ID, "User", user.ID).
		Updates(map[string]any{
			"name":         updated.Name,
			"website":      updated.Website,
			"redirect_uri": updated.RedirectURI,
			"scopes":       updated.Scopes,
			"updated_at":   now,
		}).Error; err != nil {
		return err
	}
	if scopesChanged {
		if err := s.revokeApplicationUserTokens(app.ID, user.ID); err != nil {
			return err
		}
		return c.Redirect(http.StatusFound, "/settings/applications/"+strconv.FormatInt(app.ID, 10)+"?notice="+url.QueryEscape(settingsT(locale, "applications.token_regenerated", "Token regenerated")))
	}
	return c.Redirect(http.StatusFound, "/settings/applications/"+strconv.FormatInt(app.ID, 10)+"?notice="+url.QueryEscape(settingsT(locale, "generic.changes_saved_msg", "Changes saved")))
}

func (s *Server) destroySettingsApplication(c *echo.Context) error {
	_, _, user, err := s.requireFunctionalAccountForWeb(c)
	if err != nil {
		return webAuthResponseError(err)
	}
	locale := s.webLocale(c, user)
	if strings.EqualFold(c.FormValue("_method"), "delete") || c.Request().Method == http.MethodDelete {
		app, err := s.findUserOAuthApplication(user.ID, c.Param("id"))
		if err != nil {
			return apiError(c, http.StatusNotFound, "Record not found")
		}
		var revokedTokenIDs []int64
		if err := s.db.Transaction(func(tx *gorm.DB) error {
			now := time.Now().UTC()
			if err := tx.Model(&models.OAuthAccessToken{}).
				Where("application_id = ?", app.ID).
				Pluck("id", &revokedTokenIDs).Error; err != nil {
				return err
			}
			if err := tx.Model(&models.OAuthAccessToken{}).
				Where("application_id = ? AND revoked_at IS NULL", app.ID).
				Update("revoked_at", now).Error; err != nil {
				return err
			}
			return tx.Delete(&oauthApplication{}, app.ID).Error
		}); err != nil {
			return err
		}
		s.publishAccessTokenKills(revokedTokenIDs)
	}
	return c.Redirect(http.StatusFound, "/settings/applications?notice="+url.QueryEscape(settingsT(locale, "applications.destroyed", "Application deleted")))
}

func (s *Server) postSettingsApplication(c *echo.Context) error {
	switch strings.ToLower(strings.TrimSpace(c.FormValue("_method"))) {
	case "put", "patch":
		return s.updateSettingsApplication(c)
	case "delete":
		return s.destroySettingsApplication(c)
	default:
		return s.updateSettingsApplication(c)
	}
}

func (s *Server) regenerateSettingsApplicationToken(c *echo.Context) error {
	_, _, user, err := s.requireFunctionalAccountForWeb(c)
	if err != nil {
		return webAuthResponseError(err)
	}
	locale := s.webLocale(c, user)
	app, err := s.findUserOAuthApplication(user.ID, c.Param("id"))
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	if err := s.revokeApplicationUserTokens(app.ID, user.ID); err != nil {
		return err
	}
	return c.Redirect(http.StatusFound, "/settings/applications/"+strconv.FormatInt(app.ID, 10)+"?notice="+url.QueryEscape(settingsT(locale, "applications.token_regenerated", "Token regenerated")))
}

func (s *Server) userOAuthApplications(userID int64, c *echo.Context) ([]oauthApplication, error) {
	if s.db == nil {
		return []oauthApplication{}, nil
	}
	var apps []oauthApplication
	err := s.db.Where("owner_type = ? AND owner_id = ?", "User", userID).
		Order("id DESC").
		Offset(adminRailsPageOffset(c)).
		Limit(adminRailsDefaultPageSize).
		Find(&apps).Error
	return apps, err
}

func (s *Server) findUserOAuthApplication(userID int64, id string) (*oauthApplication, error) {
	if s.db == nil {
		return nil, gorm.ErrRecordNotFound
	}
	var app oauthApplication
	err := s.db.Where("id = ? AND owner_type = ? AND owner_id = ?", id, "User", userID).First(&app).Error
	return &app, err
}

func (s *Server) settingsAccessTokenForApplication(user *models.User, app *oauthApplication) (*models.OAuthAccessToken, error) {
	if s.db == nil || user == nil || app == nil || !app.OwnerID.Valid || app.OwnerID.Int64 != user.ID || !strings.EqualFold(app.OwnerType.String, "User") {
		return nil, gorm.ErrRecordNotFound
	}
	var token models.OAuthAccessToken
	err := s.db.Where("application_id = ? AND resource_owner_id = ? AND revoked_at IS NULL", app.ID, user.ID).
		Order("created_at ASC, id ASC").
		First(&token).Error
	if err == nil {
		return &token, nil
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	now := time.Now().UTC()
	token = models.OAuthAccessToken{
		Token:           randomHex(32),
		CreatedAt:       now,
		Scopes:          models.NullSafeString(app.Scopes),
		ApplicationID:   sql.NullInt64{Int64: app.ID, Valid: true},
		ResourceOwnerID: sql.NullInt64{Int64: user.ID, Valid: true},
	}
	return &token, s.db.Create(&token).Error
}

func (s *Server) revokeApplicationUserTokens(applicationID int64, userID int64) error {
	if s.db == nil {
		return gorm.ErrInvalidDB
	}
	var revokedTokenIDs []int64
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.OAuthAccessToken{}).
			Where("application_id = ? AND resource_owner_id = ? AND revoked_at IS NULL", applicationID, userID).
			Pluck("id", &revokedTokenIDs).Error; err != nil {
			return err
		}
		if len(revokedTokenIDs) == 0 {
			return nil
		}
		if err := tx.Where("access_token_id IN ?", revokedTokenIDs).Delete(&models.WebPushSubscription{}).Error; err != nil {
			return err
		}
		if err := tx.Where("access_token_id IN ?", revokedTokenIDs).Delete(&models.SessionActivation{}).Error; err != nil {
			return err
		}
		return tx.Delete(&models.OAuthAccessToken{}, revokedTokenIDs).Error
	})
	if err != nil {
		return err
	}
	s.publishAccessTokenKills(revokedTokenIDs)
	return nil
}

func applicationFromRequest(c *echo.Context) (oauthApplication, error) {
	if err := c.Request().ParseForm(); err != nil {
		return oauthApplication{}, err
	}
	name := firstNonBlankRaw(c.FormValue("doorkeeper_application[name]"))
	redirectURI := firstNonBlankRaw(c.FormValue("doorkeeper_application[redirect_uri]"))
	scopes := normalizeApplicationScopes(applicationScopeFormValues(c), c.FormValue("doorkeeper_application[scopes]"))
	website := firstApplicationFormValueRaw(c, "doorkeeper_application[website]")
	app := oauthApplication{Name: name, RedirectURI: redirectURI, Scopes: scopes, Website: models.NullSafeString(website)}
	if strings.TrimSpace(name) == "" {
		return app, errApplicationNameRequired
	}
	if err := validateOAuthApplicationName(name); err != nil {
		return app, err
	}
	if strings.TrimSpace(redirectURI) == "" {
		return app, errApplicationRedirectURIRequired
	}
	if err := validateOAuthRedirectURI(redirectURI); err != nil {
		return app, err
	}
	if scopes == "" {
		scopes = "read"
		app.Scopes = scopes
	}
	if err := validateOAuthApplicationScopes(scopes); err != nil {
		return app, err
	}
	if err := validateOAuthApplicationWebsite(website); err != nil {
		return app, err
	}
	return app, nil
}

func railsNestedFormRootPresent(c *echo.Context, root string) bool {
	req := c.Request()
	_ = req.ParseForm()
	prefix := root + "["
	for key, values := range req.Form {
		if strings.HasPrefix(key, prefix) {
			return true
		}
		if key == root {
			for _, value := range values {
				if strings.TrimSpace(value) != "" {
					return true
				}
			}
		}
	}
	return false
}

func applicationFormValues(c *echo.Context, key string) []string {
	if c.Request().Form == nil {
		return nil
	}
	return c.Request().Form[key]
}

func applicationScopeFormValues(c *echo.Context) []string {
	if values := applicationFormValues(c, "doorkeeper_application[scopes][]"); len(values) > 0 {
		return values
	}
	return applicationFormValues(c, "doorkeeper_application[scopes]")
}

func firstApplicationFormValueRaw(c *echo.Context, keys ...string) string {
	if c.Request().Form == nil {
		return ""
	}
	for _, key := range keys {
		if values, ok := c.Request().Form[key]; ok && len(values) > 0 {
			return values[0]
		}
	}
	return ""
}

func normalizeApplicationScopes(values []string, fallback string) string {
	items := []string{}
	if len(values) > 0 {
		for _, value := range values {
			items = append(items, strings.Fields(value)...)
		}
	} else {
		items = strings.Fields(fallback)
	}
	seen := map[string]bool{}
	out := []string{}
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" || seen[item] {
			continue
		}
		seen[item] = true
		out = append(out, item)
	}
	return strings.Join(out, " ")
}

func applicationsIndexHTML(apps []oauthApplication, notice string, errorText string, page string, localeAndTheme ...string) string {
	loc := settingsLocaleArgOrEnglish(localeAndTheme...)
	var rows strings.Builder
	for _, app := range apps {
		rows.WriteString(`<tr><td><a href="/settings/applications/`)
		rows.WriteString(strconv.FormatInt(app.ID, 10))
		rows.WriteString(`">`)
		rows.WriteString(html.EscapeString(app.Name))
		rows.WriteString(`</a></td><th>`)
		rows.WriteString(html.EscapeString(app.Scopes))
		rows.WriteString(`</th><td><a class="table-action-link" data-method="delete" data-confirm="`)
		rows.WriteString(html.EscapeString(settingsT(loc, "doorkeeper.applications.confirmations.destroy", "Are you sure?")))
		rows.WriteString(`" href="/settings/applications/`)
		rows.WriteString(strconv.FormatInt(app.ID, 10))
		rows.WriteString(`"><i class="fa fa-times fa-fw"></i> `)
		rows.WriteString(html.EscapeString(settingsT(loc, "doorkeeper.applications.index.delete", "Delete")))
		rows.WriteString(`</a></td></tr>`)
	}
	newApplicationAction := `<a class="button" href="/settings/applications/new">` + html.EscapeString(settingsT(loc, "doorkeeper.applications.index.new", "New application")) + `</a>`
	body := ""
	if rows.Len() == 0 {
		body += `<div class="muted-hint center-text">` + html.EscapeString(settingsT(loc, "doorkeeper.applications.index.empty", "No applications")) + `</div>`
	} else {
		body += `<div class="table-wrapper"><table class="table">
      <thead><tr><th>` + html.EscapeString(settingsT(loc, "doorkeeper.applications.index.application", "Application")) + `</th><th>` + html.EscapeString(settingsT(loc, "doorkeeper.applications.index.scopes", "Scopes")) + `</th><th></th></tr></thead>
      <tbody>` + rows.String() + `</tbody>
    </table></div>`
	}
	body += applicationsPaginationHTML(page, len(apps) == adminRailsDefaultPageSize, loc)
	title := settingsT(loc, "doorkeeper.applications.index.title", "Applications")
	if len(localeAndTheme) > 2 && strings.TrimSpace(localeAndTheme[2]) != "" {
		return settingsPageShellWithHeading(title, settingsNavigationArg(localeAndTheme, loc), settingsInlineFlashHTML(notice, errorText)+body, loc, settingsThemeArg(localeAndTheme...), "", `<div class="content__heading__actions">`+newApplicationAction+`</div>`)
	}
	return authPageHTML(title, notice, errorText, `<p>`+newApplicationAction+`</p>`+body, localeAndTheme...)
}

func applicationsPaginationHTML(page string, hasNext bool, locale ...string) string {
	loc := settingsLocaleArgOrEnglish(locale...)
	pageNum, err := strconv.Atoi(strings.TrimSpace(page))
	if err != nil || pageNum < 1 {
		pageNum = 1
	}
	var links []string
	if pageNum > 1 {
		params := url.Values{"page": []string{strconv.Itoa(pageNum - 1)}}
		links = append(links, `<a href="/settings/applications?`+html.EscapeString(params.Encode())+`">`+html.EscapeString(settingsT(loc, "pagination.previous", "Previous"))+`</a>`)
	}
	if hasNext {
		params := url.Values{"page": []string{strconv.Itoa(pageNum + 1)}}
		links = append(links, `<a href="/settings/applications?`+html.EscapeString(params.Encode())+`">`+html.EscapeString(settingsT(loc, "pagination.next", "Next"))+`</a>`)
	}
	if len(links) == 0 {
		return ""
	}
	return `<nav class="pagination">` + strings.Join(links, " ") + `</nav>`
}

func applicationFormHTML(title string, action string, method string, app oauthApplication, notice string, errorText string, localeAndTheme ...string) string {
	loc := settingsLocaleArgOrEnglish(localeAndTheme...)
	return authPageHTML(title, notice, errorText, applicationFormBody(action, method, app, loc), localeAndTheme...)
}

func applicationFormBody(action string, method string, app oauthApplication, locale ...string) string {
	loc := settingsLocaleArgOrEnglish(locale...)
	methodOverride := ""
	if method != http.MethodPost {
		methodOverride = `<input type="hidden" name="_method" value="` + html.EscapeString(strings.ToLower(method)) + `">`
	}
	formClass := "new_doorkeeper_application"
	formID := "new_doorkeeper_application"
	buttonLabel := settingsT(loc, "doorkeeper.applications.buttons.submit", "Submit")
	if method != http.MethodPost {
		formClass = "edit_doorkeeper_application"
		formID = "edit_doorkeeper_application_" + strconv.FormatInt(app.ID, 10)
		buttonLabel = settingsT(loc, "generic.save_changes", "Save changes")
	}
	required := filterRequiredMarker(loc)
	return `
    <form class="simple_form ` + formClass + `" id="` + formID + `" novalidate="novalidate" method="post" action="` + html.EscapeString(action) + `">
      ` + methodOverride + `
	      <div class="fields-group"><div class="input with_label string required doorkeeper_application_name"><div class="label_input"><label class="string required" for="doorkeeper_application_name">` + html.EscapeString(settingsT(loc, "activerecord.attributes.doorkeeper/application.name", "Name")) + required + `</label><div class="label_input__wrapper"><input type="text" id="doorkeeper_application_name" class="string required" name="doorkeeper_application[name]" value="` + html.EscapeString(app.Name) + `"></div></div></div></div>
	      <div class="fields-group"><div class="input with_label string optional doorkeeper_application_website"><div class="label_input"><label class="string optional" for="doorkeeper_application_website">` + html.EscapeString(settingsT(loc, "activerecord.attributes.doorkeeper/application.website", "Website")) + `</label><div class="label_input__wrapper"><input type="text" id="doorkeeper_application_website" class="string optional" name="doorkeeper_application[website]" value="` + html.EscapeString(string(app.Website)) + `"></div></div></div></div>
      <div class="fields-group"><div class="input with_block_label text optional doorkeeper_application_redirect_uri field_with_hint"><label class="text optional" for="doorkeeper_application_redirect_uri">` + html.EscapeString(settingsT(loc, "activerecord.attributes.doorkeeper/application.redirect_uri", "Redirect URI")) + `</label><span class="hint">` + html.EscapeString(settingsT(loc, "doorkeeper.applications.help.redirect_uri", "Use one line per URI")) + `</span><div class="label_input"><textarea id="doorkeeper_application_redirect_uri" class="text optional" name="doorkeeper_application[redirect_uri]">` + html.EscapeString(app.RedirectURI) + `</textarea></div></div><p class="hint">` + settingsTVars(loc, "doorkeeper.applications.help.native_redirect_uri", "Use %{native_redirect_uri} for local tests", map[string]string{"native_redirect_uri": `<code>` + html.EscapeString(nativeOAuthRedirectURI) + `</code>`}) + `</p></div>
      <div class="field-group"><div class="input with_block_label"><label>` + html.EscapeString(settingsT(loc, "activerecord.attributes.doorkeeper/application.scopes", "Scopes")) + `</label><span class="hint">` + html.EscapeString(settingsT(loc, "simple_form.hints.defaults.scopes", "Scopes grant access to parts of your account.")) + `</span></div>` + scopeCheckboxes(app.Scopes, loc) + `</div>
      <div class="actions"><button name="button" type="submit" class="btn">` + html.EscapeString(buttonLabel) + `</button></div>
    </form>`
}

func applicationShowHTML(app oauthApplication, token string, notice string, errorText string, localeAndTheme ...string) string {
	loc := settingsLocaleArgOrEnglish(localeAndTheme...)
	return authPageHTML(settingsTVars(loc, "doorkeeper.applications.show.title", "Application: %{name}", map[string]string{"name": app.Name}), notice, errorText, `
    <p class="hint">`+html.EscapeString(settingsT(loc, "applications.warning", "Keep your application credentials private."))+`</p>
    <div class="table-wrapper"><table class="table">
      <tbody>
        <tr><th>`+html.EscapeString(settingsT(loc, "doorkeeper.applications.show.application_id", "Application ID"))+`</th><td><code>`+html.EscapeString(app.UID)+`</code></td></tr>
        <tr><th>`+html.EscapeString(settingsT(loc, "doorkeeper.applications.show.secret", "Secret"))+`</th><td><code>`+html.EscapeString(app.Secret)+`</code></td></tr>
        <tr><th rowspan="2">`+html.EscapeString(settingsT(loc, "applications.your_token", "Your token"))+`</th><td><code>`+html.EscapeString(token)+`</code></td></tr>
	        <tr><td><a class="table-action-link" data-method="post" href="/settings/applications/`+strconv.FormatInt(app.ID, 10)+`/regenerate"><i class="fa fa-refresh fa-fw"></i> `+html.EscapeString(settingsT(loc, "applications.regenerate_token", "Regenerate token"))+`</a></td></tr>
      </tbody>
    </table></div>
    <hr>
	    `+applicationFormBody("/settings/applications/"+strconv.FormatInt(app.ID, 10), http.MethodPut, app, loc), localeAndTheme...)
}

func scopeCheckboxes(scopes string, locale ...string) string {
	loc := settingsLocaleArgOrEnglish(locale...)
	selected := map[string]bool{}
	for _, scope := range strings.Fields(scopes) {
		selected[scope] = true
	}
	var out strings.Builder
	for _, group := range oauthApplicationScopeGroups() {
		out.WriteString(`<div class="input with_block_label check_boxes optional doorkeeper_application_scopes"><div class="label_input"><ul><input type="hidden" name="doorkeeper_application[scopes][]" value="" autocomplete="off">`)
		for _, scope := range group {
			id := "doorkeeper_application_scopes_" + strings.ReplaceAll(scope, ":", "")
			checked := ""
			if selected[scope] {
				checked = ` selected="selected" checked="checked"`
			}
			out.WriteString(`<li class="checkbox"><label for="` + html.EscapeString(id) + `"><input class="check_boxes optional" type="checkbox" value="` + html.EscapeString(scope) + `" name="doorkeeper_application[scopes][]" id="` + html.EscapeString(id) + `"` + checked + `><samp`)
			if className := applicationScopeClass(scope); className != "" {
				out.WriteString(` class="` + html.EscapeString(className) + `"`)
			}
			out.WriteString(`>` + html.EscapeString(scope) + `</samp><span class="hint">` + html.EscapeString(settingsT(loc, "doorkeeper.scopes."+scope, scope)) + `</span></label></li>`)
		}
		out.WriteString(`</ul></div></div>`)
	}
	return out.String()
}

func applicationScopeClass(scope string) string {
	switch scope {
	case "read", "write", "follow":
		return "scope-danger"
	default:
		return ""
	}
}

func oauthApplicationScopeGroups() [][]string {
	groups := make([][]string, 0, 6)
	positions := map[string]int{}
	for _, scope := range oauthConfiguredScopeOrder {
		prefix := strings.SplitN(scope, ":", 2)[0]
		position, ok := positions[prefix]
		if !ok {
			position = len(groups)
			positions[prefix] = position
			groups = append(groups, nil)
		}
		groups[position] = append(groups[position], scope)
	}
	return groups
}

type applicationInputError string

func (e applicationInputError) Error() string { return string(e) }

const (
	errApplicationNameRequired        applicationInputError = "Application name is required"
	errApplicationNameTooLong         applicationInputError = "Application name is too long"
	errApplicationRedirectURIRequired applicationInputError = "Redirect URI is required"
	errApplicationRedirectURIInvalid  applicationInputError = "Redirect URI is invalid"
	errApplicationRedirectURITooLong  applicationInputError = "Redirect URI is too long"
	errApplicationScopesInvalid       applicationInputError = "Scopes are invalid"
	errApplicationWebsiteInvalid      applicationInputError = "Website is invalid"
	errApplicationWebsiteTooLong      applicationInputError = "Website is too long"
)

func applicationErrorText(locale string, err error) string {
	switch err {
	case errApplicationNameRequired:
		return settingsT(locale, "doorkeeper.errors.messages.invalid_request", err.Error())
	case errApplicationNameTooLong:
		return settingsT(locale, "doorkeeper.errors.messages.invalid_request", err.Error())
	case errApplicationRedirectURIRequired:
		return settingsT(locale, "doorkeeper.errors.messages.invalid_redirect_uri", err.Error())
	case errApplicationRedirectURIInvalid:
		return settingsT(locale, "doorkeeper.errors.messages.invalid_redirect_uri", err.Error())
	case errApplicationRedirectURITooLong:
		return settingsT(locale, "doorkeeper.errors.messages.invalid_redirect_uri", err.Error())
	case errApplicationScopesInvalid:
		return settingsT(locale, "doorkeeper.errors.messages.invalid_scope", err.Error())
	case errApplicationWebsiteInvalid:
		return settingsT(locale, "doorkeeper.errors.messages.invalid_request", err.Error())
	case errApplicationWebsiteTooLong:
		return settingsT(locale, "doorkeeper.errors.messages.invalid_request", err.Error())
	default:
		return err.Error()
	}
}

func validateOAuthApplicationName(name string) error {
	if utf8.RuneCountInString(name) > 60 {
		return errApplicationNameTooLong
	}
	return nil
}

func validateOAuthRedirectURI(values string) error {
	if len(values) > 2000 {
		return errApplicationRedirectURITooLong
	}
	for _, item := range strings.Fields(values) {
		parsed, err := url.Parse(item)
		if err != nil {
			return errApplicationRedirectURIInvalid
		}
		switch strings.ToLower(parsed.Scheme) {
		case "data", "vbscript", "javascript":
			return errApplicationRedirectURIInvalid
		}
	}
	return nil
}

func validateOAuthApplicationScopes(scopes string) error {
	for _, scope := range strings.Fields(scopes) {
		if _, ok := oauthConfiguredScopes[scope]; !ok {
			return errApplicationScopesInvalid
		}
	}
	return nil
}

func validateOAuthApplicationWebsite(website string) error {
	if strings.TrimSpace(website) == "" {
		return nil
	}
	if len(website) > 2000 {
		return errApplicationWebsiteTooLong
	}
	parsed, err := url.Parse(website)
	if err != nil || parsed.Host == "" {
		return errApplicationWebsiteInvalid
	}
	switch parsed.Scheme {
	case "http", "https":
		return nil
	default:
		return errApplicationWebsiteInvalid
	}
}

var oauthConfiguredScopeOrder = []string{
	"read",
	"profile",
	"write",
	"write:accounts",
	"write:blocks",
	"write:bookmarks",
	"write:conversations",
	"write:favourites",
	"write:filters",
	"write:follows",
	"write:lists",
	"write:media",
	"write:mutes",
	"write:notifications",
	"write:reports",
	"write:statuses",
	"read:accounts",
	"read:blocks",
	"read:bookmarks",
	"read:favourites",
	"read:filters",
	"read:follows",
	"read:lists",
	"read:mutes",
	"read:notifications",
	"read:search",
	"read:statuses",
	"follow",
	"push",
	"admin:read",
	"admin:read:accounts",
	"admin:read:reports",
	"admin:read:domain_allows",
	"admin:read:domain_blocks",
	"admin:read:ip_blocks",
	"admin:read:email_domain_blocks",
	"admin:read:canonical_email_blocks",
	"admin:write",
	"admin:write:accounts",
	"admin:write:reports",
	"admin:write:domain_allows",
	"admin:write:domain_blocks",
	"admin:write:ip_blocks",
	"admin:write:email_domain_blocks",
	"admin:write:canonical_email_blocks",
}

var oauthConfiguredScopes = func() map[string]struct{} {
	configured := make(map[string]struct{}, len(oauthConfiguredScopeOrder))
	for _, scope := range oauthConfiguredScopeOrder {
		configured[scope] = struct{}{}
	}
	return configured
}()
