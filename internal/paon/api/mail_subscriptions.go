package api

import (
	"errors"
	"html"
	"net/http"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

var unsubscribeEmailTypes = map[string]string{
	"follow":         "notification_emails.follow",
	"reblog":         "notification_emails.reblog",
	"favourite":      "notification_emails.favourite",
	"mention":        "notification_emails.mention",
	"follow_request": "notification_emails.follow_request",
}

func (s *Server) unsubscribePage(c *echo.Context) error {
	if err := requireHTMLOnlyOptionalFormat(c); err != nil {
		return err
	}
	settingKey, ok := unsubscribeEmailTypeFromParam(c.QueryParam("type"))
	if !ok || strings.TrimSpace(c.QueryParam("token")) == "" {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	user, _, err := s.unsubscribeTokenUser(c.QueryParam("token"))
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apiError(c, http.StatusNotFound, "Record not found")
		}
		return err
	}
	if user == nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	locale := s.webLocale(c, user)
	theme := "default"
	if user != nil {
		theme = settingsWebTheme(decodeUserSettings(user.Settings.String))
	}
	email := ""
	if user != nil {
		email = user.Email
	}
	return c.HTML(http.StatusOK, unsubscribeHTML(c.QueryParam("token"), c.QueryParam("type"), settingKey, email, "", false, s.cfg.LocalDomain, locale, theme))
}

func (s *Server) createUnsubscribe(c *echo.Context) error {
	if err := requireHTMLOnlyOptionalFormat(c); err != nil {
		return err
	}
	settingKey, ok := unsubscribeEmailTypeFromParam(c.FormValue("type"))
	if !ok || strings.TrimSpace(c.FormValue("token")) == "" {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	user, _, err := s.unsubscribeTokenUser(c.FormValue("token"))
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apiError(c, http.StatusNotFound, "Record not found")
		}
		return err
	}
	if user == nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	locale := s.webLocale(c, user)
	theme := settingsWebTheme(decodeUserSettings(user.Settings.String))
	if err := s.updateUserSettingsAttributes(user.ID, map[string]any{settingKey: false}); err != nil {
		return err
	}
	return c.HTML(http.StatusOK, unsubscribeHTML(c.FormValue("token"), c.FormValue("type"), settingKey, user.Email, "", true, s.cfg.LocalDomain, locale, theme))
}

func (s *Server) unsubscribeTokenUser(token string) (*models.User, bool, error) {
	if strings.TrimSpace(s.cfg.SecretKeyBase) == "" {
		return nil, false, gorm.ErrRecordNotFound
	}
	userID, ok := railsSignedGlobalIDUserID(token, s.cfg.SecretKeyBase, time.Now)
	if !ok {
		return nil, false, gorm.ErrRecordNotFound
	}
	var user models.User
	if err := s.db.Select("id, email, settings").Where("id = ?", userID).First(&user).Error; err != nil {
		return nil, true, err
	}
	return &user, true, nil
}

func unsubscribeEmailTypeFromParam(value string) (string, bool) {
	settingKey, ok := unsubscribeEmailTypes[value]
	return settingKey, ok
}

func unsubscribeHTML(token string, rawType string, settingKey string, email string, errorText string, complete bool, domainAndLocale ...string) string {
	domain := ""
	if len(domainAndLocale) > 0 {
		domain = domainAndLocale[0]
	}
	locArgs := []string{}
	if len(domainAndLocale) > 1 {
		locArgs = domainAndLocale[1:]
	}
	loc := settingsLocaleArgOrEnglish(locArgs...)
	theme := settingsThemeArg(locArgs...)
	title := settingsT(loc, "mail_subscriptions.unsubscribe.title", "Unsubscribe")
	emailType := mailSubscriptionEmailTypeLabel(loc, settingKey)
	settingsPath := "/settings/preferences/notifications"
	vars := mailSubscriptionHTMLVars(emailType, domain, email, settingsPath)
	if complete {
		body := `<div class="simple_form"><h1 class="title">` + html.EscapeString(settingsT(loc, "mail_subscriptions.unsubscribe.complete", "Unsubscribed")) + `</h1><p class="lead">` + settingsTVars(loc, "mail_subscriptions.unsubscribe.success_html", "Unsubscribed from %{type} for %{email}.", vars) + `</p>
    <p class="lead">` + settingsTVars(loc, "mail_subscriptions.unsubscribe.resubscribe_html", `You can re-subscribe from your <a href="%{settings_path}">e-mail notification settings</a>.`, vars) + `</p></div>`
		return authShellHTML(title, "", errorText, body, loc, theme)
	}
	emailText := "your account"
	if strings.TrimSpace(email) != "" {
		emailText = email
	}
	vars = mailSubscriptionHTMLVars(emailType, domain, emailText, settingsPath)
	body := `<div class="simple_form"><h1 class="title">` + html.EscapeString(title) + `</h1><p class="lead">` + settingsTVars(loc, "mail_subscriptions.unsubscribe.confirmation_html", `Confirm that you want to unsubscribe %{email} from %{type}. You can re-subscribe from your <a href="%{settings_path}">e-mail notification settings</a>.`, vars) + `</p>
    <form method="post" action="/unsubscribe">
      <input type="hidden" name="token" value="` + html.EscapeString(token) + `">
      <input type="hidden" name="type" value="` + html.EscapeString(rawType) + `">
      <button class="button" type="submit">` + html.EscapeString(settingsT(loc, "mail_subscriptions.unsubscribe.action", "Yes, unsubscribe")) + `</button>
    </form>
    </div>`
	return authShellHTML(title, "", errorText, body, loc, theme)
}

func mailSubscriptionEmailTypeLabel(locale string, settingKey string) string {
	return settingsT(locale, "mail_subscriptions.unsubscribe.emails."+settingKey, settingKey)
}

func mailSubscriptionHTMLVars(emailType string, domain string, email string, settingsPath string) map[string]string {
	return map[string]string{
		"type":          mailSubscriptionStrong(emailType),
		"domain":        mailSubscriptionStrong(domain),
		"email":         mailSubscriptionStrong(email),
		"settings_path": html.EscapeString(settingsPath),
	}
}

func mailSubscriptionStrong(value string) string {
	return `<strong>` + html.EscapeString(value) + `</strong>`
}
