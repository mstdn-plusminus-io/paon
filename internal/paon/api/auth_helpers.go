package api

import (
	"errors"
	"html"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

func (s *Server) createAuthChallenge(c *echo.Context) error {
	user, _, err := s.currentUserIncludingDisabled(c)
	if err != nil {
		return redirectToSignIn(c)
	}
	returnTo, password, err := authChallengeParams(c)
	if errors.Is(err, errAuthChallengeParamsMissing) {
		return apiError(c, http.StatusBadRequest, "Malformed request")
	}
	if err != nil {
		return err
	}
	locale := s.webLocale(c, user)
	if !validBCryptPassword(user.EncryptedPassword, password) {
		return c.HTML(http.StatusOK, authChallengeHTML(returnTo, "/auth/challenge", settingsT(locale, "challenge.invalid_password", "Invalid password"), locale))
	}
	if err := s.setBrowserChallengePassed(c, user.ID); err != nil {
		return err
	}
	expireCookie(c, "paon_challenge_passed_at", s.cfg.ForceSSL)
	return c.Redirect(http.StatusFound, safeLocalReturnPath(returnTo))
}

var errAuthChallengeParamsMissing = errors.New("auth challenge root parameter is missing")

const railsChallengeTimeout = time.Hour

func authChallengeParams(c *echo.Context) (string, string, error) {
	req := c.Request()
	if err := req.ParseForm(); err != nil {
		return "", "", err
	}
	const prefix = "form_challenge"
	if !formHasNestedPrefix(req.Form, prefix) {
		return "", "", errAuthChallengeParamsMissing
	}
	return firstNonEmpty(lastFormValue(req.Form, prefix+"[return_to]"), "/"), lastFormValue(req.Form, prefix+"[current_password]"), nil
}

func (s *Server) webauthnOptions(c *echo.Context) error {
	user, _, err := s.currentUser(c)
	if err != nil {
		user, err = s.webauthnAttemptUser(c)
		if err != nil {
			return apiError(c, http.StatusUnauthorized, "WebAuthn is not enabled")
		}
	}
	credentials, err := s.webauthnCredentialsForUser(user.ID)
	if err != nil {
		return err
	}
	if len(credentials) == 0 {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "WebAuthn is not enabled"})
	}
	allow := make([]map[string]any, 0, len(credentials))
	for _, credential := range credentials {
		allow = append(allow, map[string]any{"type": "public-key", "id": credential.ExternalID})
	}
	challenge := webauthnChallenge()
	if err := s.setBrowserWebAuthnChallenge(c, user.ID, "login", challenge); err != nil {
		return err
	}
	expireCookie(c, webauthnChallengeCookie, s.cfg.ForceSSL)
	return c.JSON(http.StatusOK, map[string]any{
		"challenge":        challenge,
		"timeout":          railsWebauthnTimeout,
		"rpId":             webauthnRPID(s.cfg.WebDomain),
		"userVerification": "discouraged",
		"allowCredentials": allow,
	})
}

func (s *Server) confirmCaptcha(c *echo.Context) error {
	locale := s.webLocale(c, nil)
	token := requestRawParamValue(c, "confirmation_token")
	if token == "" {
		return c.HTML(http.StatusOK, authPageHTML(settingsT(locale, "auth.captcha_confirmation.title", "Captcha confirmation"), "", "", `<p class="lead">`+html.EscapeString(settingsT(locale, "auth.captcha_confirmation.not_required", "Captcha confirmation is not required."))+`</p><p><a href="/auth/sign_in">`+html.EscapeString(settingsT(locale, "auth.captcha_confirmation.continue", "Continue"))+`</a></p>`, locale))
	}
	required, err := s.confirmationCaptchaRequired(token)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return c.HTML(http.StatusUnprocessableEntity, s.authConfirmationHTML("", "", authConfirmationTokenInvalidMessage(locale), locale))
		}
		return err
	}
	if !required {
		return c.Redirect(http.StatusFound, "/auth/confirmation?confirmation_token="+url.QueryEscape(token))
	}
	var captchaErr error
	if s.hcaptchaAvailable() {
		captchaErr = s.checkHCaptcha(c, c.FormValue("h-captcha-response"))
	} else {
		captchaErr = s.checkCloudflareTurnstile(c, c.FormValue("cf-turnstile-response"))
	}
	if captchaErr != nil {
		return c.HTML(http.StatusUnprocessableEntity, s.confirmationCaptchaHTML(token, apiErrorMessage(captchaErr), locale))
	}
	user, err := s.confirmUserByToken(token)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return c.HTML(http.StatusUnprocessableEntity, s.authConfirmationHTML("", "", authConfirmationTokenInvalidMessage(locale), locale))
		}
		return err
	}
	return s.redirectAfterConfirmation(c, user)
}

func (s *Server) webauthnCredentialsForUser(userID int64) ([]models.WebauthnCredential, error) {
	var credentials []models.WebauthnCredential
	err := s.db.Where("user_id = ?", userID).Order("id ASC").Find(&credentials).Error
	return credentials, err
}

func (s *Server) webauthnAttemptUser(c *echo.Context) (*models.User, error) {
	userID, _, ok := s.browserTwoFactorAttempt(c)
	if !ok || s.db == nil {
		return nil, gorm.ErrRecordNotFound
	}
	var user models.User
	if err := s.db.Where("id = ?", userID).First(&user).Error; err != nil {
		return nil, err
	}
	return &user, nil
}

func authInvalidEmailMessage(locale string) string {
	return settingsT(locale, "users.invalid_email", "Email is invalid")
}

func authInvalidPasswordMessage(locale string) string {
	return settingsT(locale, "users.invalid_password", "Password is invalid")
}

func authPasswordConfirmationMismatchMessage(locale string) string {
	return settingsT(locale, "users.password_confirmation_mismatch", "Password confirmation does not match")
}

func authInvalidResetPasswordTokenMessage(locale string) string {
	return settingsT(locale, "auth.invalid_reset_password_token", "Password reset token is invalid or expired. Please request a new one.")
}

func authConfirmationInstructionsQueuedMessage(locale string) string {
	return settingsT(locale, "auth.setup.new_confirmation_instructions_sent", settingsT(locale, "devise.confirmations.send_instructions", "You will receive an email with instructions for how to confirm your email address in a few minutes. Please check your spam folder if you didn't receive this email."))
}

func authConfirmationTokenInvalidMessage(locale string) string {
	return settingsT(locale, "auth.confirmations.invalid_token", "Confirmation token is invalid or has already been used")
}

func authConfirmationEmailNotFoundMessage(locale string) string {
	return settingsT(locale, "auth.confirmations.email_not_found", "Email not found or already confirmed")
}

func authChallengeHTML(returnTo string, action string, errorText string, locale string) string {
	prompt := webT(locale, "challenge.prompt")
	if action == "" {
		action = "/auth/challenge"
	}
	fieldID := "form_challenge_current_password"
	var body strings.Builder
	body.WriteString(simpleFormOpen(action, "post"))
	body.WriteString(`<input type="hidden" name="form_challenge[return_to]" value="` + htmlEscapeAttr(returnTo) + `">`)
	body.WriteString(`<div class="field-group"><div class="input with_block_label password required form_challenge_current_password field_with_hint">`)
	body.WriteString(`<label class="password required" for="` + fieldID + `">` + html.EscapeString(prompt) + ` <abbr title="required">*</abbr></label>`)
	body.WriteString(`<span class="hint">` + html.EscapeString(webT(locale, "simple_form.hints.form_challenge.current_password")) + `</span>`)
	body.WriteString(`<div class="label_input"><input autocomplete="current-password" autofocus="autofocus" class="password required" required="required" aria-required="true" type="password" name="form_challenge[current_password]" id="` + fieldID + `"></div></div></div>`)
	body.WriteString(simpleSubmit(webT(locale, "challenge.confirm")))
	body.WriteString(simpleFormClose())
	body.WriteString(`<p class="hint subtle-hint">` + webT(locale, "challenge.hint_html") + `</p>`)
	body.WriteString(authFormFooter(`<ul class="no-list"><li><a href="/auth/edit">` + html.EscapeString(webT(locale, "settings.account_settings")) + `</a></li><li><a data-method="delete" href="/auth/sign_out">` + html.EscapeString(webT(locale, "auth.logout")) + `</a></li></ul>`))
	return authShellHTML(prompt, "", errorText, body.String(), locale)
}

func setCookie(c *echo.Context, name string, value string, maxAge int, secure bool) {
	c.SetCookie(&http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   secure,
	})
}

func safeLocalReturnPath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "/"
	}
	if strings.HasPrefix(value, "/") && !strings.HasPrefix(value, "//") {
		return value
	}
	return "/"
}

func htmlEscapeAttr(value string) string {
	return strings.NewReplacer("&", "&amp;", `"`, "&quot;", "<", "&lt;", ">", "&gt;").Replace(value)
}
