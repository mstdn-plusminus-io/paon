package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"github.com/mstdn-plusminus-io/paon/internal/paon/serializer"
	"gorm.io/gorm"
)

var (
	adminAccountsPaginationParams = []string{
		"limit",
		"local",
		"remote",
		"by_domain",
		"active",
		"pending",
		"disabled",
		"sensitized",
		"silenced",
		"suspended",
		"username",
		"display_name",
		"email",
		"ip",
		"staff",
	}
	adminAccountsV2PaginationParams = []string{
		"limit",
		"origin",
		"status",
		"permissions",
		"username",
		"by_domain",
		"display_name",
		"email",
		"ip",
		"invited_by",
		"role_ids",
	}
	adminReportsPaginationParams = []string{
		"limit",
		"resolved",
		"account_id",
		"target_account_id",
	}
	adminLimitPaginationParams = []string{"limit"}
)

func (s *Server) adminAccounts(c *echo.Context) error {
	if _, err := s.requireAdminRead(c, "admin:read:accounts"); err != nil {
		return err
	}
	if s.db == nil {
		return c.JSON(http.StatusOK, []any{})
	}

	query := s.adminAccountQuery(c)
	query, err := applyAdminAccountOrder(c, query)
	if err != nil {
		return err
	}
	var accounts []models.Account
	limitValue := limit(c, 100, 200)
	if err := applyIDPagination(c, query, "accounts.id").
		Limit(limitValue).
		Find(&accounts).Error; err != nil {
		return err
	}
	if queryParamValuePresent(c, "min_id") {
		reverseRows(accounts)
	}
	if len(accounts) > 0 {
		c.Response().Header().Set("Link", paginationLinkWithAllowedParams(c, accounts[0].ID, accounts[len(accounts)-1].ID, "min_id", len(accounts) == limitValue, true, adminAccountsPaginationParamsForRequest(c)))
	}

	out := make([]serializer.AdminAccount, 0, len(accounts))
	for _, account := range accounts {
		item, err := s.adminAccountFromModel(account)
		if err != nil {
			return err
		}
		out = append(out, item)
	}
	return c.JSON(http.StatusOK, out)
}

func adminAccountsPaginationParamsForRequest(c *echo.Context) []string {
	if c != nil && c.Request() != nil && c.Request().URL != nil && strings.Contains(c.Request().URL.Path, "/api/v2/admin/accounts") {
		return adminAccountsV2PaginationParams
	}
	return adminAccountsPaginationParams
}

func (s *Server) showAdminAccount(c *echo.Context) error {
	if _, err := s.requireAdminRead(c, "admin:read:accounts"); err != nil {
		return err
	}
	var account models.Account
	if err := s.db.Preload("User.Role").Preload("AccountStat").Where("accounts.id = ?", c.Param("id")).First(&account).Error; err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	out, err := s.adminAccountFromModel(account)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, out)
}

func (s *Server) destroyAdminAccount(c *echo.Context) error {
	if _, err := s.requireAdminWriteWithPermissions(c, []string{"admin:write:accounts"}, rolePermissionDeleteUserData); err != nil {
		return err
	}
	account, err := s.loadAdminAccount(c.Param("id"))
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	if ok, err := s.adminAccountCanDestroyWithRailsPolicy(account.ID); err != nil {
		return err
	} else if !ok {
		return apiError(c, http.StatusForbidden, "This action is not allowed")
	}
	if err := s.enqueueAdminAccountDeletionOrRun(c.Request().Context(), account.ID); err != nil {
		return err
	}
	return renderEmpty(c)
}

func (s *Server) adminAccountAction(c *echo.Context) error {
	user, err := s.requireAdminWrite(c, "admin:write:accounts")
	if err != nil {
		return err
	}
	payload, err := parseAdminAccountActionPayload(c)
	if err != nil {
		return apiError(c, http.StatusBadRequest, "The request body could not be parsed")
	}
	action, ok := adminAccountActionCode(payload.Type)
	if !ok {
		return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Type is not included in the list")
	}

	var target models.Account
	if err := s.db.Preload("User").Where("accounts.id = ?", c.Param("account_id")).First(&target).Error; err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	if ok, err := s.adminAccountActionPermittedByRailsPolicy(user, &target, payload.Type); err != nil {
		return err
	} else if !ok {
		return apiError(c, http.StatusForbidden, "This action is not allowed")
	}
	if ok, err := s.adminAccountReportResolutionPermittedByRailsPolicy(user, target.ID, payload.ReportID, payload.Type); err != nil {
		return err
	} else if !ok {
		return apiError(c, http.StatusForbidden, "This action is not allowed")
	}

	now := time.Now().UTC()
	var createdWarning models.AccountWarning
	err = s.db.Transaction(func(tx *gorm.DB) error {
		switch payload.Type {
		case "disable":
			if err := tx.Model(&models.User{}).Where("account_id = ?", target.ID).Updates(map[string]any{"disabled": true, "updated_at": now}).Error; err != nil {
				return err
			}
		case "sensitive":
			if err := tx.Model(&models.Account{}).Where("id = ?", target.ID).Updates(map[string]any{"sensitized_at": now, "updated_at": now}).Error; err != nil {
				return err
			}
		case "silence":
			if err := tx.Model(&models.Account{}).Where("id = ?", target.ID).Updates(map[string]any{"silenced_at": now, "updated_at": now}).Error; err != nil {
				return err
			}
		case "suspend":
			if err := tx.Model(&models.Account{}).Where("id = ?", target.ID).Updates(map[string]any{"suspended_at": now, "suspension_origin": 0, "updated_at": now}).Error; err != nil {
				return err
			}
			if err := createCanonicalEmailBlockForAccountTx(tx, target, now); err != nil {
				return err
			}
		}

		warningText, err := s.adminAccountWarningText(tx, payload)
		if err != nil {
			return err
		}
		warning := models.AccountWarning{
			AccountID:       models.AccountWarningAccountID(user.AccountID),
			TargetAccountID: models.AccountWarningTargetAccountID(target.ID),
			Action:          action,
			Text:            accountWarningText(warningText),
			CreatedAt:       now,
			UpdatedAt:       now,
		}
		if payload.ReportID > 0 {
			warning.ReportID = sql.NullInt64{Int64: payload.ReportID, Valid: true}
			var report models.Report
			if err := tx.Where("id = ?", payload.ReportID).First(&report).Error; err != nil {
				return err
			}
			warning.StatusIDs = reportStatusIDStrings(report.StatusIDs)
		}
		if err := tx.Create(&warning).Error; err != nil {
			return err
		}
		createdWarning = warning
		if err := logAdminAction(tx, user.AccountID, "create", accountWarningAuditLogTarget(warning), now); err != nil {
			return err
		}
		if err := s.logAdminAccountAction(tx, user.AccountID, &target, payload.Type, now); err != nil {
			return err
		}
		return s.resolveAdminAccountReports(tx, target.ID, payload.ReportID, user.AccountID, now, payload.Type)
	})
	if err != nil {
		return err
	}
	switch payload.Type {
	case "sensitive", "silence", "suspend":
		s.triggerAccountWebhook("account.updated", target.ID)
	}
	if payload.Type == "disable" {
		s.publishStreamingKillForLocalAccount(target)
	}
	if payload.Type == "suspend" {
		s.publishStreamingKillForLocalAccount(target)
		if err := s.enqueueAdminSuspensionOrRun(c.Request().Context(), s.db, target.ID); err != nil {
			return err
		}
	}
	if payload.SendEmailNotification && target.User.ID != 0 {
		target.User.Account = &target
		_ = s.sendAccountWarningMail(target.User, createdWarning)
	}
	return renderEmpty(c)
}

func (s *Server) enableAdminAccount(c *echo.Context) error {
	return s.updateLocalAdminAccountUser(c, map[string]any{"disabled": false}, "enable")
}

func (s *Server) approveAdminAccount(c *echo.Context) error {
	return s.updateLocalAdminAccountUser(c, map[string]any{"approved": true}, "approve")
}

func (s *Server) rejectAdminAccount(c *echo.Context) error {
	user, err := s.requireAdminWrite(c, "admin:write:accounts")
	if err != nil {
		return err
	}
	account, err := s.loadAdminAccount(c.Param("id"))
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	if !account.Local() || account.User.ID == 0 {
		return apiError(c, http.StatusForbidden, "This action is not allowed")
	}
	if !adminAccountRejectPermittedByRailsPolicy(account) {
		return apiError(c, http.StatusForbidden, "This action is not allowed")
	}
	if err := s.deleteRejectedLocalAccountRows(c.Request().Context(), user.AccountID, account, time.Now().UTC()); err != nil {
		return err
	}
	return renderEmpty(c)
}

func (s *Server) unsensitiveAdminAccount(c *echo.Context) error {
	return s.updateAdminAccount(c, map[string]any{"sensitized_at": nil}, "unsensitive")
}

func (s *Server) unsilenceAdminAccount(c *echo.Context) error {
	return s.updateAdminAccount(c, map[string]any{"silenced_at": nil}, "unsilence")
}

func (s *Server) unsuspendAdminAccount(c *echo.Context) error {
	if _, err := s.requireAdminWrite(c, "admin:write:accounts"); err != nil {
		return err
	}
	account, err := s.loadAdminAccount(c.Param("id"))
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	if !adminAccountUnsuspendPermittedByRailsPolicy(account) {
		return apiError(c, http.StatusForbidden, "This action is not allowed")
	}
	return s.updateAdminAccount(c, map[string]any{"suspended_at": nil, "suspension_origin": nil}, "unsuspend")
}

func (s *Server) adminReports(c *echo.Context) error {
	user, err := s.requireAdminRead(c, "admin:read:reports")
	if err != nil {
		return err
	}
	if s.db == nil {
		return c.JSON(http.StatusOK, []any{})
	}

	query := s.adminReportQuery(c)
	var reports []models.Report
	limitValue := limit(c, 100, 200)
	if err := applyIDPagination(c, query, "reports.id").
		Order("reports.id DESC").
		Limit(limitValue).
		Find(&reports).Error; err != nil {
		return err
	}
	if queryParamValuePresent(c, "min_id") {
		reverseRows(reports)
	}
	if len(reports) > 0 {
		c.Response().Header().Set("Link", paginationLinkWithAllowedParams(c, reports[0].ID, reports[len(reports)-1].ID, "min_id", len(reports) == limitValue, true, adminReportsPaginationParams))
	}

	out := make([]serializer.AdminReport, 0, len(reports))
	for _, report := range reports {
		statuses, err := s.reportStatuses(report)
		if err != nil {
			return err
		}
		item, err := s.adminReportFromModel(report, statuses, user)
		if err != nil {
			return err
		}
		out = append(out, item)
	}
	return c.JSON(http.StatusOK, out)
}

func (s *Server) showAdminReport(c *echo.Context) error {
	user, err := s.requireAdminRead(c, "admin:read:reports")
	if err != nil {
		return err
	}
	report, err := s.loadAdminReport(c.Param("id"))
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	return s.renderAdminReport(c, *report, user)
}

func (s *Server) updateAdminReport(c *echo.Context) error {
	user, err := s.requireAdminWrite(c, "admin:write:reports")
	if err != nil {
		return err
	}
	payload, err := parseAdminReportPayload(c)
	if err != nil {
		return apiError(c, http.StatusBadRequest, "The request body could not be parsed")
	}
	updates := map[string]any{"updated_at": time.Now().UTC()}
	var currentReport *models.Report
	if payload.Category != nil {
		category, ok := reportCategoryValueOK(*payload.Category)
		if !ok {
			return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Category is not included in the list")
		}
		updates["category"] = category
		if category != 2000 {
			updates["rule_ids"] = models.Int64Array(nil)
		}
	}
	if payload.RuleIDs != nil {
		ruleIDs := compactInt64Array(payload.RuleIDs)
		if len(ruleIDs) == 0 {
			ruleIDs = nil
		}
		effectiveCategory, ok := updates["category"].(int)
		if !ok {
			currentReport, err = s.loadAdminReport(c.Param("id"))
			if err != nil {
				return apiError(c, http.StatusNotFound, "Record not found")
			}
			effectiveCategory = currentReport.Category
		}
		if effectiveCategory != 2000 && len(ruleIDs) > 0 {
			return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Rule ids must be blank")
		}
		updates["rule_ids"] = ruleIDs
	}
	if len(updates) == 1 {
		if currentReport == nil {
			currentReport, err = s.loadAdminReport(c.Param("id"))
			if err != nil {
				return apiError(c, http.StatusNotFound, "Record not found")
			}
		}
		return s.renderAdminReport(c, *currentReport, user)
	}
	if err := s.db.Model(&models.Report{}).Where("id = ?", c.Param("id")).Updates(updates).Error; err != nil {
		return err
	}
	report, err := s.loadAdminReport(c.Param("id"))
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	s.triggerReportUpdatedWebhook(report.ID)
	return s.renderAdminReport(c, *report, user)
}

func (s *Server) assignAdminReportToSelf(c *echo.Context) error {
	user, err := s.requireAdminWrite(c, "admin:write:reports")
	if err != nil {
		return err
	}
	return s.updateAdminReportAndRender(c, map[string]any{"assigned_account_id": user.AccountID})
}

func (s *Server) unassignAdminReport(c *echo.Context) error {
	return s.updateAdminReportAndRender(c, map[string]any{"assigned_account_id": nil})
}

func (s *Server) reopenAdminReport(c *echo.Context) error {
	return s.updateAdminReportAndRender(c, map[string]any{"action_taken_at": nil, "action_taken_by_account_id": nil})
}

func (s *Server) resolveAdminReport(c *echo.Context) error {
	user, err := s.requireAdminWrite(c, "admin:write:reports")
	if err != nil {
		return err
	}
	return s.updateAdminReportAndRender(c, map[string]any{
		"action_taken_at":            time.Now().UTC(),
		"action_taken_by_account_id": user.AccountID,
	})
}

func (s *Server) adminTags(c *echo.Context) error {
	if _, err := s.requireAdminReadWithPermissions(c, nil, rolePermissionManageTaxonomies); err != nil {
		return err
	}
	if s.db == nil {
		return c.JSON(http.StatusOK, []any{})
	}

	var tags []models.Tag
	limitValue := limit(c, 100, 200)
	query := applyIDPagination(c, s.db.Model(&models.Tag{}), "tags.id").
		Order("tags.id DESC").
		Limit(limitValue)
	if err := query.Find(&tags).Error; err != nil {
		return err
	}
	if queryParamValuePresent(c, "min_id") {
		reverseRows(tags)
	}
	if len(tags) > 0 {
		c.Response().Header().Set("Link", paginationLinkWithAllowedParams(c, tags[0].ID, tags[len(tags)-1].ID, "min_id", len(tags) == limitValue, true, adminLimitPaginationParams))
	}
	out := make([]serializer.AdminTag, 0, len(tags))
	for _, tag := range tags {
		out = append(out, s.adminTagFromModel(c, tag))
	}
	return c.JSON(http.StatusOK, out)
}

func (s *Server) showAdminTag(c *echo.Context) error {
	if _, err := s.requireAdminReadWithPermissions(c, nil, rolePermissionManageTaxonomies); err != nil {
		return err
	}
	tag, err := s.findAdminTag(c.Param("id"))
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	return c.JSON(http.StatusOK, s.adminTagFromModel(c, *tag))
}

func (s *Server) updateAdminTag(c *echo.Context) error {
	if _, err := s.requireAdminWriteWithPermissions(c, []string{"admin:write"}, rolePermissionManageTaxonomies); err != nil {
		return err
	}
	tag, err := s.findAdminTag(c.Param("id"))
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}

	updates := map[string]any{
		"reviewed_at": time.Now().UTC(),
		"updated_at":  time.Now().UTC(),
	}
	payload, err := parseAdminTagPayload(c)
	if err != nil {
		return apiError(c, http.StatusBadRequest, "Malformed request")
	}
	if payload.DisplayName != nil {
		if err := validateAdminTagDisplayName(*payload.DisplayName, tag.Name); err != nil {
			return apiError(c, http.StatusUnprocessableEntity, err.Error())
		}
		updates["display_name"] = sql.NullString{String: *payload.DisplayName, Valid: *payload.DisplayName != ""}
		tag.DisplayName = sql.NullString{String: *payload.DisplayName, Valid: *payload.DisplayName != ""}
	}
	if payload.Trendable != nil {
		updates["trendable"] = sql.NullBool{Bool: *payload.Trendable, Valid: true}
		tag.Trendable = sql.NullBool{Bool: *payload.Trendable, Valid: true}
	}
	if payload.Usable != nil {
		updates["usable"] = sql.NullBool{Bool: *payload.Usable, Valid: true}
		tag.Usable = sql.NullBool{Bool: *payload.Usable, Valid: true}
	}
	if payload.Listable != nil {
		updates["listable"] = sql.NullBool{Bool: *payload.Listable, Valid: true}
		tag.Listable = sql.NullBool{Bool: *payload.Listable, Valid: true}
	}
	if err := s.db.Model(&models.Tag{}).Where("id = ?", tag.ID).Updates(updates).Error; err != nil {
		return err
	}
	s.meiliIndexTagsBestEffort(c.Request().Context(), []int64{tag.ID})
	tag.ReviewedAt = sql.NullTime{Time: updates["reviewed_at"].(time.Time), Valid: true}
	return c.JSON(http.StatusOK, s.adminTagFromModel(c, *tag))
}

type adminTagPayload struct {
	DisplayName *string
	Trendable   *bool
	Usable      *bool
	Listable    *bool
}

func parseAdminTagPayload(c *echo.Context) (adminTagPayload, error) {
	var payload adminTagPayload
	if requestIsJSON(c) {
		var raw map[string]any
		decoder := json.NewDecoder(c.Request().Body)
		decoder.UseNumber()
		if err := decoder.Decode(&raw); err != nil {
			return payload, err
		}
		if value, ok := raw["display_name"]; ok {
			displayName := rawString(value)
			payload.DisplayName = &displayName
		}
		if value, ok := raw["trendable"]; ok {
			trendable := railsBool(value, false)
			payload.Trendable = &trendable
		}
		if value, ok := raw["usable"]; ok {
			usable := railsBool(value, false)
			payload.Usable = &usable
		}
		if value, ok := raw["listable"]; ok {
			listable := railsBool(value, false)
			payload.Listable = &listable
		}
		return payload, nil
	}
	if value, ok := formField(c, "display_name"); ok {
		payload.DisplayName = &value
	}
	if value, ok := formBoolField(c, "trendable"); ok {
		payload.Trendable = &value
	}
	if value, ok := formBoolField(c, "usable"); ok {
		payload.Usable = &value
	}
	if value, ok := formBoolField(c, "listable"); ok {
		payload.Listable = &value
	}
	return payload, nil
}

func validateAdminTagDisplayName(displayName string, previousName string) error {
	if displayName == "" {
		return nil
	}
	if !railsValidTagName(displayName) {
		return errors.New("Validation failed: Display name is invalid")
	}
	normalized := railsNormalizeHashtagName(displayName)
	if normalized != strings.ToLower(previousName) {
		return errors.New("Validation failed: Display name does not match the previous name")
	}
	return nil
}

func (s *Server) findAdminTag(id string) (*models.Tag, error) {
	var tag models.Tag
	if err := s.db.Where("id = ?", id).First(&tag).Error; err != nil {
		return nil, err
	}
	return &tag, nil
}

type adminAccountActionPayload struct {
	Type                  string
	Text                  string
	ReportID              int64
	WarningPresetID       int64
	SendEmailNotification bool
}

type adminReportPayload struct {
	Category *string
	RuleIDs  []string
}

func parseAdminAccountActionPayload(c *echo.Context) (adminAccountActionPayload, error) {
	var payload adminAccountActionPayload
	if strings.Contains(strings.ToLower(c.Request().Header.Get("Content-Type")), "json") {
		var raw map[string]any
		decoder := json.NewDecoder(c.Request().Body)
		decoder.UseNumber()
		if err := decoder.Decode(&raw); err != nil {
			return payload, err
		}
		payload.Type = strings.TrimSpace(rawString(raw["type"]))
		payload.Text = rawString(raw["text"])
		payload.ReportID = rawInt64(raw["report_id"])
		payload.WarningPresetID = rawInt64(raw["warning_preset_id"])
		payload.SendEmailNotification = railsBool(raw["send_email_notification"], true)
		return payload, nil
	}

	values, err := c.FormValues()
	if err != nil {
		return payload, err
	}
	payload.Type = strings.TrimSpace(values.Get("type"))
	payload.Text = values.Get("text")
	payload.ReportID = parseOptionalInt64(values.Get("report_id"))
	payload.WarningPresetID = parseOptionalInt64(values.Get("warning_preset_id"))
	if values.Has("send_email_notification") {
		payload.SendEmailNotification = railsBool(values.Get("send_email_notification"), true)
	} else {
		payload.SendEmailNotification = true
	}
	return payload, nil
}

func railsBool(value any, defaultValue bool) bool {
	switch v := value.(type) {
	case nil:
		return defaultValue
	case bool:
		return v
	case string:
		text := strings.TrimSpace(v)
		if text == "" {
			return false
		}
		return truthy(text)
	case json.Number:
		n, err := v.Int64()
		if err != nil {
			return defaultValue
		}
		return n != 0
	case float64:
		return v != 0
	default:
		return defaultValue
	}
}

func adminAccountActionCode(actionType string) (int, bool) {
	switch actionType {
	case "none":
		return 0, true
	case "disable":
		return 1000, true
	case "sensitive":
		return 2000, true
	case "silence":
		return 3000, true
	case "suspend":
		return 4000, true
	default:
		return 0, false
	}
}

func (s *Server) adminAccountWarningText(tx *gorm.DB, payload adminAccountActionPayload) (string, error) {
	parts := make([]string, 0, 2)
	if payload.WarningPresetID > 0 {
		var preset models.AccountWarningPreset
		if err := tx.Where("id = ?", payload.WarningPresetID).First(&preset).Error; err != nil {
			return "", err
		}
		if strings.TrimSpace(preset.Text) != "" {
			parts = append(parts, preset.Text)
		}
	}
	if strings.TrimSpace(payload.Text) != "" {
		parts = append(parts, payload.Text)
	}
	return strings.Join(parts, "\n\n"), nil
}

func accountWarningText(text string) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	return text
}

func (s *Server) adminAccountActionPermittedByRailsPolicy(user *models.User, account *models.Account, actionType string) (bool, error) {
	switch actionType {
	case "none":
		return true, nil
	case "disable":
		if account == nil || account.User.ID == 0 || !account.Local() {
			return false, nil
		}
		return s.adminUserOverridesTarget(user, account.User)
	case "sensitive", "silence":
		return s.adminAccountRoleOverriddenByRailsPolicy(user, account)
	case "suspend":
		if account == nil || account.ID == -99 {
			return false, nil
		}
		return s.adminAccountRoleOverriddenByRailsPolicy(user, account)
	default:
		return false, nil
	}
}

func (s *Server) adminAccountRoleOverriddenByRailsPolicy(user *models.User, account *models.Account) (bool, error) {
	if account == nil {
		return false, nil
	}
	if account.User.ID != 0 {
		return s.adminUserOverridesTarget(user, account.User)
	}
	return s.adminUserOverridesTarget(user, models.User{})
}

func (s *Server) adminAccountReportResolutionPermittedByRailsPolicy(user *models.User, targetAccountID int64, reportID int64, actionType string) (bool, error) {
	if s.userCan(user, rolePermissionManageReports) {
		return true, nil
	}
	willResolve, err := s.adminAccountActionWillResolveReports(targetAccountID, reportID, actionType)
	if err != nil {
		return false, err
	}
	return !willResolve, nil
}

func (s *Server) adminAccountActionWillResolveReports(targetAccountID int64, reportID int64, actionType string) (bool, error) {
	if actionType == "none" {
		return reportID > 0, nil
	}
	if s == nil || s.db == nil || targetAccountID == 0 {
		return false, nil
	}
	var count int64
	err := s.db.Model(&models.Report{}).Where("target_account_id = ? AND action_taken_at IS NULL", targetAccountID).Count(&count).Error
	return count > 0, err
}

func adminAccountRejectPermittedByRailsPolicy(account *models.Account) bool {
	return account != nil && account.Local() && account.User.ID != 0 && !account.User.Approved
}

func adminAccountUnsuspendPermittedByRailsPolicy(account *models.Account) bool {
	return account != nil && account.SuspensionOrigin.Valid && account.SuspensionOrigin.Int64 == 0
}

func (s *Server) resolveAdminAccountReports(tx *gorm.DB, targetAccountID int64, reportID int64, actorAccountID int64, now time.Time, actionType string) error {
	updates := map[string]any{
		"action_taken_at":            now,
		"action_taken_by_account_id": actorAccountID,
		"updated_at":                 now,
	}
	query := tx.Model(&models.Report{}).Where("target_account_id = ? AND action_taken_at IS NULL", targetAccountID)
	if actionType == "none" {
		if reportID <= 0 {
			return nil
		}
		query = query.Where("id = ?", reportID)
	}
	return query.Updates(updates).Error
}

func (s *Server) updateAdminAccount(c *echo.Context, updates map[string]any, actionType string) error {
	user, err := s.requireAdminWrite(c, "admin:write:accounts")
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	updates["updated_at"] = now
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.Account{}).Where("id = ?", c.Param("id")).Updates(updates).Error; err != nil {
			return err
		}
		account, err := s.loadAdminAccount(c.Param("id"))
		if err != nil {
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
	account, err := s.loadAdminAccount(c.Param("id"))
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	if actionType == "unsuspend" {
		if err := s.enqueueAdminUnsuspensionOrRun(s.db, account.ID); err != nil {
			return err
		}
	}
	s.triggerAccountWebhook("account.updated", account.ID)
	out, err := s.adminAccountFromModel(*account)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, out)
}

func (s *Server) updateLocalAdminAccountUser(c *echo.Context, updates map[string]any, actionType string) error {
	user, err := s.requireAdminWrite(c, "admin:write:accounts")
	if err != nil {
		return err
	}
	account, err := s.loadAdminAccount(c.Param("id"))
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	if !account.Local() || account.User.ID == 0 {
		return apiError(c, http.StatusForbidden, "This action is not allowed")
	}
	wasApproved := account.User.Approved
	now := time.Now().UTC()
	updates["updated_at"] = now
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.User{}).Where("account_id = ?", account.ID).Updates(updates).Error; err != nil {
			return err
		}
		account, err := s.loadAdminAccount(c.Param("id"))
		if err != nil {
			return err
		}
		return s.logAdminAccountAction(tx, user.AccountID, account, actionType, now)
	}); err != nil {
		return err
	}
	account, err = s.loadAdminAccount(c.Param("id"))
	if err != nil {
		return err
	}
	if actionType == "approve" && !wasApproved && account.User.Approved && account.User.ConfirmedAt.Valid {
		if err := s.runApprovedAccountBootstrap(c.Request().Context(), account.ID, now); err != nil {
			return err
		}
		s.triggerAccountWebhook("account.approved", account.ID)
	}
	out, err := s.adminAccountFromModel(*account)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, out)
}

func (s *Server) adminAccountFromModel(account models.Account) (serializer.AdminAccount, error) {
	rows, err := s.adminAccountIPHistory(account.User.ID)
	if err != nil {
		return serializer.AdminAccount{}, err
	}
	role, everyone := s.adminAccountRole(account)
	inviteRequest, invitedByAccountID, err := s.adminAccountInviteFields(account)
	if err != nil {
		return serializer.AdminAccount{}, err
	}
	return serializer.AdminAccountFromModelWithOptions(s.cfg, account, serializer.AdminAccountOptions{
		IPs:                adminAccountIPsForSerializer(rows),
		Role:               role,
		EveryoneRole:       everyone,
		InviteRequest:      inviteRequest,
		InvitedByAccountID: invitedByAccountID,
	}), nil
}

func (s *Server) adminAccountRole(account models.Account) (*models.UserRole, *models.UserRole) {
	if account.User.ID == 0 {
		return nil, nil
	}
	everyone, _ := s.userRoleByID(-99)
	if account.User.RoleID.Valid {
		if account.User.Role.ID != 0 && account.User.Role.ID == account.User.RoleID.Int64 {
			return &account.User.Role, everyone
		}
		if role, err := s.userRoleByID(account.User.RoleID.Int64); err == nil {
			return role, everyone
		}
	}
	return everyone, everyone
}

func (s *Server) adminAccountInviteFields(account models.Account) (*string, *string, error) {
	if s.db == nil || account.User.ID == 0 {
		return nil, nil, nil
	}
	inviteRequest, err := s.adminAccountInviteRequest(account.User.ID)
	if err != nil {
		return nil, nil, err
	}
	invitedByAccountID, err := s.adminAccountInvitedByAccountID(account.User.InviteID)
	if err != nil {
		return nil, nil, err
	}
	return inviteRequest, invitedByAccountID, nil
}

func (s *Server) adminAccountInviteRequest(userID int64) (*string, error) {
	var request models.UserInviteRequest
	err := s.db.Where("user_id = ?", userID).First(&request).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !request.Text.Valid {
		return nil, nil
	}
	return &request.Text.String, nil
}

func (s *Server) adminAccountInvitedByAccountID(inviteID sql.NullInt64) (*string, error) {
	if !inviteID.Valid {
		return nil, nil
	}
	var invite models.Invite
	err := s.db.Preload("User").Where("id = ?", inviteID.Int64).First(&invite).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if invite.User.AccountID == 0 {
		return nil, nil
	}
	accountID := strconv.FormatInt(invite.User.AccountID, 10)
	return &accountID, nil
}

func adminAccountIPsForSerializer(rows []adminAccountIPHistoryRow) []serializer.AdminAccountIP {
	out := make([]serializer.AdminAccountIP, 0, len(rows))
	for _, row := range rows {
		var usedAt *string
		if row.UsedAt.Valid {
			formatted := row.UsedAt.Time.UTC().Format(time.RFC3339Nano)
			usedAt = &formatted
		}
		out = append(out, serializer.AdminAccountIP{
			IP:     row.IP,
			UsedAt: usedAt,
		})
	}
	return out
}

func (s *Server) logAdminAccountAction(tx *gorm.DB, actorAccountID int64, account *models.Account, actionType string, at time.Time) error {
	action, target, ok := adminAccountAuditAction(account, actionType)
	if !ok {
		return nil
	}
	return logAdminAction(tx, actorAccountID, action, target, at)
}

func adminAccountAuditAction(account *models.Account, actionType string) (string, adminAuditLogTarget, bool) {
	if account == nil {
		return "", adminAuditLogTarget{}, false
	}
	switch actionType {
	case "enable":
		if account.User.ID == 0 {
			return "", adminAuditLogTarget{}, false
		}
		return "enable", userAuditLogTarget(account.User), true
	case "approve":
		if account.User.ID == 0 {
			return "", adminAuditLogTarget{}, false
		}
		return "approve", userAuditLogTarget(account.User), true
	case "reject":
		if account.User.ID == 0 {
			return "", adminAuditLogTarget{}, false
		}
		return "reject", userAuditLogTarget(account.User), true
	case "disable":
		if account.User.ID == 0 {
			return "", adminAuditLogTarget{}, false
		}
		return "disable", userAuditLogTarget(account.User), true
	case "sensitive":
		return "sensitive", accountAuditLogTarget(*account), true
	case "silence":
		return "silence", accountAuditLogTarget(*account), true
	case "suspend":
		return "suspend", accountAuditLogTarget(*account), true
	case "unsensitive":
		return "unsensitive", accountAuditLogTarget(*account), true
	case "unsilence":
		return "unsilence", accountAuditLogTarget(*account), true
	case "unsuspend":
		return "unsuspend", accountAuditLogTarget(*account), true
	case "memorialize":
		return "memorialize", accountAuditLogTarget(*account), true
	case "remove_avatar":
		if account.User.ID == 0 {
			return "", adminAuditLogTarget{}, false
		}
		return "remove_avatar", userAuditLogTarget(account.User), true
	case "remove_header":
		if account.User.ID == 0 {
			return "", adminAuditLogTarget{}, false
		}
		return "remove_header", userAuditLogTarget(account.User), true
	case "unblock_email":
		return "unblock_email", accountAuditLogTarget(*account), true
	default:
		return "", adminAuditLogTarget{}, false
	}
}

func (s *Server) loadAdminAccount(id string) (*models.Account, error) {
	var account models.Account
	if err := s.db.Preload("User.Role").Preload("AccountStat").Where("accounts.id = ?", id).First(&account).Error; err != nil {
		return nil, err
	}
	return &account, nil
}

func reportStatusIDStrings(ids models.Int64Array) models.StringArray {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, strconv.FormatInt(id, 10))
	}
	return out
}

func rawString(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case json.Number:
		return v.String()
	case float64:
		return strconv.FormatInt(int64(v), 10)
	default:
		return ""
	}
}

func rawInt64(value any) int64 {
	return parseOptionalInt64(rawString(value))
}

func parseOptionalInt64(value string) int64 {
	id, _ := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	return id
}

func parseAdminReportPayload(c *echo.Context) (adminReportPayload, error) {
	var payload adminReportPayload
	if strings.Contains(strings.ToLower(c.Request().Header.Get("Content-Type")), "json") {
		var raw map[string]any
		decoder := json.NewDecoder(c.Request().Body)
		decoder.UseNumber()
		if err := decoder.Decode(&raw); err != nil {
			return payload, err
		}
		if value, ok := raw["category"]; ok {
			category := rawString(value)
			payload.Category = &category
		}
		if value, ok := raw["rule_ids"]; ok {
			payload.RuleIDs = rawStringSlice(value)
		}
		return payload, nil
	}

	values, err := c.FormValues()
	if err != nil {
		return payload, err
	}
	if values.Has("category") {
		category := values.Get("category")
		payload.Category = &category
	}
	if values.Has("report[category]") {
		category := values.Get("report[category]")
		payload.Category = &category
	}
	payload.RuleIDs = append(payload.RuleIDs, values["rule_ids[]"]...)
	payload.RuleIDs = append(payload.RuleIDs, values["report[rule_ids][]"]...)
	return payload, nil
}

func rawStringSlice(value any) []string {
	switch v := value.(type) {
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if text := rawString(item); text != "" {
				out = append(out, text)
			}
		}
		return out
	case []string:
		return v
	default:
		return nil
	}
}

func (s *Server) adminAccountQuery(c *echo.Context) *gorm.DB {
	query := s.db.Model(&models.Account{}).
		Select("accounts.*").
		Joins("LEFT JOIN users ON users.account_id = accounts.id").
		Preload("User.Role").
		Preload("AccountStat")

	origin := adminAccountOriginFilter(c)
	switch origin {
	case "remote":
		query = query.Where("accounts.domain IS NOT NULL")
	case "local":
		query = query.Where("accounts.domain IS NULL")
	case "":
	default:
		query.AddError(adminAccountInvalidFilterError("origin", origin))
		return query
	}
	if domain := strings.TrimSpace(c.QueryParam("by_domain")); domain != "" {
		query = query.Where("accounts.domain = ?", domain)
	}
	if username := strings.TrimSpace(c.QueryParam("username")); username != "" {
		query = query.Where("lower(accounts.username) LIKE lower(?)", strings.TrimPrefix(username, "@")+"%")
	}
	if displayName := strings.TrimSpace(c.QueryParam("display_name")); displayName != "" {
		query = query.Where("accounts.display_name ILIKE ?", displayName+"%")
	}
	if email := strings.TrimSpace(c.QueryParam("email")); email != "" {
		query = query.Where("lower(users.email) LIKE lower(?)", strings.ToLower(email)+"%")
	}
	if ip := strings.TrimSpace(c.QueryParam("ip")); ip != "" {
		if adminAccountValidIPFilter(ip) {
			query = query.Joins("LEFT JOIN user_ips ON user_ips.user_id = users.id").
				Where("user_ips.ip <<= ?", ip).
				Group("accounts.id")
		} else {
			query = query.Where("1 = 0")
		}
	}
	if invitedBy := strings.TrimSpace(c.QueryParam("invited_by")); invitedBy != "" {
		query = query.Joins("LEFT JOIN invites admin_account_invites ON admin_account_invites.id = users.invite_id").
			Where("admin_account_invites.user_id = ?", invitedBy)
	}

	status := adminAccountStatusFilter(c)
	switch status {
	case "pending":
		query = query.Where("users.id IS NOT NULL AND users.approved = false")
	case "disabled":
		query = query.Where("users.id IS NOT NULL AND users.disabled = true").
			Where("accounts.suspended_at IS NULL")
	case "silenced":
		query = query.Where("accounts.silenced_at IS NOT NULL")
	case "suspended":
		query = query.Where("accounts.suspended_at IS NOT NULL")
	case "sensitized":
		query = query.Where("accounts.sensitized_at IS NOT NULL")
	case "active":
		query = query.Where("accounts.suspended_at IS NULL")
	case "":
	default:
		query.AddError(adminAccountInvalidFilterError("status", status))
		return query
	}

	if adminAccountStaffFilter(c) {
		roleIDs, err := s.roleIDsWithPermission(s.db, rolePermissionManageReports)
		if err != nil {
			query.AddError(err)
			return query
		}
		query = adminAccountRoleScope(query, adminAccountRoleIDStrings(roleIDs))
	} else if roleIDs := adminAccountRoleIDParams(c); len(roleIDs) > 0 {
		query = adminAccountRoleScope(query, roleIDs)
	}
	return query
}

func adminAccountOriginFilter(c *echo.Context) string {
	if queryParamPresent(c, "remote") {
		return "remote"
	}
	if queryParamPresent(c, "local") {
		return "local"
	}
	if origin := strings.TrimSpace(c.QueryParam("origin")); origin != "" {
		return origin
	}
	if strings.Contains(c.Request().URL.Path, "/api/v1/admin/accounts") {
		return "local"
	}
	return ""
}

func adminAccountStatusFilter(c *echo.Context) string {
	if status := strings.TrimSpace(c.QueryParam("status")); status != "" {
		return status
	}
	for _, status := range []string{"active", "pending", "disabled", "silenced", "suspended"} {
		if queryParamPresent(c, status) {
			return status
		}
	}
	if strings.Contains(c.Request().URL.Path, "/api/v1/admin/accounts") {
		return "active"
	}
	return ""
}

func applyAdminAccountOrder(c *echo.Context, query *gorm.DB) (*gorm.DB, error) {
	order := adminAccountOrderFilter(c)
	switch order {
	case "active":
		return query.Order("COALESCE(users.current_sign_in_at, account_stats.last_status_at, to_timestamp(0)) DESC, accounts.id DESC"), nil
	case "recent", "":
		return query.Order("accounts.id DESC"), nil
	default:
		return query, adminAccountInvalidFilterError("order", order)
	}
}

func adminAccountOrderFilter(c *echo.Context) string {
	return strings.TrimSpace(c.QueryParam("order"))
}

func adminAccountStaffFilter(c *echo.Context) bool {
	return queryParamPresent(c, "staff") || strings.TrimSpace(c.QueryParam("permissions")) == "staff"
}

func adminAccountValidIPFilter(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	if _, err := netip.ParseAddr(value); err == nil {
		return true
	}
	_, err := netip.ParsePrefix(value)
	return err == nil
}

func adminAccountInvalidFilterError(name string, value string) error {
	return apiHTTPError{status: http.StatusBadRequest, message: "Unknown " + name + ": " + value}
}

func adminAccountRoleIDParams(c *echo.Context) []string {
	values := append([]string{}, c.QueryParams()["role_ids[]"]...)
	out := make([]string, 0, len(values))
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func adminAccountRoleIDStrings(ids []int64) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, strconv.FormatInt(id, 10))
	}
	return out
}

func adminAccountRoleScope(query *gorm.DB, roleIDs []string) *gorm.DB {
	if len(roleIDs) == 0 {
		return query.Where("1 = 0")
	}
	return query.Where("users.role_id IN ?", roleIDs)
}

func queryParamPresent(c *echo.Context, key string) bool {
	_, ok := c.QueryParams()[key]
	return ok
}

func (s *Server) adminReportQuery(c *echo.Context) *gorm.DB {
	query := s.adminReportBaseQuery()
	if queryParamPresent(c, "resolved") {
		query = query.Where("reports.action_taken_at IS NOT NULL")
	} else {
		query = query.Where("reports.action_taken_at IS NULL")
	}
	if queryParamPresent(c, "account_id") {
		query = query.Where("reports.account_id = ?", c.QueryParam("account_id"))
	}
	if queryParamPresent(c, "target_account_id") {
		query = query.Where("reports.target_account_id = ?", c.QueryParam("target_account_id"))
	}
	return query
}

func (s *Server) adminReportBaseQuery() *gorm.DB {
	return s.db.Model(&models.Report{}).
		Preload("Account.User.Role").
		Preload("Account.AccountStat").
		Preload("TargetAccount.User.Role").
		Preload("TargetAccount.AccountStat").
		Preload("AssignedAccount.User.Role").
		Preload("AssignedAccount.AccountStat").
		Preload("ActionTakenByAccount.User.Role").
		Preload("ActionTakenByAccount.AccountStat")
}

func (s *Server) loadAdminReport(id string) (*models.Report, error) {
	var report models.Report
	if err := s.adminReportBaseQuery().Where("reports.id = ?", id).First(&report).Error; err != nil {
		return nil, err
	}
	return &report, nil
}

func (s *Server) updateAdminReportAndRender(c *echo.Context, updates map[string]any) error {
	user, err := s.requireAdminWrite(c, "admin:write:reports")
	if err != nil {
		return err
	}
	updates["updated_at"] = time.Now().UTC()
	if err := s.db.Model(&models.Report{}).Where("id = ?", c.Param("id")).Updates(updates).Error; err != nil {
		return err
	}
	report, err := s.loadAdminReport(c.Param("id"))
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	s.triggerReportUpdatedWebhook(report.ID)
	return s.renderAdminReport(c, *report, user)
}

func (s *Server) renderAdminReport(c *echo.Context, report models.Report, user *models.User) error {
	statuses, err := s.reportStatuses(report)
	if err != nil {
		return err
	}
	out, err := s.adminReportFromModel(report, statuses, user)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, out)
}

func (s *Server) adminReportFromModel(report models.Report, statuses []models.Status, user *models.User) (serializer.AdminReport, error) {
	account, err := s.adminAccountFromModel(report.Account)
	if err != nil {
		return serializer.AdminReport{}, err
	}
	targetAccount, err := s.adminAccountFromModel(report.TargetAccount)
	if err != nil {
		return serializer.AdminReport{}, err
	}
	var assignedAccount *serializer.AdminAccount
	if report.AssignedAccount.ID != 0 {
		out, err := s.adminAccountFromModel(report.AssignedAccount)
		if err != nil {
			return serializer.AdminReport{}, err
		}
		assignedAccount = &out
	}
	var actionTakenByAccount *serializer.AdminAccount
	if report.ActionTakenByAccount.ID != 0 {
		out, err := s.adminAccountFromModel(report.ActionTakenByAccount)
		if err != nil {
			return serializer.AdminReport{}, err
		}
		actionTakenByAccount = &out
	}
	rules, err := s.reportRules(report)
	if err != nil {
		return serializer.AdminReport{}, err
	}
	var currentAccount *models.Account
	if user != nil && user.AccountID != 0 {
		currentAccount = &models.Account{ID: user.AccountID}
	}
	return serializer.AdminReportFromModelWithAdminAccountsAndCurrent(s.cfg, report, statuses, account, targetAccount, assignedAccount, actionTakenByAccount, rules, currentAccount), nil
}

func (s *Server) reportStatuses(report models.Report) ([]models.Status, error) {
	if len(report.StatusIDs) == 0 {
		return []models.Status{}, nil
	}
	var statuses []models.Status
	err := s.statusQuery().Where("statuses.id IN ?", []int64(report.StatusIDs)).Find(&statuses).Error
	return statuses, err
}

func (s *Server) reportRules(report models.Report) ([]models.Rule, error) {
	if len(report.RuleIDs) == 0 {
		return []models.Rule{}, nil
	}
	var rules []models.Rule
	if err := s.db.Where("id IN ?", []int64(report.RuleIDs)).Find(&rules).Error; err != nil {
		return nil, err
	}
	byID := make(map[int64]models.Rule, len(rules))
	for _, rule := range rules {
		byID[rule.ID] = rule
	}
	out := make([]models.Rule, 0, len(rules))
	for _, id := range report.RuleIDs {
		if rule, ok := byID[int64(id)]; ok {
			out = append(out, rule)
		}
	}
	return out, nil
}

func (s *Server) requireAdminRead(c *echo.Context, scopes ...string) (*models.User, error) {
	return s.requireAdminReadWithPermissions(c, scopes, adminRolePermissionsForScopes(scopes, false)...)
}

func (s *Server) requireAdminReadToken(c *echo.Context, scopes ...string) (*models.User, error) {
	c.Response().Header().Set("Vary", "Authorization")
	if bearerToken(c) == "" && browserRequestHasAuthenticationCookie(c.Request()) {
		user, _, err := s.currentUser(c)
		if err != nil {
			return nil, apiError(c, http.StatusUnauthorized, "The access token is invalid")
		}
		return user, nil
	}
	accessToken, err := s.accessTokenFromRequest(c)
	if err != nil || !accessToken.ResourceOwnerID.Valid {
		return nil, apiError(c, http.StatusUnauthorized, "The access token is invalid")
	}
	if !tokenHasAnyScope(accessToken.Scopes, append(scopes, "admin:read")...) {
		return nil, apiError(c, http.StatusForbidden, "This action is outside the authorized scopes")
	}
	var user models.User
	if err := s.db.Where("id = ? AND disabled = false", accessToken.ResourceOwnerID.Int64).First(&user).Error; err != nil {
		return nil, apiError(c, http.StatusUnauthorized, "The access token is invalid")
	}
	return &user, nil
}

func (s *Server) requireAdminReadWithPermissions(c *echo.Context, scopes []string, permissions ...int64) (*models.User, error) {
	c.Response().Header().Set("Vary", "Authorization")
	if bearerToken(c) == "" && browserRequestHasAuthenticationCookie(c.Request()) {
		return s.requireAdminBrowserReadWithPermissions(c, permissions...)
	}
	accessToken, err := s.accessTokenFromRequest(c)
	if err != nil || !accessToken.ResourceOwnerID.Valid {
		return nil, apiError(c, http.StatusUnauthorized, "The access token is invalid")
	}
	if !tokenHasAnyScope(accessToken.Scopes, append(scopes, "admin:read")...) {
		return nil, apiError(c, http.StatusForbidden, "This action is outside the authorized scopes")
	}
	var user models.User
	if err := s.db.Where("id = ? AND disabled = false", accessToken.ResourceOwnerID.Int64).First(&user).Error; err != nil {
		return nil, apiError(c, http.StatusUnauthorized, "The access token is invalid")
	}
	if !s.userCanAny(&user, permissions...) {
		return nil, apiError(c, http.StatusForbidden, "This action is outside the authorized role")
	}
	return &user, nil
}

func (s *Server) requireAdminBrowserReadWithPermissions(c *echo.Context, permissions ...int64) (*models.User, error) {
	// Rails admin React components use the signed browser session and CSRF header, not an admin OAuth scope.
	// Bearer-token clients still follow the normal admin scope checks above.
	user, _, err := s.currentUser(c)
	if err != nil {
		return nil, apiError(c, http.StatusUnauthorized, "The access token is invalid")
	}
	if c.Request().Method != http.MethodGet && c.Request().Method != http.MethodHead && c.Request().Method != http.MethodOptions {
		state, err := s.browserSession(c, false)
		if err != nil || !browserCSRFTokenValid(c, state.CSRFToken) {
			return nil, apiError(c, http.StatusUnprocessableEntity, railsCSRFErrorMessage)
		}
	}
	if !s.userCanAny(user, permissions...) {
		return nil, apiError(c, http.StatusForbidden, "This action is outside the authorized role")
	}
	return user, nil
}

func tokenHasAnyScope[S ~string](tokenScopes S, required ...string) bool {
	available := map[string]struct{}{}
	for _, scope := range strings.Fields(string(tokenScopes)) {
		available[scope] = struct{}{}
	}
	for _, scope := range required {
		if _, ok := available[scope]; ok {
			return true
		}
	}
	return false
}

func applyIDPagination(c *echo.Context, query *gorm.DB, column string) *gorm.DB {
	if minID := c.QueryParam("min_id"); queryParamValuePresent(c, "min_id") {
		query = query.Where(column+" > ?", minID).Order(column + " ASC")
		if maxID := c.QueryParam("max_id"); queryParamValuePresent(c, "max_id") {
			query = query.Where(column+" < ?", maxID)
		}
		return query
	}
	if maxID := c.QueryParam("max_id"); queryParamValuePresent(c, "max_id") {
		query = query.Where(column+" < ?", maxID)
	}
	if sinceID := c.QueryParam("since_id"); queryParamValuePresent(c, "since_id") {
		query = query.Where(column+" > ?", sinceID)
	}
	return query
}

func reverseRows[T any](rows []T) {
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
}
