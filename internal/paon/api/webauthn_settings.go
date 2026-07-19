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
)

func (s *Server) settingsSecurityKeysPage(c *echo.Context) error {
	user, _, err := s.currentUserIncludingDisabled(c)
	if err != nil {
		return redirectToSignIn(c)
	}
	credentials, err := s.webauthnCredentialsForUser(user.ID)
	if err != nil {
		return err
	}
	locale := s.webLocale(c, user)
	if err := settingsSecurityKeysOTPEnabledGuard(c, user, locale); err != nil {
		return err
	}
	if err := settingsSecurityKeysWebauthnEnabledGuard(c, credentials, locale); err != nil {
		return err
	}
	theme := settingsWebTheme(decodeUserSettings(user.Settings.String))
	renderArgs, err := s.settingsRenderArgs(c.Request().URL.Path, locale, theme, user, nil)
	if err != nil {
		return err
	}
	return c.HTML(http.StatusOK, securityKeysHTML(credentials, c.QueryParam("notice"), c.QueryParam("error"), renderArgs...))
}

func (s *Server) newSettingsSecurityKey(c *echo.Context) error {
	user, _, err := s.currentUserIncludingDisabled(c)
	if err != nil {
		return redirectToSignIn(c)
	}
	locale := s.webLocale(c, user)
	if err := settingsSecurityKeysOTPEnabledGuard(c, user, locale); err != nil {
		return err
	}
	theme := settingsWebTheme(decodeUserSettings(user.Settings.String))
	renderArgs, err := s.settingsRenderArgs(c.Request().URL.Path, locale, theme, user, nil)
	if err != nil {
		return err
	}
	return c.HTML(http.StatusOK, newSecurityKeyHTML(s.packAssetPath("two_factor_authentication.js"), renderArgs...))
}

func (s *Server) settingsSecurityKeyOptions(c *echo.Context) error {
	user, _, err := s.currentUserIncludingDisabled(c)
	if err != nil {
		return redirectToSignIn(c)
	}
	locale := s.webLocale(c, user)
	if err := settingsSecurityKeysOTPEnabledGuard(c, user, locale); err != nil {
		return err
	}
	account, err := s.userAccount(user.AccountID)
	if err != nil {
		return err
	}
	credentials, err := s.webauthnCredentialsForUser(user.ID)
	if err != nil {
		return err
	}
	webauthnID, err := s.ensureWebauthnID(user)
	if err != nil {
		return err
	}
	challenge := webauthnChallenge()
	if err := s.setBrowserWebAuthnChallenge(c, user.ID, "registration", challenge); err != nil {
		return err
	}
	expireCookie(c, webauthnCreateChallengeCookie, s.cfg.ForceSSL)
	return c.JSON(http.StatusOK, webauthnCreateOptions(webauthnRPID(s.cfg.WebDomain), account.Username, webauthnID, challenge, credentials))
}

func (s *Server) createSettingsSecurityKey(c *echo.Context) error {
	user, _, err := s.currentUserIncludingDisabled(c)
	if err != nil {
		return redirectToSignIn(c)
	}
	locale := s.webLocale(c, user)
	if err := settingsSecurityKeysOTPEnabledGuard(c, user, locale); err != nil {
		return err
	}
	challenge, ok := s.browserWebAuthnChallenge(c, user.ID, "registration")
	if !ok {
		return c.JSON(http.StatusUnauthorized, map[string]string{
			"error":         settingsT(locale, "webauthn_credentials.challenge_expired", "WebAuthn challenge is missing or expired"),
			"redirect_path": "/settings/two_factor_authentication_methods",
		})
	}
	if _, err := s.ensureWebauthnID(user); err != nil {
		return err
	}
	if err := s.db.Where("id = ?", user.ID).First(user).Error; err != nil {
		return err
	}
	provider, err := s.webauthnProvider()
	if err != nil {
		return err
	}
	webauthnUser, err := s.webauthnUser(user)
	if err != nil {
		return err
	}
	credentialRequest, envelope, err := webauthnCredentialRequest(c.Request())
	if err != nil {
		return err
	}
	credential, err := provider.FinishRegistration(webauthnUser, webauthnRegistrationSession(user, challenge, webauthnRPID(s.cfg.WebDomain)), credentialRequest)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, map[string]string{
			"error":         webT(locale, "webauthn_credentials.create.error"),
			"redirect_path": "/settings/two_factor_authentication_methods",
		})
	}
	nickname := firstNonBlankRaw(envelope.Nickname, envelope.Name, envelope.User.Nickname, envelope.User.Name, c.FormValue("nickname"), c.FormValue("webauthn_credential[nickname]"), c.FormValue("name"))
	if strings.TrimSpace(nickname) == "" {
		return c.JSON(http.StatusUnprocessableEntity, map[string]string{
			"error":         webT(locale, "webauthn_credentials.create.error"),
			"redirect_path": "/settings/two_factor_authentication_methods",
		})
	}
	externalID := webauthnCredentialExternalID(credential)
	var existing int64
	if err := s.db.Model(&models.WebauthnCredential{}).
		Where("external_id = ? OR (user_id = ? AND nickname = ?)", externalID, user.ID, nickname).
		Count(&existing).Error; err != nil {
		return err
	}
	if existing > 0 {
		return c.JSON(http.StatusUnprocessableEntity, map[string]string{
			"error":         settingsT(locale, "webauthn_credentials.already_exists", "Security key already exists"),
			"redirect_path": "/settings/two_factor_authentication_methods",
		})
	}
	now := time.Now().UTC()
	row := models.WebauthnCredential{
		ExternalID: externalID,
		PublicKey:  webauthnCredentialPublicKey(credential),
		Nickname:   nickname,
		SignCount:  int64(credential.Authenticator.SignCount),
		UserID:     models.WebauthnCredentialUserID(user.ID),
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := s.db.Create(&row).Error; err != nil {
		return err
	}
	if len(webauthnUser.credentials) == 0 {
		if err := s.sendWebauthnEnabledMail(*user); err != nil {
			return mailDeliveryError("webauthn enabled", err)
		}
	} else if err := s.sendWebauthnCredentialAddedMail(*user, row); err != nil {
		return mailDeliveryError("webauthn credential added", err)
	}
	if err := s.clearBrowserWebAuthnChallenge(c); err != nil {
		return err
	}
	expireCookie(c, webauthnCreateChallengeCookie, s.cfg.ForceSSL)
	return c.JSON(http.StatusOK, map[string]string{"redirect_path": "/settings/two_factor_authentication_methods"})
}

func (s *Server) destroySettingsSecurityKey(c *echo.Context) error {
	user, _, err := s.currentUserIncludingDisabled(c)
	if err != nil {
		return redirectToSignIn(c)
	}
	locale := s.webLocale(c, user)
	credentials, err := s.webauthnCredentialsForUser(user.ID)
	if err != nil {
		return err
	}
	if err := settingsSecurityKeysOTPEnabledGuard(c, user, locale); err != nil {
		return err
	}
	if err := settingsSecurityKeysWebauthnEnabledGuard(c, credentials, locale); err != nil {
		return err
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	var credential models.WebauthnCredential
	if err := s.db.Where("id = ? AND user_id = ?", id, user.ID).First(&credential).Error; err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	if err := s.db.Where("id = ? AND user_id = ?", id, user.ID).Delete(&models.WebauthnCredential{}).Error; err != nil {
		return err
	}
	var remaining int64
	if err := s.db.Model(&models.WebauthnCredential{}).Where("user_id = ?", user.ID).Count(&remaining).Error; err != nil {
		return err
	}
	if remaining == 0 {
		if err := s.sendWebauthnDisabledMail(*user); err != nil {
			return mailDeliveryError("webauthn disabled", err)
		}
	} else if err := s.sendWebauthnCredentialDeletedMail(*user, credential); err != nil {
		return mailDeliveryError("webauthn credential deleted", err)
	}
	return c.Redirect(http.StatusFound, "/settings/two_factor_authentication_methods?notice="+url.QueryEscape(webT(locale, "webauthn_credentials.destroy.success")))
}

func settingsSecurityKeysOTPEnabledGuard(c *echo.Context, user *models.User, locale string) error {
	if user == nil || !user.OTPRequiredForLogin {
		return c.Redirect(http.StatusFound, "/settings/two_factor_authentication_methods?error="+url.QueryEscape(webT(locale, "webauthn_credentials.otp_required")))
	}
	return nil
}

func settingsSecurityKeysWebauthnEnabledGuard(c *echo.Context, credentials []models.WebauthnCredential, locale string) error {
	if len(credentials) == 0 {
		return c.Redirect(http.StatusFound, "/settings/two_factor_authentication_methods?error="+url.QueryEscape(webT(locale, "webauthn_credentials.not_enabled")))
	}
	return nil
}

func (s *Server) ensureWebauthnID(user *models.User) (string, error) {
	if user.WebauthnID.Valid && strings.TrimSpace(user.WebauthnID.String) != "" {
		return user.WebauthnID.String, nil
	}
	value := randomHex(32)
	err := s.db.Model(&models.User{}).Where("id = ?", user.ID).Update("webauthn_id", sql.NullString{String: value, Valid: true}).Error
	return value, err
}

func webauthnCreateOptions(rpID string, username string, userID string, challenge string, credentials []models.WebauthnCredential) map[string]any {
	if strings.TrimSpace(rpID) == "" {
		rpID = "localhost"
	}
	exclude := make([]map[string]any, 0, len(credentials))
	for _, credential := range credentials {
		exclude = append(exclude, map[string]any{"type": "public-key", "id": credential.ExternalID})
	}
	return map[string]any{
		"challenge": challenge,
		"rp":        map[string]any{"name": railsWebauthnRPName, "id": rpID},
		"user": map[string]any{
			"name":        username,
			"displayName": username,
			"id":          userID,
		},
		"pubKeyCredParams": []map[string]any{
			{"type": "public-key", "alg": -7},
			{"type": "public-key", "alg": -257},
		},
		"timeout":            railsWebauthnTimeout,
		"excludeCredentials": exclude,
		"authenticatorSelection": map[string]any{
			"userVerification": "discouraged",
		},
	}
}

func securityKeysHTML(credentials []models.WebauthnCredential, notice string, errorText string, localeAndTheme ...string) string {
	locale := settingsLocaleArgOrEnglish(localeAndTheme...)
	title := settingsT(locale, "settings.webauthn_authentication", "Security keys")
	var rows strings.Builder
	for _, credential := range credentials {
		rows.WriteString(`<tr><td>`)
		rows.WriteString(html.EscapeString(credential.Nickname))
		rows.WriteString(`</td><td>`)
		rows.WriteString(html.EscapeString(settingsTVars(locale, "webauthn_credentials.registered_on", "Registered on %{date}", map[string]string{"date": credential.CreatedAt.UTC().Format("2006-01-02")})))
		rows.WriteString(`</td><td><a class="table-action-link" data-method="delete" data-confirm="` + html.EscapeString(webT(locale, "webauthn_credentials.delete_confirmation")) + `" href="/settings/security_keys/`)
		rows.WriteString(strconv.FormatInt(credential.ID, 10))
		rows.WriteString(`"><i class="fa fa-trash fa-fw"></i> ` + html.EscapeString(webT(locale, "webauthn_credentials.delete")) + `</a></td></tr>`)
	}
	body := `<div class="table-wrapper"><table class="table"><tbody>` + rows.String() + `</tbody></table></div><hr class="spacer"><div class="simple_form"><a class="block-button" href="/settings/security_keys/new">` + html.EscapeString(webT(locale, "webauthn_credentials.add")) + `</a></div>`
	return accountSecurityPageHTML(title, "security_keys", notice, errorText, body, localeAndTheme...)
}

func firstNonBlankRaw(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func newSecurityKeyHTML(twoFactorScriptPath string, localeAndTheme ...string) string {
	locale := settingsLocaleArgOrEnglish(localeAndTheme...)
	theme := settingsThemeArg(localeAndTheme...)
	script := ""
	if strings.TrimSpace(twoFactorScriptPath) != "" {
		script = `<script src="` + html.EscapeString(twoFactorScriptPath) + `" crossorigin="anonymous" defer></script>`
	}
	addLabel := webT(locale, "webauthn_credentials.add")
	var body strings.Builder
	body.WriteString(`<form class="simple_form" id="new_webauthn_credential" novalidate="novalidate" method="post" action="/settings/security_keys">`)
	body.WriteString(`<p class="flash-message hidden" id="unsupported-browser-message">` + html.EscapeString(webT(locale, "webauthn_credentials.not_supported")) + `</p>`)
	body.WriteString(`<p class="flash-message alert hidden" id="security-key-error-message">` + html.EscapeString(webT(locale, "webauthn_credentials.invalid_credential")) + `</p>`)
	body.WriteString(`<p class="hint">` + webT(locale, "webauthn_credentials.description_html") + `</p>`)
	body.WriteString(`<div class="fields_group"><div class="input with_block_label string required new_webauthn_credential_nickname field_with_hint"><label class="string required" for="new_webauthn_credential_nickname">` + html.EscapeString(webT(locale, "webauthn_credentials.nickname")) + filterRequiredMarker(locale) + `</label><span class="hint">` + html.EscapeString(webT(locale, "webauthn_credentials.nickname_hint")) + `</span><div class="label_input"><input autocomplete="off" class="string required" required="required" aria-required="true" type="text" name="new_webauthn_credential[nickname]" id="new_webauthn_credential_nickname"></div></div></div>`)
	body.WriteString(`<div class="actions"><button name="button" type="submit" class="btn js-webauthn">` + html.EscapeString(addLabel) + `</button></div>`)
	body.WriteString(`</form>`)
	body.WriteString(script)
	if len(localeAndTheme) > 2 && strings.TrimSpace(localeAndTheme[2]) != "" {
		return accountSecurityPageHTML(addLabel, "security_keys", "", "", body.String(), localeAndTheme...)
	}
	bodyWithTitle := `<h1 class="title">` + html.EscapeString(addLabel) + `</h1>` + body.String() + authFormFooter(`<ul class="no-list"><li><a href="/settings/security_keys">`+html.EscapeString(webT(locale, "settings.webauthn_authentication"))+`</a></li></ul>`)
	return authShellHTML(addLabel, "", "", bodyWithTitle, locale, theme)
}
