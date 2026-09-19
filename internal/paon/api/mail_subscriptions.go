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
	"quote":          "notification_emails.quote",
}

func (s *Server) unsubscribePage(c *echo.Context) error {
	if err := requireHTMLOnlyOptionalFormat(c); err != nil {
		return err
	}
	if strings.TrimSpace(c.QueryParam("token")) == "" {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	if subscription, err := s.unsubscribeTokenEmailSubscription(c.QueryParam("token")); err == nil && subscription != nil {
		return c.HTML(http.StatusOK, emailSubscriptionUnsubscribeHTML(c.QueryParam("token"), *subscription, false, s.webLocale(c, nil)))
	}
	settingKey, ok := unsubscribeEmailTypeFromParam(c.QueryParam("type"))
	if !ok {
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
	if strings.TrimSpace(c.FormValue("token")) == "" {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	if subscription, err := s.unsubscribeTokenEmailSubscription(c.FormValue("token")); err == nil && subscription != nil {
		if err := s.db.Delete(subscription).Error; err != nil {
			return err
		}
		return c.HTML(http.StatusOK, emailSubscriptionUnsubscribeHTML(c.FormValue("token"), *subscription, true, s.webLocale(c, nil)))
	}
	settingKey, ok := unsubscribeEmailTypeFromParam(c.FormValue("type"))
	if !ok {
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

func (s *Server) unsubscribeTokenEmailSubscription(token string) (*models.EmailSubscription, error) {
	if strings.TrimSpace(s.cfg.SecretKeyBase) == "" {
		return nil, gorm.ErrRecordNotFound
	}
	gid, ok := railsSignedGlobalIDMessage(token, s.cfg.SecretKeyBase, railsSignedGlobalIDPurposeUnsubscribe, time.Now)
	if !ok {
		return nil, gorm.ErrRecordNotFound
	}
	id, ok := railsGlobalIDModelID(gid, "EmailSubscription")
	if !ok {
		return nil, gorm.ErrRecordNotFound
	}
	var subscription models.EmailSubscription
	if err := s.db.Preload("Account").Where("id = ?", id).First(&subscription).Error; err != nil {
		return nil, err
	}
	return &subscription, nil
}

func emailSubscriptionUnsubscribeHTML(token string, subscription models.EmailSubscription, complete bool, locale string) string {
	locale = firstNonEmpty(strings.TrimSpace(subscription.Locale), locale, "en")
	name := accountDisplayName(subscription.Account)
	title := settingsT(locale, "email_subscriptions.unsubscribe.title", "Unsubscribe")
	message := settingsTVars(locale, "email_subscriptions.unsubscribe.confirmation", "Confirm that you want to stop receiving email updates from %{name}.", map[string]string{"name": name})
	action := settingsT(locale, "email_subscriptions.unsubscribe.action", "Yes, unsubscribe")
	form := `<form method="post" action="/unsubscribe"><input type="hidden" name="token" value="` + html.EscapeString(token) + `"><button class="button" type="submit">` + html.EscapeString(action) + `</button></form>`
	if complete {
		title = settingsT(locale, "email_subscriptions.unsubscribe.complete", "Unsubscribed")
		message = settingsTVars(locale, "email_subscriptions.unsubscribe.success", "You will no longer receive email updates from %{name}.", map[string]string{"name": name})
		form = ""
	}
	body := `<div class="simple_form"><h1 class="title">` + html.EscapeString(title) + `</h1><p class="lead">` + html.EscapeString(message) + `</p>` + form + `</div>`
	return authShellHTML(title, "", "", body, locale, "default")
}

func (s *Server) confirmEmailSubscription(c *echo.Context) error {
	token := strings.TrimSpace(c.QueryParam("confirmation_token"))
	var subscription models.EmailSubscription
	if token == "" || s.db.Preload("Account").Where("confirmation_token = ?", token).First(&subscription).Error != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	if !subscription.ConfirmedAt.Valid {
		now := time.Now().UTC()
		if err := s.db.Model(&models.EmailSubscription{}).Where("id = ?", subscription.ID).Updates(map[string]any{"confirmed_at": now, "updated_at": now}).Error; err != nil {
			return err
		}
	}
	name := accountDisplayName(subscription.Account)
	locale := firstNonEmpty(strings.TrimSpace(subscription.Locale), s.webLocale(c, nil), "en")
	title := settingsT(locale, "email_subscriptions.confirmed.title", "Subscription confirmed")
	message := settingsTVars(locale, "email_subscriptions.confirmed.body", "You will receive new public posts from %{name} by email.", map[string]string{"name": name})
	body := `<div class="simple_form"><h1 class="title">` + html.EscapeString(title) + `</h1><p class="lead">` + html.EscapeString(message) + `</p></div>`
	return c.HTML(http.StatusOK, authShellHTML(title, "", "", body, locale, "default"))
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
