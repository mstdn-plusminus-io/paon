package api

import (
	"context"
	"database/sql"
	"errors"
	"html"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

const adminAccountStatusesPageSize = 20

func (s *Server) adminAccountStatusesPage(c *echo.Context) error {
	user, handled, err := s.requireAdminAccountsWebUser(c)
	if handled || err != nil {
		return err
	}
	account, err := s.loadAdminAccount(c.Param("account_id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "account not found")
	}
	statuses, err := s.adminAccountStatusModels(account.ID, c)
	if err != nil {
		return err
	}
	return c.HTML(http.StatusOK, adminAccountStatusesHTML(*account, statuses, c.QueryParam("notice"), c.QueryParam("error"), adminAccountStatusFilters{
		Page:     adminTrendsPageValue(c),
		Media:    c.QueryParam("media"),
		ReportID: c.QueryParam("report_id"),
	}, s.webLocale(c, user)))
}

func (s *Server) adminAccountStatusPage(c *echo.Context) error {
	user, handled, err := s.requireAdminAccountsWebUser(c)
	if handled || err != nil {
		return err
	}
	account, err := s.loadAdminAccount(c.Param("account_id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "account not found")
	}
	status, err := s.findAdminAccountStatus(account.ID, c.Param("id"))
	if err != nil {
		return err
	}
	edits, err := s.adminStatusEdits(status.ID)
	if err != nil {
		return err
	}
	return c.HTML(http.StatusOK, adminAccountStatusHTML(*account, status, edits, c.QueryParam("notice"), c.QueryParam("error"), s.webLocale(c, user)))
}

func (s *Server) batchAdminAccountStatusesWeb(c *echo.Context) error {
	user, handled, err := s.requireAdminAccountsWebUser(c)
	if handled || err != nil {
		return err
	}
	account, err := s.loadAdminAccount(c.Param("account_id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "account not found")
	}
	ids := parseAdminStatusBatchIDs(c)
	reportID := parseOptionalInt64(c.FormValue("report_id"))
	if len(ids) == 0 {
		return c.Redirect(http.StatusFound, adminAccountStatusBatchRedirectURL(c, c.Param("account_id"), reportID, "error", adminT(s.webLocale(c, user), "admin.statuses.no_status_selected", "No statuses selected")))
	}
	action := adminStatusBatchActionFromRequest(c)
	if action == "" {
		return c.Redirect(http.StatusFound, adminAccountStatusBatchRedirectURL(c, c.Param("account_id"), reportID, "", ""))
	}
	if err := s.applyAdminStatusBatchAction(user, account.ID, ids, reportID, action); err != nil {
		return err
	}
	return c.Redirect(http.StatusFound, adminAccountStatusBatchRedirectURL(c, c.Param("account_id"), reportID, "", ""))
}

func (s *Server) adminAccountRelationshipsPage(c *echo.Context) error {
	user, handled, err := s.requireAdminAccountsWebUser(c)
	if handled || err != nil {
		return err
	}
	account, err := s.loadAdminAccount(c.Param("account_id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "account not found")
	}
	filters, err := relationshipPageFilters(c)
	if err != nil {
		return err
	}
	accounts, err := s.adminAccountRelationshipModels(account, filters, c)
	if err != nil {
		return err
	}
	return c.HTML(http.StatusOK, adminAccountRelationshipsHTML(*account, accounts, c.QueryParam("notice"), c.QueryParam("error"), adminAccountRelationshipFilters{
		Page:         adminTrendsPageValue(c),
		Relationship: filters.Relationship,
		Location:     filters.Location,
		Status:       filters.Status,
		Order:        filters.Order,
		Activity:     filters.Activity,
		ByDomain:     filters.ByDomain,
	}, s.webLocale(c, user)))
}

func (s *Server) adminAccountChangeEmailPage(c *echo.Context) error {
	user, handled, err := s.requireAdminAccountsWebUser(c)
	if handled || err != nil {
		return err
	}
	account, err := s.loadAdminAccount(c.Param("account_id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "account not found")
	}
	if !account.Local() || account.User.ID == 0 {
		return c.Redirect(http.StatusFound, "/admin/accounts/"+url.PathEscape(c.Param("account_id")))
	}
	return c.HTML(http.StatusOK, adminAccountChangeEmailHTML(*account, c.QueryParam("error"), s.webLocale(c, user)))
}

func (s *Server) updateAdminAccountChangeEmail(c *echo.Context) error {
	user, handled, err := s.requireAdminAccountsWebUser(c)
	if handled || err != nil {
		return err
	}
	account, err := s.loadAdminAccount(c.Param("account_id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "account not found")
	}
	if !account.Local() || account.User.ID == 0 {
		return c.Redirect(http.StatusFound, "/admin/accounts/"+url.PathEscape(c.Param("account_id")))
	}
	newEmail, err := adminAccountChangeEmailParams(c)
	if errors.Is(err, errAdminAccountChangeEmailParamsMissing) {
		return apiError(c, http.StatusBadRequest, "Malformed request")
	}
	if err != nil {
		return err
	}
	if !railsEmailAddressValid(newEmail) {
		return c.Redirect(http.StatusFound, "/admin/accounts/"+url.PathEscape(c.Param("account_id"))+"/change_email?error="+url.QueryEscape(settingsT(s.webLocale(c, user), "users.invalid_email", "E-mail is invalid")))
	}
	if s.db == nil {
		return c.Redirect(http.StatusFound, "/admin/accounts/"+url.PathEscape(c.Param("account_id"))+"/change_email?error="+url.QueryEscape(adminAccountChangeEmailMessage(s.webLocale(c, user), "errors.database_unavailable", "DATABASE_URL is not set")))
	}
	if newEmail != account.User.Email {
		now := time.Now().UTC()
		emailUpdates, delivery := s.confirmationUpdateForEmailChange(account.User, newEmail, now)
		emailUpdates["updated_at"] = now
		if err := s.db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Model(&models.User{}).Where("id = ?", account.User.ID).Updates(emailUpdates).Error; err != nil {
				return err
			}
			return logAdminAction(tx, user.AccountID, "change_email", userAuditLogTarget(account.User), now)
		}); err != nil {
			return err
		}
		if err := s.sendConfirmationDelivery(delivery); err != nil {
			return mailDeliveryError("admin change email confirmation", err)
		}
	}
	return c.Redirect(http.StatusFound, "/admin/accounts/"+url.PathEscape(c.Param("account_id"))+"?notice="+url.QueryEscape(adminT(s.webLocale(c, user), "admin.accounts.change_email.changed_msg", "E-mail change saved")))
}

var errAdminAccountChangeEmailParamsMissing = errors.New("admin account change email root parameter is missing")

func adminAccountChangeEmailParams(c *echo.Context) (string, error) {
	req := c.Request()
	if err := req.ParseForm(); err != nil {
		return "", err
	}
	const prefix = "user"
	if !formHasNestedPrefix(req.Form, prefix) {
		return "", errAdminAccountChangeEmailParamsMissing
	}
	return strings.TrimSpace(lastFormValue(req.Form, prefix+"[unconfirmed_email]")), nil
}

func (s *Server) resetAdminAccountPasswordWeb(c *echo.Context) error {
	user, handled, err := s.requireAdminAccountsWebUser(c)
	if handled || err != nil {
		return err
	}
	account, err := s.loadAdminAccount(c.Param("account_id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "account not found")
	}
	if account.User.ID == 0 {
		return c.Redirect(http.StatusFound, "/admin/accounts/"+url.PathEscape(c.Param("account_id")))
	}
	token := randomHex(24)
	randomPasswordHash, err := bcrypt.GenerateFromPassword([]byte(randomHex(32)), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	var revokedTokenIDs []int64
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.OAuthAccessToken{}).
			Where("resource_owner_id = ? AND revoked_at IS NULL", account.User.ID).
			Pluck("id", &revokedTokenIDs).Error; err != nil {
			return err
		}
		if err := tx.Where("id IN (?)", tx.Model(&models.SessionActivation{}).Select("web_push_subscription_id").Where("user_id = ? AND web_push_subscription_id IS NOT NULL", account.User.ID)).Delete(&models.WebPushSubscription{}).Error; err != nil {
			return err
		}
		if err := tx.Where("user_id = ?", account.User.ID).Delete(&models.SessionActivation{}).Error; err != nil {
			return err
		}
		if err := tx.Model(&models.OAuthAccessGrant{}).Where("resource_owner_id = ? AND revoked_at IS NULL", account.User.ID).Update("revoked_at", now).Error; err != nil {
			return err
		}
		if err := tx.Model(&models.OAuthAccessToken{}).Where("resource_owner_id = ? AND revoked_at IS NULL", account.User.ID).Update("revoked_at", now).Error; err != nil {
			return err
		}
		if err := tx.Model(&models.User{}).Where("id = ?", account.User.ID).Updates(map[string]any{
			"encrypted_password":     string(randomPasswordHash),
			"reset_password_token":   sql.NullString{String: deviseTokenForStorage(token, deviseResetPasswordTokenColumn, s.cfg.SecretKeyBase), Valid: true},
			"reset_password_sent_at": sql.NullTime{Time: now, Valid: true},
			"sign_in_token":          nil,
			"sign_in_token_sent_at":  nil,
			"skip_sign_in_token":     nil,
			"updated_at":             now,
		}).Error; err != nil {
			return err
		}
		return logAdminAction(tx, user.AccountID, "reset_password", userAuditLogTarget(account.User), now)
	}); err != nil {
		return err
	}
	s.publishAccessTokenKills(revokedTokenIDs)
	if err := s.sendResetPasswordMail(account.User.Email, token, account.User); err != nil {
		return mailDeliveryError("admin reset password", err)
	}
	return c.Redirect(http.StatusFound, "/admin/accounts/"+url.PathEscape(c.Param("account_id")))
}

func (s *Server) confirmAdminAccountWeb(c *echo.Context) error {
	user, handled, err := s.requireAdminAccountsWebUser(c)
	if handled || err != nil {
		return err
	}
	return s.updateAdminAccountConfirmation(c, user, true)
}

func (s *Server) resendAdminAccountConfirmationWeb(c *echo.Context) error {
	user, handled, err := s.requireAdminAccountsWebUser(c)
	if handled || err != nil {
		return err
	}
	return s.updateAdminAccountConfirmation(c, user, false)
}

func (s *Server) updateAdminAccountConfirmation(c *echo.Context, actor *models.User, confirm bool) error {
	account, err := s.loadAdminAccount(c.Param("account_id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "account not found")
	}
	if account.User.ID == 0 {
		return echo.NewHTTPError(http.StatusNotFound, "user not found")
	}
	locale := s.webLocale(c, actor)
	if !confirm && account.User.ConfirmedAt.Valid {
		return c.Redirect(http.StatusFound, "/admin/accounts?error="+url.QueryEscape(adminT(locale, "admin.accounts.resend_confirmation.already_confirmed", "Account is already confirmed")))
	}
	now := time.Now().UTC()
	updates := map[string]any{"updated_at": now}
	delivery := confirmationDelivery{}
	action := "resend"
	if confirm {
		action = "confirm"
		updates["confirmed_at"] = sql.NullTime{Time: now, Valid: true}
		updates["confirmation_token"] = nil
		updates["unconfirmed_email"] = nil
	} else if !account.User.ConfirmedAt.Valid {
		token := randomHex(16)
		updates["confirmation_token"] = deviseTokenForStorage(token, deviseConfirmationTokenColumn, s.cfg.SecretKeyBase)
		updates["confirmation_sent_at"] = sql.NullTime{Time: now, Valid: true}
		delivery = confirmationDelivery{Email: confirmationRecipient(account.User), Token: token, User: account.User, HasUser: true}
	}
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.User{}).Where("id = ?", account.User.ID).Updates(updates).Error; err != nil {
			return err
		}
		return logAdminAction(tx, actor.AccountID, action, userAuditLogTarget(account.User), now)
	}); err != nil {
		return err
	}
	if delivery.Token != "" {
		if err := s.sendConfirmationDelivery(delivery); err != nil {
			return mailDeliveryError("admin resend confirmation", err)
		}
	}
	if confirm {
		return c.Redirect(http.StatusFound, "/admin/accounts")
	}
	return c.Redirect(http.StatusFound, "/admin/accounts?notice="+url.QueryEscape(adminT(locale, "admin.accounts.resend_confirmation.success", "Confirmation instructions sent")))
}

func (s *Server) adminAccountStatusModels(accountID int64, c *echo.Context) ([]models.Status, error) {
	if s.db == nil {
		return []models.Status{}, nil
	}
	query := s.db.Preload("Account.AccountStat").
		Preload("Application").
		Preload("MediaAttachments").
		Preload("Poll").
		Preload("StatusStat").
		Where("account_id = ? AND visibility IN ? AND deleted_at IS NULL", accountID, []int{0, 1}).
		Order("id DESC").
		Offset(adminPageOffset(c, adminAccountStatusesPageSize)).
		Limit(adminAccountStatusesPageSize)
	if strings.TrimSpace(c.QueryParam("media")) != "" {
		query = query.Joins("JOIN media_attachments ON media_attachments.status_id = statuses.id").Group("statuses.id")
	}
	var statuses []models.Status
	err := query.Find(&statuses).Error
	return statuses, err
}

func (s *Server) findAdminAccountStatus(accountID int64, rawID string) (models.Status, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(rawID), 10, 64)
	if err != nil || id <= 0 || s.db == nil {
		return models.Status{}, echo.NewHTTPError(http.StatusNotFound, "status not found")
	}
	var status models.Status
	if err := s.db.Preload("Account.AccountStat").Preload("Application").Preload("MediaAttachments").Preload("Poll").Preload("StatusStat").Where("id = ? AND account_id = ?", id, accountID).First(&status).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return status, echo.NewHTTPError(http.StatusNotFound, "status not found")
		}
		return status, err
	}
	return status, nil
}

func (s *Server) adminStatusEdits(statusID int64) ([]models.StatusEdit, error) {
	if s.db == nil {
		return []models.StatusEdit{}, nil
	}
	var edits []models.StatusEdit
	err := s.db.Where("status_id = ?", statusID).Order("id ASC").Find(&edits).Error
	return edits, err
}

func (s *Server) adminAccountRelationshipModels(account *models.Account, filters relationshipFilters, c *echo.Context) ([]models.Account, error) {
	if s.db == nil {
		return []models.Account{}, nil
	}
	query := s.relationshipPageBaseQuery(account.ID, account.User.ID, filters).
		Preload("AccountStat").
		Preload("User")
	var accounts []models.Account
	err := query.Offset(adminRailsPageOffset(c)).Limit(adminRailsDefaultPageSize).Find(&accounts).Error
	return accounts, err
}

func parseAdminStatusBatchIDs(c *echo.Context) []int64 {
	req := c.Request()
	_ = req.ParseForm()
	values := req.Form["admin_status_batch_action[status_ids][]"]
	out := make([]int64, 0, len(values))
	seen := map[int64]struct{}{}
	for _, value := range values {
		id, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err == nil && id > 0 {
			if _, ok := seen[id]; !ok {
				seen[id] = struct{}{}
				out = append(out, id)
			}
		}
	}
	return out
}

func adminStatusBatchActionFromRequest(c *echo.Context) string {
	for _, action := range []string{"report", "remove_from_report", "delete"} {
		if adminStatusBatchFormParamExists(c, action) {
			return action
		}
	}
	return ""
}

func adminStatusBatchFormParamExists(c *echo.Context, key string) bool {
	_ = c.Request().ParseForm()
	_, ok := c.Request().Form[key]
	return ok
}

func adminAccountStatusBatchRedirectURL(c *echo.Context, rawAccountID string, reportID int64, messageKey string, message string) string {
	if reportID > 0 {
		values := url.Values{}
		if messageKey != "" && message != "" {
			values.Set(messageKey, message)
		}
		if query := values.Encode(); query != "" {
			return "/admin/reports/" + strconv.FormatInt(reportID, 10) + "?" + query
		}
		return "/admin/reports/" + strconv.FormatInt(reportID, 10)
	}
	return adminAccountStatusesRedirectURL(c, rawAccountID, messageKey, message)
}

func (s *Server) applyAdminStatusBatchAction(user *models.User, accountID int64, statusIDs []int64, reportID int64, action string) error {
	now := time.Now().UTC()
	var deletedStatusIDs []int64
	var createdWarning models.AccountWarning
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		switch action {
		case "delete":
			var statuses []models.Status
			if err := tx.Where("account_id = ? AND id IN ?", accountID, statusIDs).Find(&statuses).Error; err != nil {
				return err
			}
			actualStatusIDs := make([]int64, 0, len(statuses))
			for _, status := range statuses {
				actualStatusIDs = append(actualStatusIDs, status.ID)
			}
			if err := discardAdminStatusBatchWithReblogs(tx, actualStatusIDs, now); err != nil {
				return err
			}
			for _, status := range statuses {
				deletedStatusIDs = append(deletedStatusIDs, status.ID)
				if err := logAdminAction(tx, user.AccountID, "destroy", statusAuditLogTarget(status), now); err != nil {
					return err
				}
			}
			if reportID > 0 {
				var report models.Report
				if err := tx.Where("id = ?", reportID).First(&report).Error; err != nil {
					return err
				}
				if err := tx.Model(&models.Report{}).Where("id = ?", report.ID).Updates(map[string]any{"action_taken_at": sql.NullTime{Time: now, Valid: true}, "action_taken_by_account_id": user.AccountID, "updated_at": now}).Error; err != nil {
					return err
				}
				if err := logAdminAction(tx, user.AccountID, "resolve", reportAuditLogTarget(report), now); err != nil {
					return err
				}
			}
			warning, err := createAdminStatusBatchWarning(tx, user.AccountID, accountID, reportID, "delete_statuses", actualStatusIDs, "", now)
			if err != nil {
				return err
			}
			createdWarning = warning
		case "remove_from_report":
			if reportID <= 0 {
				return nil
			}
			var report models.Report
			if err := tx.Where("id = ?", reportID).First(&report).Error; err != nil {
				return err
			}
			report.StatusIDs = removeInt64s(report.StatusIDs, statusIDs)
			return tx.Model(&models.Report{}).Where("id = ?", report.ID).Updates(map[string]any{"status_ids": report.StatusIDs, "updated_at": now}).Error
		case "report":
			var report models.Report
			if reportID > 0 {
				if err := tx.Where("id = ?", reportID).First(&report).Error; err != nil {
					return err
				}
				report.StatusIDs = appendUniqueInt64s(report.StatusIDs, statusIDs)
				return tx.Model(&models.Report{}).Where("id = ?", report.ID).Updates(map[string]any{"status_ids": report.StatusIDs, "updated_at": now}).Error
			}
			reportURI, err := s.reportURIForAccountID(tx, user.AccountID)
			if err != nil {
				return err
			}
			report = models.Report{
				AccountID:       user.AccountID,
				TargetAccountID: accountID,
				StatusIDs:       models.Int64Array(statusIDs),
				Comment:         "",
				URI:             reportURI,
				CreatedAt:       now,
				UpdatedAt:       now,
			}
			return tx.Create(&report).Error
		}
		return nil
	}); err != nil {
		return err
	}
	if action == "delete" {
		payload := s.adminStatusBatchRemovalPayload(accountID)
		if !s.enqueueRemovalTasksForStatusIDs(deletedStatusIDs, payload) {
			s.enqueueFASPContentDeletionForIDs(context.Background(), s.db, deletedStatusIDs)
			s.applyDeletedStatusMediaForIDs(context.Background(), deletedStatusIDs, payload)
			s.applyAdminDeletedStatusSideEffects(context.Background(), s.db, deletedStatusIDs)
		}
	}
	if createdWarning.ID != 0 {
		s.publishModerationWarningNotification(createdWarning.ID)
		_ = s.sendAdminStatusBatchWarningMail(accountID, createdWarning, false)
	}
	return nil
}

func discardAdminStatusBatchWithReblogs(tx *gorm.DB, statusIDs []int64, now time.Time) error {
	if tx == nil || len(statusIDs) == 0 {
		return nil
	}
	if err := tx.Model(&models.Status{}).
		Where("id IN ?", statusIDs).
		Updates(map[string]any{"deleted_at": sql.NullTime{Time: now, Valid: true}, "updated_at": now}).Error; err != nil {
		return err
	}
	return tx.Model(&models.Status{}).
		Where("reblog_of_id IN ? AND deleted_at IS NULL", statusIDs).
		Updates(map[string]any{"deleted_at": sql.NullTime{Time: now, Valid: true}, "updated_at": now}).Error
}

func (s *Server) adminStatusBatchRemovalPayload(targetAccountID int64) asynqRemovalPayload {
	var target models.Account
	if s == nil || s.db == nil || targetAccountID == 0 || s.db.Select("id", "domain").Where("id = ?", targetAccountID).First(&target).Error != nil {
		return asynqRemovalPayload{}
	}
	local := target.Local()
	return asynqRemovalPayload{Preserve: local, Immediate: !local, Remote: !local}
}

func createAdminStatusBatchWarning(tx *gorm.DB, actorAccountID int64, targetAccountID int64, reportID int64, action string, statusIDs []int64, text string, now time.Time) (models.AccountWarning, error) {
	if tx == nil || targetAccountID == 0 {
		return models.AccountWarning{}, nil
	}
	code, ok := accountWarningActionCode(action)
	if !ok {
		return models.AccountWarning{}, nil
	}
	warning := models.AccountWarning{
		AccountID:       models.AccountWarningAccountID(actorAccountID),
		TargetAccountID: models.AccountWarningTargetAccountID(targetAccountID),
		Action:          code,
		Text:            accountWarningText(text),
		StatusIDs:       accountWarningStatusIDStrings(statusIDs),
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if reportID > 0 {
		warning.ReportID = sql.NullInt64{Int64: reportID, Valid: true}
	}
	if err := tx.Create(&warning).Error; err != nil {
		return models.AccountWarning{}, err
	}
	if err := createModerationWarningNotification(tx, warning, now); err != nil {
		return models.AccountWarning{}, err
	}
	return warning, nil
}

func accountWarningActionCode(action string) (int, bool) {
	switch action {
	case "mark_statuses_as_sensitive":
		return 1250, true
	case "delete_statuses":
		return 1500, true
	default:
		return 0, false
	}
}

func accountWarningStatusIDStrings(statusIDs []int64) models.StringArray {
	out := make(models.StringArray, 0, len(statusIDs))
	for _, id := range statusIDs {
		if id > 0 {
			out = append(out, strconv.FormatInt(id, 10))
		}
	}
	return out
}

func (s *Server) sendAdminStatusBatchWarningMail(accountID int64, warning models.AccountWarning, send bool) error {
	if s == nil || s.db == nil || !send {
		return nil
	}
	var account models.Account
	if err := s.db.Preload("User").Where("id = ?", accountID).First(&account).Error; err != nil {
		return err
	}
	if !account.Local() || account.User.ID == 0 {
		return nil
	}
	account.User.Account = &account
	return s.sendAccountWarningMail(account.User, warning)
}

func removeInt64s(values models.Int64Array, remove []int64) models.Int64Array {
	blocked := map[int64]struct{}{}
	for _, id := range remove {
		blocked[id] = struct{}{}
	}
	out := make(models.Int64Array, 0, len(values))
	for _, id := range values {
		if _, ok := blocked[id]; !ok {
			out = append(out, id)
		}
	}
	return out
}

func appendUniqueInt64s(values models.Int64Array, add []int64) models.Int64Array {
	seen := map[int64]struct{}{}
	out := make(models.Int64Array, 0, len(values)+len(add))
	for _, id := range values {
		if _, ok := seen[id]; !ok {
			seen[id] = struct{}{}
			out = append(out, id)
		}
	}
	for _, id := range add {
		if _, ok := seen[id]; !ok {
			seen[id] = struct{}{}
			out = append(out, id)
		}
	}
	return out
}

type adminAccountStatusFilters struct {
	Page     string
	Media    string
	ReportID string
}

func adminAccountStatusHiddenFields(filters adminAccountStatusFilters) string {
	values := map[string]string{
		"page":      firstNonEmpty(filters.Page, "1"),
		"media":     filters.Media,
		"report_id": filters.ReportID,
	}
	var body strings.Builder
	for _, key := range []string{"page", "media", "report_id"} {
		if value := strings.TrimSpace(values[key]); value != "" {
			body.WriteString(`<input type="hidden" name="` + key + `" value="` + html.EscapeString(value) + `">`)
		}
	}
	return body.String()
}

func adminAccountStatusesRedirectURL(c *echo.Context, rawAccountID string, messageKey string, message string) string {
	values := url.Values{}
	for _, key := range []string{"page", "media"} {
		value := strings.TrimSpace(firstNonEmpty(c.FormValue(key), c.QueryParam(key)))
		if value != "" {
			values.Set(key, value)
		}
	}
	if messageKey != "" && message != "" {
		values.Set(messageKey, message)
	}
	if query := values.Encode(); query != "" {
		return "/admin/accounts/" + url.PathEscape(rawAccountID) + "/statuses?" + query
	}
	return "/admin/accounts/" + url.PathEscape(rawAccountID) + "/statuses"
}

func adminAccountStatusesHTML(account models.Account, statuses []models.Status, notice string, errorText string, filters adminAccountStatusFilters, locale ...string) string {
	loc := settingsLocaleArgOrEnglish(locale...)
	var body strings.Builder
	accountID := strconv.FormatInt(account.ID, 10)
	backHref := "/admin/accounts/" + accountID
	backLabel := adminT(loc, "admin.statuses.back_to_account", "Back to account")
	if reportID := strings.TrimSpace(filters.ReportID); reportID != "" {
		backHref = "/admin/reports/" + url.PathEscape(reportID)
		backLabel = adminT(loc, "admin.statuses.back_to_report", "Back to report")
	}
	body.WriteString(`<div class="filters">`)
	body.WriteString(relationshipFilterSubsetHTML(adminT(loc, "admin.statuses.media.title", "Media"), []relationshipFilterLink{
		{Label: adminT(loc, "generic.all", "All"), Href: adminAccountStatusFilterHref(accountID, filters, ""), Active: strings.TrimSpace(filters.Media) == ""},
		{Label: adminT(loc, "admin.statuses.with_media", "With media"), Href: adminAccountStatusFilterHref(accountID, filters, "1"), Active: strings.TrimSpace(filters.Media) == "1"},
	}))
	body.WriteString(`<div class="back-link"><a href="` + html.EscapeString(backHref) + `"><i class="fa fa-chevron-left fa-fw"></i> ` + html.EscapeString(backLabel) + `</a></div></div><hr class="spacer">`)
	body.WriteString(`<form method="post" action="/admin/accounts/` + accountID + `/statuses/batch" class="new_admin_status_batch_action">` + adminAccountStatusHiddenFields(filters) + `<div class="batch-table"><div class="batch-table__toolbar"><label class="batch-table__toolbar__select batch-checkbox-all"><input type="checkbox" name="batch_checkbox_all"></label><div class="batch-table__toolbar__actions">`)
	if len(statuses) > 0 {
		body.WriteString(`<button name="report" value="1" type="submit" class="table-action-link" data-confirm="` + html.EscapeString(adminT(loc, "admin.reports.are_you_sure", "Are you sure?")) + `"><i class="fa fa-flag"></i> ` + html.EscapeString(adminT(loc, "admin.statuses.batch.report", "Report")) + `</button>`)
	}
	body.WriteString(`</div></div><div class="batch-table__body">`)
	if len(statuses) == 0 {
		body.WriteString(adminNothingHereHTML(loc, "nothing-here--under-tabs"))
	} else {
		for _, status := range statuses {
			body.WriteString(adminAccountStatusRowHTML(loc, account.ID, status))
		}
	}
	body.WriteString(`</div></div></form>`)
	query := url.Values{}
	if strings.TrimSpace(filters.Media) != "" {
		query.Set("media", filters.Media)
	}
	if strings.TrimSpace(filters.ReportID) != "" {
		query.Set("report_id", filters.ReportID)
	}
	body.WriteString(adminRailsPaginationHTML(loc, "/admin/accounts/"+accountID+"/statuses", filters.Page, query, len(statuses)))
	return authPageHTML(adminT(loc, "admin.accounts.statuses", "Posts"), notice, errorText, body.String(), loc)
}

func adminAccountStatusFilterHref(accountID string, filters adminAccountStatusFilters, media string) string {
	values := url.Values{}
	if strings.TrimSpace(media) != "" {
		values.Set("media", media)
	}
	if strings.TrimSpace(filters.ReportID) != "" {
		values.Set("report_id", filters.ReportID)
	}
	path := "/admin/accounts/" + accountID + "/statuses"
	if query := values.Encode(); query != "" {
		return path + "?" + query
	}
	return path
}

func adminAccountStatusRowHTML(locale string, accountID int64, status models.Status) string {
	statusID := strconv.FormatInt(status.ID, 10)
	var body strings.Builder
	body.WriteString(`<div class="batch-table__row"><label class="batch-table__row__select batch-checkbox"><input type="checkbox" name="admin_status_batch_action[status_ids][]" value="` + statusID + `"></label><div class="batch-table__row__content">`)
	body.WriteString(`<div class="status__content">` + adminAccountStatusRowContentHTML(status, locale) + `</div>`)
	body.WriteString(adminAccountStatusPollHTML(status.Poll, locale))
	body.WriteString(adminAccountStatusRowMediaHTML(status, locale))
	body.WriteString(`<div class="detailed-status__meta">`)
	if status.Application != nil && strings.TrimSpace(status.Application.Name) != "" {
		body.WriteString(html.EscapeString(status.Application.Name) + ` · `)
	}
	created := status.CreatedAt.UTC().Format(time.RFC3339)
	body.WriteString(`<a href="/admin/accounts/` + strconv.FormatInt(accountID, 10) + `/statuses/` + statusID + `" class="detailed-status__datetime"><time class="formatted" datetime="` + html.EscapeString(created) + `" title="` + html.EscapeString(created) + `">` + html.EscapeString(created) + `</time></a>`)
	if status.EditedAt.Valid {
		edited := status.EditedAt.Time.UTC().Format(time.RFC3339)
		editedLabel := webT(locale, "statuses.edited_at_html", map[string]string{"date": edited})
		if editedLabel == "statuses.edited_at_html" {
			editedLabel = "Edited " + edited
		}
		body.WriteString(` · <a href="/admin/accounts/` + strconv.FormatInt(accountID, 10) + `/statuses/` + statusID + `" class="detailed-status__datetime">` + html.EscapeString(editedLabel) + `</a>`)
	}
	if status.DeletedAt.Valid {
		body.WriteString(` · <span class="negative-hint">` + html.EscapeString(adminT(locale, "admin.statuses.deleted", "Deleted")) + `</span>`)
	}
	body.WriteString(` · ` + html.EscapeString(statusEmbedVisibilityLabel(status.Visibility, locale)))
	if status.Sensitive {
		body.WriteString(` · <span class="sensitive-hint">` + html.EscapeString(settingsT(locale, "stream_entries.sensitive_content", "Sensitive content")) + `</span>`)
	}
	body.WriteString(`</div></div></div>`)
	return body.String()
}

func adminAccountStatusRowContentHTML(status models.Status, locale string) string {
	content := strings.ReplaceAll(html.EscapeString(status.Text), "\n", "<br>")
	spoiler := strings.TrimSpace(status.SpoilerText)
	if spoiler == "" {
		return content
	}
	warning := webT(locale, "stream_entries.content_warning")
	if warning == "stream_entries.content_warning" {
		warning = "Content warning:"
	}
	return `<details><summary><strong>` + html.EscapeString(warning) + ` ` + html.EscapeString(spoiler) + `</strong></summary>` + content + `</details>`
}

func adminAccountStatusRowMediaHTML(status models.Status, locale string) string {
	attachments := snapshotMediaAttachments(status)
	if len(attachments) == 0 {
		return ""
	}
	var body strings.Builder
	body.WriteString(`<div class="media attachments-list">`)
	for _, attachment := range attachments {
		label := statusEmbedMediaLabel(attachment)
		if filename := strings.TrimSpace(attachment.FileFileName.String); filename != "" {
			label = filename
		}
		body.WriteString(`<span class="attachments-list__item">` + html.EscapeString(label) + `</span>`)
	}
	body.WriteString(`</div>`)
	if status.Sensitive {
		return `<details class="media sensitive-media"><summary class="media-spoiler"><span><span class="media-spoiler__warning">` + html.EscapeString(settingsT(locale, "stream_entries.sensitive_content", "Sensitive content")) + `</span></span></summary>` + body.String() + `</details>`
	}
	return body.String()
}

func adminAccountStatusPollHTML(poll *models.Poll, locale string) string {
	if poll == nil || len(poll.Options) == 0 {
		return ""
	}
	role := "radio"
	inputClass := "poll__input"
	if poll.Multiple {
		role = "checkbox"
		inputClass += " checkbox"
	}
	var body strings.Builder
	body.WriteString(`<div class="poll"><ul>`)
	for _, option := range poll.Options {
		body.WriteString(`<li><label class="poll__option disabled"><span class="` + inputClass + `" role="` + role + `" aria-label="` + html.EscapeString(option) + `"></span><span class="poll__option__text">` + html.EscapeString(option) + `</span></label></li>`)
	}
	vote := webT(locale, "polls.vote")
	if vote == "polls.vote" {
		vote = "Vote"
	}
	body.WriteString(`</ul><button class="button button-secondary" disabled>` + html.EscapeString(vote) + `</button></div>`)
	return body.String()
}

func adminStatusEditContentHTML(edit models.StatusEdit, locale string) string {
	content := strings.ReplaceAll(html.EscapeString(edit.Text), "\n", "<br>")
	if strings.TrimSpace(edit.SpoilerText) == "" {
		return content
	}
	warning := webT(locale, "stream_entries.content_warning")
	if warning == "stream_entries.content_warning" {
		warning = "Content warning:"
	}
	return `<details><summary><strong>` + html.EscapeString(warning) + ` ` + html.EscapeString(edit.SpoilerText) + `</strong></summary>` + content + `</details>`
}

func adminAccountStatusHTML(account models.Account, status models.Status, edits []models.StatusEdit, notice string, errorText string, locale ...string) string {
	loc := settingsLocaleArgOrEnglish(locale...)
	var body strings.Builder
	if publicURL := firstNonEmpty(status.URL.String, status.URI.String); strings.TrimSpace(publicURL) != "" {
		body.WriteString(`<div class="content__heading__actions"><a class="button" href="` + html.EscapeString(publicURL) + `" target="_blank" rel="noopener noreferrer">` + html.EscapeString(adminT(loc, "admin.statuses.open", "Open post")) + `</a></div>`)
	}
	body.WriteString(`<h3>` + html.EscapeString(adminT(loc, "admin.statuses.metadata", "Status metadata")) + `</h3><div class="table-wrapper"><table class="table horizontal-table"><tbody>`)
	body.WriteString(`<tr><th>` + html.EscapeString(adminT(loc, "admin.accounts.account", "Account")) + `</th><td><a href="/admin/accounts/` + strconv.FormatInt(account.ID, 10) + `">` + html.EscapeString(adminReportAccountLabel(account)) + `</a></td></tr>`)
	if status.InReplyToAccountID.Valid && status.InReplyToID.Valid {
		body.WriteString(`<tr><th>` + html.EscapeString(adminT(loc, "admin.statuses.in_reply_to", "In reply to")) + `</th><td><a href="/admin/accounts/` + strconv.FormatInt(status.InReplyToAccountID.Int64, 10) + `/statuses/` + strconv.FormatInt(status.InReplyToID.Int64, 10) + `">#` + strconv.FormatInt(status.InReplyToID.Int64, 10) + `</a></td></tr>`)
	}
	application := ""
	if status.Application != nil {
		application = status.Application.Name
	}
	body.WriteString(`<tr><th>` + html.EscapeString(adminT(loc, "admin.statuses.application", "Application")) + `</th><td>` + html.EscapeString(application) + `</td></tr><tr><th>` + html.EscapeString(adminT(loc, "admin.statuses.language", "Language")) + `</th><td>` + html.EscapeString(railsStandardLocaleName(status.Language.String)) + `</td></tr><tr><th>` + html.EscapeString(adminT(loc, "admin.statuses.visibility", "Visibility")) + `</th><td>` + html.EscapeString(statusEmbedVisibilityLabel(status.Visibility, loc)) + `</td></tr><tr><th>` + html.EscapeString(adminT(loc, "admin.statuses.reblogs", "Reblogs")) + `</th><td>` + strconv.FormatInt(status.StatusStat.ReblogsCount, 10) + `</td></tr><tr><th>` + html.EscapeString(adminT(loc, "admin.statuses.favourites", "Favourites")) + `</th><td>` + strconv.FormatInt(status.StatusStat.FavouritesCount, 10) + `</td></tr>`)
	body.WriteString(`</tbody></table></div><hr class="spacer"><h3>` + html.EscapeString(adminT(loc, "admin.statuses.contents", "Contents")) + `</h3><div class="status"><div class="status__content">` + adminAccountStatusRowContentHTML(status, loc) + `</div>` + adminAccountStatusPollHTML(status.Poll, loc) + adminAccountStatusRowMediaHTML(status, loc) + `</div><hr class="spacer"><h3>` + html.EscapeString(adminT(loc, "admin.statuses.history", "History")) + `</h3>`)
	body.WriteString(`<ol class="history">`)
	for i, edit := range edits {
		title := adminT(loc, "admin.statuses.status_changed", "Status changed")
		if i == 0 {
			title = adminT(loc, "admin.statuses.original_status", "Original status")
		}
		editPoll := (*models.Poll)(nil)
		if len(edit.PollOptions) > 0 {
			editPoll = &models.Poll{Options: edit.PollOptions}
		}
		body.WriteString(`<li><div class="history__entry"><h5>` + html.EscapeString(title) + ` · <time class="formatted" datetime="` + html.EscapeString(edit.CreatedAt.UTC().Format(time.RFC3339)) + `">` + html.EscapeString(edit.CreatedAt.UTC().Format(time.RFC3339)) + `</time></h5><div class="status"><div class="status__content">` + adminStatusEditContentHTML(edit, loc) + `</div>` + adminAccountStatusPollHTML(editPoll, loc) + `</div></div></li>`)
	}
	body.WriteString(`</ol>`)
	return authPageHTML(adminT(loc, "admin.statuses.title", "Post"), notice, errorText, body.String(), loc)
}

type adminAccountRelationshipFilters struct {
	Page         string
	Relationship string
	Location     string
	Status       string
	Order        string
	Activity     string
	ByDomain     string
}

func adminAccountRelationshipHiddenFields(filters adminAccountRelationshipFilters) string {
	values := map[string]string{
		"page":         firstNonEmpty(filters.Page, "1"),
		"relationship": filters.Relationship,
		"location":     filters.Location,
		"status":       filters.Status,
		"order":        filters.Order,
		"activity":     filters.Activity,
		"by_domain":    filters.ByDomain,
	}
	var body strings.Builder
	for _, key := range []string{"page", "relationship", "location", "status", "order", "activity", "by_domain"} {
		if value := strings.TrimSpace(values[key]); value != "" {
			body.WriteString(`<input type="hidden" name="` + key + `" value="` + html.EscapeString(value) + `">`)
		}
	}
	return body.String()
}

func adminAccountRelationshipsHTML(account models.Account, accounts []models.Account, notice string, errorText string, filters adminAccountRelationshipFilters, locale ...string) string {
	loc := settingsLocaleArgOrEnglish(locale...)
	accountID := strconv.FormatInt(account.ID, 10)
	var body strings.Builder
	relationship := firstNonEmpty(filters.Relationship, "following")
	body.WriteString(`<div class="filters">`)
	body.WriteString(relationshipFilterSubsetHTML(adminT(loc, "relationships.relationship", "Relationship"), []relationshipFilterLink{
		{Label: adminT(loc, "relationships.following", "Following"), Href: adminAccountRelationshipFilterHref(accountID, filters, "relationship", "following"), Active: relationship == "following"},
		{Label: adminT(loc, "relationships.followers", "Followers"), Href: adminAccountRelationshipFilterHref(accountID, filters, "relationship", "followed_by"), Active: relationship == "followed_by"},
		{Label: adminT(loc, "relationships.mutual", "Mutual"), Href: adminAccountRelationshipFilterHref(accountID, filters, "relationship", "mutual"), Active: relationship == "mutual"},
		{Label: adminT(loc, "relationships.invited", "Invited"), Href: adminAccountRelationshipFilterHref(accountID, filters, "relationship", "invited"), Active: relationship == "invited"},
	}))
	body.WriteString(relationshipFilterSubsetHTML(adminT(loc, "admin.accounts.location.title", "Location"), []relationshipFilterLink{
		{Label: adminT(loc, "admin.accounts.moderation.all", "All"), Href: adminAccountRelationshipFilterHref(accountID, filters, "location", ""), Active: strings.TrimSpace(filters.Location) == ""},
		{Label: adminT(loc, "admin.accounts.location.local", "Local"), Href: adminAccountRelationshipFilterHref(accountID, filters, "location", "local"), Active: filters.Location == "local"},
		{Label: adminT(loc, "admin.accounts.location.remote", "Remote"), Href: adminAccountRelationshipFilterHref(accountID, filters, "location", "remote"), Active: filters.Location == "remote"},
	}))
	body.WriteString(`<div class="back-link"><a href="/admin/accounts/` + accountID + `"><i class="fa fa-chevron-left fa-fw"></i> ` + html.EscapeString(adminT(loc, "admin.statuses.back_to_account", "Back to account")) + `</a></div></div><hr class="spacer">`)
	body.WriteString(`<form method="post" action="/admin/accounts/batch" class="new_form_account_batch">` + adminAccountRelationshipHiddenFields(filters) + `<div class="batch-table"><div class="batch-table__toolbar"><label class="batch-table__toolbar__select batch-checkbox-all"><input type="checkbox" name="batch_checkbox_all"></label><div class="batch-table__toolbar__actions"><button name="suspend" value="1" type="submit" class="table-action-link" data-confirm="` + html.EscapeString(adminT(loc, "admin.reports.are_you_sure", "Are you sure?")) + `"><i class="fa fa-lock"></i> ` + html.EscapeString(adminT(loc, "admin.accounts.perform_full_suspension", "Suspend")) + `</button></div></div><div class="batch-table__body">`)
	if len(accounts) == 0 {
		body.WriteString(adminNothingHereHTML(loc, "nothing-here--under-tabs"))
	} else {
		for _, row := range accounts {
			body.WriteString(adminAccountRowHTMLWithConfig(config.Config{}, row, loc))
		}
	}
	body.WriteString(`</div></div></form>`)
	body.WriteString(adminRailsPaginationHTML(loc, "/admin/accounts/"+accountID+"/relationships", filters.Page, adminAccountRelationshipFilterValues(filters), len(accounts)))
	return authPageHTML(adminT(loc, "admin.accounts.relationships.title", "Follows and followers"), notice, errorText, body.String(), loc)
}

func adminAccountRelationshipFilterValues(filters adminAccountRelationshipFilters) url.Values {
	values := url.Values{}
	for key, value := range map[string]string{
		"relationship": filters.Relationship,
		"location":     filters.Location,
		"status":       filters.Status,
		"order":        filters.Order,
		"activity":     filters.Activity,
		"by_domain":    filters.ByDomain,
	} {
		if strings.TrimSpace(value) != "" {
			values.Set(key, value)
		}
	}
	return values
}

func adminAccountRelationshipFilterHref(accountID string, filters adminAccountRelationshipFilters, key string, value string) string {
	values := adminAccountRelationshipFilterValues(filters)
	values.Del("page")
	if key == "relationship" && value == "following" {
		values.Del(key)
	} else if strings.TrimSpace(value) == "" {
		values.Del(key)
	} else {
		values.Set(key, value)
	}
	path := "/admin/accounts/" + accountID + "/relationships"
	if query := values.Encode(); query != "" {
		return path + "?" + query
	}
	return path
}

func adminAccountChangeEmailHTML(account models.Account, errorText string, locale ...string) string {
	loc := settingsLocaleArgOrEnglish(locale...)
	body := `<form method="post" action="/admin/accounts/` + strconv.FormatInt(account.ID, 10) + `/change_email" class="simple_form edit_user">
  <div class="fields-group"><div class="input email optional user_email"><div class="label_input"><label for="user_email">` + html.EscapeString(adminT(loc, "admin.accounts.change_email.current_email", "Current email")) + `</label><input class="string email optional" id="user_email" name="user[email]" value="` + html.EscapeString(account.User.Email) + `" type="email" disabled></div></div></div>
  <div class="fields-group"><div class="input email optional user_unconfirmed_email"><div class="label_input"><label for="user_unconfirmed_email">` + html.EscapeString(adminT(loc, "admin.accounts.change_email.new_email", "New email")) + `</label><input class="string email optional" id="user_unconfirmed_email" name="user[unconfirmed_email]" value="` + html.EscapeString(account.User.UnconfirmedEmail.String) + `" type="email"></div></div></div>
  <div class="actions"><button class="button" type="submit">` + html.EscapeString(adminT(loc, "admin.accounts.change_email.submit", "Change e-mail")) + `</button></div>
</form>`
	return authPageHTML(adminT(loc, "admin.accounts.change_email.title", "Change email"), "", errorText, body, loc)
}

func adminAccountChangeEmailMessage(locale, key, fallback string) string {
	return adminT(locale, "admin.accounts.change_email."+key, fallback)
}
