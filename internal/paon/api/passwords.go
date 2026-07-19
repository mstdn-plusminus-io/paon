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
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

const resetPasswordWindow = 6 * time.Hour

func (s *Server) newAuthPassword(c *echo.Context) error {
	locale := s.webLocale(c, nil)
	return c.HTML(http.StatusOK, s.authPasswordNewHTML(c.QueryParam("email"), c.QueryParam("notice"), c.QueryParam("error"), locale))
}

func (s *Server) postAuthPassword(c *echo.Context) error {
	if methodOverrideIs(c, "put", "patch") || c.FormValue("user[reset_password_token]") != "" {
		return s.updateAuthPassword(c)
	}
	return s.createAuthPassword(c)
}

func (s *Server) createAuthPassword(c *echo.Context) error {
	locale := s.webLocale(c, nil)
	email := strings.ToLower(strings.TrimSpace(c.FormValue("user[email]")))
	if email == "" || !strings.Contains(email, "@") {
		return c.HTML(http.StatusUnprocessableEntity, s.authPasswordNewHTML(email, "", authInvalidEmailMessage(locale), locale))
	}
	if s.db == nil {
		return c.HTML(http.StatusServiceUnavailable, s.authPasswordNewHTML(email, "", settingsDatabaseUnavailableMessage(locale), locale))
	}
	token, user, err := s.issuePasswordResetToken(email)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	if token != "" {
		if err := s.sendResetPasswordMail(email, token, user); err != nil {
			return mailDeliveryError("password reset", err)
		}
	}
	return c.Redirect(http.StatusFound, "/auth/sign_in?notice="+url.QueryEscape(settingsT(locale, "devise.passwords.send_instructions", "If your email address exists in our database, you will receive a password recovery link at your email address in a few minutes. Please check your spam folder if you didn't receive this email.")))
}

func (s *Server) editAuthPassword(c *echo.Context) error {
	locale := s.webLocale(c, nil)
	token := requestRawQueryParamValue(c.Request(), "reset_password_token")
	if token == "" {
		return c.Redirect(http.StatusFound, "/auth/password/new?error="+url.QueryEscape(authInvalidResetPasswordTokenMessage(locale)))
	}
	if _, err := s.findUserByResetPasswordToken(token, time.Now().UTC()); err != nil {
		return c.Redirect(http.StatusFound, "/auth/password/new?error="+url.QueryEscape(authInvalidResetPasswordTokenMessage(locale)))
	}
	return c.HTML(http.StatusOK, s.authPasswordEditHTML(token, "", "", locale))
}

func (s *Server) updateAuthPassword(c *echo.Context) error {
	locale := s.webLocale(c, nil)
	token := firstRawRequestParamValue(c, "user[reset_password_token]")
	password := c.FormValue("user[password]")
	confirmation := c.FormValue("user[password_confirmation]")
	if token == "" {
		return c.HTML(http.StatusUnprocessableEntity, s.authPasswordNewHTML("", "", authInvalidResetPasswordTokenMessage(locale), locale))
	}
	if len(password) < 8 || len(password) > 72 {
		return c.HTML(http.StatusUnprocessableEntity, s.authPasswordEditHTML(token, "", authInvalidPasswordMessage(locale), locale))
	}
	if confirmation != "" && confirmation != password {
		return c.HTML(http.StatusUnprocessableEntity, s.authPasswordEditHTML(token, "", authPasswordConfirmationMismatchMessage(locale), locale))
	}
	user, err := s.findUserByResetPasswordToken(token, time.Now().UTC())
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return c.HTML(http.StatusUnprocessableEntity, s.authPasswordNewHTML("", "", authInvalidResetPasswordTokenMessage(locale), locale))
		}
		return err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	if err := s.resetUserPassword(user.ID, string(hash)); err != nil {
		return err
	}
	if err := s.sendPasswordChangedMail(*user); err != nil {
		return mailDeliveryError("password changed", err)
	}
	clearSessionCookie(c, s.cfg.ForceSSL)
	return c.Redirect(http.StatusFound, "/auth/sign_in?notice="+url.QueryEscape(settingsT(locale, "devise.passwords.updated_not_active", "Your password has been changed successfully.")))
}

func (s *Server) issuePasswordResetToken(email string) (string, models.User, error) {
	var user models.User
	if err := s.db.Where("lower(email) = ? AND encrypted_password <> '' AND disabled = false", email).First(&user).Error; err != nil {
		return "", models.User{}, err
	}
	token := randomHex(24)
	now := time.Now().UTC()
	err := s.db.Model(&models.User{}).Where("id = ?", user.ID).Updates(map[string]any{
		"reset_password_token":   deviseTokenForStorage(token, deviseResetPasswordTokenColumn, s.cfg.SecretKeyBase),
		"reset_password_sent_at": now,
		"updated_at":             now,
	}).Error
	return token, user, err
}

func (s *Server) findUserByResetPasswordToken(token string, now time.Time) (*models.User, error) {
	if s.db == nil {
		return nil, gorm.ErrRecordNotFound
	}
	var user models.User
	err := s.db.Where("reset_password_token IN ? AND reset_password_sent_at >= ?", deviseTokenLookupValues(token, deviseResetPasswordTokenColumn, s.cfg.SecretKeyBase), now.Add(-resetPasswordWindow)).First(&user).Error
	if err != nil {
		return nil, err
	}
	return &user, nil
}

func (s *Server) resetUserPassword(userID int64, passwordHash string) error {
	if s.db == nil {
		return gorm.ErrRecordNotFound
	}
	now := time.Now().UTC()
	var revokedTokenIDs []int64
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.OAuthAccessToken{}).
			Where("resource_owner_id = ? AND revoked_at IS NULL", userID).
			Pluck("id", &revokedTokenIDs).Error; err != nil {
			return err
		}
		if err := tx.Where("id IN (?)", tx.Model(&models.SessionActivation{}).Select("web_push_subscription_id").Where("user_id = ? AND web_push_subscription_id IS NOT NULL", userID)).Delete(&models.WebPushSubscription{}).Error; err != nil {
			return err
		}
		if err := tx.Where("user_id = ?", userID).Delete(&models.SessionActivation{}).Error; err != nil {
			return err
		}
		if err := tx.Model(&models.OAuthAccessGrant{}).Where("resource_owner_id = ? AND revoked_at IS NULL", userID).Update("revoked_at", now).Error; err != nil {
			return err
		}
		if err := tx.Model(&models.OAuthAccessToken{}).Where("resource_owner_id = ? AND revoked_at IS NULL", userID).Update("revoked_at", now).Error; err != nil {
			return err
		}
		return tx.Model(&models.User{}).Where("id = ?", userID).Updates(map[string]any{
			"encrypted_password":     passwordHash,
			"reset_password_token":   nil,
			"reset_password_sent_at": nil,
			"sign_in_token":          nil,
			"sign_in_token_sent_at":  nil,
			"updated_at":             now,
			"skip_sign_in_token":     nil,
		}).Error
	})
	if err != nil {
		return err
	}
	s.publishAccessTokenKills(revokedTokenIDs)
	return nil
}

func authPasswordNewHTML(email string, notice string, errorText string, locale string) string {
	return authPasswordNewHTMLWithSignUpPath(email, notice, errorText, locale, "/auth/sign_up")
}

func (s *Server) authPasswordNewHTML(email string, notice string, errorText string, locale string) string {
	return authPasswordNewHTMLWithSignUpPath(email, notice, errorText, locale, s.availableSignUpPath())
}

func authPasswordNewHTMLWithSignUpPath(email string, notice string, errorText string, locale string, signUpPath string) string {
	title := webT(locale, "auth.reset_password")
	var body strings.Builder
	body.WriteString(`<h1 class="title">` + html.EscapeString(title) + `</h1>`)
	body.WriteString(simpleFormOpen("/auth/password", "post"))
	body.WriteString(simpleTextInput(webT(locale, "simple_form.labels.defaults.email"), "user[email]", email, "email", `autocomplete="email" required`))
	body.WriteString(simpleSubmit(title))
	body.WriteString(simpleFormClose())
	body.WriteString(authSharedFooterHTML("passwords", signUpPath, locale))
	return authShellHTML(title, notice, errorText, body.String(), locale)
}

func authPasswordEditHTML(token string, notice string, errorText string, locale string) string {
	return authPasswordEditHTMLWithSignUpPath(token, notice, errorText, locale, "/auth/sign_up")
}

func (s *Server) authPasswordEditHTML(token string, notice string, errorText string, locale string) string {
	return authPasswordEditHTMLWithSignUpPath(token, notice, errorText, locale, s.availableSignUpPath())
}

func authPasswordEditHTMLWithSignUpPath(token string, notice string, errorText string, locale string, signUpPath string) string {
	title := webT(locale, "auth.set_new_password")
	var body strings.Builder
	body.WriteString(`<h1 class="title">` + html.EscapeString(title) + `</h1>`)
	body.WriteString(simpleFormOpen("/auth/password", "put"))
	body.WriteString(`<input type="hidden" name="user[reset_password_token]" value="` + html.EscapeString(token) + `">`)
	body.WriteString(simpleTextInput(webT(locale, "simple_form.labels.defaults.new_password"), "user[password]", "", "password", `autocomplete="new-password" minlength="8" maxlength="72" required`))
	body.WriteString(simpleTextInput(webT(locale, "simple_form.labels.defaults.confirm_new_password"), "user[password_confirmation]", "", "password", `autocomplete="new-password" minlength="8" maxlength="72" required`))
	body.WriteString(simpleSubmit(title))
	body.WriteString(simpleFormClose())
	body.WriteString(authSharedFooterHTML("passwords", signUpPath, locale))
	return authShellHTML(title, notice, errorText, body.String(), locale)
}
