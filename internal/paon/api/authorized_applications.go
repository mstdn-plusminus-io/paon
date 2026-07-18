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
	"gorm.io/gorm"
)

type authorizedOAuthApplication struct {
	ID           int64
	Name         string
	Website      string
	Scopes       string
	Superapp     bool
	AuthorizedAt time.Time
	LastUsedAt   sql.NullTime
}

func (s *Server) oauthAuthorizedApplications(c *echo.Context) error {
	setPrivateNoStoreCacheHeaders(c)
	user, _, err := s.currentUserIncludingDisabled(c)
	if err != nil {
		return redirectToSignIn(c)
	}
	account, err := s.accountForUser(user)
	if err != nil {
		return err
	}
	apps, err := s.authorizedApplicationsForUser(user.ID)
	if err != nil {
		return err
	}
	locale := s.webLocale(c, user)
	theme := settingsWebTheme(decodeUserSettings(user.Settings.String))
	navigation, err := s.settingsNavigationForUser(c.Request().URL.Path, locale, user, account)
	if err != nil {
		return err
	}
	return c.HTML(http.StatusOK, authorizedApplicationsHTML(apps, c.QueryParam("notice"), c.QueryParam("error"), !account.SuspendedAt.Valid, locale, theme, navigation))
}

func (s *Server) destroyOAuthAuthorizedApplication(c *echo.Context) error {
	setPrivateNoStoreCacheHeaders(c)
	user, _, err := s.currentUserIncludingDisabled(c)
	if err != nil {
		return redirectToSignIn(c)
	}
	account, err := s.accountForUser(user)
	if err != nil {
		return err
	}
	if account.SuspendedAt.Valid {
		return apiError(c, http.StatusForbidden, "This account is suspended")
	}
	if c.Request().Method == http.MethodPost && !methodOverrideIs(c, "delete") {
		return c.Redirect(http.StatusFound, "/oauth/authorized_applications")
	}
	appID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || appID <= 0 {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	if err := s.revokeAuthorizedApplication(user.ID, appID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apiError(c, http.StatusNotFound, "Record not found")
		}
		return err
	}
	locale := s.webLocale(c, user)
	return c.Redirect(http.StatusFound, "/oauth/authorized_applications?notice="+url.QueryEscape(oauthT(locale, "doorkeeper.flash.authorized_applications.destroy.notice", "Application revoked.")))
}

func (s *Server) authorizedApplicationsForUser(userID int64) ([]authorizedOAuthApplication, error) {
	if s.db == nil {
		return []authorizedOAuthApplication{}, nil
	}
	var apps []authorizedOAuthApplication
	err := s.db.Table("oauth_access_tokens AS tokens").
		Select(`oauth_applications.id,
			oauth_applications.name,
			COALESCE(oauth_applications.website, '') AS website,
			COALESCE(NULLIF(oauth_applications.scopes, ''), STRING_AGG(DISTINCT tokens.scopes, ' '), '') AS scopes,
			oauth_applications.superapp,
			oauth_applications.created_at AS authorized_at,
			MAX(tokens.last_used_at) AS last_used_at`).
		Joins("JOIN oauth_applications ON oauth_applications.id = tokens.application_id").
		Where("tokens.resource_owner_id = ? AND tokens.revoked_at IS NULL", userID).
		Group("oauth_applications.id, oauth_applications.name, oauth_applications.website, oauth_applications.scopes, oauth_applications.superapp, oauth_applications.created_at").
		Order("oauth_applications.id DESC").
		Scan(&apps).Error
	return apps, err
}

func (s *Server) revokeAuthorizedApplication(userID int64, applicationID int64) error {
	if s.db == nil {
		return gorm.ErrRecordNotFound
	}
	now := time.Now().UTC()
	var revokedTokenIDs []int64
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var count int64
		if err := tx.Model(&models.OAuthAccessToken{}).
			Where("application_id = ? AND resource_owner_id = ?", applicationID, userID).
			Pluck("id", &revokedTokenIDs).Error; err != nil {
			return err
		}
		if err := tx.Model(&models.OAuthAccessToken{}).
			Where("application_id = ? AND resource_owner_id = ? AND revoked_at IS NULL", applicationID, userID).
			Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			return gorm.ErrRecordNotFound
		}
		if err := tx.Where("access_token_id IN (?)", tx.Model(&models.OAuthAccessToken{}).
			Select("id").
			Where("application_id = ? AND resource_owner_id = ? AND revoked_at IS NULL", applicationID, userID)).
			Delete(&models.WebPushSubscription{}).Error; err != nil {
			return err
		}
		return tx.Model(&models.OAuthAccessToken{}).
			Where("application_id = ? AND resource_owner_id = ? AND revoked_at IS NULL", applicationID, userID).
			Update("revoked_at", now).Error
	})
	if err != nil {
		return err
	}
	s.publishAccessTokenKills(revokedTokenIDs)
	return nil
}

func authorizedApplicationsHTML(apps []authorizedOAuthApplication, notice string, errorText string, canRevoke bool, localeAndTheme ...string) string {
	locale := settingsLocaleArg(localeAndTheme...)
	theme := settingsThemeArg(localeAndTheme...)
	applicationName := settingsApplicationNameArg(localeAndTheme)
	title := oauthT(locale, "doorkeeper.authorized_applications.index.title", "Your authorized applications")
	var body strings.Builder
	body.WriteString(settingsFlashHTML(notice, errorText))
	body.WriteString(`<p>` + oauthT(locale, "doorkeeper.authorized_applications.index.description_html", "These are applications that can access your account using the API. If there are applications you do not recognize here, or an application is misbehaving, you can revoke its access.") + `</p><hr class="spacer">`)
	if len(apps) == 0 {
		body.WriteString(`<div class="applications-list"><p class="muted-hint center-text">` + html.EscapeString(oauthT(locale, "doorkeeper.authorized_applications.index.empty", "No authorized applications")) + `</p></div>`)
		return settingsPageShell(title, settingsNavigationArg(localeAndTheme, locale), body.String(), locale, theme)
	}
	body.WriteString(`<div class="applications-list">`)
	for _, app := range apps {
		body.WriteString(`<div class="applications-list__item">`)
		if strings.TrimSpace(app.Website) != "" {
			body.WriteString(`<a class="announcements-list__item__title" href="` + html.EscapeString(app.Website) + `" target="_blank" rel="noopener noreferrer">` + html.EscapeString(app.Name) + `</a>`)
		} else {
			body.WriteString(`<strong class="announcements-list__item__title">` + html.EscapeString(app.Name))
			if app.Superapp {
				body.WriteString(` <span class="information-badge superapp">` + html.EscapeString(oauthT(locale, "doorkeeper.authorized_applications.index.superapp", "Internal")) + `</span>`)
			}
			body.WriteString(`</strong>`)
		}
		body.WriteString(`<div class="announcements-list__item__action-bar"><div class="announcements-list__item__meta">`)
		if app.LastUsedAt.Valid {
			body.WriteString(html.EscapeString(oauthT(locale, "doorkeeper.authorized_applications.index.last_used_at", "Last used on %{date}", map[string]string{"date": formatOptionalDate(locale, app.LastUsedAt.Time)})))
		} else {
			body.WriteString(html.EscapeString(oauthT(locale, "doorkeeper.authorized_applications.index.never_used", "Never used")))
		}
		body.WriteString(` · ` + html.EscapeString(oauthT(locale, "doorkeeper.authorized_applications.index.authorized_at", "Authorized on %{date}", map[string]string{"date": formatOptionalDate(locale, app.AuthorizedAt)})) + `</div>`)
		if canRevoke && !app.Superapp {
			body.WriteString(`<div><a class="table-action-link" data-method="delete" data-confirm="` + html.EscapeString(oauthT(locale, "doorkeeper.authorized_applications.confirmations.revoke", "Are you sure?")) + `" href="/oauth/authorized_applications/` + strconv.FormatInt(app.ID, 10) + `"><i class="fa fa-times fa-fw"></i> ` + html.EscapeString(oauthT(locale, "doorkeeper.authorized_applications.buttons.revoke", "Revoke")) + `</a></div>`)
		}
		body.WriteString(`</div>` + authorizedApplicationScopesHTML(app.Scopes, locale, applicationName) + `</div>`)
	}
	body.WriteString(`</div>`)
	return settingsPageShell(title, settingsNavigationArg(localeAndTheme, locale), body.String(), locale, theme)
}

func oauthT(locale string, key string, fallback string, vars ...map[string]string) string {
	value := webT(locale, key, vars...)
	if value == key || strings.TrimSpace(value) == "" {
		value = fallback
		if len(vars) > 0 {
			for name, replacement := range vars[0] {
				value = strings.ReplaceAll(value, "%{"+name+"}", replacement)
			}
		}
	}
	return value
}

func scopeList(scopes string) []string {
	fields := uniqueStrings(strings.Fields(scopes))
	if len(fields) == 0 {
		return []string{"none"}
	}
	return fields
}

func authorizedApplicationScopesHTML(scopes string, locale string, applicationNames ...string) string {
	applicationName := "Mastodon"
	if len(applicationNames) > 0 && strings.TrimSpace(applicationNames[0]) != "" {
		applicationName = applicationNames[0]
	}
	var b strings.Builder
	b.WriteString(`<div class="announcements-list__item__permissions"><ul class="permissions-list">`)
	for _, scope := range groupedOAuthScopes(scopes) {
		title := strings.ReplaceAll(oauthGroupedScopeTitle(scope.Key, locale), "Mastodon", applicationName)
		b.WriteString(`<li class="permissions-list__item"><div class="permissions-list__item__icon"><i class="fa fa-check fa-fw"></i></div><div class="permissions-list__item__text"><div class="permissions-list__item__text__title">` + html.EscapeString(title) + `</div><div class="permissions-list__item__text__type">` + html.EscapeString(oauthGroupedScopeAccess(scope.Access, locale)) + `</div></div></li>`)
	}
	b.WriteString(`</ul></div>`)
	return b.String()
}

type groupedOAuthScope struct {
	Key    string
	Access string
}

func groupedOAuthScopes(scopes string) []groupedOAuthScope {
	type accessSet map[string]struct{}
	groups := map[string]accessSet{}
	order := []string{}
	for _, raw := range scopeList(scopes) {
		key, accesses := parseOAuthScopeGroup(raw)
		if _, ok := groups[key]; !ok {
			groups[key] = accessSet{}
			order = append(order, key)
		}
		for _, access := range accesses {
			groups[key][access] = struct{}{}
		}
	}
	out := make([]groupedOAuthScope, 0, len(order))
	for _, key := range order {
		out = append(out, groupedOAuthScope{Key: key, Access: oauthScopeAccessString(groups[key])})
	}
	return out
}

func parseOAuthScopeGroup(scope string) (string, []string) {
	scope = strings.TrimSpace(scope)
	if scope == "" || scope == "none" {
		return "none", []string{"read"}
	}
	parts := strings.Split(scope, ":")
	namespace := ""
	if len(parts) > 0 && parts[0] == "admin" {
		namespace = "admin"
		parts = parts[1:]
	}
	accesses := []string{"read", "write"}
	term := "all"
	if len(parts) > 0 && (parts[0] == "read" || parts[0] == "write") {
		accesses = []string{parts[0]}
		parts = parts[1:]
	}
	if len(parts) > 0 && strings.TrimSpace(parts[0]) != "" {
		term = parts[0]
	}
	if namespace != "" {
		return namespace + "/" + term, accesses
	}
	return term, accesses
}

func oauthScopeAccessString(accesses map[string]struct{}) string {
	_, read := accesses["read"]
	_, write := accesses["write"]
	switch {
	case read && write:
		return "read/write"
	case write:
		return "write"
	default:
		return "read"
	}
}

func oauthGroupedScopeTitle(scope string, locale string) string {
	if scope == "none" {
		return oauthT(locale, "doorkeeper.grouped_scopes.title.none", "No permissions")
	}
	return oauthT(locale, "doorkeeper.grouped_scopes.title."+scope, oauthScopeTitle(scope, locale))
}

func oauthGroupedScopeAccess(access string, locale string) string {
	return oauthT(locale, "doorkeeper.grouped_scopes.access."+access, access)
}

func oauthScopeTitle(scope string, locale string) string {
	if scope == "none" {
		return oauthT(locale, "doorkeeper.grouped_scopes.title.none", "No permissions")
	}
	return oauthT(locale, "doorkeeper.scopes."+scope, scope)
}

func formatOptionalDate(locale string, value time.Time) string {
	if value.IsZero() {
		return "unknown"
	}
	value = value.UTC()
	format := settingsT(locale, "date.formats.default", "%b %d, %Y")
	return strings.NewReplacer(
		"%Y", value.Format("2006"),
		"%m", value.Format("01"),
		"%d", value.Format("02"),
		"%e", value.Format("_2"),
		"%b", value.Format("Jan"),
		"%B", value.Format("January"),
	).Replace(format)
}
