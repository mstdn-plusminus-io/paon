package api

import (
	"errors"
	"html"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

func (s *Server) requireAdminEmailSubscriptionsWebUser(c *echo.Context) (*models.User, bool, error) {
	user, _, handled, err := s.requireFunctionalWebUser(c)
	if handled || err != nil {
		return nil, handled, err
	}
	if !s.userCan(user, rolePermissionManageSettings) {
		locale := s.webLocale(c, user)
		return nil, true, c.HTML(http.StatusForbidden, authPageHTML(adminT(locale, "admin.email_subscriptions.index.title", "Email subscriptions"), "", adminT(locale, "admin.email_subscriptions.not_permitted", "You are not allowed to manage email subscriptions."), "", locale))
	}
	return user, false, nil
}

func (s *Server) requireEmailSubscriptionsDeployment(c *echo.Context) error {
	if s == nil || !s.cfg.EmailSubscriptionsEnabled {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	return nil
}

func (s *Server) adminEmailSubscriptionsPage(c *echo.Context) error {
	user, handled, err := s.requireAdminEmailSubscriptionsWebUser(c)
	if handled || err != nil {
		return err
	}
	roles := []models.UserRole{}
	accounts := []models.Account{}
	if s.db != nil {
		mask := rolePermissionManageEmailSubscriptions | rolePermissionAdministrator
		if err := s.db.Where("permissions & ? <> 0", mask).Order("position DESC").Find(&roles).Error; err != nil {
			return err
		}
		if err := s.db.Preload("User").Joins("JOIN email_subscriptions ON email_subscriptions.account_id = accounts.id").Where("accounts.domain IS NULL").Group("accounts.id").Order("accounts.id ASC").Find(&accounts).Error; err != nil {
			return err
		}
	}
	return c.HTML(http.StatusOK, adminEmailSubscriptionsIndexHTML(s.settingBoolValue("email_subscriptions", false), s.cfg.EmailSubscriptionsEnabled, roles, accounts, c.QueryParam("notice"), s.webLocale(c, user)))
}

func adminEmailSubscriptionsIndexHTML(enabled bool, deploymentEnabled bool, roles []models.UserRole, accounts []models.Account, notice string, locale string) string {
	var body strings.Builder
	if notice != "" {
		body.WriteString(`<p class="flash-message notice">` + html.EscapeString(notice) + `</p>`)
	}
	body.WriteString(`<p class="lead">` + html.EscapeString(adminT(locale, "admin.email_subscriptions.index.lead", "Let selected accounts send email updates to subscribers.")) + `</p>`)
	if !enabled {
		body.WriteString(`<p>` + html.EscapeString(adminT(locale, "admin.email_subscriptions.index.disabled.description", "Email subscriptions are disabled.")) + `</p>`)
		if deploymentEnabled {
			body.WriteString(`<p><a class="button" href="/admin/email_subscriptions/setup">` + html.EscapeString(adminT(locale, "admin.email_subscriptions.index.disabled.get_started", "Get started")) + `</a></p>`)
		}
		return authPageHTML(adminT(locale, "admin.email_subscriptions.index.title", "Email subscriptions"), "", "", body.String(), locale)
	}
	body.WriteString(`<h3>` + html.EscapeString(adminT(locale, "admin.email_subscriptions.roles.title", "Allowed roles")) + `</h3><ul>`)
	for _, role := range roles {
		body.WriteString(`<li><a href="/admin/roles/` + strconv.FormatInt(role.ID, 10) + `">` + html.EscapeString(role.Name) + `</a></li>`)
	}
	body.WriteString(`</ul><h3>` + html.EscapeString(adminT(locale, "admin.email_subscriptions.accounts.title", "Accounts")) + `</h3><div class="table-wrapper"><table><tbody>`)
	for _, account := range accounts {
		body.WriteString(`<tr><td><a href="/admin/email_subscriptions/accounts/` + strconv.FormatInt(account.ID, 10) + `">` + html.EscapeString(account.Acct()) + `</a></td></tr>`)
	}
	body.WriteString(`</tbody></table></div><p><a class="button button-secondary" href="/admin/email_subscriptions/additional_footer_text">` + html.EscapeString(adminT(locale, "admin.email_subscriptions.compliance_settings.additional_footer_text.action", "Edit footer text")) + `</a></p>`)
	body.WriteString(`<form method="post" action="/admin/email_subscriptions/disable"><button class="button button-secondary button--destructive" type="submit">` + html.EscapeString(adminT(locale, "admin.email_subscriptions.danger_zone.disable_feature.action", "Disable")) + `</button></form>`)
	body.WriteString(`<form method="post" action="/admin/email_subscriptions/purge"><button class="button button-secondary button--destructive" type="submit">` + html.EscapeString(adminT(locale, "admin.email_subscriptions.danger_zone.erase_all_data.action", "Erase all subscription data")) + `</button></form>`)
	return authPageHTML(adminT(locale, "admin.email_subscriptions.index.title", "Email subscriptions"), "", "", body.String(), locale)
}

func (s *Server) destroyAdminEmailSubscription(c *echo.Context) error {
	_, handled, err := s.requireAdminEmailSubscriptionsWebUser(c)
	if handled || err != nil {
		return err
	}
	id, err := strconv.ParseInt(strings.TrimSpace(c.Param("id")), 10, 64)
	if err != nil || id <= 0 || s.db == nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	var subscription models.EmailSubscription
	if err := s.db.Where("id = ?", id).First(&subscription).Error; err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	if err := s.db.Delete(&subscription).Error; err != nil {
		return err
	}
	return c.Redirect(http.StatusFound, "/admin/email_subscriptions/accounts/"+strconv.FormatInt(subscription.AccountID, 10))
}

func (s *Server) disableAdminEmailSubscriptions(c *echo.Context) error {
	user, handled, err := s.requireAdminEmailSubscriptionsWebUser(c)
	if handled || err != nil {
		return err
	}
	if s.db == nil {
		return apiError(c, http.StatusServiceUnavailable, "Database unavailable")
	}
	if err := upsertGlobalSetting(s.db, "email_subscriptions", boolSettingValue(false)); err != nil {
		return err
	}
	return c.Redirect(http.StatusFound, "/admin/email_subscriptions?notice="+url.QueryEscape(adminT(s.webLocale(c, user), "admin.email_subscriptions.disabled_msg", "Email subscriptions disabled")))
}

func (s *Server) purgeAdminEmailSubscriptions(c *echo.Context) error {
	user, handled, err := s.requireAdminEmailSubscriptionsWebUser(c)
	if handled || err != nil {
		return err
	}
	if s.db == nil {
		return apiError(c, http.StatusServiceUnavailable, "Database unavailable")
	}
	if err := s.db.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&models.EmailSubscription{}).Error; err != nil {
		return err
	}
	return c.Redirect(http.StatusFound, "/admin/email_subscriptions?notice="+url.QueryEscape(adminT(s.webLocale(c, user), "admin.email_subscriptions.purged_msg", "Email subscription data has been purged")))
}

func (s *Server) adminEmailSubscriptionAccount(c *echo.Context) (*models.Account, *models.User, error) {
	if err := s.requireEmailSubscriptionsDeployment(c); err != nil {
		return nil, nil, err
	}
	id, err := strconv.ParseInt(strings.TrimSpace(c.Param("id")), 10, 64)
	if err != nil || id <= 0 || s.db == nil {
		return nil, nil, gorm.ErrRecordNotFound
	}
	var account models.Account
	if err := s.db.Where("id = ?", id).First(&account).Error; err != nil {
		return nil, nil, err
	}
	var user models.User
	if err := s.db.Where("account_id = ?", account.ID).First(&user).Error; err != nil {
		return nil, nil, err
	}
	return &account, &user, nil
}

func (s *Server) adminEmailSubscriptionAccountPage(c *echo.Context) error {
	admin, handled, err := s.requireAdminEmailSubscriptionsWebUser(c)
	if handled || err != nil {
		return err
	}
	account, user, err := s.adminEmailSubscriptionAccount(c)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apiError(c, http.StatusNotFound, "Record not found")
		}
		return err
	}
	pageText := adminTrendsPageValue(c)
	page, _ := strconv.Atoi(pageText)
	if page <= 0 {
		page = 1
	}
	subscriptions := []models.EmailSubscription{}
	if err := s.db.Where("account_id = ?", account.ID).Order("id DESC").Offset(adminPageOffset(c, adminCollectionsPageSize)).Limit(adminCollectionsPageSize).Find(&subscriptions).Error; err != nil {
		return err
	}
	var count int64
	if err := s.db.Model(&models.EmailSubscription{}).Where("account_id = ?", account.ID).Count(&count).Error; err != nil {
		return err
	}
	canSend := s.userCan(user, rolePermissionManageEmailSubscriptions)
	return c.HTML(http.StatusOK, adminEmailSubscriptionAccountHTML(*account, *user, subscriptions, count, page, canSend, s.webLocale(c, admin)))
}

func adminEmailSubscriptionAccountHTML(account models.Account, user models.User, subscriptions []models.EmailSubscription, count int64, page int, canSend bool, locale string) string {
	enabled := userSettingBool(user, "email_subscriptions", false)
	var body strings.Builder
	body.WriteString(`<p><a class="button button-secondary" href="/admin/accounts/` + strconv.FormatInt(account.ID, 10) + `">` + html.EscapeString(adminT(locale, "admin.email_subscriptions.accounts.view_account", "View account")) + `</a></p>`)
	body.WriteString(`<dl class="metadata"><div><dt>` + html.EscapeString(adminT(locale, "email_subscriptions.status", "Status")) + `</dt><dd>` + html.EscapeString(emailSubscriptionAdminStatus(enabled, canSend)) + `</dd></div><div><dt>` + html.EscapeString(adminT(locale, "email_subscriptions.subscribers", "Subscribers")) + `</dt><dd>` + strconv.FormatInt(count, 10) + `</dd></div></dl>`)
	if canSend {
		action, label := "enable", adminT(locale, "admin.email_subscriptions.accounts.enable_feature", "Enable")
		if enabled {
			action, label = "disable", adminT(locale, "admin.email_subscriptions.accounts.disable_feature", "Disable")
		}
		body.WriteString(`<form method="post" action="/admin/email_subscriptions/accounts/` + strconv.FormatInt(account.ID, 10) + `/` + action + `"><button class="button button-secondary" type="submit">` + html.EscapeString(label) + `</button></form>`)
	}
	body.WriteString(`<div class="table-wrapper"><table><thead><tr><th>` + html.EscapeString(adminT(locale, "admin.email_subscriptions.accounts.email", "Email")) + `</th><th>` + html.EscapeString(adminT(locale, "admin.email_subscriptions.accounts.date", "Date")) + `</th><th></th></tr></thead><tbody>`)
	for _, subscription := range subscriptions {
		body.WriteString(`<tr><td>` + html.EscapeString(subscription.Email) + `</td><td>` + html.EscapeString(subscription.CreatedAt.UTC().Format("2006-01-02 15:04:05 UTC")) + `</td><td><form method="post" action="/admin/email_subscriptions/` + strconv.FormatInt(subscription.ID, 10) + `"><input type="hidden" name="_method" value="delete"><button type="submit">` + html.EscapeString(adminT(locale, "generic.delete", "Delete")) + `</button></form></td></tr>`)
	}
	body.WriteString(`</tbody></table></div>`)
	return authPageHTML(adminTVars(locale, "admin.email_subscriptions.accounts.title", "Email subscriptions for %{name}", map[string]string{"name": accountDisplayName(account)}), "", "", body.String(), locale)
}

func emailSubscriptionAdminStatus(enabled bool, canSend bool) string {
	switch {
	case canSend && enabled:
		return "active"
	case canSend:
		return "disabled"
	case enabled:
		return "no access"
	default:
		return "inactive"
	}
}

func (s *Server) setAdminAccountEmailSubscriptions(c *echo.Context, enabled bool) error {
	_, handled, err := s.requireAdminEmailSubscriptionsWebUser(c)
	if handled || err != nil {
		return err
	}
	account, user, err := s.adminEmailSubscriptionAccount(c)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apiError(c, http.StatusNotFound, "Record not found")
		}
		return err
	}
	if !s.userCan(user, rolePermissionManageEmailSubscriptions) {
		return apiError(c, http.StatusForbidden, "This account does not have permission to send email subscriptions")
	}
	if err := s.updateUserSettingsAttributes(user.ID, map[string]any{"email_subscriptions": enabled}); err != nil {
		return err
	}
	return c.Redirect(http.StatusFound, "/admin/email_subscriptions/accounts/"+strconv.FormatInt(account.ID, 10))
}

func (s *Server) enableAdminAccountEmailSubscriptions(c *echo.Context) error {
	return s.setAdminAccountEmailSubscriptions(c, true)
}

func (s *Server) disableAdminAccountEmailSubscriptions(c *echo.Context) error {
	return s.setAdminAccountEmailSubscriptions(c, false)
}

func (s *Server) adminEmailSubscriptionFooterPage(c *echo.Context) error {
	user, handled, err := s.requireAdminEmailSubscriptionsWebUser(c)
	if handled || err != nil {
		return err
	}
	locale := s.webLocale(c, user)
	value := s.settingStringValue("email_footer_text", "")
	body := `<form method="post" action="/admin/email_subscriptions/additional_footer_text"><input type="hidden" name="_method" value="patch"><div class="fields-group"><label for="email-footer-text">` + html.EscapeString(adminT(locale, "simple_form.labels.defaults.email_footer_text", "Additional footer text")) + `</label><textarea id="email-footer-text" rows="8" name="form_admin_settings[email_footer_text]">` + html.EscapeString(value) + `</textarea></div><button class="button" type="submit">` + html.EscapeString(adminT(locale, "generic.save_changes", "Save changes")) + `</button></form>`
	return c.HTML(http.StatusOK, authPageHTML(adminT(locale, "admin.email_subscriptions.additional_footer_texts.show.title", "Additional footer text"), "", "", body, locale))
}

func (s *Server) updateAdminEmailSubscriptionFooter(c *echo.Context) error {
	_, handled, err := s.requireAdminEmailSubscriptionsWebUser(c)
	if handled || err != nil {
		return err
	}
	if s.db == nil {
		return apiError(c, http.StatusServiceUnavailable, "Database unavailable")
	}
	if err := c.Request().ParseForm(); err != nil {
		return err
	}
	if !formHasNestedPrefix(c.Request().Form, "form_admin_settings") {
		return apiError(c, http.StatusBadRequest, "Malformed request")
	}
	if err := upsertGlobalSetting(s.db, "email_footer_text", lastFormValue(c.Request().Form, "form_admin_settings[email_footer_text]")); err != nil {
		return err
	}
	return c.Redirect(http.StatusFound, "/admin/email_subscriptions")
}

func (s *Server) adminEmailSubscriptionSetupPage(c *echo.Context) error {
	user, handled, err := s.requireAdminEmailSubscriptionsWebUser(c)
	if handled || err != nil {
		return err
	}
	if err := s.requireEmailSubscriptionsDeployment(c); err != nil {
		return err
	}
	return c.HTML(http.StatusOK, adminEmailSubscriptionSetupHTML("", s.webLocale(c, user)))
}

func (s *Server) createAdminEmailSubscriptionSetup(c *echo.Context) error {
	user, handled, err := s.requireAdminEmailSubscriptionsWebUser(c)
	if handled || err != nil {
		return err
	}
	if err := s.requireEmailSubscriptionsDeployment(c); err != nil {
		return err
	}
	if err := c.Request().ParseForm(); err != nil {
		return err
	}
	root := "form_email_subscriptions_confirmation"
	volume := truthy(lastFormValue(c.Request().Form, root+"[agreement_email_volume]"))
	privacy := truthy(lastFormValue(c.Request().Form, root+"[agreement_privacy_and_terms]"))
	if !formHasNestedPrefix(c.Request().Form, root) || !volume || !privacy {
		return c.HTML(http.StatusOK, adminEmailSubscriptionSetupHTML(adminT(s.webLocale(c, user), "admin.email_subscriptions.setups.agreement_required", "You must accept both agreements."), s.webLocale(c, user)))
	}
	if s.db == nil {
		return apiError(c, http.StatusServiceUnavailable, "Database unavailable")
	}
	if err := upsertGlobalSetting(s.db, "email_subscriptions", boolSettingValue(true)); err != nil {
		return err
	}
	return c.Redirect(http.StatusFound, "/admin/email_subscriptions")
}

func adminEmailSubscriptionSetupHTML(errorText string, locale string) string {
	errorHTML := ""
	if errorText != "" {
		errorHTML = `<p class="flash-message alert">` + html.EscapeString(errorText) + `</p>`
	}
	body := errorHTML + `<p class="lead">` + html.EscapeString(adminT(locale, "admin.email_subscriptions.index.lead", "Enable email subscriptions after confirming the compliance requirements.")) + `</p><form method="post" action="/admin/email_subscriptions/setup"><label><input type="checkbox" name="form_email_subscriptions_confirmation[agreement_email_volume]" value="1">` + html.EscapeString(adminT(locale, "admin.email_subscriptions.setups.agreement_email_volume", "I understand the email volume requirements")) + `</label><label><input type="checkbox" name="form_email_subscriptions_confirmation[agreement_privacy_and_terms]" value="1">` + html.EscapeString(adminT(locale, "admin.email_subscriptions.setups.agreement_privacy_and_terms", "I accept the privacy and terms requirements")) + `</label><button class="button" type="submit">` + html.EscapeString(adminT(locale, "generic.confirm", "Confirm")) + `</button></form>`
	return authPageHTML(adminT(locale, "admin.email_subscriptions.index.title", "Email subscriptions"), "", "", body, locale)
}
