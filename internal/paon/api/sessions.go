package api

import (
	"errors"
	"html"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

func (s *Server) settingsSessionsPage(c *echo.Context) error {
	user, currentToken, err := s.currentUserIncludingDisabled(c)
	if err != nil {
		return redirectToSignIn(c)
	}
	account, err := s.currentAccountForSettings(user.AccountID)
	if err != nil {
		return err
	}
	if account.SuspendedAt.Valid {
		return apiError(c, http.StatusForbidden, "This account is suspended")
	}
	sessions, err := s.userSessionActivations(user.ID)
	if err != nil {
		return err
	}
	currentTokenID, err := s.currentAccessTokenID(currentToken)
	if err != nil {
		return err
	}
	locale := s.webLocale(c, user)
	theme := settingsWebTheme(decodeUserSettings(user.Settings.String))
	return c.HTML(http.StatusOK, settingsSessionsHTML(sessions, currentTokenID, c.QueryParam("notice"), c.QueryParam("error"), locale, theme))
}

func (s *Server) destroySettingsSession(c *echo.Context) error {
	user, currentToken, err := s.currentUserIncludingDisabled(c)
	if err != nil {
		return redirectToSignIn(c)
	}
	account, err := s.currentAccountForSettings(user.AccountID)
	if err != nil {
		return err
	}
	if account.SuspendedAt.Valid {
		return apiError(c, http.StatusForbidden, "This account is suspended")
	}
	if c.Request().Method == http.MethodPost && !strings.EqualFold(c.FormValue("_method"), "delete") {
		return c.Redirect(http.StatusFound, "/auth/edit")
	}
	sessionID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || sessionID <= 0 {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	clearedCurrent, err := s.revokeSettingsSession(user.ID, sessionID, currentToken)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apiError(c, http.StatusNotFound, "Record not found")
		}
		return err
	}
	if clearedCurrent {
		clearSessionCookie(c, s.cfg.ForceSSL)
	}
	return c.Redirect(http.StatusFound, "/auth/edit?notice="+url.QueryEscape(settingsT(s.webLocale(c, user), "sessions.revoke_success", "Session successfully revoked")))
}

func (s *Server) userSessionActivations(userID int64) ([]models.SessionActivation, error) {
	var sessions []models.SessionActivation
	if s.db == nil {
		return sessions, gorm.ErrRecordNotFound
	}
	err := s.db.Where("user_id = ?", userID).Order("updated_at DESC").Find(&sessions).Error
	return sessions, err
}

func (s *Server) currentAccessTokenID(token string) (int64, error) {
	if strings.TrimSpace(token) == "" || s.db == nil {
		return 0, nil
	}
	var accessToken models.OAuthAccessToken
	if err := s.db.Select("id").Where("token = ? AND revoked_at IS NULL", token).First(&accessToken).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, nil
		}
		return 0, err
	}
	return accessToken.ID, nil
}

func (s *Server) revokeSettingsSession(userID int64, sessionID int64, currentToken string) (bool, error) {
	if s.db == nil {
		return false, gorm.ErrRecordNotFound
	}
	var clearedCurrent bool
	var revokedTokenID int64
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var activation models.SessionActivation
		if err := tx.Where("id = ? AND user_id = ?", sessionID, userID).First(&activation).Error; err != nil {
			return err
		}
		if activation.WebPushSubscriptionID.Valid {
			if err := tx.Delete(&models.WebPushSubscription{}, "id = ?", activation.WebPushSubscriptionID.Int64).Error; err != nil {
				return err
			}
		}
		if activation.AccessTokenID.Valid {
			var token models.OAuthAccessToken
			if err := tx.Where("id = ?", activation.AccessTokenID.Int64).First(&token).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			if token.ID != 0 && token.Token == currentToken {
				clearedCurrent = true
			}
			revokedTokenID = activation.AccessTokenID.Int64
			if err := tx.Delete(&models.OAuthAccessToken{}, "id = ?", activation.AccessTokenID.Int64).Error; err != nil {
				return err
			}
		}
		return tx.Delete(&models.SessionActivation{}, "id = ?", activation.ID).Error
	})
	if err == nil {
		s.publishAccessTokenKills([]int64{revokedTokenID})
	}
	return clearedCurrent, err
}

func (s *Server) clearOtherSessionsForRequest(userID int64, currentToken string, c *echo.Context) error {
	if s.db == nil || userID == 0 {
		return nil
	}
	return s.clearOtherSessions(userID, s.railsSessionIDForRequest(c), currentToken)
}

func (s *Server) railsSessionIDForRequest(c *echo.Context) string {
	sessionID, _ := s.railsSessionIDFromCookie(c)
	if sessionID == "" {
		sessionID, _ = s.railsSessionIDFromEncryptedSession(c)
	}
	return strings.TrimSpace(sessionID)
}

func (s *Server) revokeRailsSessionActivationForRequest(c *echo.Context) error {
	sessionID := s.railsSessionIDForRequest(c)
	if s.db == nil || sessionID == "" {
		return nil
	}
	var activations []models.SessionActivation
	if err := s.db.Where("session_id = ?", sessionID).Find(&activations).Error; err != nil {
		return err
	}
	return s.deleteSessionActivations(activations)
}

func (s *Server) clearOtherSessions(userID int64, currentSessionID string, currentToken string) error {
	if s.db == nil || userID == 0 {
		return nil
	}
	query := s.db.Where("user_id = ?", userID)
	if strings.TrimSpace(currentSessionID) != "" {
		query = query.Where("session_id <> ?", strings.TrimSpace(currentSessionID))
	} else if currentTokenID, err := s.currentAccessTokenID(currentToken); err != nil {
		return err
	} else if currentTokenID > 0 {
		query = query.Where("access_token_id IS NULL OR access_token_id <> ?", currentTokenID)
	} else {
		return nil
	}
	var activations []models.SessionActivation
	if err := query.Find(&activations).Error; err != nil {
		return err
	}
	return s.deleteSessionActivations(activations)
}

func (s *Server) deleteSessionActivations(activations []models.SessionActivation) error {
	if len(activations) == 0 {
		return nil
	}
	activationIDs := make([]int64, 0, len(activations))
	tokenIDs := make([]int64, 0, len(activations))
	pushIDs := make([]int64, 0, len(activations))
	for _, activation := range activations {
		activationIDs = append(activationIDs, activation.ID)
		if activation.AccessTokenID.Valid {
			tokenIDs = append(tokenIDs, activation.AccessTokenID.Int64)
		}
		if activation.WebPushSubscriptionID.Valid {
			pushIDs = append(pushIDs, activation.WebPushSubscriptionID.Int64)
		}
	}
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if len(pushIDs) > 0 {
			if err := tx.Where("id IN ?", pushIDs).Delete(&models.WebPushSubscription{}).Error; err != nil {
				return err
			}
		}
		if len(tokenIDs) > 0 {
			if err := tx.Where("id IN ?", tokenIDs).Delete(&models.OAuthAccessToken{}).Error; err != nil {
				return err
			}
		}
		return tx.Where("id IN ?", activationIDs).Delete(&models.SessionActivation{}).Error
	}); err != nil {
		return err
	}
	s.publishAccessTokenKills(tokenIDs)
	return nil
}

func settingsSessionsHTML(sessions []models.SessionActivation, currentTokenID int64, notice string, errorText string, localeAndTheme ...string) string {
	loc := settingsLocaleArgOrEnglish(localeAndTheme...)
	return accountSecurityPageHTML(settingsT(loc, "sessions.title", "Active sessions"), "account", notice, errorText, authEditSessionsHTML(sessions, currentTokenID, loc, settingsApplicationNameArg(localeAndTheme)), localeAndTheme...)
}

func authEditSessionsHTML(sessions []models.SessionActivation, currentTokenID int64, locale string, applicationNames ...string) string {
	return authEditSessionsHTMLWithOptions(sessions, currentTokenID, locale, true, applicationNames...)
}

func authEditSessionsHTMLWithOptions(sessions []models.SessionActivation, currentTokenID int64, locale string, canRevoke bool, applicationNames ...string) string {
	loc := firstNonEmpty(locale, "en")
	applicationName := "Mastodon"
	if len(applicationNames) > 0 && strings.TrimSpace(applicationNames[0]) != "" {
		applicationName = strings.TrimSpace(applicationNames[0])
	}
	var rows strings.Builder
	for _, session := range sessions {
		current := currentTokenID > 0 && session.AccessTokenID.Valid && session.AccessTokenID.Int64 == currentTokenID
		ip := ""
		if session.IP.Valid {
			ip = session.IP.String
		}
		stamp := session.UpdatedAt.UTC().Format(time.RFC3339)
		localizedStamp := formatRailsLocalizedTime(loc, session.UpdatedAt)
		activity := `<time class="time-ago" datetime="` + html.EscapeString(stamp) + `" title="` + html.EscapeString(localizedStamp) + `">` + html.EscapeString(localizedStamp) + `</time>`
		action := ""
		if canRevoke {
			action = `<a class="table-action-link" data-method="delete" href="/settings/sessions/` + strconv.FormatInt(session.ID, 10) + `"><i class="fa fa-times fa-fw"></i> ` + html.EscapeString(settingsT(loc, "sessions.revoke", "Revoke")) + `</a>`
		}
		if current {
			activity = html.EscapeString(settingsT(loc, "sessions.current_session", "Current session"))
			action = ""
		}
		icon := sessionDeviceIcon(session.UserAgent)
		rows.WriteString(`<tr><td><span title="` + html.EscapeString(session.UserAgent) + `"><i class="fa fa-` + icon + ` fa-fw" aria-label="` + icon + `"></i> ` + html.EscapeString(sessionDescription(session.UserAgent, loc)) + `</span></td><td><samp>` + html.EscapeString(ip) + `</samp></td><td>` + activity + `</td><td>` + action + `</td></tr>`)
	}
	explanation := strings.ReplaceAll(settingsT(loc, "sessions.explanation", "These are the browsers and devices currently signed in to your account."), "Mastodon", applicationName)
	return `<h3>` + html.EscapeString(settingsT(loc, "sessions.title", "Active sessions")) + `</h3>
    <p class="muted-hint">` + html.EscapeString(explanation) + ` <a href="/settings/login_activities">` + html.EscapeString(settingsT(loc, "sessions.view_authentication_history", "View authentication history")) + `</a></p>
    <hr class="spacer">
    <div class="table-wrapper"><table class="table inline-table"><thead><tr><th>` + html.EscapeString(settingsT(loc, "sessions.browser", "Browser")) + `</th><th>` + html.EscapeString(settingsT(loc, "sessions.ip", "IP")) + `</th><th>` + html.EscapeString(settingsT(loc, "sessions.activity", "Activity")) + `</th><th></th></tr></thead><tbody>` + rows.String() + `</tbody></table></div>`
}

func sessionDescription(userAgent string, locales ...string) string {
	locale := "en"
	if len(locales) > 0 && strings.TrimSpace(locales[0]) != "" {
		locale = locales[0]
	}
	return localizedUserAgentDescription(userAgent, locale)
}

func sessionDeviceIcon(userAgent string) string {
	lower := strings.ToLower(userAgent)
	if strings.Contains(lower, "ipad") || strings.Contains(lower, "tablet") {
		return "tablet"
	}
	if strings.Contains(lower, "iphone") || strings.Contains(lower, "ipod") || strings.Contains(lower, "android") || strings.Contains(lower, "mobile") {
		return "mobile"
	}
	return "desktop"
}

func accountSecurityNavHTML(active string, locale ...string) string {
	loc := settingsLocaleArgOrEnglish(locale...)
	items := []struct {
		key   string
		label string
		href  string
	}{
		{key: "account", label: settingsT(loc, "settings.account_settings", "Account settings"), href: "/auth/edit"},
		{key: "login_activities", label: settingsT(loc, "login_activities.title", "Authentication history"), href: "/settings/login_activities"},
		{key: "two_factor", label: settingsT(loc, "settings.two_factor_authentication", "Two-factor authentication"), href: "/settings/two_factor_authentication_methods"},
		{key: "security_keys", label: settingsT(loc, "settings.webauthn_authentication", "Security keys"), href: "/settings/security_keys"},
	}
	var b strings.Builder
	b.WriteString(`<nav class="content__heading__tabs account-security__tabs"><ul>`)
	for _, item := range items {
		class := ""
		if item.key == active {
			class = ` class="active"`
		}
		b.WriteString(`<li` + class + `><a href="` + item.href + `">` + html.EscapeString(item.label) + `</a></li>`)
	}
	b.WriteString(`</ul></nav>`)
	return b.String()
}

func accountSecurityPageHTML(title string, _ string, notice string, errorText string, body string, localeAndTheme ...string) string {
	locale := settingsLocaleArgOrEnglish(localeAndTheme...)
	if len(localeAndTheme) > 2 && strings.TrimSpace(localeAndTheme[2]) != "" {
		return settingsPageShellWithHeading(title, settingsNavigationArg(localeAndTheme, locale), settingsInlineFlashHTML(notice, errorText)+body, locale, settingsThemeArg(localeAndTheme...), "", "")
	}
	return authPageHTML(title, notice, errorText, body, localeAndTheme...)
}
