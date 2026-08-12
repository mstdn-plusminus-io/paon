package api

import (
	"crypto/rand"
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

const inviteCodeAlphabet = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"

func (s *Server) invitesPage(c *echo.Context) error {
	setPrivateNoStoreCacheHeaders(c)
	account, _, user, err := s.requireFunctionalAccountForWeb(c)
	if err != nil {
		return webAuthResponseError(err)
	}
	locale := s.webLocale(c, user)
	theme := settingsWebTheme(decodeUserSettings(user.Settings.String))
	if !s.userCan(user, rolePermissionInviteUsers) {
		return c.HTML(http.StatusForbidden, authPageHTML(settingsT(locale, "invites.title", "Invites"), "", settingsT(locale, "invites.not_permitted", "You are not allowed to invite users."), "", locale, theme))
	}
	invites, err := s.userInvites(user.ID)
	if err != nil {
		return err
	}
	navigation, err := s.settingsNavigationForUser(c.Request().URL.Path, locale, user, account)
	if err != nil {
		return err
	}
	return c.HTML(http.StatusOK, invitesHTML(s.cfg.BaseURL(), invites, c.QueryParam("error"), locale, theme, navigation))
}

func (s *Server) createInvite(c *echo.Context) error {
	setPrivateNoStoreCacheHeaders(c)
	_, _, user, err := s.requireFunctionalAccountForWeb(c)
	if err != nil {
		return webAuthResponseError(err)
	}
	locale := s.webLocale(c, user)
	theme := settingsWebTheme(decodeUserSettings(user.Settings.String))
	if !s.userCan(user, rolePermissionInviteUsers) {
		return c.HTML(http.StatusForbidden, authPageHTML(settingsT(locale, "invites.title", "Invites"), "", settingsT(locale, "invites.not_permitted", "You are not allowed to invite users."), "", locale, theme))
	}
	invite, err := inviteFromRequest(c, user.ID, time.Now().UTC())
	if err != nil {
		if errors.Is(err, errInviteParamsMissing) {
			return apiError(c, http.StatusBadRequest, "Malformed request")
		}
		return s.renderInviteError(c, user, inviteErrorText(locale, err), locale, theme)
	}
	if s.db == nil {
		return c.Redirect(http.StatusFound, "/invites?error="+url.QueryEscape(settingsDatabaseUnavailableMessage(locale)))
	}
	if err := s.assignUniqueInviteCode(&invite); err != nil {
		return err
	}
	if err := s.db.Create(&invite).Error; err != nil {
		return err
	}
	return c.Redirect(http.StatusFound, "/invites")
}

func (s *Server) destroyInvite(c *echo.Context) error {
	setPrivateNoStoreCacheHeaders(c)
	_, _, user, err := s.requireFunctionalAccountForWeb(c)
	if err != nil {
		return webAuthResponseError(err)
	}
	locale := s.webLocale(c, user)
	theme := settingsWebTheme(decodeUserSettings(user.Settings.String))
	if !s.userCan(user, rolePermissionInviteUsers) {
		return c.HTML(http.StatusForbidden, authPageHTML(settingsT(locale, "invites.title", "Invites"), "", settingsT(locale, "invites.not_permitted", "You are not allowed to invite users."), "", locale, theme))
	}
	if strings.EqualFold(c.FormValue("_method"), "delete") || c.Request().Method == http.MethodDelete {
		now := time.Now().UTC()
		if s.db == nil {
			return c.Redirect(http.StatusFound, "/invites?error="+url.QueryEscape(settingsDatabaseUnavailableMessage(locale)))
		}
		err := s.db.Model(&models.Invite{}).
			Where("id = ? AND user_id = ?", c.Param("id"), user.ID).
			Updates(map[string]any{"expires_at": now, "updated_at": now}).Error
		if err != nil {
			return err
		}
	}
	return c.Redirect(http.StatusFound, "/invites")
}

func (s *Server) adminInvitesPage(c *echo.Context) error {
	user, _, handled, err := s.requireFunctionalWebUser(c)
	if handled || err != nil {
		return err
	}
	locale := s.webLocale(c, user)
	theme := settingsWebTheme(decodeUserSettings(user.Settings.String))
	if !s.userCan(user, rolePermissionManageInvites) {
		return c.HTML(http.StatusForbidden, authPageHTML(adminT(locale, "admin.invites.title", "Admin invites"), "", adminT(locale, "admin.invites.not_permitted", "You are not allowed to manage invites."), "", locale, theme))
	}
	invites, err := s.adminInvites(c)
	if err != nil {
		return err
	}
	filters := adminInviteFilters{
		Page:      adminTrendsPageValue(c),
		Available: c.QueryParam("available"),
		Expired:   c.QueryParam("expired"),
	}
	return c.HTML(http.StatusOK, adminInvitesHTML(s.cfg.BaseURL(), invites, c.QueryParam("error"), s.userCan(user, rolePermissionInviteUsers), filters, locale, theme))
}

func (s *Server) createAdminInvite(c *echo.Context) error {
	user, _, handled, err := s.requireFunctionalWebUser(c)
	if handled || err != nil {
		return err
	}
	locale := s.webLocale(c, user)
	theme := settingsWebTheme(decodeUserSettings(user.Settings.String))
	if !s.userCan(user, rolePermissionInviteUsers) {
		return c.HTML(http.StatusForbidden, authPageHTML(adminT(locale, "admin.invites.title", "Admin invites"), "", settingsT(locale, "invites.not_permitted", "You are not allowed to invite users."), "", locale, theme))
	}
	invite, err := adminInviteFromRequest(c, user.ID, time.Now().UTC())
	if err != nil {
		if errors.Is(err, errInviteParamsMissing) {
			return apiError(c, http.StatusBadRequest, "Malformed request")
		}
		return s.renderAdminInviteError(c, user, inviteErrorText(locale, err), locale, theme)
	}
	if s.db == nil {
		return c.Redirect(http.StatusFound, "/admin/invites?error="+url.QueryEscape(settingsDatabaseUnavailableMessage(locale)))
	}
	if err := s.assignUniqueInviteCode(&invite); err != nil {
		return err
	}
	if err := s.db.Create(&invite).Error; err != nil {
		return err
	}
	return c.Redirect(http.StatusFound, "/admin/invites")
}

func (s *Server) destroyAdminInvite(c *echo.Context) error {
	user, _, handled, err := s.requireFunctionalWebUser(c)
	if handled || err != nil {
		return err
	}
	locale := s.webLocale(c, user)
	theme := settingsWebTheme(decodeUserSettings(user.Settings.String))
	if strings.EqualFold(c.FormValue("_method"), "delete") || c.Request().Method == http.MethodDelete {
		canManageInvites := s.userCan(user, rolePermissionManageInvites)
		if !canManageInvites {
			invite, err := s.findInviteByID(c.Param("id"))
			if err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return c.Redirect(http.StatusFound, "/admin/invites")
				}
				return err
			}
			if invite.UserID != user.ID {
				return c.HTML(http.StatusForbidden, authPageHTML(adminT(locale, "admin.invites.title", "Admin invites"), "", adminT(locale, "admin.invites.not_permitted", "You are not allowed to manage invites."), "", locale, theme))
			}
		}
		if err := s.expireInvite(c.Param("id"), time.Now().UTC()); err != nil {
			return err
		}
	}
	return c.Redirect(http.StatusFound, "/admin/invites")
}

func (s *Server) deactivateAllAdminInvites(c *echo.Context) error {
	user, _, handled, err := s.requireFunctionalWebUser(c)
	if handled || err != nil {
		return err
	}
	locale := s.webLocale(c, user)
	theme := settingsWebTheme(decodeUserSettings(user.Settings.String))
	if !s.userCan(user, rolePermissionManageInvites) {
		return c.HTML(http.StatusForbidden, authPageHTML(adminT(locale, "admin.invites.title", "Admin invites"), "", adminT(locale, "admin.invites.not_permitted", "You are not allowed to manage invites."), "", locale, theme))
	}
	if s.db == nil {
		return c.Redirect(http.StatusFound, "/admin/invites?error="+url.QueryEscape(settingsDatabaseUnavailableMessage(locale)))
	}
	now := time.Now().UTC()
	if err := s.db.Model(&models.Invite{}).
		Where(inviteAvailableSQL(), now).
		Updates(map[string]any{"expires_at": now, "updated_at": now}).Error; err != nil {
		return err
	}
	return c.Redirect(http.StatusFound, "/admin/invites")
}

func (s *Server) userInvites(userID int64) ([]models.Invite, error) {
	if s.db == nil {
		return []models.Invite{}, nil
	}
	var invites []models.Invite
	err := s.db.Where("user_id = ?", userID).Order("id DESC").Find(&invites).Error
	return invites, err
}

func (s *Server) adminInvites(c *echo.Context) ([]models.Invite, error) {
	if s.db == nil {
		return []models.Invite{}, nil
	}
	query := s.db.Preload("User.Account").Order("invites.id DESC").
		Offset(adminRailsPageOffset(c)).
		Limit(adminRailsDefaultPageSize)
	now := time.Now().UTC()
	if strings.TrimSpace(c.QueryParam("available")) != "" {
		query = query.Where(inviteAvailableSQL(), now)
	}
	if strings.TrimSpace(c.QueryParam("expired")) != "" {
		query = query.Where("expires_at IS NOT NULL AND expires_at < ?", now)
	}
	var invites []models.Invite
	err := query.Find(&invites).Error
	return invites, err
}

func (s *Server) expireInvite(id string, now time.Time) error {
	if s.db == nil {
		return nil
	}
	return s.db.Model(&models.Invite{}).
		Where("id = ?", id).
		Updates(map[string]any{"expires_at": now, "updated_at": now}).Error
}

func (s *Server) findInviteByID(id string) (*models.Invite, error) {
	if s.db == nil {
		return nil, gorm.ErrRecordNotFound
	}
	var invite models.Invite
	if err := s.db.Where("id = ?", id).First(&invite).Error; err != nil {
		return nil, err
	}
	return &invite, nil
}

func inviteFromRequest(c *echo.Context, userID int64, now time.Time) (models.Invite, error) {
	if !railsNestedFormRootPresent(c, "invite") {
		return models.Invite{}, errInviteParamsMissing
	}
	comment := c.FormValue("invite[comment]")
	if len([]rune(comment)) > 420 {
		return models.Invite{}, errInviteCommentTooLong
	}
	invite := models.Invite{
		UserID:     userID,
		Uses:       0,
		CreatedAt:  now,
		UpdatedAt:  now,
		Autofollow: formBoolValue(c.FormValue("invite[autofollow]")),
	}
	if c.Request().PostForm.Has("invite[comment]") || comment != "" {
		invite.Comment = sql.NullString{String: comment, Valid: true}
	}
	if maxUses := strings.TrimSpace(c.FormValue("invite[max_uses]")); maxUses != "" {
		value := railsToInt64(maxUses)
		invite.MaxUses = sql.NullInt64{Int64: value, Valid: true}
	}
	if expiresIn := strings.TrimSpace(c.FormValue("invite[expires_in]")); expiresIn != "" {
		value := railsToInt64(expiresIn)
		invite.ExpiresAt = sql.NullTime{Time: now.Add(time.Duration(value) * time.Second), Valid: true}
	}
	return invite, nil
}

func adminInviteFromRequest(c *echo.Context, userID int64, now time.Time) (models.Invite, error) {
	if !railsNestedFormRootPresent(c, "invite") {
		return models.Invite{}, errInviteParamsMissing
	}
	invite := models.Invite{
		UserID:     userID,
		Uses:       0,
		CreatedAt:  now,
		UpdatedAt:  now,
		Autofollow: formBoolValue(c.FormValue("invite[autofollow]")),
	}
	if maxUses := strings.TrimSpace(c.FormValue("invite[max_uses]")); maxUses != "" {
		value := railsToInt64(maxUses)
		invite.MaxUses = sql.NullInt64{Int64: value, Valid: true}
	}
	if expiresIn := strings.TrimSpace(c.FormValue("invite[expires_in]")); expiresIn != "" {
		value := railsToInt64(expiresIn)
		invite.ExpiresAt = sql.NullTime{Time: now.Add(time.Duration(value) * time.Second), Valid: true}
	}
	return invite, nil
}

func (s *Server) renderInviteError(c *echo.Context, user *models.User, errorText string, locale string, theme string) error {
	invites, err := s.userInvites(user.ID)
	if err != nil {
		return err
	}
	navigation, err := s.settingsNavigationForUser(c.Request().URL.Path, locale, user, nil)
	if err != nil {
		return err
	}
	return c.HTML(http.StatusOK, invitesHTML(s.cfg.BaseURL(), invites, errorText, locale, theme, navigation))
}

func (s *Server) renderAdminInviteError(c *echo.Context, user *models.User, errorText string, locale string, theme string) error {
	invites, err := s.adminInvites(c)
	if err != nil {
		return err
	}
	filters := adminInviteFilters{
		Page:      adminTrendsPageValue(c),
		Available: c.QueryParam("available"),
		Expired:   c.QueryParam("expired"),
	}
	return c.HTML(http.StatusOK, adminInvitesHTML(s.cfg.BaseURL(), invites, errorText, true, filters, locale, theme))
}

func (s *Server) assignUniqueInviteCode(invite *models.Invite) error {
	for i := 0; i < 20; i++ {
		code, err := randomInviteCode()
		if err != nil {
			return err
		}
		var count int64
		if err := s.db.Model(&models.Invite{}).Where("code = ?", code).Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			invite.Code = code
			return nil
		}
	}
	return gorm.ErrDuplicatedKey
}

func randomInviteCode() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	var out strings.Builder
	out.Grow(8)
	for _, b := range buf {
		out.WriteByte(inviteCodeAlphabet[int(b)%len(inviteCodeAlphabet)])
	}
	return out.String(), nil
}

func inviteExpired(invite models.Invite, now time.Time) bool {
	return invite.ExpiresAt.Valid && invite.ExpiresAt.Time.Before(now)
}

func inviteAvailableSQL() string {
	return "expires_at IS NULL OR expires_at >= ?"
}

func inviteUsesText(invite models.Invite) string {
	if invite.MaxUses.Valid {
		return strconv.FormatInt(invite.Uses, 10) + "/" + strconv.FormatInt(invite.MaxUses.Int64, 10)
	}
	return strconv.FormatInt(invite.Uses, 10)
}

func invitesHTML(baseURL string, invites []models.Invite, errorText string, locales ...string) string {
	locale := settingsLocaleArgOrEnglish(locales...)
	theme := settingsThemeArg(locales...)
	now := time.Now().UTC()
	var rows strings.Builder
	for _, invite := range invites {
		rows.WriteString(inviteTableRowHTML(baseURL, invite, "/invites/", false, now, locale))
	}
	body := settingsFlashHTML("", errorText) + `<p>` + html.EscapeString(settingsT(locale, "invites.prompt", "Generate and share links with others to grant access to this server")) + `</p>` +
		inviteFormHTML("/invites", locale) + `<hr class="spacer"><div class="table-wrapper simple_form"><table class="table table--invites">
      <thead><tr><th></th><th>` + html.EscapeString(settingsT(locale, "invites.table.uses", "Uses")) + `</th><th>` + html.EscapeString(settingsT(locale, "invites.table.expires_at", "Expires")) + `</th><th></th></tr></thead>
      <tbody>` + rows.String() + `</tbody>
    </table></div>`
	return settingsPageShell(settingsT(locale, "invites.title", "Invites"), settingsNavigationArg(locales, locale), body, locale, theme)
}

type adminInviteFilters struct {
	Page      string
	Available string
	Expired   string
}

func adminInvitesHTML(baseURL string, invites []models.Invite, errorText string, canCreate bool, filters adminInviteFilters, locales ...string) string {
	locale := settingsLocaleArgOrEnglish(locales...)
	theme := settingsThemeArg(locales...)
	now := time.Now().UTC()
	var rows strings.Builder
	for _, invite := range invites {
		owner := invite.User.Email
		if invite.User.Account != nil && invite.User.Account.Username != "" {
			owner = invite.User.Account.Acct()
		}
		rows.WriteString(inviteTableRowHTML(baseURL, invite, "/admin/invites/", true, now, locale, owner))
	}
	formHTML := ""
	if canCreate {
		formHTML = `<p>` + html.EscapeString(settingsT(locale, "invites.prompt", "Create a new invite link.")) + `</p>` + inviteFormHTML("/admin/invites", locale) + `<hr class="spacer">`
	}
	body := settingsFlashHTML("", errorText) + `<div class="filters">` + relationshipFilterSubsetHTML(adminT(locale, "admin.invites.filter.title", "Filter"), []relationshipFilterLink{
		{Label: adminT(locale, "admin.invites.filter.all", "All"), Href: "/admin/invites", Active: filters.Available == "" && filters.Expired == ""},
		{Label: adminT(locale, "admin.invites.filter.available", "Available"), Href: "/admin/invites?available=1", Active: filters.Available == "1"},
		{Label: adminT(locale, "admin.invites.filter.expired", "Expired"), Href: "/admin/invites?expired=1", Active: filters.Expired == "1"},
	}) + `</div>
    <hr class="spacer">` + formHTML + `
    <div class="table-wrapper simple_form"><table class="table table--invites">
      <thead><tr><th></th><th></th><th>` + html.EscapeString(settingsT(locale, "invites.table.uses", "Uses")) + `</th><th>` + html.EscapeString(settingsT(locale, "invites.table.expires_at", "Expires")) + `</th><th></th></tr></thead>
      <tbody>` + rows.String() + `</tbody>
    </table></div>
	    ` + adminInvitesPaginationHTML(filters, len(invites) == adminRailsDefaultPageSize, locale) +
		`<a class="button" href="/admin/invites/deactivate_all" data-method="post" data-confirm="` + html.EscapeString(adminT(locale, "admin.accounts.are_you_sure", "Are you sure?")) + `">` + html.EscapeString(adminT(locale, "admin.invites.deactivate_all", "Deactivate all available invites")) + `</a>`
	return authPageHTML(adminT(locale, "admin.invites.title", "Admin invites"), "", "", body, locale, theme)
}

func inviteFormHTML(action string, locale string) string {
	return `<form class="simple_form new_invite" id="new_invite" novalidate="novalidate" method="post" action="` + html.EscapeString(action) + `">
      <div class="fields-row">
        <div class="fields-row__column fields-row__column-6 fields-group"><div class="input with_label select optional invite_max_uses"><div class="label_input"><label class="select optional" for="invite_max_uses">` + html.EscapeString(settingsT(locale, "simple_form.labels.defaults.max_uses", "Max number of uses")) + `</label><div class="label_input__wrapper"><select class="select optional" id="invite_max_uses" name="invite[max_uses]">` + inviteMaxUsesOptionsHTML(locale) + `</select></div></div></div></div>
        <div class="fields-row__column fields-row__column-6 fields-group"><div class="input with_label select optional invite_expires_in"><div class="label_input"><label class="select optional" for="invite_expires_in">` + html.EscapeString(settingsT(locale, "simple_form.labels.defaults.expires_in", "Expire after")) + `</label><div class="label_input__wrapper"><select class="select optional" id="invite_expires_in" name="invite[expires_in]">` + inviteExpiresOptionsHTML(locale) + `</select></div></div></div></div>
      </div>
      <div class="fields-group"><div class="input with_label boolean optional invite_autofollow field_with_hint"><div class="label_input"><label class="boolean optional" for="invite_autofollow">` + html.EscapeString(settingsT(locale, "simple_form.labels.defaults.autofollow", "Invite to follow your account")) + `</label><div class="label_input__wrapper"><input type="hidden" name="invite[autofollow]" value="0" autocomplete="off"><label class="checkbox"><input class="boolean optional" type="checkbox" value="1" name="invite[autofollow]" id="invite_autofollow"></label></div></div><span class="hint">` + html.EscapeString(settingsT(locale, "simple_form.hints.defaults.autofollow", "People who sign up through the invite will automatically follow you")) + `</span></div></div>
	  <div class="actions"><button name="button" type="submit" class="button">` + html.EscapeString(settingsT(locale, "invites.generate", "Generate invite link")) + `</button></div>
    </form>`
}

func inviteTableRowHTML(baseURL string, invite models.Invite, actionPrefix string, includeOwner bool, now time.Time, locale string, ownerValue ...string) string {
	link := strings.TrimRight(baseURL, "/") + "/invite/" + url.PathEscape(invite.Code)
	available := !inviteUnavailable(invite, now)
	var row strings.Builder
	row.WriteString(`<tr><td><div class="input-copy"><div class="input-copy__wrapper"><input type="text" maxlength="999" spellcheck="false" readonly="true" value="` + html.EscapeString(link) + `"></div><button type="button">` + html.EscapeString(settingsT(locale, "generic.copy", "Copy")) + `</button></div></td>`)
	if includeOwner {
		owner := ""
		if len(ownerValue) > 0 {
			owner = ownerValue[0]
		}
		avatar := "/avatars/original/missing.png"
		if invite.User.Account != nil {
			avatar = statusEmbedAccountAvatarURL(baseURL, *invite.User.Account)
		}
		row.WriteString(`<td><div class="name-tag"><img src="` + html.EscapeString(avatar) + `" alt="" width="16" height="16" class="avatar"><span class="username">` + html.EscapeString(owner) + `</span></div></td>`)
	}
	if available {
		row.WriteString(`<td><i class="fa fa-user fa-fw"></i> ` + html.EscapeString(inviteUsesText(invite)) + `</td><td>`)
		if invite.ExpiresAt.Valid {
			stamp := invite.ExpiresAt.Time.UTC().Format(time.RFC3339)
			row.WriteString(`<time class="formatted" datetime="` + html.EscapeString(stamp) + `" title="` + html.EscapeString(stamp) + `">` + html.EscapeString(stamp) + `</time>`)
		} else {
			row.WriteString(`∞`)
		}
		row.WriteString(`</td>`)
	} else {
		row.WriteString(`<td colspan="2">` + html.EscapeString(settingsT(locale, "invites.expired", "Expired")) + `</td>`)
	}
	row.WriteString(`<td>`)
	if available {
		row.WriteString(`<a class="table-action-link" href="` + html.EscapeString(actionPrefix) + strconv.FormatInt(invite.ID, 10) + `" data-method="delete"><i class="fa fa-times fa-fw"></i> ` + html.EscapeString(settingsT(locale, "invites.delete", "Deactivate")) + `</a>`)
	}
	row.WriteString(`</td></tr>`)
	return row.String()
}

func adminInvitesPaginationHTML(filters adminInviteFilters, hasNext bool, locale ...string) string {
	loc := settingsLocaleArgOrEnglish(locale...)
	pageNum, err := strconv.Atoi(strings.TrimSpace(filters.Page))
	if err != nil || pageNum < 1 {
		pageNum = 1
	}
	var links []string
	if pageNum > 1 {
		params := adminInvitePageParams(filters, pageNum-1)
		links = append(links, `<a href="/admin/invites?`+html.EscapeString(params.Encode())+`">`+html.EscapeString(settingsT(loc, "pagination.previous", "Previous"))+`</a>`)
	}
	if hasNext {
		params := adminInvitePageParams(filters, pageNum+1)
		links = append(links, `<a href="/admin/invites?`+html.EscapeString(params.Encode())+`">`+html.EscapeString(settingsT(loc, "pagination.next", "Next"))+`</a>`)
	}
	if len(links) == 0 {
		return ""
	}
	return `<nav class="pagination">` + strings.Join(links, " ") + `</nav>`
}

func adminInvitePageParams(filters adminInviteFilters, page int) url.Values {
	params := url.Values{"page": []string{strconv.Itoa(page)}}
	if strings.TrimSpace(filters.Available) != "" {
		params.Set("available", strings.TrimSpace(filters.Available))
	}
	if strings.TrimSpace(filters.Expired) != "" {
		params.Set("expired", strings.TrimSpace(filters.Expired))
	}
	return params
}

func inviteExpiresOptionsHTML(locale string) string {
	options := []struct {
		Value string
		Key   string
		Text  string
	}{
		{"", "invites.expires_in_prompt", "Never"},
		{"1800", "invites.expires_in.1800", "30 minutes"},
		{"3600", "invites.expires_in.3600", "1 hour"},
		{"21600", "invites.expires_in.21600", "6 hours"},
		{"43200", "invites.expires_in.43200", "12 hours"},
		{"86400", "invites.expires_in.86400", "1 day"},
		{"604800", "invites.expires_in.604800", "1 week"},
	}
	var out strings.Builder
	for _, option := range options {
		out.WriteString(`<option value="`)
		out.WriteString(html.EscapeString(option.Value))
		out.WriteString(`">`)
		out.WriteString(html.EscapeString(settingsT(locale, option.Key, option.Text)))
		out.WriteString(`</option>`)
	}
	return out.String()
}

func inviteMaxUsesOptionsHTML(locale string) string {
	var out strings.Builder
	out.WriteString(`<option value="">` + html.EscapeString(settingsT(locale, "invites.max_uses_prompt", "No limit")) + `</option>`)
	for _, count := range []int{1, 5, 10, 25, 50, 100} {
		key := "invites.max_uses.other"
		fallback := "%{count} uses"
		if count == 1 && invitePluralKeyExists(locale, "invites.max_uses.one") {
			key = "invites.max_uses.one"
			fallback = "1 use"
		}
		label := settingsT(locale, key, fallback)
		label = strings.ReplaceAll(label, "%{count}", strconv.Itoa(count))
		out.WriteString(`<option value="` + strconv.Itoa(count) + `">` + html.EscapeString(label) + `</option>`)
	}
	return out.String()
}

func invitePluralKeyExists(locale string, key string) bool {
	if store := currentWebI18n(); store != nil {
		_, ok := store.Dict(locale)[key]
		return ok
	}
	return strings.HasPrefix(strings.ToLower(locale), "en")
}

func inviteUnavailable(invite models.Invite, now time.Time) bool {
	return inviteExpired(invite, now) || (invite.MaxUses.Valid && invite.Uses >= invite.MaxUses.Int64)
}

type inviteInputError string

func (e inviteInputError) Error() string { return string(e) }

const (
	errInviteCommentTooLong    inviteInputError = "Comment is too long"
	errInviteInvalidMaxUses    inviteInputError = "Max uses is invalid"
	errInviteInvalidExpiration inviteInputError = "Expiration is invalid"
)

var errInviteParamsMissing = errors.New("invite root parameter is missing")

func inviteErrorText(locale string, err error) string {
	switch err {
	case errInviteCommentTooLong:
		return settingsT(locale, "invites.errors.comment_too_long", err.Error())
	case errInviteInvalidMaxUses:
		return settingsT(locale, "invites.errors.invalid_max_uses", err.Error())
	case errInviteInvalidExpiration:
		return settingsT(locale, "invites.errors.invalid_expiration", err.Error())
	default:
		return err.Error()
	}
}
