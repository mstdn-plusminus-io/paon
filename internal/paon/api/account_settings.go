package api

import (
	"database/sql"
	"html"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"golang.org/x/crypto/bcrypt"
)

func (s *Server) authEditPage(c *echo.Context) error {
	user, currentToken, err := s.currentUserIncludingDisabled(c)
	if err != nil {
		return c.Redirect(http.StatusFound, "/auth/sign_in?redirect_to="+url.QueryEscape(c.Request().URL.RequestURI()))
	}
	sessions, err := s.userSessionActivations(user.ID)
	if err != nil {
		return err
	}
	account, err := s.currentAccountForSettings(user.AccountID)
	if err != nil {
		return err
	}
	strikes, err := s.authEditStrikes(user.AccountID, time.Now().UTC())
	if err != nil {
		return err
	}
	currentTokenID, err := s.currentAccessTokenID(currentToken)
	if err != nil {
		return err
	}
	locale := s.webLocale(c, user)
	theme := settingsWebTheme(decodeUserSettings(user.Settings.String))
	renderArgs, err := s.settingsRenderArgs(c.Request().URL.Path, locale, theme, user, account)
	if err != nil {
		return err
	}
	return c.HTML(http.StatusOK, authEditHTMLWithOptions(user.Email, c.QueryParam("notice"), c.QueryParam("error"), authEditHTMLOptions{
		Locale:                locale,
		Theme:                 theme,
		Sessions:              sessions,
		CurrentTokenID:        currentTokenID,
		OmniAuthOnly:          s.cfg.OmniAuthOnly,
		SSOAccountSettingsURL: s.cfg.SSOAccountSettingsURL,
		SeamlessExternalLogin: s.cfg.PAMEnabled || s.cfg.LDAPEnabled,
		EncryptedPassword:     user.EncryptedPassword,
		User:                  user,
		Account:               account,
		Strikes:               strikes,
		RenderArgs:            renderArgs,
	}))
}

func (s *Server) updateUserRegistration(c *echo.Context) error {
	user, currentToken, err := s.currentUserIncludingDisabled(c)
	if err != nil {
		return c.Redirect(http.StatusFound, "/auth/sign_in?redirect_to="+url.QueryEscape("/auth/edit"))
	}
	locale := s.webLocale(c, user)
	account, err := s.currentAccountForSettings(user.AccountID)
	if err != nil {
		return err
	}
	if account.SuspendedAt.Valid {
		return apiError(c, http.StatusForbidden, "This account is suspended")
	}
	currentPassword := c.FormValue("user[current_password]")
	if !validBCryptPassword(user.EncryptedPassword, currentPassword) {
		return c.Redirect(http.StatusFound, "/auth/edit?error="+url.QueryEscape(settingsT(locale, "auth.invalid_password", "Current password is invalid")))
	}

	updates := map[string]any{"updated_at": time.Now().UTC()}
	delivery := confirmationDelivery{}
	changedEmail := ""
	passwordChanged := false
	if email := strings.ToLower(strings.TrimSpace(c.FormValue("user[email]"))); email != "" && email != strings.ToLower(user.Email) {
		if err := s.ensureEmailDomainAllowed(c.Request().Context(), email, c.RealIP(), s.shouldRunEmailDomainProviderBlockForUser(*user), false); err != nil {
			return c.Redirect(http.StatusFound, "/auth/edit?error="+url.QueryEscape(apiErrorMessage(err)))
		}
		emailUpdates, emailDelivery := s.confirmationUpdateForEmailChange(*user, email, time.Now().UTC())
		for key, value := range emailUpdates {
			updates[key] = value
		}
		delivery = emailDelivery
		changedEmail = email
	}
	password := c.FormValue("user[password]")
	if strings.TrimSpace(password) != "" {
		if len(password) < 8 || len(password) > 72 {
			return c.Redirect(http.StatusFound, "/auth/edit?error="+url.QueryEscape(settingsT(locale, "users.invalid_password", "Password is invalid")))
		}
		if confirmation := c.FormValue("user[password_confirmation]"); confirmation != "" && confirmation != password {
			return c.Redirect(http.StatusFound, "/auth/edit?error="+url.QueryEscape(settingsT(locale, "users.password_confirmation_mismatch", "Password confirmation does not match")))
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			return err
		}
		updates["encrypted_password"] = string(hash)
		passwordChanged = true
	}
	if len(updates) > 1 {
		if err := s.db.Model(&models.User{}).Where("id = ?", user.ID).Updates(updates).Error; err != nil {
			if isUniqueConstraintError(err) {
				return c.Redirect(http.StatusFound, "/auth/edit?error="+url.QueryEscape(settingsT(locale, "users.email_taken", "Email has already been taken")))
			}
			return err
		}
	}
	if changedEmail != "" {
		if err := s.sendEmailChangedMail(*user, changedEmail); err != nil {
			return mailDeliveryError("email changed", err)
		}
	}
	if passwordChanged {
		if err := s.clearOtherSessionsForRequest(user.ID, currentToken, c); err != nil {
			return err
		}
		if err := s.sendPasswordChangedMail(*user); err != nil {
			return mailDeliveryError("password changed", err)
		}
	}
	if delivery.Token != "" {
		if err := s.sendConfirmationDelivery(delivery); err != nil {
			return mailDeliveryError("confirmation", err)
		}
	}
	return c.Redirect(http.StatusFound, "/auth/edit?notice="+url.QueryEscape(settingsT(locale, "generic.changes_saved_msg", "Account settings saved")))
}

func (s *Server) authSetupPage(c *echo.Context) error {
	user, _, err := s.currentUser(c)
	if err != nil {
		return c.Redirect(http.StatusFound, "/auth/sign_in?redirect_to="+url.QueryEscape(c.Request().URL.RequestURI()))
	}
	if user.ConfirmedAt.Valid && user.Approved {
		return c.Redirect(http.StatusFound, "/")
	}
	locale := s.webLocale(c, user)
	theme := settingsWebTheme(decodeUserSettings(user.Settings.String))
	return c.HTML(http.StatusOK, authSetupHTML(user.Email, c.QueryParam("notice"), c.QueryParam("error"), s.packAssetPath("sign_up.js"), locale, theme))
}

func (s *Server) updateAuthSetup(c *echo.Context) error {
	user, _, err := s.currentUser(c)
	if err != nil {
		return c.Redirect(http.StatusFound, "/auth/sign_in?redirect_to="+url.QueryEscape("/auth/setup"))
	}
	locale := s.webLocale(c, user)
	if user.ConfirmedAt.Valid && user.Approved {
		return c.Redirect(http.StatusFound, "/")
	}
	if !railsNestedFormRootPresent(c, "user") {
		return apiError(c, http.StatusBadRequest, "Malformed request")
	}
	email := strings.ToLower(strings.TrimSpace(c.FormValue("user[email]")))
	if email == "" || !strings.Contains(email, "@") {
		return s.renderAuthSetupShow(c, user, email, settingsT(locale, "users.invalid_email", "Email is invalid"))
	}
	if err := s.ensureEmailDomainAllowed(c.Request().Context(), email, c.RealIP(), s.shouldRunEmailDomainProviderBlockForUser(*user), false); err != nil {
		return s.renderAuthSetupShow(c, user, email, apiErrorMessage(err))
	}
	now := time.Now().UTC()
	updates, delivery := s.confirmationUpdateForEmailChange(*user, email, now)
	updates["updated_at"] = now
	if err := s.db.Model(&models.User{}).Where("id = ?", user.ID).Updates(updates).Error; err != nil {
		if isUniqueConstraintError(err) {
			return s.renderAuthSetupShow(c, user, email, settingsT(locale, "users.email_taken", "Email has already been taken"))
		}
		return err
	}
	if delivery.Token != "" {
		if err := s.sendConfirmationDelivery(delivery); err != nil {
			return mailDeliveryError("confirmation", err)
		}
	}
	return c.Redirect(http.StatusFound, "/auth/setup?notice="+url.QueryEscape(authConfirmationInstructionsQueuedMessage(locale)))
}

func (s *Server) renderAuthSetupShow(c *echo.Context, user *models.User, email string, errorText string) error {
	locale := s.webLocale(c, user)
	theme := ""
	if user != nil {
		theme = settingsWebTheme(decodeUserSettings(user.Settings.String))
	}
	return c.HTML(http.StatusOK, authSetupHTML(email, "", errorText, s.packAssetPath("sign_up.js"), locale, theme))
}

func (s *Server) confirmationUpdateForEmailChange(user models.User, email string, now time.Time) (map[string]any, confirmationDelivery) {
	email = strings.ToLower(strings.TrimSpace(email))
	token := randomHex(16)
	updates := map[string]any{
		"confirmation_token":   deviseTokenForStorage(token, deviseConfirmationTokenColumn, s.cfg.SecretKeyBase),
		"confirmation_sent_at": now,
	}
	if user.ConfirmedAt.Valid {
		updates["unconfirmed_email"] = email
		user.UnconfirmedEmail = sql.NullString{String: email, Valid: true}
		return updates, confirmationDelivery{Email: email, Token: token, Reconfirmation: true, User: user, HasUser: true}
	} else {
		updates["email"] = email
		updates["unconfirmed_email"] = nil
		updates["confirmed_at"] = nil
		user.Email = email
		user.UnconfirmedEmail = sql.NullString{}
		user.ConfirmedAt = sql.NullTime{}
	}
	return updates, confirmationDelivery{Email: email, Token: token, User: user, HasUser: true}
}

func authEditHTML(email string, notice string, errorText string, localeAndTheme ...string) string {
	return authEditHTMLWithOptions(email, notice, errorText, authEditHTMLOptions{
		Locale:            settingsLocaleArgOrEnglish(localeAndTheme...),
		Theme:             settingsThemeArg(localeAndTheme...),
		EncryptedPassword: "present",
	})
}

type authEditHTMLOptions struct {
	Locale                string
	Theme                 string
	Sessions              []models.SessionActivation
	CurrentTokenID        int64
	OmniAuthOnly          bool
	SSOAccountSettingsURL string
	SeamlessExternalLogin bool
	EncryptedPassword     string
	User                  *models.User
	Account               *models.Account
	Strikes               []models.AccountWarning
	RenderArgs            []string
}

func authEditHTMLWithOptions(email string, notice string, errorText string, options authEditHTMLOptions) string {
	loc := firstNonEmpty(options.Locale, "en")
	theme := options.Theme
	suspended := options.Account != nil && options.Account.SuspendedAt.Valid
	disabledAttr := ""
	if suspended {
		disabledAttr = " disabled"
	}
	body := authEditStatusHTML(options.User, options.Account, options.Strikes, loc) + `<h3>` + html.EscapeString(settingsT(loc, "auth.security", "Security")) + `</h3>
    <form class="simple_form auth_edit" id="edit_user" method="post" action="/auth">`
	if (!options.SeamlessExternalLogin || strings.TrimSpace(options.EncryptedPassword) != "") && !options.OmniAuthOnly {
		body += `
      <input type="hidden" name="_method" value="put">
      <div class="fields-row">
		<div class="fields-row__column fields-group fields-row__column-6">` + authSettingsInputHTMLWithHint("email", settingsT(loc, "simple_form.labels.defaults.email", "Email"), settingsT(loc, "simple_form.hints.defaults.email", "A confirmation e-mail will be sent"), "user[email]", "email", email, `required`+disabledAttr, loc) + `</div>
		<div class="fields-row__column fields-group fields-row__column-6">` + authSettingsInputHTML("current_password", settingsT(loc, "simple_form.labels.defaults.current_password", "Current password"), "user[current_password]", "password", "", `autocomplete="current-password" required`+disabledAttr, loc) + `</div>
      </div>
      <div class="fields-row">
		<div class="fields-row__column fields-group fields-row__column-6">` + authSettingsInputHTMLWithHint("password", settingsT(loc, "simple_form.labels.defaults.new_password", "New password"), settingsT(loc, "simple_form.hints.defaults.password", "Use at least 8 characters."), "user[password]", "password", "", `autocomplete="new-password" minlength="8" maxlength="72"`+disabledAttr, loc) + `</div>
		<div class="fields-row__column fields-group fields-row__column-6">` + authSettingsInputHTML("password_confirmation", settingsT(loc, "simple_form.labels.defaults.confirm_new_password", "Confirm new password"), "user[password_confirmation]", "password", "", `autocomplete="new-password"`+disabledAttr, loc) + `</div>
      </div>
      <div class="actions"><button class="button" type="submit"` + disabledAttr + `>` + html.EscapeString(settingsT(loc, "generic.save_changes", "Save changes")) + `</button></div>
    `
	} else if options.OmniAuthOnly && strings.TrimSpace(options.SSOAccountSettingsURL) != "" {
		body += `<a href="` + html.EscapeString(options.SSOAccountSettingsURL) + `">` + html.EscapeString(settingsT(loc, "users.go_to_sso_account_settings", "Go to your identity provider's account settings")) + `</a>`
	} else {
		body += `<p class="hint">` + html.EscapeString(settingsT(loc, "users.seamless_external_login", "Your account is managed by an external identity provider.")) + `</p>`
	}
	body += `</form>
	<hr class="spacer">` + authEditSessionsHTMLWithOptions(options.Sessions, options.CurrentTokenID, loc, !suspended, settingsApplicationNameArg(options.RenderArgs))
	if !suspended {
		body += `
    <hr class="spacer">
    <h3>` + html.EscapeString(settingsT(loc, "auth.migrate_account", "Move to a different account")) + `</h3>
    <p class="muted-hint">` + settingsTVars(loc, "auth.migrate_account_html", "You can <a href=\"%{path}\">move to a different account</a>.", map[string]string{"path": "/settings/migration"}) + `</p>
    <hr class="spacer">
    <h3>` + html.EscapeString(settingsT(loc, "migrations.incoming_migrations", "Moving from a different account")) + `</h3>
    <p class="muted-hint">` + settingsTVars(loc, "migrations.incoming_migrations_html", "You can <a href=\"%{path}\">create an account alias</a>.", map[string]string{"path": "/settings/aliases"}) + `</p>
    <hr class="spacer">
    <h3>` + html.EscapeString(settingsT(loc, "auth.delete_account", "Delete account")) + `</h3>
    <p class="muted-hint">` + settingsTVars(loc, "auth.delete_account_html", "You can <a href=\"%{path}\">delete your account</a>.", map[string]string{"path": "/settings/delete"}) + `</p>`
	}
	renderArgs := options.RenderArgs
	if len(renderArgs) == 0 {
		renderArgs = []string{loc, theme}
	}
	return accountSecurityPageHTML(settingsT(loc, "settings.account_settings", "Account settings"), "account", notice, errorText, body, renderArgs...)
}

func (s *Server) authEditStrikes(accountID int64, now time.Time) ([]models.AccountWarning, error) {
	if s.db == nil || accountID == 0 {
		return nil, nil
	}
	var strikes []models.AccountWarning
	err := s.db.Where("target_account_id = ? AND account_warnings.created_at >= ?", accountID, now.AddDate(0, -3, 0)).
		Order("id DESC").
		Find(&strikes).Error
	return strikes, err
}

func authEditStatusHTML(user *models.User, account *models.Account, strikes []models.AccountWarning, locale string) string {
	if user == nil || account == nil {
		return ""
	}
	var body strings.Builder
	if !user.ConfirmedAt.Valid {
		body.WriteString(`<div class="flash-message warning">` + html.EscapeString(settingsT(locale, "auth.status.confirming", "Waiting for email confirmation.")) + ` <a href="/auth/confirmation/new">` + html.EscapeString(settingsT(locale, "auth.didnt_get_confirmation", "Didn't receive confirmation instructions?")) + `</a></div>`)
	} else if !user.Approved {
		body.WriteString(`<div class="flash-message warning">` + html.EscapeString(settingsT(locale, "auth.status.pending", "Your account is still pending review.")) + `</div>`)
	} else if account.MovedToAccountID.Valid {
		acct := ""
		if account.MovedToAccount != nil {
			acct = account.MovedToAccount.Acct()
		}
		body.WriteString(`<div class="flash-message warning">` + html.EscapeString(settingsTVars(locale, "auth.status.redirecting_to", "Your account is redirecting to %{acct}.", map[string]string{"acct": acct})) + ` <a href="/settings/migration">` + html.EscapeString(settingsT(locale, "migrations.cancel", "Cancel")) + `</a></div>`)
	}
	body.WriteString(`<h3>` + html.EscapeString(settingsT(locale, "auth.status.account_status", "Account status")) + `</h3><p class="hint">`)
	switch {
	case account.SuspendedAt.Valid:
		body.WriteString(`<span class="negative-hint">` + html.EscapeString(settingsT(locale, "user_mailer.warning.explanation.suspend", "Your account has been suspended.")) + `</span>`)
	case user.Disabled:
		body.WriteString(`<span class="negative-hint">` + html.EscapeString(settingsT(locale, "user_mailer.warning.explanation.disable", "Your account has been disabled.")) + `</span>`)
	case account.SilencedAt.Valid:
		body.WriteString(`<span class="warning-hint">` + html.EscapeString(settingsT(locale, "user_mailer.warning.explanation.silence", "Your account has been limited.")) + `</span>`)
	default:
		body.WriteString(`<span class="positive-hint">` + html.EscapeString(settingsT(locale, "auth.status.functional", "Your account is fully operational.")) + `</span>`)
	}
	body.WriteString(`</p>`)
	for _, strike := range strikes {
		id := strconv.FormatInt(strike.ID, 10)
		body.WriteString(`<a class="log-entry" href="/disputes/strikes/` + id + `"><div class="log-entry__content"><div class="log-entry__title">` + html.EscapeString(disputeStrikeTitle(strike, locale)) + `</div><div class="log-entry__timestamp"><time datetime="` + html.EscapeString(strike.CreatedAt.UTC().Format(time.RFC3339)) + `">` + html.EscapeString(strike.CreatedAt.UTC().Format(time.RFC3339)) + `</time>`)
		if strike.OverruledAt.Valid {
			body.WriteString(` · <span class="positive-hint">` + html.EscapeString(settingsT(locale, "disputes.strikes.your_appeal_approved", "Your appeal has been approved")) + `</span>`)
		}
		body.WriteString(`</div></div></a>`)
	}
	if len(strikes) > 0 {
		body.WriteString(`<hr class="spacer"><p class="muted-hint"><a href="/disputes/strikes">` + html.EscapeString(settingsT(locale, "auth.status.view_strikes", "View account strikes")) + `</a></p>`)
	}
	body.WriteString(`<hr class="spacer">`)
	return body.String()
}

func authSetupHTML(email string, notice string, errorText string, signUpScriptPath string, localeAndTheme ...string) string {
	loc := settingsLocaleArgOrEnglish(localeAndTheme...)
	theme := settingsThemeArg(localeAndTheme...)
	script := ""
	if strings.TrimSpace(signUpScriptPath) != "" {
		script = `<script src="` + html.EscapeString(signUpScriptPath) + `" crossorigin="anonymous"></script>`
	}
	body := `<form class="simple_form edit_user" id="edit_user" novalidate="novalidate" method="post" action="/auth/setup">` + registrationProgressHTML("confirm", false, loc) + `
      <h1 class="title">` + html.EscapeString(settingsT(loc, "auth.setup.title", settingsT(loc, "auth.confirmations.confirm_email", "Confirm your email"))) + `</h1>
      <p class="lead">` + settingsTVars(loc, "auth.setup.email_settings_hint_html", "We sent a confirmation link to <strong>%{email}</strong>.", map[string]string{"email": html.EscapeString(email)}) + `</p>
      <p class="lead"><strong>` + html.EscapeString(settingsT(loc, "auth.setup.link_not_received", "Did not receive the link?")) + `</strong></p>
      <p class="lead">` + settingsT(loc, "auth.setup.email_below_hint_html", "You can update your e-mail address below and request another confirmation link.") + `</p>
	  <div class="fields-group">` + authSettingsInputHTML("email", settingsT(loc, "simple_form.labels.defaults.email", "Email"), "user[email]", "email", email, `autocomplete="off" required`, loc) + `</div>
      <div class="actions"><button class="button timer-button" type="submit" disabled>` + html.EscapeString(settingsT(loc, "auth.resend_confirmation", settingsT(loc, "auth.didnt_get_confirmation", "Resend confirmation"))) + `</button></div>
    </form><div class="form-footer"><ul class="no-list"><li><a href="/auth/edit">` + html.EscapeString(settingsT(loc, "settings.account_settings", "Account settings")) + `</a></li><li><a data-method="delete" href="/auth/sign_out">` + html.EscapeString(settingsT(loc, "auth.logout", "Log out")) + `</a></li></ul></div>` + script
	return authShellHTML(settingsT(loc, "auth.setup.title", settingsT(loc, "auth.confirmations.confirm_email", "Confirm your email")), notice, errorText, body, loc, theme)
}

func authSettingsInputHTML(id string, label string, name string, inputType string, value string, attrs string, locale ...string) string {
	return authSettingsInputHTMLWithHint(id, label, "", name, inputType, value, attrs, locale...)
}

func authSettingsInputHTMLWithHint(id string, label string, hint string, name string, inputType string, value string, attrs string, locale ...string) string {
	loc := settingsLocaleArgOrEnglish(locale...)
	requirement := "optional"
	if settingsHTMLAttributePresent(attrs, "required") {
		requirement = "required"
	}
	fieldType := inputType
	inputClasses := inputType + " " + requirement
	if inputType == "email" {
		inputClasses = "string email " + requirement
	}
	groupClasses := "input with_label " + fieldType + " " + requirement + " user_" + id
	if strings.TrimSpace(hint) != "" {
		groupClasses += " field_with_hint"
	}
	out := `<div class="` + html.EscapeString(groupClasses) + `"><div class="label_input"><label class="` + html.EscapeString(fieldType+" "+requirement) + `" for="user_` + html.EscapeString(id) + `">` + html.EscapeString(label)
	if requirement == "required" {
		out += ` <abbr title="` + html.EscapeString(settingsT(loc, "simple_form.required.text", "required")) + `">*</abbr>`
	}
	out += `</label><input class="` + html.EscapeString(inputClasses) + `" id="user_` + html.EscapeString(id) + `" name="` + html.EscapeString(name) + `" type="` + html.EscapeString(inputType) + `" value="` + html.EscapeString(value) + `" aria-label="` + html.EscapeString(label) + `" ` + attrs + `></div>`
	if strings.TrimSpace(hint) != "" {
		out += `<span class="hint">` + html.EscapeString(hint) + `</span>`
	}
	return out + `</div>`

}

func settingsHTMLAttributePresent(attrs string, name string) bool {
	for _, field := range strings.Fields(attrs) {
		if field == name || strings.HasPrefix(field, name+"=") {
			return true
		}
	}
	return false
}

func authPageHTML(title string, notice string, errorText string, body string, localeAndTheme ...string) string {
	locale := settingsLocaleArg(localeAndTheme...)
	theme := settingsThemeArg(localeAndTheme...)
	if len(localeAndTheme) > 2 && strings.TrimSpace(localeAndTheme[2]) != "" {
		return settingsPageShell(title, settingsNavigationArg(localeAndTheme, locale), settingsInlineFlashHTML(notice, errorText)+body, locale, theme)
	}
	flashes := settingsFlashHTML(notice, errorText)
	return `<!DOCTYPE html>
<html lang="` + html.EscapeString(locale) + `">
  <head>
    ` + buildAppHead(title, theme) + `
  </head>
  <body class="app-body">
    <main role="main">
      <h1>` + html.EscapeString(title) + `</h1>
	  ` + flashes + body + `
    </main>
    <div class="logo-resources" tabindex="-1" aria-hidden="true"></div>
  </body>
</html>`
}
