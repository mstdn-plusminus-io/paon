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
	"golang.org/x/net/idna"
	"gorm.io/gorm"
)

func (s *Server) settingsAliasesPage(c *echo.Context) error {
	account, _, user, err := s.currentAccountForWeb(c)
	if err != nil {
		return redirectToSignIn(c)
	}
	if account.SuspendedAt.Valid {
		return apiError(c, http.StatusForbidden, "This account is suspended")
	}
	aliases, err := s.accountAliases(account.ID)
	if err != nil {
		return err
	}
	locale := s.webLocale(c, user)
	theme := settingsWebTheme(decodeUserSettings(user.Settings.String))
	renderArgs, err := s.settingsRenderArgs(c.Request().URL.Path, locale, theme, user, account)
	if err != nil {
		return err
	}
	return c.HTML(http.StatusOK, aliasesHTML(aliases, c.QueryParam("notice"), c.QueryParam("error"), renderArgs...))
}

func (s *Server) createSettingsAlias(c *echo.Context) error {
	account, _, user, err := s.currentAccountForWeb(c)
	if err != nil {
		return redirectToSignIn(c)
	}
	locale := s.webLocale(c, user)
	if account.SuspendedAt.Valid {
		return apiError(c, http.StatusForbidden, "This account is suspended")
	}
	acct, err := settingsAliasAcct(c)
	if errors.Is(err, errSettingsAliasParamsMissing) {
		return apiError(c, http.StatusBadRequest, "Malformed request")
	}
	if err != nil {
		return err
	}
	uri, err := s.aliasURIForAcct(acct, account)
	if err != nil {
		return s.renderSettingsAliasError(c, account.ID, user, aliasErrorText(locale, err), locale)
	}
	now := time.Now().UTC()
	alias := models.AccountAlias{AccountID: models.AccountAliasAccountID(account.ID), Acct: acct, URI: uri, CreatedAt: now, UpdatedAt: now}
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		var existing models.AccountAlias
		err := tx.Where("account_id = ? AND uri = ?", account.ID, uri).First(&existing).Error
		if err == nil {
			return errAliasAlreadyExists
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if err := tx.Create(&alias).Error; err != nil {
			return err
		}
		return updateAccountAlsoKnownAs(tx, account.ID, uri, true)
	}); err != nil {
		if _, ok := err.(aliasInputError); ok {
			return s.renderSettingsAliasError(c, account.ID, user, aliasErrorText(locale, err), locale)
		}
		return err
	}
	s.triggerAccountWebhook("account.updated", account.ID)
	_ = s.enqueueFASPAccountLifecycleByID(c.Request().Context(), account.ID, "update")
	_ = s.enqueueActivityPubAccountUpdate(*account, 0)
	return c.Redirect(http.StatusFound, "/settings/aliases?notice="+url.QueryEscape(settingsT(locale, "aliases.created_msg", "Alias added")))
}

func (s *Server) renderSettingsAliasError(c *echo.Context, accountID int64, user *models.User, message string, locale string) error {
	aliases, err := s.accountAliases(accountID)
	if err != nil {
		return err
	}
	theme := settingsWebTheme(decodeUserSettings(user.Settings.String))
	renderArgs, err := s.settingsRenderArgs(c.Request().URL.Path, locale, theme, user, nil)
	if err != nil {
		return err
	}
	return c.HTML(http.StatusOK, aliasesHTML(aliases, "", message, renderArgs...))
}

func (s *Server) destroySettingsAlias(c *echo.Context) error {
	account, _, user, err := s.currentAccountForWeb(c)
	if err != nil {
		return redirectToSignIn(c)
	}
	locale := s.webLocale(c, user)
	if account.SuspendedAt.Valid {
		return apiError(c, http.StatusForbidden, "This account is suspended")
	}
	if strings.EqualFold(c.FormValue("_method"), "delete") || c.Request().Method == http.MethodDelete {
		var alias models.AccountAlias
		if err := s.db.Where("id = ? AND account_id = ?", c.Param("id"), account.ID).First(&alias).Error; err != nil {
			return apiError(c, http.StatusNotFound, "Record not found")
		}
		if err := s.db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Delete(&models.AccountAlias{}, alias.ID).Error; err != nil {
				return err
			}
			return updateAccountAlsoKnownAs(tx, account.ID, alias.URI, false)
		}); err != nil {
			return err
		}
		s.triggerAccountWebhook("account.updated", account.ID)
		_ = s.enqueueFASPAccountLifecycleByID(c.Request().Context(), account.ID, "update")
	}
	return c.Redirect(http.StatusFound, "/settings/aliases?notice="+url.QueryEscape(settingsT(locale, "aliases.deleted_msg", "Alias removed")))
}

func (s *Server) accountAliases(accountID int64) ([]models.AccountAlias, error) {
	if s.db == nil {
		return []models.AccountAlias{}, nil
	}
	var aliases []models.AccountAlias
	err := s.db.Where("account_id = ?", accountID).Order("id DESC").Find(&aliases).Error
	return aliases, err
}

var errSettingsAliasParamsMissing = errors.New("settings alias root parameter is missing")

func settingsAliasAcct(c *echo.Context) (string, error) {
	req := c.Request()
	if err := req.ParseForm(); err != nil {
		return "", err
	}
	const prefix = "account_alias"
	if !formHasNestedPrefix(req.Form, prefix) {
		return "", errSettingsAliasParamsMissing
	}
	return normalizeAliasAcct(lastFormValue(req.Form, prefix+"[acct]")), nil
}

func (s *Server) aliasURIForAcct(acct string, current *models.Account) (string, error) {
	if acct == "" || !strings.Contains(acct, "@") {
		return "", errAliasInvalidAcct
	}
	if strings.EqualFold(acct, current.Acct()) || strings.EqualFold(acct, current.Username+"@"+s.cfg.LocalDomain) {
		return "", errAliasMoveToSelf
	}
	if account, err := s.findAccountByAcct(acct); err == nil {
		uri := activityPubActorURL(s, *account)
		if aliasURIIsCurrentAccount(s, current, account, uri) {
			return "", errAliasMoveToSelf
		}
		return uri, nil
	}
	remoteAcct, ok := s.importRemoteAcct(acct)
	if !ok {
		return "", errAliasInvalidAcct
	}
	account, err := s.fetchAndStoreActivityActorForAcct(remoteAcct)
	if err != nil || !accountMatchesImportAcct(account, remoteAcct) {
		return "", errAliasNotFound
	}
	uri := activityPubActorURL(s, *account)
	if aliasURIIsCurrentAccount(s, current, account, uri) {
		return "", errAliasMoveToSelf
	}
	return uri, nil
}

func aliasURIIsCurrentAccount(s *Server, current *models.Account, target *models.Account, uri string) bool {
	if current == nil {
		return false
	}
	if target != nil && current.ID != 0 && target.ID == current.ID {
		return true
	}
	return strings.EqualFold(uri, activityPubActorURL(s, *current))
}

func updateAccountAlsoKnownAs(tx *gorm.DB, accountID int64, uri string, add bool) error {
	var account models.Account
	if err := tx.Select("id", "also_known_as").Where("id = ?", accountID).First(&account).Error; err != nil {
		return err
	}
	values := accountAlsoKnownAsAfterAliasChange(account.AlsoKnownAs, uri, add)
	return tx.Model(&models.Account{}).Where("id = ?", accountID).Updates(map[string]any{
		"also_known_as": models.StringArray(values),
		"updated_at":    time.Now().UTC(),
	}).Error
}

func accountAlsoKnownAsAfterAliasChange(existing []string, uri string, add bool) []string {
	values := append([]string{}, existing...)
	if add {
		return append(values, uri)
	}
	values = values[:0]
	for _, value := range existing {
		if value != uri {
			values = append(values, value)
		}
	}
	return values
}

func normalizeAliasAcct(value string) string {
	return strings.TrimPrefix(strings.TrimSpace(value), "@")
}

func accountAliasPrettyAcct(acct string) string {
	acct = strings.TrimSpace(acct)
	username, domain, ok := strings.Cut(acct, "@")
	if !ok {
		return acct
	}
	unicodeDomain, err := idna.Lookup.ToUnicode(domain)
	if err != nil || strings.TrimSpace(unicodeDomain) == "" {
		unicodeDomain = domain
	}
	return username + "@" + unicodeDomain
}

func aliasesHTML(aliases []models.AccountAlias, notice string, errorText string, localeAndTheme ...string) string {
	loc := settingsLocaleArgOrEnglish(localeAndTheme...)
	var rows strings.Builder
	for _, alias := range aliases {
		rows.WriteString(`<tr><td>`)
		rows.WriteString(html.EscapeString(accountAliasPrettyAcct(alias.Acct)))
		rows.WriteString(`</td><td><a class="table-action-link" data-method="delete" href="/settings/aliases/`)
		rows.WriteString(strconv.FormatInt(alias.ID, 10))
		rows.WriteString(`"><i class="fa fa-trash fa-fw"></i> `)
		rows.WriteString(html.EscapeString(settingsT(loc, "aliases.remove", "Remove")))
		rows.WriteString(`</a></td></tr>`)
	}
	if rows.Len() == 0 {
		rows.WriteString(`<tr><td class="muted-hint" colspan="2">` + html.EscapeString(settingsT(loc, "aliases.empty", "No aliases")) + `</td></tr>`)
	}
	return authPageHTML(settingsT(loc, "settings.aliases", "Aliases"), notice, errorText, `
	<form class="simple_form new_account_alias" id="new_account_alias" novalidate="novalidate" method="post" action="/settings/aliases">
	  <p class="hint">`+settingsT(loc, "aliases.hint_html", "Create an alias before moving followers from another account.")+`</p>
	  <hr class="spacer">
	  <div class="fields-group"><div class="input with_block_label string required account_alias_acct field_with_hint"><label class="string required" for="account_alias_acct">`+html.EscapeString(settingsT(loc, "simple_form.labels.account_alias.acct", "Account alias"))+filterRequiredMarker(loc)+`</label><span class="hint">`+html.EscapeString(settingsT(loc, "simple_form.hints.account_alias.acct", "Specify the username@domain of the account you are moving from"))+`</span><div class="label_input"><input autocapitalize="none" autocorrect="off" class="string required" type="text" value="" name="account_alias[acct]" id="account_alias_acct"></div></div></div>
	  <div class="actions"><button name="button" type="submit" class="btn button">`+html.EscapeString(settingsT(loc, "aliases.add_new", "Add alias"))+`</button></div>
    </form>
	<hr class="spacer">
	<div class="table-wrapper"><table class="table inline-table">
	  <thead><tr><th>`+html.EscapeString(settingsT(loc, "simple_form.labels.account_alias.acct", "Account alias"))+`</th><th></th></tr></thead>
      <tbody>`+rows.String()+`</tbody>
	</table></div>`, localeAndTheme...)
}

type aliasInputError string

func (e aliasInputError) Error() string { return string(e) }

const (
	errAliasInvalidAcct   aliasInputError = "Alias account address is invalid"
	errAliasMoveToSelf    aliasInputError = "You cannot alias the current account"
	errAliasNotFound      aliasInputError = "Alias account could not be found"
	errAliasAlreadyExists aliasInputError = "Alias account has already been taken"
)

func aliasErrorText(locale string, err error) string {
	switch err {
	case errAliasInvalidAcct:
		return settingsT(locale, "aliases.errors.invalid_acct", err.Error())
	case errAliasMoveToSelf:
		return settingsT(locale, "aliases.errors.move_to_self", err.Error())
	case errAliasNotFound:
		return settingsT(locale, "aliases.errors.not_found", err.Error())
	case errAliasAlreadyExists:
		return settingsT(locale, "aliases.errors.already_exists", err.Error())
	default:
		return err.Error()
	}
}
