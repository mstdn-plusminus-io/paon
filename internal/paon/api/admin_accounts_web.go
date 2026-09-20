package api

import (
	"context"
	"database/sql"
	"errors"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"github.com/mstdn-plusminus-io/paon/internal/paon/serializer"
	"gorm.io/gorm"
)

var adminModerationNoteURLPattern = regexp.MustCompile(`https?://[^\s<>"']+`)

func (s *Server) adminAccountsPage(c *echo.Context) error {
	user, handled, err := s.requireAdminAccountsWebUser(c)
	if handled || err != nil {
		return err
	}
	accounts, err := s.adminAccountModels(c)
	if err != nil {
		return err
	}
	roles, err := s.adminAssignableAccountRoles()
	if err != nil {
		return err
	}
	totalCount, err := s.adminAccountFilteredCount(c)
	if err != nil {
		return err
	}
	pendingCount, err := s.adminPendingAccountCount()
	if err != nil {
		return err
	}
	return c.HTML(http.StatusOK, adminAccountsHTMLWithOptions(accounts, c.QueryParam("notice"), c.QueryParam("error"), adminAccountFilterValues(c), adminAccountsViewOptions{
		Config:       s.cfg,
		Roles:        roles,
		TotalCount:   totalCount,
		PendingCount: pendingCount,
	}, s.webLocale(c, user)))
}

func (s *Server) adminAccountPage(c *echo.Context) error {
	user, handled, err := s.requireAdminAccountsWebUser(c)
	if handled || err != nil {
		return err
	}
	account, err := s.loadAdminAccount(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "account not found")
	}
	notes, err := s.adminAccountModerationNotes(account.ID)
	if err != nil {
		return err
	}
	ipHistory, err := s.adminAccountIPHistory(account.User.ID)
	if err != nil {
		return err
	}
	counts, err := s.adminAccountDashboardCounts(account.ID)
	if err != nil {
		return err
	}
	return c.HTML(http.StatusOK, adminAccountHTMLWithViewData(*account, c.QueryParam("notice"), c.QueryParam("error"), ipHistory, s.webLocale(c, user), s.cfg, counts, notes...))
}

func (s *Server) batchAdminAccountsWeb(c *echo.Context) error {
	user, handled, err := s.requireAdminAccountsWebUser(c)
	if handled || err != nil {
		return err
	}
	ids, err := s.adminAccountBatchIDs(c)
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		return c.Redirect(http.StatusFound, adminAccountBatchRedirectURL(c, "error", adminT(s.webLocale(c, user), "admin.accounts.no_account_selected", "No accounts selected")))
	}
	action := ""
	if adminBatchFormParamExists(c, "approve") {
		action = "approve"
	} else if adminBatchFormParamExists(c, "reject") {
		action = "reject"
	} else if adminBatchFormParamExists(c, "suspend") {
		action = "suspend"
	}
	for _, id := range ids {
		accountAction := action
		if accountAction == "" {
			continue
		}
		if action == "suspend" {
			account, err := s.loadAdminAccount(strconv.FormatInt(id, 10))
			if err != nil {
				return echo.NewHTTPError(http.StatusNotFound, "account not found")
			}
			if adminAccountBatchShouldRejectPending(account) {
				accountAction = "reject"
			}
		}
		if err := s.applyAdminAccountWebAction(user, strconv.FormatInt(id, 10), accountAction, ""); err != nil {
			return err
		}
	}
	return c.Redirect(http.StatusFound, adminAccountBatchRedirectURL(c, "", ""))
}

func adminAccountBatchShouldRejectPending(account *models.Account) bool {
	return account != nil && account.Local() && account.User.ID != 0 && !account.User.Approved
}

func (s *Server) destroyAdminAccountWeb(c *echo.Context) error {
	user, handled, err := s.requireAdminAccountsWebUserWithAny(c, rolePermissionDeleteUserData)
	if handled || err != nil {
		return err
	}
	if err := s.applyAdminAccountWebAction(user, c.Param("id"), "destroy", ""); err != nil {
		return err
	}
	return c.Redirect(http.StatusFound, "/admin/accounts/"+url.PathEscape(c.Param("id"))+"?notice="+url.QueryEscape(adminAccountNotice(s.webLocale(c, user), "admin.accounts.destroyed_msg", "Account deletion queued", c.Param("id"))))
}

func (s *Server) adminAccountMemberAction(c *echo.Context) error {
	if methodOverrideIs(c, "delete") {
		return s.destroyAdminAccountWeb(c)
	}
	return c.Redirect(http.StatusFound, "/admin/accounts/"+url.PathEscape(c.Param("id")))
}

func (s *Server) enableAdminAccountWeb(c *echo.Context) error {
	user, handled, err := s.requireAdminAccountsWebUser(c)
	if handled || err != nil {
		return err
	}
	if err := s.applyAdminAccountWebAction(user, c.Param("id"), "enable", ""); err != nil {
		return err
	}
	return c.Redirect(http.StatusFound, "/admin/accounts/"+url.PathEscape(c.Param("id"))+"?notice="+url.QueryEscape(adminAccountNotice(s.webLocale(c, user), "admin.accounts.enabled_msg", "Account enabled", c.Param("id"))))
}

func (s *Server) unsensitiveAdminAccountWeb(c *echo.Context) error {
	user, handled, err := s.requireAdminAccountsWebUser(c)
	if handled || err != nil {
		return err
	}
	if err := s.applyAdminAccountWebAction(user, c.Param("id"), "unsensitive", ""); err != nil {
		return err
	}
	return c.Redirect(http.StatusFound, "/admin/accounts/"+url.PathEscape(c.Param("id")))
}

func (s *Server) unsilenceAdminAccountWeb(c *echo.Context) error {
	user, handled, err := s.requireAdminAccountsWebUser(c)
	if handled || err != nil {
		return err
	}
	if err := s.applyAdminAccountWebAction(user, c.Param("id"), "unsilence", ""); err != nil {
		return err
	}
	return c.Redirect(http.StatusFound, "/admin/accounts/"+url.PathEscape(c.Param("id"))+"?notice="+url.QueryEscape(adminAccountNotice(s.webLocale(c, user), "admin.accounts.unsilenced_msg", "Account is no longer limited", c.Param("id"))))
}

func (s *Server) unsuspendAdminAccountWeb(c *echo.Context) error {
	user, handled, err := s.requireAdminAccountsWebUser(c)
	if handled || err != nil {
		return err
	}
	if err := s.applyAdminAccountWebAction(user, c.Param("id"), "unsuspend", ""); err != nil {
		return err
	}
	return c.Redirect(http.StatusFound, "/admin/accounts/"+url.PathEscape(c.Param("id"))+"?notice="+url.QueryEscape(adminAccountNotice(s.webLocale(c, user), "admin.accounts.unsuspended_msg", "Account is no longer suspended", c.Param("id"))))
}

func (s *Server) redownloadAdminAccountWeb(c *echo.Context) error {
	user, handled, err := s.requireAdminFederationWebUser(c)
	if handled || err != nil {
		return err
	}
	account, err := s.loadAdminAccount(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "account not found")
	}
	if account.Local() {
		return c.Redirect(http.StatusFound, "/admin/accounts/"+url.PathEscape(c.Param("id")))
	}
	if err := s.redownloadAdminRemoteAccount(account, time.Now().UTC()); err != nil {
		return err
	}
	return c.Redirect(http.StatusFound, "/admin/accounts/"+url.PathEscape(c.Param("id"))+"?notice="+url.QueryEscape(adminAccountNotice(s.webLocale(c, user), "admin.accounts.redownloaded_msg", "Remote account refresh requested", c.Param("id"))))
}

func (s *Server) removeAvatarAdminAccountWeb(c *echo.Context) error {
	user, handled, err := s.requireAdminAccountsWebUserWithAny(c, rolePermissionManageUsers, rolePermissionManageReports)
	if handled || err != nil {
		return err
	}
	if err := s.applyAdminAccountWebAction(user, c.Param("id"), "remove_avatar", ""); err != nil {
		return err
	}
	return c.Redirect(http.StatusFound, "/admin/accounts/"+url.PathEscape(c.Param("id"))+"?notice="+url.QueryEscape(adminAccountNotice(s.webLocale(c, user), "admin.accounts.removed_avatar_msg", "Avatar removed", c.Param("id"))))
}

func (s *Server) removeHeaderAdminAccountWeb(c *echo.Context) error {
	user, handled, err := s.requireAdminAccountsWebUserWithAny(c, rolePermissionManageUsers, rolePermissionManageReports)
	if handled || err != nil {
		return err
	}
	if err := s.applyAdminAccountWebAction(user, c.Param("id"), "remove_header", ""); err != nil {
		return err
	}
	return c.Redirect(http.StatusFound, "/admin/accounts/"+url.PathEscape(c.Param("id"))+"?notice="+url.QueryEscape(adminAccountNotice(s.webLocale(c, user), "admin.accounts.removed_header_msg", "Header removed", c.Param("id"))))
}

func (s *Server) memorializeAdminAccountWeb(c *echo.Context) error {
	user, handled, err := s.requireAdminAccountsWebUserWithAny(c, rolePermissionDeleteUserData)
	if handled || err != nil {
		return err
	}
	if err := s.applyAdminAccountWebAction(user, c.Param("id"), "memorialize", ""); err != nil {
		return err
	}
	return c.Redirect(http.StatusFound, "/admin/accounts/"+url.PathEscape(c.Param("id"))+"?notice="+url.QueryEscape(adminAccountNotice(s.webLocale(c, user), "admin.accounts.memorialized_msg", "Account memorialized", c.Param("id"))))
}

func (s *Server) approveAdminAccountWeb(c *echo.Context) error {
	user, handled, err := s.requireAdminAccountsWebUser(c)
	if handled || err != nil {
		return err
	}
	if err := s.applyAdminAccountWebAction(user, c.Param("id"), "approve", ""); err != nil {
		return err
	}
	return c.Redirect(http.StatusFound, "/admin/accounts?status=pending&notice="+url.QueryEscape(adminAccountNotice(s.webLocale(c, user), "admin.accounts.approved_msg", "Account approved", c.Param("id"))))
}

func (s *Server) rejectAdminAccountWeb(c *echo.Context) error {
	user, handled, err := s.requireAdminAccountsWebUser(c)
	if handled || err != nil {
		return err
	}
	if err := s.applyAdminAccountWebAction(user, c.Param("id"), "reject", ""); err != nil {
		return err
	}
	return c.Redirect(http.StatusFound, "/admin/accounts?status=pending&notice="+url.QueryEscape(adminAccountNotice(s.webLocale(c, user), "admin.accounts.rejected_msg", "Account rejected", c.Param("id"))))
}

func (s *Server) unblockEmailAdminAccountWeb(c *echo.Context) error {
	user, handled, err := s.requireAdminAccountsWebUser(c)
	if handled || err != nil {
		return err
	}
	if err := s.applyAdminAccountWebAction(user, c.Param("id"), "unblock_email", ""); err != nil {
		return err
	}
	return c.Redirect(http.StatusFound, "/admin/accounts/"+url.PathEscape(c.Param("id"))+"?notice="+url.QueryEscape(adminAccountNotice(s.webLocale(c, user), "admin.accounts.unblocked_email_msg", "E-mail block removed", c.Param("id"))))
}

func (s *Server) newAdminAccountActionPage(c *echo.Context) error {
	user, handled, err := s.requireAdminAccountsWebUser(c)
	if handled || err != nil {
		return err
	}
	account, err := s.loadAdminAccount(c.Param("account_id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "account not found")
	}
	actionType := strings.TrimSpace(firstNonEmpty(c.QueryParam("type"), "none"))
	reportID := parseOptionalInt64(c.QueryParam("report_id"))
	presets, err := s.adminWarningPresetModels()
	if err != nil {
		return err
	}
	return c.HTML(http.StatusOK, adminAccountActionFormHTMLWithPresets(*account, actionType, reportID, presets, c.QueryParam("error"), s.webLocale(c, user)))
}

func (s *Server) createAdminAccountActionWeb(c *echo.Context) error {
	user, handled, err := s.requireAdminAccountsWebUser(c)
	if handled || err != nil {
		return err
	}
	actionType, text, opts, err := adminAccountActionParams(c)
	if errors.Is(err, errAdminAccountActionParamsMissing) {
		return apiError(c, http.StatusBadRequest, "Malformed request")
	}
	if err != nil {
		return err
	}
	if err := s.applyAdminAccountWebAction(user, c.Param("account_id"), actionType, text, opts); err != nil {
		return err
	}
	if opts.ReportID > 0 {
		return c.Redirect(http.StatusFound, "/admin/reports?notice="+url.QueryEscape(adminReportProcessedMessage(s.webLocale(c, user), opts.ReportID)))
	}
	return c.Redirect(http.StatusFound, "/admin/accounts/"+url.PathEscape(c.Param("account_id")))
}

func (s *Server) requireAdminAccountsWebUser(c *echo.Context) (*models.User, bool, error) {
	return s.requireAdminAccountsWebUserWithAny(c, rolePermissionManageUsers)
}

func (s *Server) requireAdminAccountsWebUserWithAny(c *echo.Context, permissions ...int64) (*models.User, bool, error) {
	user, _, handled, err := s.requireFunctionalWebUser(c)
	if handled || err != nil {
		return nil, handled, err
	}
	if !s.userCanAny(user, permissions...) {
		locale := s.webLocale(c, user)
		return nil, true, c.HTML(http.StatusForbidden, authPageHTML(adminT(locale, "admin.accounts.title", "Accounts"), "", adminT(locale, "admin.accounts.not_permitted", "You are not allowed to manage accounts."), "", locale))
	}
	return user, false, nil
}

func (s *Server) adminAccountModels(c *echo.Context) ([]models.Account, error) {
	if s.db == nil {
		return []models.Account{}, nil
	}
	query := s.adminAccountQuery(c)
	ordered, err := applyAdminAccountOrder(c, query)
	if err != nil {
		return nil, err
	}
	query = ordered
	var accounts []models.Account
	err = query.Offset(adminRailsPageOffset(c)).Limit(adminRailsDefaultPageSize).Find(&accounts).Error
	return accounts, err
}

func (s *Server) adminAssignableAccountRoles() ([]models.UserRole, error) {
	if s.db == nil {
		return []models.UserRole{}, nil
	}
	var roles []models.UserRole
	err := s.db.Where("id <> ?", -99).Order("position ASC").Find(&roles).Error
	return roles, err
}

func (s *Server) adminAccountFilteredCount(c *echo.Context) (int64, error) {
	if s.db == nil {
		return 0, nil
	}
	filtered := s.adminAccountQuery(c).Select("accounts.id").Group("accounts.id")
	var count int64
	err := s.db.Table("(?) AS filtered_accounts", filtered).Count(&count).Error
	return count, err
}

func (s *Server) adminPendingAccountCount() (int64, error) {
	if s.db == nil {
		return 0, nil
	}
	var count int64
	err := s.db.Model(&models.User{}).Where("approved = ?", false).Count(&count).Error
	return count, err
}

type adminAccountWebActionOptions struct {
	ReportID              int64
	WarningPresetID       int64
	SendEmailNotification bool
	IncludeStatuses       bool
}

var errAdminAccountActionParamsMissing = errors.New("admin account action root parameter is missing")

func adminAccountActionParams(c *echo.Context) (string, string, adminAccountWebActionOptions, error) {
	req := c.Request()
	if err := req.ParseForm(); err != nil {
		return "", "", adminAccountWebActionOptions{}, err
	}
	const prefix = "admin_account_action"
	if !formHasNestedPrefix(req.Form, prefix) {
		return "", "", adminAccountWebActionOptions{}, errAdminAccountActionParamsMissing
	}
	actionType := strings.TrimSpace(firstNonEmpty(lastFormValue(req.Form, prefix+"[type]"), "none"))
	text := lastFormValue(req.Form, prefix+"[text]")
	opts := adminAccountWebActionOptions{
		ReportID:              parseOptionalInt64(lastFormValue(req.Form, prefix+"[report_id]")),
		WarningPresetID:       parseOptionalInt64(lastFormValue(req.Form, prefix+"[warning_preset_id]")),
		SendEmailNotification: true,
		IncludeStatuses:       true,
	}
	if values, ok := req.Form[prefix+"[send_email_notification]"]; ok && len(values) > 0 {
		opts.SendEmailNotification = railsBool(values[len(values)-1], true)
	}
	if values, ok := req.Form[prefix+"[include_statuses]"]; ok && len(values) > 0 {
		opts.IncludeStatuses = railsBool(values[len(values)-1], true)
	}
	return actionType, text, opts, nil
}

func (s *Server) applyAdminAccountWebAction(user *models.User, rawID string, actionType string, text string, optionList ...adminAccountWebActionOptions) error {
	opts := adminAccountWebActionOptions{SendEmailNotification: true, IncludeStatuses: true}
	if len(optionList) > 0 {
		opts = optionList[0]
	}
	if s.db == nil {
		return echo.NewHTTPError(http.StatusUnprocessableEntity, "DATABASE_URL is not set")
	}
	account, err := s.loadAdminAccount(rawID)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "account not found")
	}
	wasApproved := account.User.Approved
	now := time.Now().UTC()
	updateUserAndLog := func(updates map[string]any) error {
		updates["updated_at"] = now
		return s.db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Model(&models.User{}).Where("account_id = ?", account.ID).Updates(updates).Error; err != nil {
				return err
			}
			return s.logAdminAccountAction(tx, user.AccountID, account, actionType, now)
		})
	}
	updateAccountAndLog := func(updates map[string]any) error {
		updates["updated_at"] = now
		if err := s.db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Model(&models.Account{}).Where("id = ?", account.ID).Updates(updates).Error; err != nil {
				return err
			}
			if actionType == "unsuspend" {
				if err := destroyCanonicalEmailBlocksForAccountTx(tx, account.ID); err != nil {
					return err
				}
				if err := tx.Where("account_id = ?", account.ID).Delete(&models.AccountDeletionRequest{}).Error; err != nil {
					return err
				}
			}
			return s.logAdminAccountAction(tx, user.AccountID, account, actionType, now)
		}); err != nil {
			return err
		}
		if actionType == "unsuspend" {
			if err := s.enqueueAdminUnsuspensionOrRun(s.db, account.ID); err != nil {
				return err
			}
		}
		s.triggerAccountWebhook("account.updated", account.ID)
		return nil
	}
	switch actionType {
	case "reject":
		if !adminAccountRejectPermittedByRailsPolicy(account) {
			return echo.NewHTTPError(http.StatusForbidden, "account is not pending approval")
		}
	case "unsuspend":
		if !adminAccountUnsuspendPermittedByRailsPolicy(account) {
			return echo.NewHTTPError(http.StatusForbidden, "account suspension is not locally managed")
		}
	case "none", "disable", "sensitive", "silence", "suspend":
		if ok, err := s.adminAccountActionPermittedByRailsPolicy(user, account, actionType); err != nil {
			return err
		} else if !ok {
			return echo.NewHTTPError(http.StatusForbidden, "account action is not permitted")
		}
		if ok, err := s.adminAccountReportResolutionPermittedByRailsPolicy(user, account.ID, opts.ReportID, actionType); err != nil {
			return err
		} else if !ok {
			return echo.NewHTTPError(http.StatusForbidden, "report resolution is not permitted")
		}
	}
	switch actionType {
	case "enable":
		return updateUserAndLog(map[string]any{"disabled": false})
	case "approve":
		if err := updateUserAndLog(map[string]any{"approved": true}); err != nil {
			return err
		}
		reloaded, err := s.loadAdminAccount(rawID)
		if err != nil {
			return err
		}
		if !wasApproved && reloaded.User.Approved && reloaded.User.ConfirmedAt.Valid {
			if err := s.runApprovedAccountBootstrap(context.Background(), reloaded.ID, now); err != nil {
				return err
			}
			s.triggerAccountWebhook("account.approved", reloaded.ID)
		}
		return nil
	case "reject":
		return s.deleteRejectedLocalAccountRows(context.Background(), user.AccountID, account, now)
	case "unsensitive":
		return updateAccountAndLog(map[string]any{"sensitized_at": nil})
	case "unsilence":
		return updateAccountAndLog(map[string]any{"silenced_at": nil})
	case "unsuspend":
		return updateAccountAndLog(map[string]any{"suspended_at": nil, "suspension_origin": nil})
	case "redownload":
		// Rails Admin::AccountsController#redownload runs ResolveAccountService synchronously
		// (re-fetches the actor: profile + avatar + header). Mirror that here.
		return s.redownloadAdminRemoteAccount(account, time.Now().UTC())
	case "remove_avatar":
		s.removeAccountImageObjects(models.Account{ID: account.ID, AvatarFileName: account.AvatarFileName})
		s.removeAccountLocalImageFilesForKind(account.ID, "avatar")
		return updateAccountAndLog(map[string]any{"avatar_file_name": nil, "avatar_content_type": nil, "avatar_file_size": nil, "avatar_updated_at": nil, "avatar_remote_url": nil})
	case "remove_header":
		s.removeAccountImageObjects(models.Account{ID: account.ID, HeaderFileName: account.HeaderFileName})
		s.removeAccountLocalImageFilesForKind(account.ID, "header")
		return updateAccountAndLog(map[string]any{"header_file_name": nil, "header_content_type": nil, "header_file_size": nil, "header_updated_at": nil, "header_remote_url": ""})
	case "memorialize":
		return updateAccountAndLog(map[string]any{"memorial": true})
	case "unblock_email":
		return s.db.Transaction(func(tx *gorm.DB) error {
			if err := destroyCanonicalEmailBlocksForAccountTx(tx, account.ID); err != nil {
				return err
			}
			return s.logAdminAccountAction(tx, user.AccountID, account, actionType, now)
		})
	case "destroy":
		if ok, err := s.adminAccountCanDestroyWithRailsPolicy(account.ID); err != nil {
			return err
		} else if !ok {
			return echo.NewHTTPError(http.StatusForbidden, "account is not temporarily suspended")
		}
		if err := s.enqueueAdminAccountDeletionOrRun(context.Background(), account.ID); err != nil {
			return err
		}
		return nil
	case "none", "disable", "sensitive", "silence", "suspend":
		return s.createAdminAccountWebWarning(user, account, actionType, text, opts, now)
	default:
		return echo.NewHTTPError(http.StatusUnprocessableEntity, "unknown account action")
	}
}

func (s *Server) adminAccountCanDestroyWithRailsPolicy(accountID int64) (bool, error) {
	if s == nil || s.db == nil || accountID == 0 {
		return false, nil
	}
	var account models.Account
	if err := s.db.Select("id", "suspended_at").Where("id = ?", accountID).First(&account).Error; err != nil {
		return false, err
	}
	if !account.SuspendedAt.Valid {
		return false, nil
	}
	var count int64
	if err := s.db.Model(&models.AccountDeletionRequest{}).Where("account_id = ?", accountID).Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func (s *Server) redownloadAdminRemoteAccount(account *models.Account, now time.Time) error {
	if account == nil || account.Local() {
		return echo.NewHTTPError(http.StatusUnprocessableEntity, "account is not remote")
	}
	if err := s.db.Model(&models.Account{}).Where("id = ?", account.ID).Updates(map[string]any{"last_webfingered_at": nil, "updated_at": now}).Error; err != nil {
		return err
	}
	_, _ = s.fetchAndStoreActivityActorForAcct(account.Acct())
	s.triggerAccountWebhook("account.updated", account.ID)
	return nil
}

func (s *Server) deleteAdminAccountRows(actorAccountID int64, account *models.Account, now time.Time) error {
	if err := s.stageReservedAdminAccountDeletion(s.db, account, now); err != nil {
		return err
	}
	_ = s.deliverAdminAccountDeletionActivities(s.db, *account)
	if actorAccountID != 0 {
		return s.db.Transaction(func(tx *gorm.DB) error {
			return s.logAdminAccountAction(tx, actorAccountID, account, "suspend", now)
		})
	}
	return s.purgeAccountDeletionRequest(context.Background(), account.ID, now)
}

func (s *Server) enqueueAdminAccountDeletionOrRun(ctx context.Context, accountID int64) error {
	if s == nil || s.db == nil || accountID == 0 {
		return nil
	}
	if s.enqueueAdminAccountDeletionTask(accountID) {
		return nil
	}
	return s.runAdminAccountDeletionWorkerEffects(ctx, accountID, time.Now().UTC())
}

func (s *Server) runAdminAccountDeletionWorkerEffects(ctx context.Context, accountID int64, now time.Time) error {
	if s == nil || s.db == nil || accountID == 0 {
		return nil
	}
	var account models.Account
	if err := s.db.WithContext(ctx).Preload("User").Where("id = ?", accountID).First(&account).Error; err != nil {
		return err
	}
	if err := s.stageReservedAdminAccountDeletion(s.db.WithContext(ctx), &account, now); err != nil {
		return err
	}
	_ = s.deliverAdminAccountDeletionActivities(s.db.WithContext(ctx), account)
	return s.purgeAccountDeletionRequest(ctx, account.ID, now)
}

func (s *Server) stageReservedAdminAccountDeletion(database *gorm.DB, account *models.Account, now time.Time) error {
	if s == nil || database == nil || account == nil || account.ID == 0 {
		return nil
	}
	return database.Transaction(func(tx *gorm.DB) error {
		updates := map[string]any{
			"silenced_at":         nil,
			"suspended_at":        now,
			"suspension_origin":   0,
			"locked":              false,
			"memorial":            false,
			"discoverable":        false,
			"trendable":           false,
			"display_name":        "",
			"note":                "",
			"fields":              gorm.Expr("?::jsonb", "[]"),
			"moved_to_account_id": nil,
			"reviewed_at":         nil,
			"requested_review_at": nil,
			"avatar_file_name":    nil,
			"avatar_content_type": nil,
			"avatar_file_size":    nil,
			"avatar_updated_at":   nil,
			"avatar_remote_url":   nil,
			"header_file_name":    nil,
			"header_content_type": nil,
			"header_file_size":    nil,
			"header_updated_at":   nil,
			"header_remote_url":   "",
			"updated_at":          now,
		}
		if err := tx.Model(&models.Account{}).Where("id = ?", account.ID).Updates(updates).Error; err != nil {
			return err
		}
		if err := tx.Model(&models.AccountStat{}).Where("account_id = ?", account.ID).Updates(map[string]any{
			"statuses_count":  0,
			"followers_count": 0,
			"following_count": 0,
			"updated_at":      now,
		}).Error; err != nil {
			return err
		}
		if account.Local() && account.User.ID != 0 {
			if err := tx.Model(&models.User{}).Where("account_id = ?", account.ID).Updates(map[string]any{"disabled": true, "updated_at": now}).Error; err != nil {
				return err
			}
		}
		request := models.AccountDeletionRequest{AccountID: models.AccountDeletionRequestAccountID(account.ID), CreatedAt: now, UpdatedAt: now}
		if err := tx.Where("account_id = ?", account.ID).FirstOrCreate(&request).Error; err != nil {
			return err
		}
		return createCanonicalEmailBlockForAccountTx(tx, *account, now)
	})
}

func (s *Server) createAdminAccountWebWarning(user *models.User, account *models.Account, actionType string, text string, opts adminAccountWebActionOptions, now time.Time) error {
	action, ok := adminAccountActionCode(actionType)
	if !ok {
		return echo.NewHTTPError(http.StatusUnprocessableEntity, "unknown account action")
	}
	var createdWarning models.AccountWarning
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		var report models.Report
		if opts.ReportID > 0 {
			if err := tx.Where("id = ?", opts.ReportID).First(&report).Error; err != nil {
				return err
			}
		}
		switch actionType {
		case "disable":
			if err := tx.Model(&models.User{}).Where("account_id = ?", account.ID).Updates(map[string]any{"disabled": true, "updated_at": now}).Error; err != nil {
				return err
			}
		case "sensitive":
			if err := tx.Model(&models.Account{}).Where("id = ?", account.ID).Updates(map[string]any{"sensitized_at": now, "updated_at": now}).Error; err != nil {
				return err
			}
		case "silence":
			if err := tx.Model(&models.Account{}).Where("id = ?", account.ID).Updates(map[string]any{"silenced_at": now, "updated_at": now}).Error; err != nil {
				return err
			}
		case "suspend":
			if err := tx.Model(&models.Account{}).Where("id = ?", account.ID).Updates(map[string]any{"suspended_at": now, "suspension_origin": 0, "updated_at": now}).Error; err != nil {
				return err
			}
			if err := createCanonicalEmailBlockForAccountTx(tx, *account, now); err != nil {
				return err
			}
		}
		warningText, err := s.adminAccountWarningText(tx, adminAccountActionPayload{Text: text, WarningPresetID: opts.WarningPresetID})
		if err != nil {
			return err
		}
		warning := models.AccountWarning{
			AccountID:       models.AccountWarningAccountID(user.AccountID),
			TargetAccountID: models.AccountWarningTargetAccountID(account.ID),
			Action:          action,
			Text:            accountWarningText(warningText),
			CreatedAt:       now,
			UpdatedAt:       now,
		}
		if opts.ReportID > 0 {
			warning.ReportID = sql.NullInt64{Int64: opts.ReportID, Valid: true}
			if opts.IncludeStatuses {
				warning.StatusIDs = reportStatusIDStrings(report.StatusIDs)
			}
		}
		if err := tx.Create(&warning).Error; err != nil {
			return err
		}
		if err := createModerationWarningNotification(tx, warning, now); err != nil {
			return err
		}
		createdWarning = warning
		if err := logAdminAction(tx, user.AccountID, "create", accountWarningAuditLogTarget(warning), now); err != nil {
			return err
		}
		if err := s.logAdminAccountAction(tx, user.AccountID, account, actionType, now); err != nil {
			return err
		}
		return s.resolveAdminAccountReports(tx, account.ID, opts.ReportID, user.AccountID, now, actionType)
	}); err != nil {
		return err
	}
	s.publishModerationWarningNotification(createdWarning.ID)
	if actionType == "disable" {
		s.publishStreamingKillForLocalAccount(*account)
	}
	switch actionType {
	case "sensitive", "silence", "suspend":
		if actionType == "suspend" {
			s.publishStreamingKillForLocalAccount(*account)
			if err := s.enqueueAdminSuspensionOrRun(context.Background(), s.db, account.ID); err != nil {
				return err
			}
		}
		s.triggerAccountWebhook("account.updated", account.ID)
	}
	if opts.SendEmailNotification && account.User.ID != 0 {
		account.User.Account = account
		_ = s.sendAccountWarningMail(account.User, createdWarning)
	}
	return nil
}

func parseAdminAccountBatchIDs(c *echo.Context) []int64 {
	req := c.Request()
	_ = req.ParseForm()
	values := req.Form["form_account_batch[account_ids][]"]
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

func (s *Server) adminAccountBatchIDs(c *echo.Context) ([]int64, error) {
	if !adminAccountBatchRootPresent(c) {
		return nil, nil
	}
	if !adminAccountBatchSelectsAllMatching(c) {
		return parseAdminAccountBatchIDs(c), nil
	}
	if s.db == nil {
		return nil, nil
	}
	var ids []int64
	if err := s.adminAccountQuery(c).Pluck("accounts.id", &ids).Error; err != nil {
		return nil, err
	}
	return ids, nil
}

func adminAccountBatchSelectsAllMatching(c *echo.Context) bool {
	_ = c.Request().ParseForm()
	return truthy(firstNonEmpty(c.Request().FormValue("select_all_matching"), c.QueryParam("select_all_matching")))
}

func adminAccountBatchRootPresent(c *echo.Context) bool {
	_ = c.Request().ParseForm()
	return formHasNestedPrefix(c.Request().Form, "form_account_batch")
}

type adminAccountFilters struct {
	Page        string
	Origin      string
	Status      string
	RoleIDs     string
	Order       string
	Username    string
	ByDomain    string
	DisplayName string
	Email       string
	IP          string
}

func adminAccountFilterValues(c *echo.Context) adminAccountFilters {
	return adminAccountFilters{
		Page:        adminTrendsPageValue(c),
		Origin:      c.QueryParam("origin"),
		Status:      c.QueryParam("status"),
		RoleIDs:     c.QueryParam("role_ids"),
		Order:       firstNonEmpty(c.QueryParam("order"), "recent"),
		Username:    c.QueryParam("username"),
		ByDomain:    c.QueryParam("by_domain"),
		DisplayName: c.QueryParam("display_name"),
		Email:       c.QueryParam("email"),
		IP:          c.QueryParam("ip"),
	}
}

type adminAccountsViewOptions struct {
	Config       config.Config
	Roles        []models.UserRole
	TotalCount   int64
	PendingCount int64
}

func adminAccountsHTML(accounts []models.Account, notice string, errorText string, filters adminAccountFilters, locale ...string) string {
	totalCount := int64(len(accounts))
	if len(accounts) == adminRailsDefaultPageSize {
		totalCount++
	}
	return adminAccountsHTMLWithOptions(accounts, notice, errorText, filters, adminAccountsViewOptions{TotalCount: totalCount}, locale...)
}

func adminAccountsHTMLWithOptions(accounts []models.Account, notice string, errorText string, filters adminAccountFilters, options adminAccountsViewOptions, locale ...string) string {
	loc := settingsLocaleArgOrEnglish(locale...)
	var body strings.Builder
	body.WriteString(`<form method="get" action="/admin/accounts" class="simple_form"><div class="filters">`)
	body.WriteString(adminAccountFilterSelectHTML(adminT(loc, "admin.accounts.location.title", "Origin"), adminAccountSelect("origin", filters.Origin, []string{"", "local", "remote"}, loc)))
	statusSelect := adminAccountSelect("status", filters.Status, []string{"", "active", "silenced", "disabled", "suspended", "pending"}, loc)
	if options.PendingCount > 0 {
		pendingLabel := adminAccountSelectLabel(loc, "status", "pending") + " (" + strconv.FormatInt(options.PendingCount, 10) + ")"
		statusSelect = strings.Replace(statusSelect, `>`+html.EscapeString(adminAccountSelectLabel(loc, "status", "pending"))+`</option>`, `>`+html.EscapeString(pendingLabel)+`</option>`, 1)
	}
	body.WriteString(adminAccountFilterSelectHTML(adminT(loc, "admin.accounts.moderation.title", "Status"), statusSelect))
	body.WriteString(adminAccountFilterSelectHTML(adminT(loc, "admin.accounts.role", "Role"), adminAccountRoleSelectHTML(filters.RoleIDs, options.Roles, loc)))
	body.WriteString(adminAccountFilterSelectHTML(adminT(loc, "generic.order_by", "Order by"), adminAccountSelect("order", filters.Order, []string{"recent", "active"}, loc)))
	body.WriteString(`</div><div class="fields-group">`)
	body.WriteString(adminAccountSearchInputHTML("username", filters.Username, adminT(loc, "admin.accounts.username", "Username")))
	if filters.Origin == "remote" {
		body.WriteString(adminAccountSearchInputHTML("by_domain", filters.ByDomain, adminT(loc, "admin.accounts.by_domain", "Domain")))
	}
	body.WriteString(adminAccountSearchInputHTML("display_name", filters.DisplayName, adminT(loc, "admin.accounts.display_name", "Display name")))
	body.WriteString(adminAccountSearchInputHTML("email", filters.Email, adminT(loc, "admin.accounts.email", "Email")))
	body.WriteString(adminAccountSearchInputHTML("ip", filters.IP, adminT(loc, "admin.accounts.ip", "IP")))
	body.WriteString(`</div><div class="actions"><button class="button" type="submit">` + html.EscapeString(adminT(loc, "admin.accounts.search", "Search")) + `</button> <a class="button negative" href="/admin/accounts">` + html.EscapeString(adminT(loc, "admin.accounts.reset", "Reset")) + `</a></div></form><hr class="spacer">`)
	body.WriteString(`<form method="post" action="/admin/accounts/batch" class="new_form_account_batch" id="new_form_account_batch">` + adminAccountBatchHiddenFields(filters) + `<div class="batch-table"><div class="batch-table__toolbar"><label class="batch-table__toolbar__select batch-checkbox-all"><input type="checkbox" name="batch_checkbox_all"></label><div class="batch-table__toolbar__actions">`)
	if adminAccountsContainPending(accounts) {
		body.WriteString(`<button name="approve" value="1" class="table-action-link" type="submit" data-confirm="` + html.EscapeString(adminT(loc, "admin.reports.are_you_sure", "Are you sure?")) + `"><i class="fa fa-check"></i> ` + html.EscapeString(adminT(loc, "admin.accounts.approve", "Approve")) + `</button>`)
		body.WriteString(`<button name="reject" value="1" class="table-action-link" type="submit" data-confirm="` + html.EscapeString(adminT(loc, "admin.reports.are_you_sure", "Are you sure?")) + `"><i class="fa fa-times"></i> ` + html.EscapeString(adminT(loc, "admin.accounts.reject", "Reject")) + `</button>`)
	}
	body.WriteString(`<button name="suspend" value="1" class="table-action-link" type="submit" data-confirm="` + html.EscapeString(adminT(loc, "admin.reports.are_you_sure", "Are you sure?")) + `"><i class="fa fa-lock"></i> ` + html.EscapeString(adminT(loc, "admin.accounts.perform_full_suspension", "Suspend")) + `</button></div></div>`)
	if options.TotalCount > int64(len(accounts)) {
		pageCount := strconv.Itoa(len(accounts))
		totalCount := strconv.FormatInt(options.TotalCount, 10)
		pageSelected := adminTVars(loc, "generic.all_items_on_page_selected_html.other", "All <strong>%{count}</strong> accounts on this page selected.", map[string]string{"count": html.EscapeString(pageCount)})
		matchingSelected := adminTVars(loc, "generic.all_matching_items_selected_html.other", "All <strong>%{count}</strong> accounts matching your search selected.", map[string]string{"count": html.EscapeString(totalCount)})
		body.WriteString(`<div class="batch-table__select-all"><div class="not-selected active"><span>` + pageSelected + `</span><button type="button">` + html.EscapeString(adminT(loc, "generic.select_all_matching_items", "Select all matching accounts")) + `</button></div><div class="selected"><span>` + matchingSelected + `</span><button type="button">` + html.EscapeString(adminT(loc, "generic.deselect", "Deselect")) + `</button></div></div>`)
	}
	body.WriteString(`<div class="batch-table__body">`)
	if len(accounts) == 0 {
		body.WriteString(adminNothingHereHTML(loc, "nothing-here--under-tabs"))
	} else {
		for _, account := range accounts {
			body.WriteString(adminAccountRowHTMLWithConfig(options.Config, account, loc))
		}
	}
	body.WriteString(`</div></div></form>`)
	body.WriteString(adminRailsPaginationHTML(loc, "/admin/accounts", filters.Page, adminAccountFiltersQuery(filters), len(accounts)))
	return authPageHTML(adminT(loc, "admin.accounts.title", "Accounts"), notice, errorText, body.String(), loc)
}

func adminAccountFilterSelectHTML(label string, selectHTML string) string {
	return `<div class="filter-subset filter-subset--with-select"><strong>` + html.EscapeString(label) + `</strong><div class="input select optional">` + selectHTML + `</div></div>`
}

func adminAccountSearchInputHTML(name string, value string, placeholder string) string {
	return `<div class="input string optional"><input class="string optional" type="text" name="` + html.EscapeString(name) + `" id="` + html.EscapeString(name) + `" value="` + html.EscapeString(value) + `" placeholder="` + html.EscapeString(placeholder) + `"></div>`
}

func adminAccountRoleSelectHTML(current string, roles []models.UserRole, locale string) string {
	var out strings.Builder
	out.WriteString(`<select name="role_ids" id="role_ids"><option value="">` + html.EscapeString(adminT(locale, "admin.accounts.moderation.all", "All")) + `</option>`)
	for _, role := range roles {
		selected := ""
		if current == strconv.FormatInt(role.ID, 10) {
			selected = ` selected`
		}
		out.WriteString(`<option value="` + strconv.FormatInt(role.ID, 10) + `"` + selected + `>` + html.EscapeString(role.Name) + `</option>`)
	}
	out.WriteString(`</select>`)
	return out.String()
}

func adminAccountsContainPending(accounts []models.Account) bool {
	for _, account := range accounts {
		if account.Local() && account.User.ID != 0 && !account.User.Approved && !account.SuspendedAt.Valid {
			return true
		}
	}
	return false
}

func adminAccountFiltersQuery(filters adminAccountFilters) url.Values {
	values := url.Values{}
	for key, value := range map[string]string{
		"origin": filters.Origin, "status": filters.Status, "role_ids": filters.RoleIDs,
		"order": filters.Order, "username": filters.Username, "by_domain": filters.ByDomain,
		"display_name": filters.DisplayName, "email": filters.Email, "ip": filters.IP,
	} {
		if strings.TrimSpace(value) != "" {
			values.Set(key, value)
		}
	}
	return values
}

func adminAccountBatchHiddenFields(filters adminAccountFilters) string {
	values := map[string]string{
		"page":         firstNonEmpty(filters.Page, "1"),
		"origin":       filters.Origin,
		"status":       filters.Status,
		"role_ids":     filters.RoleIDs,
		"order":        filters.Order,
		"username":     filters.Username,
		"by_domain":    filters.ByDomain,
		"display_name": filters.DisplayName,
		"email":        filters.Email,
		"ip":           filters.IP,
	}
	var body strings.Builder
	body.WriteString(`<input type="hidden" name="select_all_matching" value="0">`)
	for _, key := range []string{"page", "origin", "status", "role_ids", "order", "username", "by_domain", "display_name", "email", "ip"} {
		if value := strings.TrimSpace(values[key]); value != "" {
			body.WriteString(`<input type="hidden" name="` + key + `" value="` + html.EscapeString(value) + `">`)
		}
	}
	return body.String()
}

func adminAccountBatchRedirectURL(c *echo.Context, messageKey string, message string) string {
	values := url.Values{}
	for _, key := range []string{"page", "origin", "status", "role_ids", "order", "username", "by_domain", "display_name", "email", "ip"} {
		value := strings.TrimSpace(firstNonEmpty(c.FormValue(key), c.QueryParam(key)))
		if value != "" {
			values.Set(key, value)
		}
	}
	if messageKey != "" && message != "" {
		values.Set(messageKey, message)
	}
	if query := values.Encode(); query != "" {
		return "/admin/accounts?" + query
	}
	return "/admin/accounts"
}

func adminAccountRowHTML(account models.Account, locale ...string) string {
	return adminAccountRowHTMLWithConfig(config.Config{}, account, settingsLocaleArgOrEnglish(locale...))
}

func adminAccountRowHTMLWithConfig(cfg config.Config, account models.Account, locale string) string {
	pending := account.Local() && account.User.ID != 0 && !account.User.Approved && !account.SuspendedAt.Valid
	classes := []string{"batch-table__row"}
	if pending {
		classes = append(classes, "batch-table__row--attention")
	}
	if account.SuspendedAt.Valid || (account.Local() && account.User.ID != 0 && !account.User.ConfirmedAt.Valid) {
		classes = append(classes, "batch-table__row--muted")
	}
	statuses := strconv.FormatInt(account.AccountStat.StatusesCount, 10)
	followers := strconv.FormatInt(account.AccountStat.FollowersCount, 10)
	lastActive := adminAccountRelevantTimestampHTML(account)
	if pending || account.SuspendedAt.Valid {
		statuses, followers, lastActive = "-", "-", "-"
	}
	return `<div class="` + strings.Join(classes, " ") + `"><label class="batch-table__row__select batch-table__row__select--aligned batch-checkbox"><input type="checkbox" name="form_account_batch[account_ids][]" value="` + strconv.FormatInt(account.ID, 10) + `"></label><div class="batch-table__row__content batch-table__row__content--unpadded"><table class="accounts-table"><tbody><tr><td>` + adminAccountLinkHTML(cfg, account) + `</td><td class="accounts-table__count optional">` + statuses + `<small>` + html.EscapeString(strings.ToLower(adminT(locale, "accounts.posts.other", "posts"))) + `</small></td><td class="accounts-table__count optional">` + followers + `<small>` + html.EscapeString(strings.ToLower(adminT(locale, "accounts.followers.other", "followers"))) + `</small></td><td class="accounts-table__count">` + lastActive + `<small>` + html.EscapeString(adminT(locale, "accounts.last_active", "last active")) + `</small></td><td class="accounts-table__extra">` + adminAccountExtraHTML(account) + `</td></tr></tbody></table></div></div>`
}

func adminAccountLinkHTML(cfg config.Config, account models.Account) string {
	view := serializer.AccountFromModel(cfg, account)
	avatar := view.AvatarStatic
	if strings.TrimSpace(avatar) == "" {
		avatar = "/avatars/original/missing.png"
	}
	displayName := statusEmbedAccountNameHTMLWithConfig(cfg, account, account.CustomEmojis)
	if strings.TrimSpace(displayName) == "" {
		displayName = html.EscapeString(account.Username)
	}
	acct := account.Username
	if account.Domain.Valid && strings.TrimSpace(account.Domain.String) != "" {
		acct += "@" + account.Domain.String
	}
	return `<div class="account account--minimal"><div class="account__wrapper"><a class="account__display-name" href="/admin/accounts/` + strconv.FormatInt(account.ID, 10) + `"><div class="account__avatar-wrapper"><img class="account__avatar" width="46" height="46" src="` + html.EscapeString(avatar) + `" alt=""></div><span class="display-name"><bdi><strong class="display-name__html emojify">` + displayName + `</strong></bdi><span class="display-name__account">@` + html.EscapeString(acct) + `</span></span></a></div></div>`
}

func adminAccountRelevantTimestampHTML(account models.Account) string {
	stamp := account.CreatedAt
	if account.Local() && account.User.CurrentSignInAt.Valid {
		stamp = account.User.CurrentSignInAt.Time
	} else if account.AccountStat.LastStatusAt.Valid {
		stamp = account.AccountStat.LastStatusAt.Time
	}
	if stamp.IsZero() {
		return "-"
	}
	date := stamp.UTC().Format("2006-01-02")
	return `<time class="time-ago" datetime="` + html.EscapeString(date) + `" title="` + html.EscapeString(date) + `">` + html.EscapeString(date) + `</time>`
}

func adminAccountExtraHTML(account models.Account) string {
	if !account.Local() || account.User.ID == 0 {
		return ""
	}
	email := strings.TrimSpace(account.User.Email)
	emailHTML := "-"
	if email != "" {
		domain := email
		if at := strings.LastIndex(email, "@"); at >= 0 && at+1 < len(email) {
			domain = email[at+1:]
		}
		query := url.Values{"email": []string{"%@" + domain}}
		emailHTML = `<a href="/admin/accounts?` + html.EscapeString(query.Encode()) + `" title="` + html.EscapeString(email) + `">` + html.EscapeString(domain) + `</a>`
	}
	ip := "-"
	if account.User.SignUpIP.Valid && strings.TrimSpace(account.User.SignUpIP.String) != "" {
		ip = html.EscapeString(account.User.SignUpIP.String)
	}
	return emailHTML + `<br><samp class="ellipsized-ip">` + ip + `</samp>`
}

type adminAccountIPHistoryRow struct {
	IP     string       `gorm:"column:ip"`
	UsedAt sql.NullTime `gorm:"column:used_at"`
}

type adminAccountDashboardCounts struct {
	MediaBytes      int64
	CreatedReports  int64
	TargetedReports int64
}

func (s *Server) adminAccountDashboardCounts(accountID int64) (adminAccountDashboardCounts, error) {
	counts := adminAccountDashboardCounts{}
	if s.db == nil || accountID <= 0 {
		return counts, nil
	}
	if err := s.db.Model(&models.MediaAttachment{}).Select("COALESCE(SUM(file_file_size), 0)").Where("account_id = ?", accountID).Scan(&counts.MediaBytes).Error; err != nil {
		return counts, err
	}
	if err := s.db.Model(&models.Report{}).Where("account_id = ?", accountID).Count(&counts.CreatedReports).Error; err != nil {
		return counts, err
	}
	if err := s.db.Model(&models.Report{}).Where("target_account_id = ?", accountID).Count(&counts.TargetedReports).Error; err != nil {
		return counts, err
	}
	return counts, nil
}

func (s *Server) adminAccountIPHistory(userID int64) ([]adminAccountIPHistoryRow, error) {
	if s.db == nil || userID <= 0 {
		return []adminAccountIPHistoryRow{}, nil
	}
	var rows []adminAccountIPHistoryRow
	err := s.db.Raw(`
SELECT ip::text AS ip, max(used_at) AS used_at
FROM (
  SELECT sign_up_ip AS ip, created_at AS used_at
  FROM users
  WHERE id = ? AND sign_up_ip IS NOT NULL
  UNION ALL
  SELECT ip, updated_at AS used_at
  FROM session_activations
  WHERE user_id = ? AND ip IS NOT NULL
  UNION ALL
  SELECT ip, created_at AS used_at
  FROM login_activities
  WHERE user_id = ? AND success = true AND ip IS NOT NULL
) user_ips
GROUP BY ip
ORDER BY used_at DESC
LIMIT 10`, userID, userID, userID).Scan(&rows).Error
	return rows, err
}

func adminAccountHTML(account models.Account, notice string, errorText string, moderationNotes ...models.AccountModerationNote) string {
	return adminAccountHTMLWithIPHistory(account, notice, errorText, nil, "en", config.Config{}, moderationNotes...)
}

func adminAccountHTMLWithIPHistory(account models.Account, notice string, errorText string, ipHistory []adminAccountIPHistoryRow, locale string, cfg config.Config, moderationNotes ...models.AccountModerationNote) string {
	return adminAccountHTMLWithViewData(account, notice, errorText, ipHistory, locale, cfg, adminAccountDashboardCounts{}, moderationNotes...)
}

func adminAccountHTMLWithViewData(account models.Account, notice string, errorText string, ipHistory []adminAccountIPHistoryRow, locale string, cfg config.Config, counts adminAccountDashboardCounts, moderationNotes ...models.AccountModerationNote) string {
	loc := settingsLocaleArgOrEnglish(locale)
	var body strings.Builder
	id := strconv.FormatInt(account.ID, 10)
	body.WriteString(adminAccountCardHTML(cfg, account))
	if strings.TrimSpace(account.Note) != "" {
		body.WriteString(`<div class="admin-account-bio"><div><div class="account__header__content emojify">` + sanitizeRemoteNoteContent(account.Note) + `</div></div></div>`)
	}
	body.WriteString(adminAccountCountersHTML(account, counts, loc))
	body.WriteString(adminAccountDetailsTableHTML(account, ipHistory, loc))
	body.WriteString(`<div class="action-buttons"><div>`)
	if account.SuspendedAt.Valid {
		body.WriteString(adminAccountPostButton("/admin/accounts/"+id+"/unsuspend", adminT(loc, "admin.accounts.undo_suspension", "Undo suspension"), ""))
		if adminAccountCanRedownload(account) && account.SuspensionOrigin.Valid && account.SuspensionOrigin.Int64 == 1 {
			body.WriteString(adminAccountPostButton("/admin/accounts/"+id+"/redownload", adminT(loc, "admin.accounts.redownload", "Refresh profile"), ""))
		}
		body.WriteString(`<a class="button button--destructive" data-method="delete" data-confirm="` + html.EscapeString(adminT(loc, "admin.accounts.are_you_sure", "Are you sure?")) + `" href="/admin/accounts/` + id + `">` + html.EscapeString(adminT(loc, "admin.accounts.delete", "Delete data")) + `</a>`)
	} else {
		if account.Local() && account.User.ID != 0 {
			if !account.User.Approved {
				body.WriteString(adminAccountPostButton("/admin/accounts/"+id+"/approve", adminT(loc, "admin.accounts.approve", "Approve"), ""))
				body.WriteString(adminAccountPostButton("/admin/accounts/"+id+"/reject", adminT(loc, "admin.accounts.reject", "Reject"), "button--destructive"))
			}
			if account.User.Disabled {
				body.WriteString(adminAccountPostButton("/admin/accounts/"+id+"/enable", adminT(loc, "admin.accounts.enable", "Unfreeze"), ""))
			} else {
				body.WriteString(`<a class="button" href="/admin/accounts/` + id + `/action/new?type=disable">` + html.EscapeString(adminT(loc, "admin.accounts.disable", "Freeze")) + `</a>`)
			}
			if !account.Memorial && account.User.Approved {
				body.WriteString(adminAccountPostButton("/admin/accounts/"+id+"/memorialize", adminT(loc, "admin.accounts.memorialize", "Turn into memoriam"), "button--destructive"))
			}
		}
		if account.SensitizedAt.Valid {
			body.WriteString(adminAccountPostButton("/admin/accounts/"+id+"/unsensitive", adminT(loc, "admin.accounts.undo_sensitized", "Undo force-sensitive"), ""))
		} else {
			body.WriteString(`<a class="button" href="/admin/accounts/` + id + `/action/new?type=sensitive">` + html.EscapeString(adminT(loc, "admin.accounts.sensitive", "Force-sensitive")) + `</a>`)
		}
		if account.SilencedAt.Valid {
			body.WriteString(adminAccountPostButton("/admin/accounts/"+id+"/unsilence", adminT(loc, "admin.accounts.undo_silenced", "Undo limit"), ""))
		} else {
			body.WriteString(`<a class="button" href="/admin/accounts/` + id + `/action/new?type=silence">` + html.EscapeString(adminT(loc, "admin.accounts.silence", "Limit")) + `</a>`)
		}
		body.WriteString(`<a class="button button--destructive" href="/admin/accounts/` + id + `/action/new?type=suspend">` + html.EscapeString(adminT(loc, "admin.accounts.suspend", "Suspend")) + `</a>`)
		if adminAccountCanRedownload(account) {
			body.WriteString(adminAccountPostButton("/admin/accounts/"+id+"/redownload", adminT(loc, "admin.accounts.redownload", "Refresh profile"), ""))
		}
	}
	body.WriteString(`</div></div><hr class="spacer"><h3>` + html.EscapeString(adminT(loc, "admin.reports.notes.title", "Moderation notes")) + `</h3><p>` + adminT(loc, "admin.reports.notes_description_html", "Private moderation notes for this account.") + `</p><div class="report-notes">`)
	for _, note := range moderationNotes {
		body.WriteString(adminAccountModerationNoteHTML(note, loc, cfg))
	}
	body.WriteString(`</div><form method="post" action="/admin/account_moderation_notes" class="simple_form new_account_moderation_note"><input type="hidden" name="account_moderation_note[target_account_id]" value="` + id + `"><div class="field-group"><div class="input text optional account_moderation_note_content"><div class="label_input"><textarea class="text optional" name="account_moderation_note[content]" rows="6" maxlength="2000" placeholder="` + html.EscapeString(adminT(loc, "admin.reports.notes.placeholder", "Leave a note")) + `"></textarea></div></div></div><div class="actions"><button class="button" type="submit">` + html.EscapeString(adminT(loc, "admin.account_moderation_notes.create", "Create note")) + `</button></div></form><hr class="spacer">`)
	return authPageHTML(adminReportAccountLabel(account), notice, errorText, body.String(), loc)
}

func adminAccountCardHTML(cfg config.Config, account models.Account) string {
	view := serializer.AccountFromModel(cfg, account)
	avatar := firstNonEmpty(view.Avatar, view.AvatarStatic, "/avatars/original/missing.png")
	header := firstNonEmpty(view.Header, view.HeaderStatic, "/headers/original/missing.png")
	displayName := statusEmbedAccountNameHTMLWithConfig(cfg, account, account.CustomEmojis)
	if strings.TrimSpace(displayName) == "" {
		displayName = html.EscapeString(account.Username)
	}
	return `<div class="card h-card"><a href="/admin/accounts/` + strconv.FormatInt(account.ID, 10) + `" target="_blank" rel="noopener noreferrer"><div class="card__img"><img src="` + html.EscapeString(header) + `" alt=""></div><div class="card__bar"><div class="avatar"><img src="` + html.EscapeString(avatar) + `" alt="" width="48" height="48" class="u-photo"></div><div class="display-name"><bdi><strong class="emojify p-name">` + displayName + `</strong></bdi><span>@` + html.EscapeString(account.Acct()) + `</span></div></div></a></div>`
}

func adminAccountCountersHTML(account models.Account, counts adminAccountDashboardCounts, locale string) string {
	id := strconv.FormatInt(account.ID, 10)
	var body strings.Builder
	body.WriteString(`<div class="dashboard__counters admin-account-counters">`)
	body.WriteString(adminAccountNumberCounterHTML("/admin/accounts/"+id+"/statuses", adminInstanceCountString(account.AccountStat.StatusesCount), adminT(locale, "admin.accounts.statuses", "Posts")))
	body.WriteString(adminAccountNumberCounterHTML("/admin/accounts/"+id+"/statuses?media=true", formatRailsHumanSize(counts.MediaBytes), adminT(locale, "admin.accounts.media_attachments", "Media")))
	body.WriteString(adminAccountNumberCounterHTML("/admin/accounts/"+id+"/relationships?relationship=followed_by", adminInstanceCountString(account.AccountStat.FollowersCount), adminT(locale, "admin.accounts.followers", "Followers")))
	body.WriteString(adminAccountNumberCounterHTML("/admin/reports?account_id="+id, adminInstanceCountString(counts.CreatedReports), adminT(locale, "admin.accounts.show.created_reports", "Made reports")))
	body.WriteString(adminAccountNumberCounterHTML("/admin/reports?target_account_id="+id, adminInstanceCountString(counts.TargetedReports), adminT(locale, "admin.accounts.show.targeted_reports", "Reported by others")))
	body.WriteString(`<div><a href="/admin/action_logs?target_account_id=` + id + `"><div class="dashboard__counters__text">` + html.EscapeString(adminAccountStateLabel(account, locale)) + `</div><div class="dashboard__counters__label">` + html.EscapeString(adminT(locale, "admin.accounts.login_status", "Login status")) + `</div></a></div></div>`)
	return body.String()
}

func adminAccountNumberCounterHTML(href string, value string, label string) string {
	return `<div><a href="` + html.EscapeString(href) + `"><div class="dashboard__counters__num">` + html.EscapeString(value) + `</div><div class="dashboard__counters__label">` + html.EscapeString(label) + `</div></a></div>`
}

func adminAccountDetailsTableHTML(account models.Account, ipHistory []adminAccountIPHistoryRow, locale string) string {
	if account.Local() && account.User.ID == 0 {
		return ""
	}
	id := strconv.FormatInt(account.ID, 10)
	var rows strings.Builder
	if account.Local() {
		if account.AvatarFileName.Valid && strings.TrimSpace(account.AvatarFileName.String) != "" {
			rows.WriteString(adminAccountDetailRow(adminT(locale, "admin.accounts.avatar", "Avatar"), "", adminAccountTableLink("trash", adminT(locale, "admin.accounts.remove_avatar", "Remove avatar"), "/admin/accounts/"+id+"/remove_avatar", "post")))
		}
		if account.HeaderFileName.Valid && strings.TrimSpace(account.HeaderFileName.String) != "" {
			rows.WriteString(adminAccountDetailRow(adminT(locale, "admin.accounts.header", "Header"), "", adminAccountTableLink("trash", adminT(locale, "admin.accounts.remove_header", "Remove header"), "/admin/accounts/"+id+"/remove_header", "post")))
		}
		roleLabel := adminT(locale, "admin.accounts.no_role_assigned", "No role assigned")
		if account.User.Role.ID != 0 && account.User.Role.ID != -99 {
			roleLabel = account.User.Role.Name
		}
		rows.WriteString(adminAccountDetailRow(adminT(locale, "admin.accounts.role", "Role"), html.EscapeString(roleLabel), adminAccountTableLink("vcard", adminT(locale, "admin.accounts.change_role.label", "Change role"), "/admin/users/"+strconv.FormatInt(account.User.ID, 10)+"/role", "")))
		rows.WriteString(adminAccountDetailRow(adminT(locale, "admin.accounts.email", "Email"), html.EscapeString(account.User.Email), adminAccountTableLink("edit", adminT(locale, "admin.accounts.change_email.label", "Change email"), "/admin/accounts/"+id+"/change_email", "")))
		emailState := adminT(locale, "admin.accounts.confirming", "Confirming")
		if account.User.ConfirmedAt.Valid {
			emailState = adminT(locale, "admin.accounts.confirmed", "Confirmed")
		}
		rows.WriteString(adminAccountDetailRow(adminT(locale, "admin.accounts.email_status", "Email status"), html.EscapeString(emailState), ""))
		security := adminT(locale, "admin.accounts.security_measures.only_password", "Password only")
		securityAction := ""
		if account.User.OTPRequiredForLogin || account.User.WebauthnID.Valid {
			security = adminT(locale, "admin.accounts.security_measures.password_and_2fa", "Password and two-factor authentication")
			securityAction = adminAccountTableLink("unlock", adminT(locale, "admin.accounts.disable_two_factor_authentication", "Disable two-factor authentication"), "/admin/users/"+strconv.FormatInt(account.User.ID, 10)+"/two_factor_authentication", "delete")
		}
		rows.WriteString(adminAccountDetailRow(adminT(locale, "admin.accounts.security", "Security"), html.EscapeString(security), securityAction))
		userLocale := "-"
		if account.User.Locale.Valid {
			userLocale = railsStandardLocaleName(account.User.Locale.String)
		}
		rows.WriteString(adminAccountDetailRow(adminT(locale, "simple_form.labels.defaults.locale", "Locale"), html.EscapeString(userLocale), ""))
		rows.WriteString(adminAccountDetailRow(adminT(locale, "admin.accounts.joined", "Joined"), adminAccountFormattedTime(account.CreatedAt), ""))
		for index, recentIP := range ipHistory {
			label := ""
			if index == 0 {
				label = adminT(locale, "admin.accounts.most_recent_ip", "Most recent IP")
			}
			rows.WriteString(adminAccountDetailRow(label, `<samp class="ellipsized-ip">`+html.EscapeString(recentIP.IP)+`</samp>`, adminAccountTableLink("search", adminT(locale, "admin.accounts.search_same_ip", "Other users with the same IP"), "/admin/accounts?ip="+url.QueryEscape(recentIP.IP), "")))
		}
		activity := ""
		if account.User.CurrentSignInAt.Valid {
			activity = adminAccountFormattedTime(account.User.CurrentSignInAt.Time)
		}
		rows.WriteString(adminAccountDetailRow(adminT(locale, "admin.accounts.most_recent_activity", "Most recent activity"), activity, ""))
	} else {
		instanceLink := adminAccountTableLink("search", adminT(locale, "admin.accounts.view_domain", "View domain"), "/admin/instances/"+url.PathEscape(account.Domain.String), "")
		rows.WriteString(adminAccountDetailRow(adminT(locale, "admin.accounts.inbox_url", "Inbox URL"), html.EscapeString(account.InboxURL), instanceLink))
		rows.WriteString(adminAccountDetailRow(adminT(locale, "admin.accounts.shared_inbox_url", "Shared inbox URL"), html.EscapeString(account.SharedInboxURL), ""))
	}
	return `<div class="table-wrapper"><table class="table inline-table"><tbody>` + rows.String() + `</tbody></table></div>`
}

func adminAccountDetailRow(label string, valueHTML string, actionHTML string) string {
	return `<tr><th>` + html.EscapeString(label) + `</th><td>` + valueHTML + `</td><td>` + actionHTML + `</td></tr>`
}

func adminAccountTableLink(icon string, label string, href string, method string) string {
	return adminTableActionLinkHTML(icon, label, href, method, "")
}

func adminTableActionLinkHTML(icon string, label string, href string, method string, confirm string) string {
	methodAttr := ""
	if method != "" {
		methodAttr = ` data-method="` + html.EscapeString(method) + `"`
	}
	confirmAttr := ""
	if confirm != "" {
		confirmAttr = ` data-confirm="` + html.EscapeString(confirm) + `"`
	}
	return `<a class="table-action-link"` + methodAttr + confirmAttr + ` href="` + html.EscapeString(href) + `"><i class="fa fa-` + html.EscapeString(icon) + ` fa-fw"></i> ` + html.EscapeString(label) + `</a>`
}

func adminAccountFormattedTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	stamp := value.UTC().Format(time.RFC3339)
	return `<time class="formatted" datetime="` + html.EscapeString(stamp) + `" title="` + html.EscapeString(stamp) + `">` + html.EscapeString(value.UTC().Format("2006-01-02 15:04")) + `</time>`
}

func adminAccountCanRedownload(account models.Account) bool {
	return !account.Local()
}

func adminAccountIPHistoryHTML(rows []adminAccountIPHistoryRow, locale ...string) string {
	loc := settingsLocaleArgOrEnglish(locale...)
	var body strings.Builder
	body.WriteString(`<h3>` + html.EscapeString(adminT(loc, "admin.accounts.ip_history", "IP history")) + `</h3><table class="table inline-table"><thead><tr><th>` + html.EscapeString(adminT(loc, "admin.accounts.ip", "IP")) + `</th><th>` + html.EscapeString(adminT(loc, "admin.accounts.last_used", "Last used")) + `</th><th></th></tr></thead><tbody>`)
	if len(rows) == 0 {
		body.WriteString(`<tr><td colspan="3">` + html.EscapeString(adminT(loc, "admin.accounts.no_ip_history", "No IP history")) + `</td></tr>`)
	} else {
		for _, row := range rows {
			searchURL := "/admin/accounts?ip=" + url.QueryEscape(row.IP)
			body.WriteString(`<tr><td>`)
			body.WriteString(`<samp class="ellipsized-ip">` + html.EscapeString(row.IP) + `</samp>`)
			body.WriteString(`</td><td>`)
			if row.UsedAt.Valid {
				body.WriteString(html.EscapeString(row.UsedAt.Time.UTC().Format(time.RFC3339)))
			}
			body.WriteString(`</td><td><a href="`)
			body.WriteString(html.EscapeString(searchURL))
			body.WriteString(`">`)
			body.WriteString(html.EscapeString(adminT(loc, "admin.accounts.search_same_ip", "Other users with the same IP")))
			body.WriteString(`</a>`)
			body.WriteString(`</td></tr>`)
		}
	}
	body.WriteString(`</tbody></table>`)
	return body.String()
}

func adminAccountModerationNoteHTML(note models.AccountModerationNote, locale string, cfg config.Config) string {
	loc := settingsLocaleArgOrEnglish(locale)
	author := adminReportAccountLabel(note.Account)
	authorHTML := html.EscapeString(author)
	if note.Account.ID != 0 {
		authorHTML = `<a href="/admin/accounts/` + strconv.FormatInt(note.Account.ID, 10) + `">` + authorHTML + `</a>`
	}
	createdAt := note.CreatedAt.UTC()
	return `<div class="report-notes__item">` +
		`<img src="` + html.EscapeString(adminAccountModerationNoteAvatarURL(cfg, note.Account)) + `" alt="" class="report-notes__item__avatar">` +
		`<div class="report-notes__item__header"><span class="username">` + authorHTML + `</span><time class="relative-formatted" datetime="` + html.EscapeString(createdAt.Format(time.RFC3339)) + `">` + html.EscapeString(createdAt.Format("2006-01-02")) + `</time></div>` +
		`<div class="report-notes__item__content">` + adminLinkifyModerationNoteContent(note.Content) + `</div>` +
		`<div class="report-notes__item__actions">` + adminAccountTableLink("trash", adminT(loc, "admin.reports.notes.delete", "Delete note"), "/admin/account_moderation_notes/"+strconv.FormatInt(note.ID, 10), "delete") + `</div>` +
		`</div>`
}

func adminAccountModerationNoteAvatarURL(cfg config.Config, account models.Account) string {
	if account.AvatarFileName.Valid && strings.TrimSpace(account.AvatarFileName.String) != "" && account.ID != 0 {
		return adminAccountPaperclipAssetURL(cfg, accountImageAssetPath(account, "avatar", "original", account.AvatarFileName.String))
	}
	if account.AvatarRemoteURL.Valid && strings.TrimSpace(account.AvatarRemoteURL.String) != "" {
		return account.AvatarRemoteURL.String
	}
	return "/avatars/original/missing.png"
}

func adminAccountPaperclipAssetURL(cfg config.Config, assetPath string) string {
	if strings.TrimSpace(cfg.StorageHost) != "" || strings.TrimSpace(cfg.WebDomain) != "" || strings.HasPrefix(strings.TrimSpace(cfg.PaperclipRootURL), "http://") || strings.HasPrefix(strings.TrimSpace(cfg.PaperclipRootURL), "https://") {
		return cfg.SystemAssetURL(assetPath)
	}
	root := strings.TrimRight(strings.TrimSpace(cfg.PaperclipRootURL), "/")
	if root == "" {
		root = "/system"
	}
	return "/" + strings.Trim(root, "/") + "/" + strings.TrimLeft(assetPath, "/")
}

func adminLinkifyModerationNoteContent(content string) string {
	var out strings.Builder
	last := 0
	for _, loc := range adminModerationNoteURLPattern.FindAllStringIndex(content, -1) {
		if loc[0] < last {
			continue
		}
		out.WriteString(adminEscapeTextWithBreaks(content[last:loc[0]]))
		rawURL := content[loc[0]:loc[1]]
		linkURL := strings.TrimRight(rawURL, ".,;:!?)]}")
		trailing := rawURL[len(linkURL):]
		if parsed, err := url.Parse(linkURL); err == nil && parsed.Host != "" && (parsed.Scheme == "http" || parsed.Scheme == "https") {
			escaped := html.EscapeString(linkURL)
			out.WriteString(`<a href="` + escaped + `" rel="nofollow noopener noreferrer" target="_blank">` + escaped + `</a>`)
			out.WriteString(adminEscapeTextWithBreaks(trailing))
		} else {
			out.WriteString(adminEscapeTextWithBreaks(rawURL))
		}
		last = loc[1]
	}
	out.WriteString(adminEscapeTextWithBreaks(content[last:]))
	return out.String()
}

func adminEscapeTextWithBreaks(value string) string {
	return strings.ReplaceAll(html.EscapeString(value), "\n", "<br>")
}

func adminAccountActionFormHTML(account models.Account, actionType string, reportID int64, errorText string, locale ...string) string {
	return adminAccountActionFormHTMLWithPresets(account, actionType, reportID, nil, errorText, locale...)
}

func adminAccountActionFormHTMLWithPresets(account models.Account, actionType string, reportID int64, presets []models.AccountWarningPreset, errorText string, locale ...string) string {
	loc := settingsLocaleArgOrEnglish(locale...)
	validTypes := []string{"none", "disable", "sensitive", "silence", "suspend"}
	if !account.Local() {
		validTypes = []string{"sensitive", "silence", "suspend"}
	}
	if !slices.Contains(validTypes, actionType) {
		actionType = validTypes[0]
	}
	var body strings.Builder
	body.WriteString(`<form method="post" action="/admin/accounts/` + strconv.FormatInt(account.ID, 10) + `/action" class="simple_form new_admin_account_action"><input type="hidden" name="admin_account_action[report_id]" value="` + html.EscapeString(strconv.FormatInt(reportID, 10)) + `"><div class="fields-group"><div class="input radio_buttons optional admin_account_action_type"><div class="label_input"><label>` + html.EscapeString(adminT(loc, "simple_form.labels.admin_account_action.type", "Action")) + `</label><span class="hint">` + adminTVars(loc, "simple_form.hints.admin_account_action.type_html", "Choose an action for %{acct}.", map[string]string{"acct": html.EscapeString(account.Acct())}) + `</span><ul>`)
	for _, value := range validTypes {
		checked := ""
		if value == actionType {
			checked = ` checked`
		}
		label := adminT(loc, "simple_form.labels.admin_account_action.types."+value, adminAccountActionLabel(loc, value))
		hint := adminT(loc, "simple_form.hints.admin_account_action.types."+value, "")
		body.WriteString(`<li><label class="radio"><input class="radio_buttons optional" type="radio" name="admin_account_action[type]" value="` + html.EscapeString(value) + `"` + checked + `> ` + html.EscapeString(label))
		if strings.TrimSpace(hint) != "" {
			body.WriteString(`<span class="hint">` + html.EscapeString(hint) + `</span>`)
		}
		body.WriteString(`</label></li>`)
	}
	body.WriteString(`</ul></div></div></div>`)
	if account.Local() {
		body.WriteString(`<hr class="spacer"><div class="fields-group"><div class="input boolean optional admin_account_action_send_email_notification"><div class="label_input"><label class="boolean"><input type="hidden" name="admin_account_action[send_email_notification]" value="0"><input class="boolean optional" type="checkbox" name="admin_account_action[send_email_notification]" value="1" checked> ` + html.EscapeString(adminT(loc, "simple_form.labels.admin_account_action.send_email_notification", "Send an e-mail notification")) + `</label></div></div></div>`)
		if reportID > 0 {
			body.WriteString(`<div class="fields-group"><div class="input boolean optional admin_account_action_include_statuses"><div class="label_input"><label class="boolean"><input type="hidden" name="admin_account_action[include_statuses]" value="0"><input class="boolean optional" type="checkbox" name="admin_account_action[include_statuses]" value="1" checked> ` + html.EscapeString(adminT(loc, "simple_form.labels.admin_account_action.include_statuses", "Include reported posts in the warning e-mail")) + `</label></div></div></div>`)
		}
		body.WriteString(`<hr class="spacer">`)
		if len(presets) > 0 {
			body.WriteString(`<div class="fields-group"><div class="input select optional admin_account_action_warning_preset_id"><div class="label_input"><label for="admin_account_action_warning_preset_id">` + html.EscapeString(adminT(loc, "simple_form.labels.admin_account_action.warning_preset_id", "Use a warning preset")) + `</label><select name="admin_account_action[warning_preset_id]" id="admin_account_action_warning_preset_id"><option value=""></option>`)
			for _, preset := range presets {
				label := strings.TrimSpace(preset.Title)
				if label == "" {
					label = strings.TrimSpace(preset.Text)
				}
				body.WriteString(`<option value="` + strconv.FormatInt(preset.ID, 10) + `">` + html.EscapeString(label) + `</option>`)
			}
			body.WriteString(`</select></div></div></div>`)
		}
		body.WriteString(`<div class="fields-group"><div class="input text optional admin_account_action_text"><div class="label_input"><label for="admin_account_action_text">` + html.EscapeString(adminT(loc, "simple_form.labels.admin_account_action.text", "Warning text")) + `</label><textarea class="text optional" name="admin_account_action[text]" id="admin_account_action_text" rows="6"></textarea><span class="hint">` + adminT(loc, "simple_form.hints.admin_account_action.text_html", "You can manage reusable warning presets from the warning presets page.") + `</span></div></div></div>`)
	}
	body.WriteString(`<div class="actions"><button class="button" type="submit">` + html.EscapeString(adminT(loc, "admin.account_actions.action", "Apply action")) + `</button></div></form>`)
	title := adminTVars(loc, "admin.account_actions.title", "Take moderation action against %{acct}", map[string]string{"acct": account.Acct()})
	return authPageHTML(title, "", errorText, body.String(), loc)
}

func adminAccountPostButton(action string, label string, extraClass string) string {
	className := "button"
	if extraClass != "" {
		className += " " + extraClass
	}
	return `<a data-method="post" href="` + html.EscapeString(action) + `" class="` + className + `">` + html.EscapeString(label) + `</a>`
}

func adminAccountSelect(name string, current string, values []string, locale ...string) string {
	loc := settingsLocaleArgOrEnglish(locale...)
	var out strings.Builder
	out.WriteString(`<select name="` + name + `">`)
	for _, value := range values {
		selected := ""
		if current == value {
			selected = ` selected`
		}
		label := adminAccountSelectLabel(loc, name, value)
		out.WriteString(`<option value="` + html.EscapeString(value) + `"` + selected + `>` + html.EscapeString(label) + `</option>`)
	}
	out.WriteString(`</select>`)
	return out.String()
}

func adminAccountStateLabel(account models.Account, locale ...string) string {
	loc := settingsLocaleArgOrEnglish(locale...)
	switch {
	case account.Memorial:
		return adminT(loc, "admin.accounts.memorialized", "Memorialized")
	case account.SuspendedAt.Valid:
		return adminT(loc, "admin.accounts.suspended", "Suspended")
	case account.SilencedAt.Valid:
		return adminT(loc, "admin.accounts.silenced", "Limited")
	case account.SensitizedAt.Valid:
		return adminT(loc, "admin.accounts.sensitized", "Marked as sensitive")
	case account.Local() && account.User.ID != 0 && account.User.Disabled:
		return adminT(loc, "admin.accounts.disabled", "Disabled")
	case account.Local() && account.User.ID != 0 && !account.User.Approved:
		return adminT(loc, "admin.accounts.pending", "Pending review")
	default:
		return adminT(loc, "admin.accounts.moderation.active", "Active")
	}
}

func adminAccountSelectLabel(locale string, name string, value string) string {
	if value == "" {
		return adminT(locale, "generic.all", "All")
	}
	switch name {
	case "origin":
		return adminT(locale, "admin.accounts.location."+value, value)
	case "status":
		return adminT(locale, "admin.accounts.moderation."+value, value)
	case "order":
		if value == "active" {
			return adminT(locale, "relationships.last_active", "Last active")
		}
		return adminT(locale, "relationships.most_recent", "Most recent")
	default:
		return value
	}
}

func adminAccountActionLabel(locale string, actionType string) string {
	switch actionType {
	case "disable":
		return adminT(locale, "admin.accounts.disable", "Freeze")
	case "sensitive":
		return adminT(locale, "admin.accounts.sensitive", "Force-sensitive")
	case "silence":
		return adminT(locale, "admin.accounts.silence", "Limit")
	case "suspend":
		return adminT(locale, "admin.accounts.suspend", "Suspend")
	default:
		return actionType
	}
}

func adminAccountNotice(locale string, key string, fallback string, username string) string {
	value := adminTVars(locale, key, fallback, map[string]string{"username": username})
	if strings.Contains(value, "%{") {
		return fallback
	}
	return value
}
