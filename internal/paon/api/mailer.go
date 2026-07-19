package api

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"mime"
	"mime/multipart"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

type mailMessage struct {
	To       string
	Subject  string
	Body     string
	HTMLBody string
	TextOnly bool
	Headers  []mailHeader
}

type mailHeader struct {
	Key   string
	Value string
}

const smtpReadTimeout = 20 * time.Second

var mailMessageIDCounter atomic.Uint64

func (s *Server) sendConfirmationMail(to string, token string) error {
	return sendMail(s.cfg, confirmationMailMessage(s.cfg, to, token, nil))
}

func (s *Server) sendConfirmationDelivery(delivery confirmationDelivery) error {
	if s == nil {
		return fmt.Errorf("confirmation mail: server is not configured")
	}
	return sendConfirmationDeliveryWithBackends(
		delivery,
		s.asynqClient != nil,
		s.enqueueConfirmationMailTask,
		s.deliverConfirmationDelivery,
	)
}

func sendConfirmationDeliveryWithBackends(delivery confirmationDelivery, asynqConfigured bool, enqueue func(int64, string) error, deliver func(confirmationDelivery) error) error {
	if strings.TrimSpace(delivery.Token) == "" {
		return nil
	}
	if asynqConfigured {
		if !delivery.HasUser || delivery.User.ID <= 0 {
			return fmt.Errorf("confirmation mail: user id is required for queued delivery")
		}
		return enqueue(delivery.User.ID, delivery.Token)
	}
	return deliver(delivery)
}

func (s *Server) deliverConfirmationDelivery(delivery confirmationDelivery) error {
	if delivery.Reconfirmation {
		if delivery.HasUser {
			return s.sendReconfirmationMail(delivery.Email, delivery.Token, delivery.User)
		}
		return s.sendReconfirmationMail(delivery.Email, delivery.Token)
	}
	if delivery.HasUser {
		return sendMail(s.cfg, confirmationMailMessage(s.cfg, delivery.Email, delivery.Token, &delivery.User))
	}
	return s.sendConfirmationMail(delivery.Email, delivery.Token)
}

func confirmationMailMessage(cfg config.Config, to string, token string, user *models.User) mailMessage {
	link := cfg.BaseURL() + "/auth/confirmation?confirmation_token=" + url.QueryEscape(token)
	instance := firstNonEmpty(cfg.LocalDomain, cfg.WebDomain, "Mastodon")
	host := firstNonEmpty(cfg.WebDomain, cfg.LocalDomain, "Mastodon")
	locale := cfg.Locale()
	approved := true
	if user != nil {
		locale = mailLocale(cfg, *user)
		approved = user.Approved
	}
	explanationKey := "devise.mailer.confirmation_instructions.explanation"
	explanationFallback := "You have created an account on %{host} with this email address. You are one click away from activating it. If this wasn't you, please ignore this email."
	if !approved {
		explanationKey = "devise.mailer.confirmation_instructions.explanation_when_pending"
		explanationFallback = "You applied for an invite to %{host} with this email address. Once you confirm your e-mail address, we will review your application. You can login to change your details or delete your account, but you cannot access most of the functions until your account is approved. If your application is rejected, your data will be removed, so no further action will be required from you. If this wasn't you, please ignore this email."
	}
	rulesURL := cfg.BaseURL() + "/about/more"
	policyURL := cfg.BaseURL() + "/privacy-policy"
	extra := stripHTML(settingsTVars(locale, "devise.mailer.confirmation_instructions.extra_html", "Please also check out the rules of the server and our terms of service.", map[string]string{"terms_path": rulesURL, "policy_path": policyURL}))
	body := settingsT(locale, "devise.mailer.confirmation_instructions.title", "Verify email address") + "\n\n===\n\n"
	body += settingsTVars(locale, explanationKey, explanationFallback, map[string]string{"host": host}) + "\n\n"
	body += "=> " + link + "\n\n"
	body += extra + "\n\n"
	body += "=> " + rulesURL + "\n"
	body += "=> " + policyURL + "\n"
	return mailMessage{
		To:      to,
		Subject: settingsTVars(locale, "devise.mailer.confirmation_instructions.subject", "Mastodon: Confirmation instructions for %{instance}", map[string]string{"instance": instance}),
		Body:    body,
	}
}

func (s *Server) sendReconfirmationMail(to string, token string, users ...models.User) error {
	return sendMail(s.cfg, reconfirmationMailMessage(s.cfg, to, token, users...))
}

func reconfirmationMailMessage(cfg config.Config, to string, token string, users ...models.User) mailMessage {
	link := cfg.BaseURL() + "/auth/confirmation?confirmation_token=" + url.QueryEscape(token)
	instance := firstNonEmpty(cfg.LocalDomain, cfg.WebDomain, "Mastodon")
	locale := cfg.Locale()
	if len(users) > 0 {
		locale = mailLocale(cfg, users[0])
	}
	body := settingsT(locale, "devise.mailer.reconfirmation_instructions.title", "Verify email address") + "\n\n===\n\n"
	body += settingsT(locale, "devise.mailer.reconfirmation_instructions.explanation", "Confirm the new address to change your email.") + "\n\n"
	body += "=> " + link + "\n\n"
	body += settingsT(locale, "devise.mailer.reconfirmation_instructions.extra", "If this change wasn't initiated by you, please ignore this email. The email address for the Mastodon account won't change until you access the link above.") + "\n"
	return mailMessage{
		To:      to,
		Subject: settingsTVars(locale, "devise.mailer.reconfirmation_instructions.subject", "Mastodon: Confirm email for %{instance}", map[string]string{"instance": instance}),
		Body:    body,
	}
}

func (s *Server) sendResetPasswordMail(to string, token string, users ...models.User) error {
	message := resetPasswordMailMessage(s.cfg, to, token, users...)
	if len(users) == 0 {
		return sendMail(s.cfg, message)
	}
	return s.enqueueOrDeliverMail(users[0], "present", message)
}

func resetPasswordMailMessage(cfg config.Config, to string, token string, users ...models.User) mailMessage {
	link := cfg.BaseURL() + "/auth/password/edit?reset_password_token=" + url.QueryEscape(token)
	locale := cfg.Locale()
	if len(users) > 0 {
		locale = mailLocale(cfg, users[0])
	}
	body := settingsT(locale, "devise.mailer.reset_password_instructions.title", "Password reset") + "\n\n===\n\n"
	body += settingsT(locale, "devise.mailer.reset_password_instructions.explanation", "You requested a new password for your account.") + "\n\n"
	body += "=> " + link + "\n\n"
	body += settingsT(locale, "devise.mailer.reset_password_instructions.extra", "If you didn't request this, please ignore this email. Your password won't change until you access the link above and create a new one.") + "\n"
	return mailMessage{
		To:      to,
		Subject: settingsT(locale, "devise.mailer.reset_password_instructions.subject", "Mastodon: Reset password instructions"),
		Body:    body,
	}
}

func (s *Server) sendEmailChangedMail(user models.User, newEmail string) error {
	if !userReceivesSecurityMail(user) {
		return nil
	}
	return s.enqueueOrDeliverMail(user, "security", emailChangedMailMessage(s.cfg, user, newEmail))
}

func (s *Server) sendPasswordChangedMail(user models.User) error {
	if !userReceivesSecurityMail(user) {
		return nil
	}
	return s.enqueueOrDeliverMail(user, "security", passwordChangedMailMessage(s.cfg, user))
}

func emailChangedMailMessage(cfg config.Config, user models.User, newEmail string) mailMessage {
	locale := mailLocale(cfg, user)
	body := settingsT(locale, "devise.mailer.email_changed.title", "New email address") + "\n\n===\n\n"
	body += settingsT(locale, "devise.mailer.email_changed.explanation", "The email address for your account is being changed to:") + "\n\n"
	body += strings.ToLower(strings.TrimSpace(newEmail)) + "\n\n"
	body += settingsT(locale, "devise.mailer.email_changed.extra", "If you did not change your email, it is likely that someone has gained access to your account. Please change your password immediately or contact the server admin if you're locked out of your account.") + "\n"
	return mailMessage{
		To:      user.Email,
		Subject: settingsT(locale, "devise.mailer.email_changed.subject", "Mastodon: Email changed"),
		Body:    body,
	}
}

func passwordChangedMailMessage(cfg config.Config, user models.User) mailMessage {
	locale := mailLocale(cfg, user)
	body := settingsT(locale, "devise.mailer.password_change.title", "Password changed") + "\n\n===\n\n"
	body += settingsT(locale, "devise.mailer.password_change.explanation", "The password for your account has been changed.") + "\n\n"
	body += settingsT(locale, "devise.mailer.password_change.extra", "If you did not change your password, it is likely that someone has gained access to your account. Please change your password immediately or contact the server admin if you're locked out of your account.") + "\n"
	return mailMessage{
		To:      user.Email,
		Subject: settingsT(locale, "devise.mailer.password_change.subject", "Mastodon: Password changed"),
		Body:    body,
	}
}

func (s *Server) sendTwoFactorEnabledMail(user models.User) error {
	return s.sendSecuritySettingsMail(user, "devise.mailer.two_factor_enabled", "Mastodon: Two-factor authentication enabled", "2FA enabled", "Two-factor authentication has been enabled for your account. A token generated by the paired TOTP app will be required for login.", "")
}

func (s *Server) sendTwoFactorDisabledMail(user models.User) error {
	return s.sendSecuritySettingsMail(user, "devise.mailer.two_factor_disabled", "Mastodon: Two-factor authentication disabled", "2FA disabled", "Two-factor authentication for your account has been disabled. Login is now possible using only e-mail address and password.", "")
}

func (s *Server) sendTwoFactorRecoveryCodesChangedMail(user models.User) error {
	return s.sendSecuritySettingsMail(user, "devise.mailer.two_factor_recovery_codes_changed", "Mastodon: Two-factor recovery codes re-generated", "2FA recovery codes changed", "The previous recovery codes have been invalidated and new ones generated.", "")
}

func (s *Server) sendWebauthnEnabledMail(user models.User) error {
	return s.sendSecuritySettingsMail(user, "devise.mailer.webauthn_enabled", "Mastodon: Security key authentication enabled", "Security keys enabled", "Security key authentication has been enabled for your account. Your security key can now be used for login.", "")
}

func (s *Server) sendWebauthnDisabledMail(user models.User) error {
	return s.sendSecuritySettingsMail(user, "devise.mailer.webauthn_disabled", "Mastodon: Authentication with security keys disabled", "Security keys disabled", "Authentication with security keys has been disabled for your account. Login is now possible using only the token generated by the paired TOTP app.", "")
}

func (s *Server) sendWebauthnCredentialAddedMail(user models.User, credential models.WebauthnCredential) error {
	return s.sendSecuritySettingsMail(user, "devise.mailer.webauthn_credential.added", "Mastodon: New security key", "A new security key has been added", "The following security key has been added to your account", credential.Nickname)
}

func (s *Server) sendWebauthnCredentialDeletedMail(user models.User, credential models.WebauthnCredential) error {
	return s.sendSecuritySettingsMail(user, "devise.mailer.webauthn_credential.deleted", "Mastodon: Security key deleted", "One of your security keys has been deleted", "The following security key has been deleted from your account", credential.Nickname)
}

func (s *Server) sendSuspiciousSignInMail(user models.User, remoteIP string, userAgent string, timestamp time.Time) error {
	if strings.TrimSpace(user.Email) == "" {
		return nil
	}
	return s.enqueueOrDeliverMail(user, "present", suspiciousSignInMailMessage(s.cfg, user, remoteIP, userAgent, timestamp))
}

func (s *Server) sendAccountWarningMail(user models.User, warning models.AccountWarning) error {
	if strings.TrimSpace(user.Email) == "" {
		return nil
	}
	return s.enqueueOrDeliverMail(user, "present", accountWarningMailMessage(s.cfg, user, warning, accountWarningMailContext{
		Statuses: s.accountWarningMailStatuses(warning),
		Rules:    s.accountWarningMailRules(warning),
	}))
}

func (s *Server) sendWelcomeMail(user models.User) error {
	if !userReceivesSecurityMail(user) || s.userAccountUnavailable(user) {
		return nil
	}
	return s.enqueueOrDeliverMail(user, "security", welcomeMailMessage(s.cfg, user))
}

func welcomeMailMessage(cfg config.Config, user models.User) mailMessage {
	locale := mailLocale(cfg, user)
	account := user.Account
	username := strings.TrimSpace(user.Email)
	handle := "@" + strings.TrimSpace(user.Email)
	if account != nil {
		if strings.TrimSpace(account.Username) != "" {
			username = account.Username
		}
		handle = "@" + firstNonEmpty(account.Acct(), account.Username)
		if account.Local() && strings.TrimSpace(account.Username) != "" {
			handle = "@" + account.Username + "@" + firstNonEmpty(cfg.LocalDomain, cfg.WebDomain, "localhost")
		}
	}
	instance := firstNonEmpty(cfg.LocalDomain, cfg.WebDomain, "Mastodon")
	body := settingsTVars(locale, "user_mailer.welcome.title", "Welcome aboard, %{name}!", map[string]string{"name": username}) + " " + settingsT(locale, "user_mailer.welcome.explanation", "Here are some tips to get you started") + "\n\n"
	body += "===\n\n"
	body += settingsT(locale, "user_mailer.welcome.full_handle", "Your full handle") + " (" + handle + ")\n"
	body += settingsTVars(locale, "user_mailer.welcome.full_handle_hint", "This is what you would tell your friends so they can message or follow you from another server.", map[string]string{"instance": instance}) + "\n\n"
	body += "---\n\n"
	body += settingsT(locale, "user_mailer.welcome.edit_profile_step", "You can customize your profile by uploading a profile picture, changing your display name and more. You can opt-in to review new followers before they're allowed to follow you.") + "\n\n"
	body += "=> " + cfg.BaseURL() + "/settings/profile\n\n"
	body += settingsT(locale, "user_mailer.welcome.final_step", "Start posting! Even without followers, your public posts may be seen by others, for example on the local timeline or in hashtags. You may want to introduce yourself on the #introductions hashtag.") + "\n\n"
	body += "=> " + cfg.BaseURL() + "/web\n"
	return mailMessage{
		To:      user.Email,
		Subject: settingsT(locale, "user_mailer.welcome.subject", "Welcome to Mastodon"),
		Body:    body,
	}
}

type accountWarningMailContext struct {
	Statuses []models.Status
	Rules    []models.Rule
}

func accountWarningMailMessage(cfg config.Config, user models.User, warning models.AccountWarning, contexts ...accountWarningMailContext) mailMessage {
	var context accountWarningMailContext
	if len(contexts) > 0 {
		context = contexts[0]
	}
	action := accountWarningAction(warning.Action)
	acct := user.Email
	if user.Account != nil && user.Account.ID != 0 {
		acct = "@" + user.Account.Acct()
		if user.Account.Local() && strings.TrimSpace(user.Account.Username) != "" {
			acct = "@" + user.Account.Username + "@" + firstNonEmpty(cfg.LocalDomain, cfg.WebDomain, "localhost")
		}
	}
	locale := mailLocale(cfg, user)
	instance := firstNonEmpty(cfg.LocalDomain, cfg.WebDomain, "this server")
	subject := settingsTVars(locale, "user_mailer.warning.subject."+action, accountWarningMailSubject(action, acct), map[string]string{"acct": acct})
	body := settingsT(locale, "user_mailer.warning.title."+action, accountWarningMailTitle(action)) + "\n\n===\n\n"
	if explanation := settingsTVars(locale, "user_mailer.warning.explanation."+action, accountWarningMailExplanation(action, instance), map[string]string{"instance": instance}); strings.TrimSpace(explanation) != "" {
		body += explanation + "\n\n"
	}
	if strings.TrimSpace(warning.Text) != "" {
		body += strings.TrimSpace(warning.Text) + "\n\n"
	}
	if warning.Report.ID != 0 && adminReportCategoryKey(warning.Report.Category) != "other" {
		category := adminReportCategoryKey(warning.Report.Category)
		body += "**" + settingsT(locale, "user_mailer.warning.reason", "Reason:") + "** " + settingsT(locale, "user_mailer.warning.categories."+category, category) + "\n\n"
		if category == "violation" && len(context.Rules) > 0 {
			for _, rule := range context.Rules {
				if text := strings.TrimSpace(rule.Text); text != "" {
					body += "- " + text + "\n"
				}
			}
			body += "\n"
		}
	}
	if len(context.Statuses) > 0 {
		body += settingsT(locale, "user_mailer.warning.statuses", "Posts:") + "\n\n"
		for _, status := range context.Statuses {
			body += notificationStatusTextBlock(locale, &status, statusURLForMail(cfg, status)) + "---\n"
		}
	} else if len(warning.StatusIDs) > 0 {
		body += settingsT(locale, "user_mailer.warning.statuses", "Posts:") + "\n" + strings.Join([]string(warning.StatusIDs), ", ") + "\n\n"
	}
	body += "---\n\n"
	body += settingsTVars(locale, "user_mailer.warning.appeal_description", "If you believe this is an error, you can submit an appeal to the staff of %{instance}.", map[string]string{"instance": instance}) + "\n"
	body += cfg.BaseURL() + "/disputes/strikes/" + fmt.Sprint(warning.ID) + "\n"
	return mailMessage{
		To:      user.Email,
		Subject: subject,
		Body:    body,
	}
}

func (s *Server) accountWarningMailStatuses(warning models.AccountWarning) []models.Status {
	if s == nil || s.db == nil || len(warning.StatusIDs) == 0 {
		return nil
	}
	ids := accountWarningStatusIDs(warning)
	if len(ids) == 0 {
		return nil
	}
	var statuses []models.Status
	if err := s.db.Preload("Account").Where("id IN ?", ids).Find(&statuses).Error; err != nil {
		return nil
	}
	byID := make(map[int64]models.Status, len(statuses))
	for _, status := range statuses {
		byID[status.ID] = status
	}
	out := make([]models.Status, 0, len(ids))
	for _, id := range ids {
		if status, ok := byID[id]; ok {
			out = append(out, status)
		}
	}
	return out
}

func (s *Server) accountWarningMailRules(warning models.AccountWarning) []models.Rule {
	if s == nil || s.db == nil || warning.Report.ID == 0 || len(warning.Report.RuleIDs) == 0 {
		return nil
	}
	var rules []models.Rule
	if err := s.db.Where("id IN ?", []int64(warning.Report.RuleIDs)).Order("priority ASC, id ASC").Find(&rules).Error; err != nil {
		return nil
	}
	return rules
}

func statusURLForMail(cfg config.Config, status models.Status) string {
	if value := notificationStatusURL(&status); value != "" {
		return value
	}
	if status.ID == 0 || status.Account.ID == 0 {
		return ""
	}
	acct := status.Account.Acct()
	if strings.TrimSpace(acct) == "" {
		acct = status.Account.Username
	}
	if strings.TrimSpace(acct) == "" {
		return ""
	}
	return cfg.BaseURL() + "/@" + strings.TrimPrefix(acct, "@") + "/" + strconv.FormatInt(status.ID, 10)
}

func (s *Server) sendAppealDecisionMail(appeal models.Appeal, approved bool) error {
	var user models.User
	if s.db == nil {
		return nil
	}
	if err := s.db.Preload("Account").Where("account_id = ?", appeal.AccountID).First(&user).Error; err != nil {
		return nil
	}
	if strings.TrimSpace(user.Email) == "" {
		return nil
	}
	return s.enqueueOrDeliverMail(user, "present", appealDecisionMailMessage(s.cfg, user, appeal, approved))
}

func (s *Server) sendBackupReadyMail(user models.User, backup models.Backup) error {
	if !userReceivesSecurityMail(user) {
		return nil
	}
	return s.enqueueOrDeliverMail(user, "security", backupReadyMailMessage(s.cfg, user, backup))
}

func backupReadyMailMessage(cfg config.Config, user models.User, backup models.Backup) mailMessage {
	locale := mailLocale(cfg, user)
	body := settingsT(locale, "user_mailer.backup_ready.title", "Archive takeout") + "\n\n===\n\n"
	body += settingsT(locale, "user_mailer.backup_ready.explanation", "You requested a full backup of your Mastodon account. It's now ready for download!") + "\n\n"
	body += "=> " + cfg.BaseURL() + "/backups/" + fmt.Sprint(backup.ID) + "/download\n"
	return mailMessage{
		To:      user.Email,
		Subject: settingsT(locale, "user_mailer.backup_ready.subject", "Your archive is ready for download"),
		Body:    body,
	}
}

func (s *Server) sendStaffNewReportMails(report models.Report) error {
	if s.db == nil {
		return nil
	}
	users, err := s.staffUsersWithPermission(s.db, rolePermissionManageReports)
	if err != nil {
		return err
	}
	for _, user := range users {
		if !userSettingBool(user, "notification_emails.report", true) || strings.TrimSpace(user.Email) == "" {
			continue
		}
		if err := s.enqueueOrDeliverMail(user, "present", staffNewReportMailMessage(s.cfg, user, report)); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) sendStaffNewAppealMails(appeal models.Appeal) error {
	if s.db == nil {
		return nil
	}
	users, err := s.staffUsersWithPermission(s.db, rolePermissionManageAppeals)
	if err != nil {
		return err
	}
	for _, user := range users {
		if !userSettingBool(user, "notification_emails.appeal", true) || strings.TrimSpace(user.Email) == "" {
			continue
		}
		if err := s.enqueueOrDeliverMail(user, "present", staffNewAppealMailMessage(s.cfg, user, appeal)); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) sendStaffNewPendingAccountMails(user models.User) error {
	if s.db == nil {
		return nil
	}
	if user.Account == nil {
		var account models.Account
		if err := s.db.Where("id = ?", user.AccountID).First(&account).Error; err == nil {
			user.Account = &account
		}
	}
	var request models.UserInviteRequest
	_ = s.db.Where("user_id = ?", user.ID).First(&request).Error
	users, err := s.staffUsersWithPermission(s.db, rolePermissionManageUsers)
	if err != nil {
		return err
	}
	for _, staff := range users {
		if !userSettingBool(staff, "notification_emails.pending_account", true) || strings.TrimSpace(staff.Email) == "" {
			continue
		}
		if err := s.enqueueOrDeliverMail(staff, "present", staffNewPendingAccountMailMessage(s.cfg, staff, user, request)); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) sendAutoCloseRegistrationsMails() error {
	if s.db == nil {
		return nil
	}
	users, err := s.staffUsersWithPermission(s.db, rolePermissionManageSettings)
	if err != nil {
		return err
	}
	for _, user := range users {
		if strings.TrimSpace(user.Email) == "" {
			continue
		}
		if err := s.enqueueOrDeliverMail(user, "present", autoCloseRegistrationsMailMessage(s.cfg, user)); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) sendSoftwareUpdateMails(updates []models.SoftwareUpdate) error {
	if s.db == nil || len(updates) == 0 {
		return nil
	}
	hasUrgent := false
	hasPatch := false
	for _, update := range updates {
		if update.Urgent {
			hasUrgent = true
		}
		if update.Type == 0 {
			hasPatch = true
		}
	}
	users, err := s.staffUsersWithPermission(s.db, rolePermissionViewDevops)
	if err != nil {
		return err
	}
	for _, user := range users {
		if strings.TrimSpace(user.Email) == "" || !shouldNotifySoftwareUpdateUser(user, hasUrgent, hasPatch) {
			continue
		}
		message := softwareUpdateMailMessage(s.cfg, user, hasUrgent)
		if err := s.enqueueOrDeliverMail(user, "present", message); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) sendTrendsReviewMails(items ...trendsReviewMailItems) error {
	if s.db == nil {
		return nil
	}
	var mailItems trendsReviewMailItems
	if len(items) > 0 {
		mailItems = items[0]
	}
	users, err := s.staffUsersWithPermission(s.db, rolePermissionManageTaxonomies)
	if err != nil {
		return err
	}
	for _, user := range users {
		if strings.TrimSpace(user.Email) == "" || !userSettingBool(user, "notification_emails.trends", true) {
			continue
		}
		if err := s.enqueueOrDeliverMail(user, "present", trendsReviewMailMessageWithItems(s.cfg, user, mailItems)); err != nil {
			return err
		}
	}
	return nil
}

func staffNewReportMailMessage(cfg config.Config, user models.User, report models.Report) mailMessage {
	locale := mailLocale(cfg, user)
	instance := firstNonEmpty(cfg.LocalDomain, cfg.WebDomain, "Mastodon")
	target := report.TargetAccount.Acct()
	reporter := report.Account.Acct()
	body := mailSalutation(locale, user) + "\n\n"
	if report.Account.Local() {
		body += adminTVars(locale, "admin_mailer.new_report.body", "%{reporter} has reported %{target}", map[string]string{"target": target, "reporter": reporter}) + "\n\n"
	} else {
		body += adminTVars(locale, "admin_mailer.new_report.body_remote", "Someone from %{domain} has reported %{target}", map[string]string{"target": target, "domain": firstNonEmpty(report.Account.Domain.String, "a remote server")}) + "\n\n"
	}
	body += settingsT(locale, "application_mailer.view", "View:") + " " + cfg.BaseURL() + "/admin/reports/" + fmt.Sprint(report.ID) + "\n"
	return mailMessage{
		To:       user.Email,
		Subject:  "[" + instance + "] " + adminTVars(locale, "admin_mailer.new_report.subject", "New report for %{instance} (#%{id})", map[string]string{"instance": instance, "id": fmt.Sprint(report.ID)}),
		Body:     body,
		TextOnly: true,
	}
}

func staffNewAppealMailMessage(cfg config.Config, user models.User, appeal models.Appeal) mailMessage {
	locale := mailLocale(cfg, user)
	instance := firstNonEmpty(cfg.LocalDomain, cfg.WebDomain, "Mastodon")
	username := appeal.Account.Username
	if username == "" {
		username = appeal.Account.Acct()
	}
	actionBy := appeal.Strike.Account.Username
	if actionBy == "" {
		actionBy = appeal.Strike.Account.Acct()
	}
	body := mailSalutation(locale, user) + "\n\n"
	body += adminTVars(locale, "admin_mailer.new_appeal.body", "%{target} is appealing a moderation decision by %{action_taken_by} from %{date}, which was %{type}. They wrote:", map[string]string{
		"target":          username,
		"action_taken_by": actionBy,
		"date":            mailDate(appeal.Strike.CreatedAt),
		"type":            accountWarningAppealActionLabel(appeal.Strike.Action, locale),
	}) + "\n\n"
	if strings.TrimSpace(appeal.Text) != "" {
		body += "> " + strings.ReplaceAll(strings.TrimSpace(appeal.Text), "\n", "\n> ") + "\n\n"
	}
	body += adminT(locale, "admin_mailer.new_appeal.next_steps", "You can approve the appeal to undo the moderation decision, or ignore it.") + "\n\n"
	body += settingsT(locale, "application_mailer.view", "View:") + " " + cfg.BaseURL() + "/disputes/strikes/" + fmt.Sprint(appeal.AccountWarningID) + "\n"
	return mailMessage{
		To:       user.Email,
		Subject:  "[" + instance + "] " + adminTVars(locale, "admin_mailer.new_appeal.subject", "%{username} is appealing a moderation decision on %{instance}", map[string]string{"instance": instance, "username": username}),
		Body:     body,
		TextOnly: true,
	}
}

func staffNewPendingAccountMailMessage(cfg config.Config, staff models.User, pending models.User, request models.UserInviteRequest) mailMessage {
	locale := mailLocale(cfg, staff)
	instance := firstNonEmpty(cfg.LocalDomain, cfg.WebDomain, "Mastodon")
	username := ""
	if pending.Account != nil {
		username = pending.Account.Username
	}
	body := mailSalutation(locale, staff) + "\n\n"
	body += adminT(locale, "admin_mailer.new_pending_account.body", "The details of the new account are below. You can approve or reject this application.") + "\n\n"
	body += pending.Email + " (@" + username + ")\n"
	if pending.SignUpIP.Valid && strings.TrimSpace(pending.SignUpIP.String) != "" {
		body += pending.SignUpIP.String + "\n"
	}
	if request.Text.Valid && strings.TrimSpace(request.Text.String) != "" {
		body += "\n> " + strings.ReplaceAll(strings.TrimSpace(request.Text.String), "\n", "\n> ") + "\n"
	}
	body += "\n" + settingsT(locale, "application_mailer.view", "View:") + " " + cfg.BaseURL() + "/admin/accounts?status=pending\n"
	return mailMessage{
		To:       staff.Email,
		Subject:  "[" + instance + "] " + adminTVars(locale, "admin_mailer.new_pending_account.subject", "New account up for review on %{instance} (%{username})", map[string]string{"instance": instance, "username": username}),
		Body:     body,
		TextOnly: true,
	}
}

func trendsReviewMailMessage(cfg config.Config, user models.User) mailMessage {
	return trendsReviewMailMessageWithItems(cfg, user, trendsReviewMailItems{})
}

func trendsReviewMailMessageWithItems(cfg config.Config, user models.User, items trendsReviewMailItems) mailMessage {
	locale := mailLocale(cfg, user)
	instance := firstNonEmpty(cfg.LocalDomain, cfg.WebDomain, "Mastodon")
	body := mailSalutation(locale, user) + "\n\n"
	body += adminT(locale, "admin_mailer.new_trends.body", "The following items need a review before they can be displayed publicly:") + "\n\n"
	if len(items.Links) > 0 {
		body += trendsReviewLinksMailSection(cfg, locale, items.Links) + "\n"
	}
	if len(items.Tags) > 0 {
		body += trendsReviewTagsMailSection(cfg, locale, items.Tags) + "\n"
	}
	if len(items.Statuses) > 0 {
		body += trendsReviewStatusesMailSection(cfg, locale, items.Statuses) + "\n"
	}
	if len(items.Links) == 0 && len(items.Tags) == 0 && len(items.Statuses) == 0 {
		body += adminT(locale, "admin_mailer.new_trends.new_trending_tags.title", "Trending hashtags") + "\n"
		body += cfg.BaseURL() + "/admin/trends/tags\n\n"
		body += adminT(locale, "admin_mailer.new_trends.new_trending_links.title", "Trending links") + "\n"
		body += cfg.BaseURL() + "/admin/trends/links\n\n"
		body += adminT(locale, "admin_mailer.new_trends.new_trending_statuses.title", "Trending posts") + "\n"
		body += cfg.BaseURL() + "/admin/trends/statuses\n"
	}
	return mailMessage{
		To:       user.Email,
		Subject:  "[" + instance + "] " + adminTVars(locale, "admin_mailer.new_trends.subject", "New trends up for review on %{instance}", map[string]string{"instance": instance}),
		Body:     body,
		TextOnly: true,
	}
}

func trendsReviewLinksMailSection(cfg config.Config, locale string, links []trendsReviewLink) string {
	var body strings.Builder
	body.WriteString(adminT(locale, "admin_mailer.new_trends.new_trending_links.title", "Trending links") + "\n\n")
	for _, link := range links {
		title := strings.TrimSpace(link.Title)
		if title == "" {
			title = strings.TrimSpace(link.URL)
		}
		body.WriteString("- " + title)
		if strings.TrimSpace(link.URL) != "" && strings.TrimSpace(link.URL) != title {
			body.WriteString(" · " + strings.TrimSpace(link.URL))
		}
		body.WriteString("\n")
		body.WriteString("  " + trendReviewMailMeta(locale, link.Language, link.Score) + "\n")
	}
	body.WriteString("\n" + settingsT(locale, "application_mailer.view", "View:") + " " + cfg.BaseURL() + "/admin/trends/links\n")
	return body.String()
}

func trendsReviewTagsMailSection(cfg config.Config, locale string, tags []trendsReviewTag) string {
	var body strings.Builder
	body.WriteString(adminT(locale, "admin_mailer.new_trends.new_trending_tags.title", "Trending hashtags") + "\n\n")
	for _, tag := range tags {
		body.WriteString("- #" + strings.TrimPrefix(strings.TrimSpace(tag.Name), "#") + "\n")
		body.WriteString("  " + trendReviewMailMeta(locale, "", tag.Score) + "\n")
	}
	body.WriteString("\n" + settingsT(locale, "application_mailer.view", "View:") + " " + cfg.BaseURL() + "/admin/trends/tags?status=pending_review\n")
	return body.String()
}

func trendsReviewStatusesMailSection(cfg config.Config, locale string, statuses []trendsReviewStatus) string {
	var body strings.Builder
	body.WriteString(adminT(locale, "admin_mailer.new_trends.new_trending_statuses.title", "Trending posts") + "\n\n")
	for _, status := range statuses {
		body.WriteString("- " + strings.TrimSpace(status.URL) + "\n")
		body.WriteString("  " + trendReviewMailMeta(locale, status.Language, status.Score) + "\n")
	}
	body.WriteString("\n" + settingsT(locale, "application_mailer.view", "View:") + " " + cfg.BaseURL() + "/admin/trends/statuses\n")
	return body.String()
}

func trendReviewMailMeta(locale string, language string, score float64) string {
	meta := strings.TrimSpace(language)
	scoreText := adminTVars(locale, "admin.trends.tags.current_score", "Current score %{score}", map[string]string{"score": fmt.Sprintf("%.2f", score)})
	if meta == "" {
		return scoreText
	}
	return meta + " · " + scoreText
}

func softwareUpdateMailMessage(cfg config.Config, user models.User, critical bool) mailMessage {
	locale := mailLocale(cfg, user)
	instance := firstNonEmpty(cfg.LocalDomain, cfg.WebDomain, "Mastodon")
	title := adminTVars(locale, "admin_mailer.new_software_updates.subject", "New Mastodon versions are available for %{instance}!", map[string]string{"instance": instance})
	body := mailSalutation(locale, user) + "\n\n"
	body += adminT(locale, "admin_mailer.new_software_updates.body", "New Mastodon versions have been released, you may want to update!") + "\n\n"
	if critical {
		title = adminTVars(locale, "admin_mailer.new_critical_software_updates.subject", "Critical Mastodon updates are available for %{instance}!", map[string]string{"instance": instance})
		body = mailSalutation(locale, user) + "\n\n"
		body += adminT(locale, "admin_mailer.new_critical_software_updates.body", "New critical versions of Mastodon have been released, you may want to update as soon as possible!") + "\n\n"
	}
	body += settingsT(locale, "application_mailer.view", "View:") + " " + cfg.BaseURL() + "/admin/software_updates\n"
	headers := []mailHeader(nil)
	if critical {
		headers = []mailHeader{
			{Key: "Priority", Value: "urgent"},
			{Key: "X-Priority", Value: "1"},
			{Key: "Importance", Value: "high"},
		}
	}
	return mailMessage{
		To:       user.Email,
		Subject:  "[" + instance + "] " + title,
		Body:     body,
		TextOnly: true,
		Headers:  headers,
	}
}

func autoCloseRegistrationsMailMessage(cfg config.Config, user models.User) mailMessage {
	locale := mailLocale(cfg, user)
	instance := firstNonEmpty(cfg.LocalDomain, cfg.WebDomain, "Mastodon")
	body := adminTVars(locale, "admin_mailer.auto_close_registrations.body", "Due to a lack of recent moderator activity, registrations on %{instance} have been automatically switched to requiring manual review, to prevent %{instance} from being used as a platform for potential bad actors. You can switch it back to open registrations at any time.", map[string]string{"instance": instance}) + "\n\n"
	body += settingsT(locale, "application_mailer.view", "View:") + " " + cfg.BaseURL() + "/admin/settings/registrations\n"
	return mailMessage{
		To:       user.Email,
		Subject:  "[" + instance + "] " + adminTVars(locale, "admin_mailer.auto_close_registrations.subject", "Registrations for %{instance} have been automatically switched to requiring approval", map[string]string{"instance": instance}),
		Body:     body,
		TextOnly: true,
	}
}

func suspiciousSignInMailMessage(cfg config.Config, user models.User, remoteIP string, userAgent string, timestamp time.Time) mailMessage {
	locale := mailLocale(cfg, user)
	browser := firstNonEmpty(strings.TrimSpace(userAgent), settingsT(locale, "sessions.browsers.unknown_browser", "Unknown Browser"))
	platform := settingsT(locale, "sessions.platforms.unknown_platform", "Unknown Platform")
	browserDescription := settingsTVars(locale, "sessions.description", "%{browser} on %{platform}", map[string]string{"browser": browser, "platform": platform})
	action := settingsT(locale, "user_mailer.suspicious_sign_in.change_password", "change your password")
	body := settingsT(locale, "user_mailer.suspicious_sign_in.title", "A new sign-in") + "\n\n===\n\n"
	body += settingsT(locale, "user_mailer.suspicious_sign_in.explanation", "We've detected a sign-in to your account from a new IP address.") + "\n\n"
	body += settingsT(locale, "user_mailer.suspicious_sign_in.details", "Here are details of the sign-in:") + "\n\n"
	body += settingsT(locale, "sessions.ip", "IP") + ": " + strings.TrimSpace(remoteIP) + "\n"
	body += settingsT(locale, "sessions.browser", "Browser") + ": " + browserDescription + "\n"
	body += mailTimeWithZone(locale, user, timestamp) + "\n\n"
	body += settingsTVars(locale, "user_mailer.suspicious_sign_in.further_actions_html", "If this wasn't you, we recommend that you %{action} immediately and enable two-factor authentication to keep your account secure.", map[string]string{"action": action}) + "\n\n"
	body += "=> " + cfg.BaseURL() + "/auth/edit\n"
	return mailMessage{
		To:      user.Email,
		Subject: settingsT(locale, "user_mailer.suspicious_sign_in.subject", "Your account has been accessed from a new IP address"),
		Body:    body,
	}
}

func appealDecisionMailMessage(cfg config.Config, user models.User, appeal models.Appeal, approved bool) mailMessage {
	locale := mailLocale(cfg, user)
	key := "user_mailer.appeal_rejected"
	appealDate := mailTimeWithZone(locale, user, appeal.CreatedAt)
	strikeDate := mailTimeWithZone(locale, user, appeal.Strike.CreatedAt)
	subjectDate := mailDate(appeal.CreatedAt)
	subjectFallback := "Your appeal from %{date} has been rejected"
	titleFallback := "Appeal rejected"
	explanationFallback := "The appeal of the strike against your account on %{strike_date} that you submitted on %{appeal_date} has been rejected."
	if approved {
		key = "user_mailer.appeal_approved"
		subjectFallback = "Your appeal from %{date} has been approved"
		titleFallback = "Appeal approved"
		explanationFallback = "The appeal of the strike against your account on %{strike_date} that you submitted on %{appeal_date} has been approved. Your account is once again in good standing."
	}
	subject := settingsTVars(locale, key+".subject", subjectFallback, map[string]string{"date": subjectDate})
	title := settingsT(locale, key+".title", titleFallback)
	explanation := settingsTVars(locale, key+".explanation", explanationFallback, map[string]string{
		"appeal_date": appealDate,
		"strike_date": strikeDate,
	})
	body := title + "\n\n===\n\n" + explanation + "\n\n=> " + cfg.BaseURL() + "/\n"
	return mailMessage{To: user.Email, Subject: subject, Body: body}
}

func mailDate(t time.Time) string {
	if t.IsZero() {
		return "unknown date"
	}
	return t.UTC().Format("Jan 2, 2006")
}

func mailTimeWithZone(locale string, user models.User, t time.Time) string {
	location := time.UTC
	if user.TimeZone.Valid && strings.TrimSpace(user.TimeZone.String) != "" {
		if loaded, err := time.LoadLocation(strings.TrimSpace(user.TimeZone.String)); err == nil {
			location = loaded
		}
	}
	localTime := t.In(location)
	if strings.HasPrefix(locale, "ja") {
		return localTime.Format("2006年01月02日 15:04 MST")
	}
	return localTime.Format("Jan 02, 2006, 15:04 MST")
}

func mailAccountName(user models.User) string {
	if user.Account != nil && strings.TrimSpace(user.Account.Username) != "" {
		return "@" + user.Account.Acct()
	}
	return firstNonEmpty(user.Email, "there")
}

func mailLocale(cfg config.Config, user models.User) string {
	if locale := userSettingString(user, "locale", ""); locale != "" {
		return locale
	}
	if user.Locale.Valid && strings.TrimSpace(user.Locale.String) != "" {
		return strings.TrimSpace(user.Locale.String)
	}
	return cfg.Locale()
}

func mailSalutation(locale string, user models.User) string {
	return settingsTVars(locale, "application_mailer.salutation", "%{name},", map[string]string{"name": mailAccountName(user)})
}

func accountWarningAppealActionLabel(action int, locale string) string {
	key := accountWarningAction(action)
	return adminT(locale, "admin_mailer.new_appeal.actions."+key, key)
}

func userSettingBool(user models.User, key string, defaultValue bool) bool {
	settings := decodeUserSettings(user.Settings.String)
	if value, ok := settings[key]; ok {
		return rawBool(value, defaultValue)
	}
	parts := strings.Split(key, ".")
	if len(parts) == 2 {
		if nested, ok := settings[parts[0]].(map[string]any); ok {
			if value, ok := nested[parts[1]]; ok {
				return rawBool(value, defaultValue)
			}
		}
	}
	return defaultValue
}

func userSettingString(user models.User, key string, defaultValue string) string {
	settings := decodeUserSettings(user.Settings.String)
	if value, ok := settings[key]; ok {
		if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
			return strings.TrimSpace(text)
		}
	}
	parts := strings.Split(key, ".")
	if len(parts) == 2 {
		if nested, ok := settings[parts[0]].(map[string]any); ok {
			if value, ok := nested[parts[1]]; ok {
				if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
					return strings.TrimSpace(text)
				}
			}
		}
	}
	return defaultValue
}

func shouldNotifySoftwareUpdateUser(user models.User, urgentVersion bool, patchVersion bool) bool {
	switch userSettingString(user, "notification_emails.software_updates", "critical") {
	case "none":
		return false
	case "patch":
		return urgentVersion || patchVersion
	case "all":
		return true
	default:
		return urgentVersion
	}
}

func decodeUserSettings(raw string) map[string]any {
	settings := map[string]any{}
	if strings.TrimSpace(raw) == "" {
		return settings
	}
	_ = json.Unmarshal([]byte(raw), &settings)
	return settings
}

func rawBool(value any, defaultValue bool) bool {
	switch v := value.(type) {
	case bool:
		return v
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "true", "1", "yes", "on":
			return true
		case "false", "0", "no", "off":
			return false
		default:
			return defaultValue
		}
	case float64:
		return v != 0
	default:
		return defaultValue
	}
}

func (s *Server) sendSecuritySettingsMail(user models.User, key string, fallbackSubject string, fallbackTitle string, fallbackExplanation string, detail string) error {
	if !userReceivesSecurityMail(user) {
		return nil
	}
	return s.enqueueOrDeliverMail(user, "security", securitySettingsMailMessage(s.cfg, user, key, fallbackSubject, fallbackTitle, fallbackExplanation, detail))
}

func (s *Server) enqueueOrDeliverMail(user models.User, eligibility string, message mailMessage) error {
	if s != nil && s.asynqClient != nil {
		return s.enqueueMailerDeliveryTask(user.ID, eligibility, message)
	}
	if s == nil {
		return fmt.Errorf("mailer delivery: server is not configured")
	}
	return sendMail(s.cfg, message)
}

func mailerRecipientStillBelongsToUser(user models.User, recipient string) bool {
	address := strings.ToLower(strings.TrimSpace(envelopeMailAddress(recipient)))
	if address == "" {
		return false
	}
	if address == strings.ToLower(strings.TrimSpace(user.Email)) {
		return true
	}
	return user.UnconfirmedEmail.Valid && address == strings.ToLower(strings.TrimSpace(user.UnconfirmedEmail.String))
}

func securitySettingsMailMessage(cfg config.Config, user models.User, key string, fallbackSubject string, fallbackTitle string, fallbackExplanation string, detail string) mailMessage {
	locale := mailLocale(cfg, user)
	subject := settingsT(locale, key+".subject", fallbackSubject)
	title := settingsT(locale, key+".title", fallbackTitle)
	explanation := settingsT(locale, key+".explanation", fallbackExplanation)
	body := title + "\n\n===\n\n" + explanation + "\n"
	if strings.TrimSpace(detail) != "" {
		body += "\n" + strings.TrimSpace(detail) + "\n"
	}
	body += "\n=> " + cfg.BaseURL() + "/auth/edit\n"
	return mailMessage{
		To:      user.Email,
		Subject: subject,
		Body:    body,
	}
}

func userReceivesSecurityMail(user models.User) bool {
	return strings.TrimSpace(user.Email) != "" && !user.Disabled && user.Approved && user.ConfirmedAt.Valid
}

func accountWarningMailSubject(action string, acct string) string {
	switch action {
	case "disable":
		return "Your account " + acct + " has been frozen"
	case "mark_statuses_as_sensitive":
		return "Your posts on " + acct + " have been marked as sensitive"
	case "delete_statuses":
		return "Your posts on " + acct + " have been removed"
	case "sensitive":
		return "Your posts on " + acct + " will be marked as sensitive from now on"
	case "silence":
		return "Your account " + acct + " has been limited"
	case "suspend":
		return "Your account " + acct + " has been suspended"
	default:
		return "Warning for " + acct
	}
}

func accountWarningMailTitle(action string) string {
	switch action {
	case "disable":
		return "Account frozen"
	case "mark_statuses_as_sensitive":
		return "Posts marked as sensitive"
	case "delete_statuses":
		return "Posts removed"
	case "sensitive":
		return "Account marked as sensitive"
	case "silence":
		return "Account limited"
	case "suspend":
		return "Account suspended"
	default:
		return "Warning"
	}
}

func accountWarningMailExplanation(action string, instance string) string {
	instance = firstNonEmpty(instance, "this server")
	switch action {
	case "disable":
		return "You can no longer use your account, but your profile and other data remains intact. You can request a backup of your data, change account settings or delete your account."
	case "mark_statuses_as_sensitive":
		return "Some of your posts have been marked as sensitive by the moderators of " + instance + ". This means that people will need to tap the media in the posts before a preview is displayed. You can mark media as sensitive yourself when posting in the future."
	case "delete_statuses":
		return "Some of your posts have been found to violate one or more community guidelines and have been subsequently removed by the moderators of " + instance + "."
	case "sensitive":
		return "From now on, all your uploaded media files will be marked as sensitive and hidden behind a click-through warning."
	case "silence":
		return "You can still use your account but only people who are already following you will see your posts on this server, and you may be excluded from various discovery features. However, others may still manually follow you."
	case "suspend":
		return "You can no longer use your account, and your profile and other data are no longer accessible. You can still login to request a backup of your data until the data is fully removed in about 30 days, but we will retain some basic data to prevent you from evading the suspension."
	default:
		return ""
	}
}

func sendMail(cfg config.Config, message mailMessage) error {
	if !smtpDeliveryEnabled(cfg) {
		captureDevelopmentMailPreview(cfg, message)
		return nil
	}
	addr := net.JoinHostPort(cfg.SMTPServer, firstNonEmpty(cfg.SMTPPort, "587"))
	client, err := smtpClient(cfg, addr)
	if err != nil {
		return err
	}
	defer client.Close()
	if auth := smtpAuthForConfig(cfg); auth != nil {
		if err := client.Auth(auth); err != nil {
			return err
		}
	}
	from := envelopeMailAddress(firstNonEmpty(cfg.SMTPReturnPath, cfg.SMTPFrom))
	to := envelopeMailAddress(message.To)
	if err := client.Mail(from); err != nil {
		return err
	}
	if err := client.Rcpt(to); err != nil {
		return err
	}
	writer, err := client.Data()
	if err != nil {
		return err
	}
	if _, err := writer.Write(buildMailMessage(cfg, message)); err != nil {
		_ = writer.Close()
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	return client.Quit()
}

func smtpAuthForConfig(cfg config.Config) smtp.Auth {
	if cfg.SMTPLogin == "" && cfg.SMTPPassword == "" {
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(cfg.SMTPAuthMethod)) {
	case "none":
		return nil
	case "login":
		return smtpLoginAuth{username: cfg.SMTPLogin, password: cfg.SMTPPassword}
	case "cram_md5":
		return smtp.CRAMMD5Auth(cfg.SMTPLogin, cfg.SMTPPassword)
	default:
		return smtp.PlainAuth("", cfg.SMTPLogin, cfg.SMTPPassword, smtpDomainForAuth(cfg))
	}
}

func smtpDomainForAuth(cfg config.Config) string {
	if cfg.SMTPDomainSet {
		return cfg.SMTPDomain
	}
	return firstNonEmpty(cfg.SMTPDomain, cfg.SMTPServer)
}

type smtpLoginAuth struct {
	username string
	password string
}

func (a smtpLoginAuth) Start(*smtp.ServerInfo) (string, []byte, error) {
	return "LOGIN", nil, nil
}

func (a smtpLoginAuth) Next(fromServer []byte, more bool) ([]byte, error) {
	if !more {
		return nil, nil
	}
	challenge := strings.ToLower(string(fromServer))
	if strings.Contains(challenge, "username") {
		return []byte(a.username), nil
	}
	if strings.Contains(challenge, "password") {
		return []byte(a.password), nil
	}
	if a.username != "" {
		return []byte(a.username), nil
	}
	return []byte(a.password), nil
}

func smtpConfigured(cfg config.Config) bool {
	return strings.TrimSpace(cfg.SMTPServer) != ""
}

func smtpDeliveryEnabled(cfg config.Config) bool {
	method := strings.ToLower(strings.TrimSpace(cfg.SMTPDeliveryMethod))
	return smtpConfigured(cfg) && (method == "" || method == "smtp")
}

func smtpClient(cfg config.Config, addr string) (*smtp.Client, error) {
	dialer := &net.Dialer{Timeout: smtpReadTimeout}
	tlsConfig, err := smtpTLSConfigForConfig(cfg)
	if err != nil {
		return nil, err
	}
	if cfg.SMTPTLS {
		conn, err := tls.DialWithDialer(dialer, "tcp", addr, tlsConfig)
		if err != nil {
			return nil, err
		}
		return smtp.NewClient(smtpTimeoutConn(conn, smtpReadTimeout), cfg.SMTPServer)
	}
	conn, err := dialer.Dial("tcp", addr)
	if err != nil {
		return nil, err
	}
	client, err := smtp.NewClient(smtpTimeoutConn(conn, smtpReadTimeout), cfg.SMTPServer)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if cfg.SMTPStartTLSRequired {
		if err := client.StartTLS(tlsConfig); err != nil {
			_ = client.Close()
			return nil, err
		}
	} else if cfg.SMTPStartTLS {
		if ok, _ := client.Extension("STARTTLS"); ok {
			if err := client.StartTLS(tlsConfig); err != nil {
				_ = client.Close()
				return nil, err
			}
		}
	}
	return client, nil
}

func smtpTLSConfigForConfig(cfg config.Config) (*tls.Config, error) {
	tlsConfig := &tls.Config{ServerName: cfg.SMTPServer, MinVersion: tls.VersionTLS12}
	if smtpVerifyModeSkipsVerify(cfg.SMTPOpenSSLVerifyMode) {
		tlsConfig.InsecureSkipVerify = true
	}
	if caFile := strings.TrimSpace(cfg.SMTPCAFile); caFile != "" {
		data, err := os.ReadFile(caFile)
		if err == nil {
			pool, err := x509.SystemCertPool()
			if err != nil || pool == nil {
				pool = x509.NewCertPool()
			}
			if pool.AppendCertsFromPEM(data) {
				tlsConfig.RootCAs = pool
			}
		} else if caFile != "/etc/ssl/certs/ca-certificates.crt" {
			return nil, err
		}
	}
	return tlsConfig, nil
}

func smtpVerifyModeSkipsVerify(mode string) bool {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "none", "verify_none", "openssl::ssl::verify_none":
		return true
	default:
		return false
	}
}

type deadlineConn struct {
	net.Conn
	timeout time.Duration
}

func smtpTimeoutConn(conn net.Conn, timeout time.Duration) net.Conn {
	if timeout <= 0 {
		return conn
	}
	return deadlineConn{Conn: conn, timeout: timeout}
}

func (c deadlineConn) Read(b []byte) (int, error) {
	_ = c.Conn.SetReadDeadline(time.Now().Add(c.timeout))
	return c.Conn.Read(b)
}

func (c deadlineConn) Write(b []byte) (int, error) {
	_ = c.Conn.SetWriteDeadline(time.Now().Add(c.timeout))
	return c.Conn.Write(b)
}

func buildMailMessage(cfg config.Config, message mailMessage) []byte {
	headers := map[string]string{
		"Date":                     time.Now().UTC().Format(time.RFC1123Z),
		"From":                     formatMailAddress(cfg.SMTPFrom),
		"To":                       formatMailAddress(message.To),
		"Subject":                  encodeMailHeader(message.Subject),
		"Message-ID":               newMailMessageID(cfg, message),
		"MIME-Version":             "1.0",
		"Precedence":               "list",
		"X-Auto-Response-Suppress": "All",
		"Auto-Submitted":           "auto-generated",
	}
	order := []string{"Date", "From", "To", "Subject", "Message-ID", "MIME-Version", "Precedence", "X-Auto-Response-Suppress", "Auto-Submitted"}
	if strings.TrimSpace(cfg.SMTPReplyTo) != "" {
		headers["Reply-To"] = formatMailAddress(cfg.SMTPReplyTo)
		order = append(order, "Reply-To")
	}
	if strings.TrimSpace(cfg.SMTPReturnPath) != "" {
		headers["Return-Path"] = formatMailAddress(cfg.SMTPReturnPath)
		order = append(order, "Return-Path")
	}
	for _, header := range message.Headers {
		key := sanitizeMailHeaderKey(header.Key)
		if key == "" {
			continue
		}
		headers[key] = encodeMailHeaderValue(key, header.Value)
		order = append(order, key)
	}
	var body bytes.Buffer
	if message.TextOnly {
		headers["Content-Type"] = "text/plain; charset=UTF-8"
		headers["Content-Transfer-Encoding"] = "8bit"
		order = append(order, "Content-Type", "Content-Transfer-Encoding")
		body.WriteString(normalizeMailBody(message.Body))
	} else {
		boundary := mailMultipartBoundary(message)
		headers["Content-Type"] = `multipart/alternative; boundary="` + boundary + `"`
		order = append(order, "Content-Type")
		writer := multipart.NewWriter(&body)
		_ = writer.SetBoundary(boundary)
		textHeader := textproto.MIMEHeader{}
		textHeader.Set("Content-Type", "text/plain; charset=UTF-8")
		textHeader.Set("Content-Transfer-Encoding", "8bit")
		textPart, _ := writer.CreatePart(textHeader)
		_, _ = textPart.Write([]byte(normalizeMailBody(message.Body)))
		htmlHeader := textproto.MIMEHeader{}
		htmlHeader.Set("Content-Type", "text/html; charset=UTF-8")
		htmlHeader.Set("Content-Transfer-Encoding", "8bit")
		htmlPart, _ := writer.CreatePart(htmlHeader)
		htmlBody := message.HTMLBody
		if strings.TrimSpace(htmlBody) == "" {
			htmlBody = mailHTMLFromText(message.Body)
		}
		_, _ = htmlPart.Write([]byte(normalizeMailBody(htmlBody)))
		_ = writer.Close()
	}
	var b strings.Builder
	for _, key := range order {
		b.WriteString(foldMailHeader(key, headers[key]))
	}
	b.WriteString("\r\n")
	b.Write(body.Bytes())
	return []byte(b.String())
}

func mailMultipartBoundary(message mailMessage) string {
	sum := sha256.Sum256([]byte(message.To + "\x00" + message.Subject + "\x00" + message.Body + "\x00" + message.HTMLBody))
	return fmt.Sprintf("paon_%x", sum[:12])
}

func newMailMessageID(cfg config.Config, message mailMessage) string {
	counter := mailMessageIDCounter.Add(1)
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d\x00%d\x00%s\x00%s", time.Now().UnixNano(), counter, message.To, message.Subject)))
	domain := firstNonEmpty(cfg.LocalDomain, cfg.WebDomain, "localhost")
	domain = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '-' {
			return r
		}
		return -1
	}, domain)
	return fmt.Sprintf("<%x@%s>", sum[:16], firstNonEmpty(domain, "localhost"))
}

func encodeMailHeader(value string) string {
	value = sanitizeHeader(value)
	if value == "" {
		return ""
	}
	return mime.QEncoding.Encode("UTF-8", value)
}

func encodeMailHeaderValue(key string, value string) string {
	value = sanitizeHeader(value)
	switch strings.ToLower(key) {
	case "in-reply-to", "references", "list-id", "list-unsubscribe", "list-unsubscribe-post", "priority", "x-priority", "importance":
		return value
	default:
		return encodeMailHeader(value)
	}
}

func formatMailAddress(value string) string {
	value = sanitizeHeader(value)
	parsed, err := mail.ParseAddress(value)
	if err != nil || strings.TrimSpace(parsed.Address) == "" {
		if value != "" {
			return value
		}
		return envelopeMailAddress(value)
	}
	parsed.Name = sanitizeHeader(parsed.Name)
	if parsed.Name == "" {
		return parsed.Address
	}
	return parsed.String()
}

func envelopeMailAddress(value string) string {
	value = sanitizeHeader(value)
	if parsed, err := mail.ParseAddress(value); err == nil && strings.TrimSpace(parsed.Address) != "" {
		return parsed.Address
	}
	if strings.Contains(value, "@") && !strings.ContainsAny(value, " <>\t") {
		return value
	}
	return "notifications@localhost"
}

func foldMailHeader(key string, value string) string {
	prefix := key + ": "
	value = sanitizeHeader(value)
	if len(prefix)+len(value) <= 78 {
		return prefix + value + "\r\n"
	}
	var b strings.Builder
	b.WriteString(prefix)
	column := len(prefix)
	for index, word := range strings.Fields(value) {
		if index > 0 && column+1+len(word) > 78 {
			b.WriteString("\r\n\t")
			column = 1
		} else if index > 0 {
			b.WriteByte(' ')
			column++
		}
		b.WriteString(word)
		column += len(word)
	}
	b.WriteString("\r\n")
	return b.String()
}

func mailHTMLFromText(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(value, "\r\n", "\n"), "\r", "\n"))
	paragraphs := strings.Split(value, "\n\n")
	var body strings.Builder
	body.WriteString(`<!doctype html><html><body style="font-family:system-ui,-apple-system,sans-serif;line-height:1.5;color:#282c37">`)
	for _, paragraph := range paragraphs {
		escaped := html.EscapeString(strings.TrimSpace(paragraph))
		if escaped == "" {
			continue
		}
		escaped = strings.ReplaceAll(escaped, "\n", "<br>\n")
		body.WriteString("<p>")
		body.WriteString(escaped)
		body.WriteString("</p>")
	}
	body.WriteString("</body></html>")
	return body.String()
}

func sanitizeHeader(value string) string {
	return textproto.TrimString(strings.NewReplacer("\r", " ", "\n", " ").Replace(value))
}

func sanitizeMailHeaderKey(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	for _, r := range value {
		if (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			return ""
		}
	}
	return value
}

func sanitizeMailAddress(value string) string {
	return formatMailAddress(value)
}

func normalizeMailBody(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	body := strings.Join(strings.Split(value, "\n"), "\r\n")
	if !strings.HasSuffix(body, "\r\n") {
		body += "\r\n"
	}
	return body
}

func mailDeliveryError(context string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s mail delivery failed: %w", context, err)
}

// notificationMailKey maps a notification type to the user-settings key that gates whether
// an e-mail is sent for it (users.settings.notification_emails.<type>), matching Rails.
func notificationMailKey(notificationType string) (string, bool) {
	switch notificationType {
	case "mention":
		return "notification_emails.mention", true
	case "follow":
		return "notification_emails.follow", true
	case "follow_request":
		return "notification_emails.follow_request", true
	case "favourite":
		return "notification_emails.favourite", true
	case "reblog":
		return "notification_emails.reblog", true
	default:
		return "", false
	}
}

// sendNotificationMail mirrors Rails NotifyService#send_email! + NotificationMailer: it
// sends a plain-text e-mail for a notification when SMTP is configured, the recipient has a
// usable e-mail, and the recipient's notification_emails.<type> setting allows it. The
// notification is expected to have FromAccount preloaded; status/statusURL may be empty for
// follow and follow-request notifications.
func (s *Server) sendNotificationMail(user models.User, notification models.Notification, status *models.Status, statusURL string) error {
	key, ok := notificationMailKey(string(notification.Type))
	if !ok {
		return nil
	}
	if !userReceivesNotificationMail(user) {
		return nil
	}
	if !userSettingBool(user, key, true) {
		return nil
	}
	var conversations []models.Conversation
	if status != nil && status.ConversationID.Valid && s != nil && s.db != nil {
		var conversation models.Conversation
		if err := s.db.Where("id = ?", status.ConversationID.Int64).First(&conversation).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return fmt.Errorf("notification mail conversation: %w", err)
		}
		conversations = append(conversations, conversation)
	}
	message := notificationMailMessage(s.cfg, user, notification, status, statusURL, conversations...)
	if message.To == "" {
		return nil
	}
	return sendMail(s.cfg, message)
}

func notificationMailMessage(cfg config.Config, user models.User, notification models.Notification, status *models.Status, statusURL string, conversations ...models.Conversation) mailMessage {
	from := notification.FromAccount.Acct()
	baseURL := cfg.BaseURL()
	locale := mailLocale(cfg, user)
	bodyName := from
	var subjectKey, subjectFallback, bodyKey, bodyFallback, body string
	switch notification.Type {
	case "mention":
		subjectKey = "notification_mailer.mention.subject"
		subjectFallback = "You were mentioned by %{name}"
		bodyKey = "notification_mailer.mention.body"
		bodyFallback = "You were mentioned by %{name} in:"
		if status != nil && status.Account.ID != 0 {
			bodyName = status.Account.Acct()
		}
	case "favourite":
		subjectKey = "notification_mailer.favourite.subject"
		subjectFallback = "%{name} favorited your post"
		bodyKey = "notification_mailer.favourite.body"
		bodyFallback = "Your post was favorited by %{name}:"
	case "reblog":
		subjectKey = "notification_mailer.reblog.subject"
		subjectFallback = "%{name} boosted your post"
		bodyKey = "notification_mailer.reblog.body"
		bodyFallback = "Your post was boosted by %{name}:"
	case "follow":
		subjectKey = "notification_mailer.follow.subject"
		subjectFallback = "%{name} is now following you"
		bodyKey = "notification_mailer.follow.body"
		bodyFallback = "%{name} is now following you!"
	case "follow_request":
		subjectKey = "notification_mailer.follow_request.subject"
		subjectFallback = "Pending follower: %{name}"
		bodyKey = "notification_mailer.follow_request.body"
		bodyFallback = "%{name} has requested to follow you"
	default:
		return mailMessage{}
	}
	vars := map[string]string{"name": bodyName}
	subject := settingsTVars(locale, subjectKey, subjectFallback, vars)
	body = mailSalutation(locale, user) + "\n\n"
	body += settingsTVars(locale, bodyKey, bodyFallback, vars) + "\n\n"
	switch notification.Type {
	case "mention", "favourite", "reblog":
		body += notificationStatusTextBlock(locale, status, statusURL)
	case "follow":
		body += settingsT(locale, "application_mailer.view", "View:") + " " + baseURL + "/@" + from + "\n"
	case "follow_request":
		body += settingsT(locale, "application_mailer.view", "View:") + " " + baseURL + "/follow_requests\n"
	}
	headers := notificationMailListHeaders(cfg, user, string(notification.Type))
	if len(conversations) > 0 && conversations[0].ID > 0 && !conversations[0].CreatedAt.IsZero() {
		messageID := notificationConversationMessageID(cfg, conversations[0])
		headers = append(headers,
			mailHeader{Key: "In-Reply-To", Value: messageID},
			mailHeader{Key: "References", Value: messageID},
		)
	}
	return mailMessage{To: user.Email, Subject: subject, Body: body, Headers: headers}
}

func userReceivesNotificationMail(user models.User) bool {
	if !userReceivesSecurityMail(user) || user.Account == nil {
		return false
	}
	account := user.Account
	return !account.SuspendedAt.Valid && !account.Memorial && !account.MovedToAccountID.Valid
}

func notificationConversationMessageID(cfg config.Config, conversation models.Conversation) string {
	domain := firstNonEmpty(cfg.LocalDomain, cfg.WebDomain, "localhost")
	domain = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '-' {
			return r
		}
		return -1
	}, domain)
	return fmt.Sprintf("<conversation-%d.%s@%s>", conversation.ID, conversation.CreatedAt.UTC().Format("2006-01-02"), firstNonEmpty(domain, "localhost"))
}

func notificationMailListHeaders(cfg config.Config, user models.User, notificationType string) []mailHeader {
	username := strings.TrimSpace(user.Email)
	if user.Account != nil {
		username = firstNonEmpty(user.Account.Username, user.Account.Acct(), username)
	}
	domain := firstNonEmpty(cfg.LocalDomain, cfg.WebDomain, "localhost")
	unsubscribeURL := cfg.BaseURL() + "/unsubscribe?token=" + url.QueryEscape(railsSignedGlobalIDForUser(user.ID, cfg.SecretKeyBase)) + "&type=" + url.QueryEscape(notificationType)
	return []mailHeader{
		{Key: "List-ID", Value: "<" + notificationType + "." + username + "." + domain + ">"},
		{Key: "List-Unsubscribe", Value: "<" + unsubscribeURL + ">"},
		{Key: "List-Unsubscribe-Post", Value: "List-Unsubscribe=One-Click"},
	}
}

func notificationStatusTextBlock(locale string, status *models.Status, statusURL string) string {
	var body strings.Builder
	if status != nil {
		if spoiler := strings.TrimSpace(stripHTML(status.SpoilerText)); spoiler != "" {
			body.WriteString("> ")
			body.WriteString(strings.ReplaceAll(spoiler, "\n", "\n> "))
			body.WriteString("\n> ----\n>\n")
		}
		if text := strings.TrimSpace(stripHTML(status.Text)); text != "" {
			body.WriteString("> ")
			body.WriteString(strings.ReplaceAll(text, "\n", "\n> "))
			body.WriteString("\n\n")
		}
	}
	if strings.TrimSpace(statusURL) != "" {
		body.WriteString(settingsT(locale, "application_mailer.view", "View:"))
		body.WriteString(" ")
		body.WriteString(statusURL)
		body.WriteString("\n")
	}
	return body.String()
}
