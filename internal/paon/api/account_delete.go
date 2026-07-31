package api

import (
	"context"
	"html"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

func (s *Server) settingsDeletePage(c *echo.Context) error {
	user, _, err := s.currentUserIncludingDisabled(c)
	if err != nil {
		return redirectToSignIn(c)
	}
	account, err := s.userAccount(user.AccountID)
	if err != nil {
		return err
	}
	if account.SuspendedAt.Valid {
		return apiError(c, http.StatusForbidden, "This account is suspended")
	}
	locale := s.webLocale(c, user)
	theme := settingsWebTheme(decodeUserSettings(user.Settings.String))
	renderArgs, err := s.settingsRenderArgs(c.Request().URL.Path, locale, theme, user, account)
	if err != nil {
		return err
	}
	renderArgs = append(renderArgs, s.settingStringValue("site_contact_email", ""))
	return c.HTML(http.StatusOK, deleteAccountHTML(*user, *account, c.QueryParam("error"), renderArgs...))
}

func (s *Server) destroyOwnAccount(c *echo.Context) error {
	user, token, err := s.currentUserIncludingDisabled(c)
	if err != nil {
		return redirectToSignIn(c)
	}
	account, err := s.userAccount(user.AccountID)
	if err != nil {
		return err
	}
	if account.SuspendedAt.Valid {
		return apiError(c, http.StatusForbidden, "This account is suspended")
	}
	locale := s.webLocale(c, user)
	if !railsNestedFormRootPresent(c, "form_delete_confirmation") {
		return apiError(c, http.StatusBadRequest, "param is missing or the value is empty: form_delete_confirmation")
	}
	if !deleteChallengePassed(*user, *account, c.FormValue("form_delete_confirmation[password]"), c.FormValue("form_delete_confirmation[username]")) {
		return c.Redirect(http.StatusFound, "/settings/delete?error="+url.QueryEscape(settingsT(locale, "deletes.challenge_not_passed", "Challenge did not pass")))
	}
	if s.db == nil {
		return c.Redirect(http.StatusFound, "/settings/delete?error="+url.QueryEscape(settingsDatabaseUnavailableMessage(locale)))
	}
	if err := s.suspendOwnAccount(account.ID, user.ID, token); err != nil {
		return err
	}
	clearSessionCookie(c, s.cfg.ForceSSL)
	return c.Redirect(http.StatusFound, "/auth/sign_in?notice="+url.QueryEscape(settingsT(locale, "deletes.success_msg", "Your account has been deleted")))
}

func (s *Server) userAccount(accountID int64) (*models.Account, error) {
	if s.db == nil {
		return nil, gorm.ErrRecordNotFound
	}
	var account models.Account
	if err := s.db.Where("id = ?", accountID).First(&account).Error; err != nil {
		return nil, err
	}
	return &account, nil
}

func (s *Server) suspendOwnAccount(accountID int64, userID int64, currentToken string) error {
	now := time.Now().UTC()
	var revokedTokenIDs []int64
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.Account{}).Where("id = ?", accountID).Updates(map[string]any{
			"suspended_at":      now,
			"suspension_origin": 0,
			"updated_at":        now,
		}).Error; err != nil {
			return err
		}
		if err := tx.Model(&models.User{}).Where("id = ?", userID).Updates(map[string]any{"disabled": true, "updated_at": now}).Error; err != nil {
			return err
		}
		if err := ownAccountRevokedTokenQuery(tx, userID, currentToken).Pluck("id", &revokedTokenIDs).Error; err != nil {
			return err
		}
		return ownAccountRevokedTokenQuery(tx, userID, currentToken).Update("revoked_at", now).Error
	})
	if err != nil {
		return err
	}
	_ = s.clearAdminSuspendedAccountFeedCaches(context.Background(), s.db, adminSingleAccountIDSubquery(s.db, accountID))
	s.publishStreamingKill(accountID, revokedTokenIDs)
	if s.enqueueAccountDeletionTask(accountID) {
		return nil
	}
	return s.runOwnAccountDeletionWorkerEffects(context.Background(), accountID, now)
}

func ownAccountRevokedTokenQuery(tx *gorm.DB, userID int64, currentToken string) *gorm.DB {
	query := tx.Model(&models.OAuthAccessToken{}).Where("resource_owner_id = ? AND revoked_at IS NULL", userID)
	if currentToken != "" {
		query = query.Or("token = ? AND revoked_at IS NULL", currentToken)
	}
	return query
}

func deleteChallengePassed(user models.User, account models.Account, password string, username string) bool {
	if strings.TrimSpace(user.EncryptedPassword) == "" {
		return account.Username == username
	}
	return validBCryptPassword(user.EncryptedPassword, password)
}

func deleteAccountHTML(user models.User, account models.Account, errorText string, localeAndTheme ...string) string {
	loc := settingsLocaleArgOrEnglish(localeAndTheme...)
	field := `<div class="input with_block_label password optional form_delete_confirmation_password field_with_hint"><label class="password optional" for="form_delete_confirmation_password">` + html.EscapeString(settingsT(loc, "simple_form.labels.defaults.password", "Password")) + `</label><span class="hint">` + html.EscapeString(settingsT(loc, "deletes.confirm_password", "Enter your current password to verify your identity")) + `</span><div class="label_input"><input autocomplete="current-password" class="password optional" type="password" name="form_delete_confirmation[password]" id="form_delete_confirmation_password"></div></div>`
	if strings.TrimSpace(user.EncryptedPassword) == "" {
		field = `<div class="input with_block_label string optional form_delete_confirmation_username field_with_hint"><label class="string optional" for="form_delete_confirmation_username">` + html.EscapeString(settingsT(loc, "simple_form.labels.defaults.username", "Username")) + `</label><span class="hint">` + html.EscapeString(settingsT(loc, "deletes.confirm_username", "Enter your username to confirm the procedure")) + `</span><div class="label_input"><input autocomplete="off" class="string optional" type="text" name="form_delete_confirmation[username]" id="form_delete_confirmation_username"></div></div>`
	}
	warnings := `<li class="warning-hint">` + html.EscapeString(settingsT(loc, "deletes.warning.irreversible", "You will not be able to restore or reactivate your account")) + `</li><li class="warning-hint">` + html.EscapeString(settingsT(loc, "deletes.warning.username_unavailable", "Your username will remain unavailable")) + `</li><li class="warning-hint">` + html.EscapeString(settingsT(loc, "deletes.warning.data_removal", "Your posts and other data will be permanently removed")) + `</li><li class="warning-hint">` + html.EscapeString(settingsT(loc, "deletes.warning.caches", "Content cached by other servers may persist")) + `</li>`
	if !user.ConfirmedAt.Valid || !user.Approved {
		contactEmail := ""
		if len(localeAndTheme) > 4 {
			contactEmail = strings.TrimSpace(localeAndTheme[4])
		}
		warnings = `<li class="positive-hint">` + settingsTVars(loc, "deletes.warning.email_change_html", "You can <a href=\"%{path}\">change your e-mail address</a> without deleting your account", map[string]string{"path": "/auth/edit"}) + `</li>` +
			`<li class="positive-hint">` + settingsTVars(loc, "deletes.warning.email_reconfirmation_html", "If you are not receiving the confirmation e-mail, you can <a href=\"%{path}\">request it again</a>", map[string]string{"path": "/auth/confirmation/new"}) + `</li>`
		if contactEmail != "" {
			warnings += `<li class="positive-hint">` + settingsTVars(loc, "deletes.warning.email_contact_html", `If it still doesn't arrive, you can e-mail <a href="mailto:%{email}">%{email}</a> for help`, map[string]string{"email": html.EscapeString(contactEmail)}) + `</li>`
		}
		warnings += `<li class="positive-hint">` + html.EscapeString(settingsT(loc, "deletes.warning.username_available", "Your username will become available again")) + `</li>`
	}
	title := settingsT(loc, "settings.delete", "Delete account")
	return authPageHTML(title, "", errorText, `
	<form class="simple_form new_form_delete_confirmation" id="new_form_delete_confirmation" novalidate="novalidate" method="post" action="/settings/delete">
      <input type="hidden" name="_method" value="delete">
	  <p class="hint">`+html.EscapeString(settingsT(loc, "deletes.warning.before", "Before proceeding, please read these notes carefully:"))+`</p>
	  <ul class="hint">`+warnings+`</ul>
	  <p class="hint">`+settingsTVars(loc, "deletes.warning.more_details_html", "See the <a href=\"%{terms_path}\">privacy policy</a> for more details.", map[string]string{"terms_path": "/privacy-policy"})+`</p>
	  <hr class="spacer">
      `+field+`
	  <div class="actions"><button name="button" type="submit" class="btn negative">`+html.EscapeString(settingsT(loc, "deletes.proceed", "Delete account"))+`</button></div>
    </form>`, localeAndTheme...)
}

func clearSessionCookie(c *echo.Context, secure bool) {
	expireCookie(c, sessionCookieName, secure)
	expireCookie(c, railsSessionCookieName, secure)
	expireCookie(c, railsSessionIDCookieName, secure)
	expireCookie(c, browserSessionCookieName, secure)
}
