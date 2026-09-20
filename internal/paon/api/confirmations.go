package api

import (
	"context"
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

type confirmationDelivery struct {
	Email          string
	Token          string
	Reconfirmation bool
	User           models.User
	HasUser        bool
}

const confirmationTokenValidFor = 48 * time.Hour

func (s *Server) newAuthConfirmation(c *echo.Context) error {
	locale := s.webLocale(c, nil)
	if target, ok := s.authConfirmationRequireUnconfirmedRedirect(c); ok {
		return c.Redirect(http.StatusFound, target)
	}
	email := c.QueryParam("email")
	if user, _, err := s.currentUser(c); err == nil {
		email = firstNonEmpty(user.UnconfirmedEmail.String, user.Email, email)
	}
	return c.HTML(http.StatusOK, s.authConfirmationHTML(email, c.QueryParam("notice"), c.QueryParam("error"), locale))
}

func (s *Server) createAuthConfirmation(c *echo.Context) error {
	locale := s.webLocale(c, nil)
	if target, ok := s.authConfirmationRequireUnconfirmedRedirect(c); ok {
		return c.Redirect(http.StatusFound, target)
	}
	email := strings.ToLower(strings.TrimSpace(c.FormValue("user[email]")))
	if email == "" {
		if user, _, err := s.currentUser(c); err == nil {
			email = strings.ToLower(strings.TrimSpace(firstNonEmpty(user.UnconfirmedEmail.String, user.Email)))
		}
	}
	if !railsEmailAddressValid(email) {
		return c.HTML(http.StatusUnprocessableEntity, s.authConfirmationHTML(email, "", authInvalidEmailMessage(locale), locale))
	}
	delivery, err := s.refreshConfirmationTokenForEmail(email)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return c.HTML(http.StatusUnprocessableEntity, s.authConfirmationHTML(email, "", authConfirmationEmailNotFoundMessage(locale), locale))
		}
		return err
	}
	if err := s.sendConfirmationDelivery(delivery); err != nil {
		return mailDeliveryError("confirmation", err)
	}
	return c.Redirect(http.StatusFound, "/auth/sign_in?notice="+url.QueryEscape(authConfirmationInstructionsQueuedMessage(locale)))
}

func (s *Server) showAuthConfirmation(c *echo.Context) error {
	locale := s.webLocale(c, nil)
	if target, ok := s.authConfirmationRequireUnconfirmedRedirect(c); ok {
		return c.Redirect(http.StatusFound, target)
	}
	token := requestRawQueryParamValue(c.Request(), "confirmation_token")
	if token == "" {
		return s.newAuthConfirmation(c)
	}
	required, err := s.confirmationCaptchaRequired(token)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return c.HTML(http.StatusUnprocessableEntity, s.authConfirmationHTML("", "", authConfirmationTokenInvalidMessage(locale), locale))
		}
		return err
	}
	if required {
		return c.HTML(http.StatusOK, s.confirmationCaptchaHTML(token, "", locale))
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

func (s *Server) redirectAfterConfirmation(c *echo.Context, user *models.User) error {
	locale := s.webLocale(c, user)
	if target, ok := s.confirmationRedirectToApp(user, c.QueryParam("redirect_to_app")); ok {
		return c.Redirect(http.StatusFound, target)
	}
	if current, _, err := s.currentUser(c); err == nil && current != nil {
		return c.Redirect(http.StatusFound, "/web/start")
	}
	return c.Redirect(http.StatusFound, "/auth/sign_in?notice="+url.QueryEscape(settingsT(locale, "devise.confirmations.confirmed", "Your email address has been successfully confirmed.")))
}

func (s *Server) authConfirmationRequireUnconfirmedRedirect(c *echo.Context) (string, bool) {
	user, _, err := s.currentUserIncludingDisabled(c)
	if err != nil || user == nil {
		return "", false
	}
	if user.ConfirmedAt.Valid && strings.TrimSpace(user.UnconfirmedEmail.String) == "" {
		if user.Approved {
			return "/", true
		}
		return "/auth/edit", true
	}
	return "", false
}

func (s *Server) confirmationRedirectToApp(user *models.User, redirectToApp string) (string, bool) {
	if s == nil || s.db == nil || user == nil || !user.CreatedByApplicationID.Valid || !truthy(redirectToApp) {
		return "", false
	}
	var app models.OAuthApplication
	if err := s.db.Select("redirect_uri").Where("id = ?", user.CreatedByApplicationID.Int64).First(&app).Error; err != nil {
		return "", false
	}
	target := firstRedirectURI(app.RedirectURI)
	if strings.TrimSpace(target) == "" {
		return "", false
	}
	return target, true
}

func (s *Server) refreshConfirmationTokenForEmail(email string) (confirmationDelivery, error) {
	if s.db == nil {
		return confirmationDelivery{}, gorm.ErrRecordNotFound
	}
	var user models.User
	if err := s.db.Where("(lower(email) = ? OR lower(unconfirmed_email) = ?) AND (confirmed_at IS NULL OR unconfirmed_email IS NOT NULL)", email, email).First(&user).Error; err != nil {
		return confirmationDelivery{}, err
	}
	now := time.Now().UTC()
	token := randomHex(16)
	if err := s.db.Model(&models.User{}).Where("id = ?", user.ID).Updates(map[string]any{
		"confirmation_token":   deviseTokenForStorage(token, deviseConfirmationTokenColumn, s.cfg.SecretKeyBase),
		"confirmation_sent_at": now,
		"updated_at":           now,
	}).Error; err != nil {
		return confirmationDelivery{}, err
	}
	return confirmationDelivery{Email: confirmationRecipient(user), Token: token, Reconfirmation: userPendingReconfirmation(user), User: user, HasUser: true}, nil
}

func (s *Server) confirmUserByToken(token string) (*models.User, error) {
	if s.db == nil {
		return nil, gorm.ErrRecordNotFound
	}
	user, err := s.findConfirmationUserByToken(token)
	if err != nil {
		return nil, err
	}
	newUser := !user.ConfirmedAt.Valid
	now := time.Now().UTC()
	if confirmationTokenExpired(*user, now) {
		return nil, gorm.ErrRecordNotFound
	}
	approveOnConfirm, err := s.userApprovedAfterConfirmation(*user, now)
	if err != nil {
		return nil, err
	}
	updates := map[string]any{
		"confirmed_at":       now,
		"confirmation_token": nil,
		"unconfirmed_email":  nil,
		"updated_at":         now,
	}
	if approveOnConfirm {
		updates["approved"] = true
	}
	if user.UnconfirmedEmail.Valid && strings.TrimSpace(user.UnconfirmedEmail.String) != "" {
		updates["email"] = strings.ToLower(strings.TrimSpace(user.UnconfirmedEmail.String))
	}
	if err := s.db.Model(&models.User{}).Where("id = ?", user.ID).Updates(updates).Error; err != nil {
		return nil, err
	}
	if err := s.db.Where("id = ?", user.ID).First(user).Error; err != nil {
		return nil, err
	}
	if newUser && user.Approved {
		if err := s.runApprovedAccountBootstrap(context.Background(), user.AccountID, now); err != nil {
			return nil, err
		}
		s.triggerAccountWebhook("account.approved", user.AccountID)
	}
	if newUser && !user.Approved {
		if err := s.sendStaffNewPendingAccountMails(*user); err != nil {
			return nil, mailDeliveryError("new pending account", err)
		}
	}
	return user, nil
}

func (s *Server) userApprovedAfterConfirmation(user models.User, now time.Time) (bool, error) {
	registrationMode := s.registrationMode()
	if registrationMode != "open" {
		return false, nil
	}
	restriction, err := s.signUpIPRestriction(user.SignUpIP.String, now)
	if err != nil {
		return false, err
	}
	emailRequiresApproval, err := s.emailSignUpRequiresApproval(context.Background(), user.Email, user.SignUpIP.String)
	if err != nil {
		return false, err
	}
	restriction.RequiresApproval = restriction.RequiresApproval || emailRequiresApproval
	return confirmationApprovesUserForRailsConfirm(registrationMode, restriction), nil
}

func confirmationApprovesUserForRailsConfirm(registrationMode string, restriction signUpIPRestriction) bool {
	return registrationMode == "open" && !restriction.RequiresApproval
}

func (s *Server) findConfirmationUserByToken(token string) (*models.User, error) {
	if s.db == nil {
		return nil, gorm.ErrRecordNotFound
	}
	var user models.User
	if err := s.db.Where("confirmation_token IN ?", deviseTokenLookupValues(token, deviseConfirmationTokenColumn, s.cfg.SecretKeyBase)).First(&user).Error; err != nil {
		return nil, err
	}
	return &user, nil
}

func (s *Server) confirmationCaptchaRequired(token string) (bool, error) {
	if !s.captchaRequired() && !s.cfg.CloudflareTurnstileEnabled {
		return false, nil
	}
	user, err := s.findConfirmationUserByToken(token)
	if err != nil {
		return false, err
	}
	return !user.ConfirmedAt.Valid, nil
}

func confirmationTokenExpired(user models.User, now time.Time) bool {
	if !user.ConfirmationSentAt.Valid {
		return true
	}
	return user.ConfirmationSentAt.Time.UTC().Add(confirmationTokenValidFor).Before(now.UTC())
}

func confirmationRecipient(user models.User) string {
	if user.UnconfirmedEmail.Valid && strings.TrimSpace(user.UnconfirmedEmail.String) != "" {
		return strings.ToLower(strings.TrimSpace(user.UnconfirmedEmail.String))
	}
	return strings.ToLower(strings.TrimSpace(user.Email))
}

func userPendingReconfirmation(user models.User) bool {
	return user.ConfirmedAt.Valid && user.UnconfirmedEmail.Valid && strings.TrimSpace(user.UnconfirmedEmail.String) != ""
}

func confirmationDeliveryForUser(user *models.User) confirmationDelivery {
	if user == nil || !user.ConfirmationToken.Valid || strings.TrimSpace(user.ConfirmationToken.String) == "" {
		return confirmationDelivery{}
	}
	return confirmationDeliveryForUserWithToken(user, user.ConfirmationToken.String)
}

func confirmationDeliveryForUserWithToken(user *models.User, token string) confirmationDelivery {
	if user == nil || strings.TrimSpace(token) == "" {
		return confirmationDelivery{}
	}
	return confirmationDelivery{Email: confirmationRecipient(*user), Token: token, Reconfirmation: userPendingReconfirmation(*user), User: *user, HasUser: true}
}

func authConfirmationHTML(email string, notice string, errorText string, locale string) string {
	return authConfirmationHTMLWithSignUpPath(email, notice, errorText, locale, "/auth/sign_up")
}

func (s *Server) authConfirmationHTML(email string, notice string, errorText string, locale string) string {
	return authConfirmationHTMLWithSignUpPath(email, notice, errorText, locale, s.availableSignUpPath())
}

func authConfirmationHTMLWithSignUpPath(email string, notice string, errorText string, locale string, signUpPath string) string {
	title := webT(locale, "auth.resend_confirmation")
	var body strings.Builder
	body.WriteString(`<h1 class="title">` + html.EscapeString(title) + `</h1>`)
	body.WriteString(simpleFormOpen("/auth/confirmation", "post"))
	body.WriteString(simpleTextInput(webT(locale, "simple_form.labels.defaults.email"), "user[email]", email, "email", `autocomplete="email" required`))
	body.WriteString(simpleSubmit(title))
	body.WriteString(simpleFormClose())
	body.WriteString(authSharedFooterHTML("confirmations", signUpPath, locale))
	return authShellHTML(title, notice, errorText, body.String(), locale)
}

func (s *Server) confirmationCaptchaHTML(token string, errorText string, locale string) string {
	captchaHTML := ""
	if s.hcaptchaAvailable() {
		captchaHTML = `<script src="https://js.hcaptcha.com/1/api.js" async defer></script><div class="h-captcha" data-sitekey="` + html.EscapeString(s.cfg.HCaptchaSiteKey) + `"></div>`
	} else if s.cfg.CloudflareTurnstileEnabled {
		captchaHTML = `<script src="https://challenges.cloudflare.com/turnstile/v0/api.js" async defer></script><div class="cf-turnstile" data-sitekey="` + html.EscapeString(s.cfg.CloudflareTurnstileSiteKey) + `"></div>`
	}
	var body strings.Builder
	body.WriteString(`<h1 class="title">` + html.EscapeString(webT(locale, "auth.captcha_confirmation.title")) + `</h1>`)
	body.WriteString(`<p class="lead">` + webT(locale, "auth.captcha_confirmation.hint_html") + `</p>`)
	body.WriteString(simpleFormOpen("/auth/captcha_confirmation", "post"))
	body.WriteString(`<input type="hidden" name="confirmation_token" value="` + html.EscapeString(token) + `">`)
	body.WriteString(captchaHTML)
	body.WriteString(simpleSubmit(webT(locale, "generic.confirm")))
	body.WriteString(simpleFormClose())
	return authShellHTML(webT(locale, "auth.captcha_confirmation.title"), "", errorText, body.String(), locale)
}
