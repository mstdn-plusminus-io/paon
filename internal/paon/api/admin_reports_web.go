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
	"github.com/mstdn-plusminus-io/paon/internal/paon/serializer"
	"gorm.io/gorm"
)

func (s *Server) adminReportsPage(c *echo.Context) error {
	user, handled, err := s.requireAdminReportsWebUser(c)
	if handled || err != nil {
		return err
	}
	reports, err := s.adminReportModels(c)
	if err != nil {
		return err
	}
	return c.HTML(http.StatusOK, adminReportsHTMLWithConfig(s.cfg, reports, c.QueryParam("notice"), c.QueryParam("error"), adminReportFilterValues(c), s.webLocale(c, user)))
}

func (s *Server) adminReportPage(c *echo.Context) error {
	user, handled, err := s.requireAdminReportsWebUser(c)
	if handled || err != nil {
		return err
	}
	report, err := s.loadAdminReport(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "report not found")
	}
	notes, err := s.adminReportNotes(report.ID)
	if err != nil {
		return err
	}
	statuses, err := s.reportStatuses(*report)
	if err != nil {
		return err
	}
	rules, err := s.instanceRuleModels()
	if err != nil {
		return err
	}
	return c.HTML(http.StatusOK, adminReportHTMLWithConfig(s.cfg, *report, notes, statuses, rules, c.QueryParam("notice"), c.QueryParam("error"), s.webLocale(c, user)))
}

func (s *Server) updateAdminReportReasonWeb(c *echo.Context) error {
	user, handled, err := s.requireAdminReportsWebUser(c)
	if handled || err != nil {
		return err
	}
	loc := s.webLocale(c, user)
	payload, err := parseAdminReportPayload(c)
	if err != nil {
		return c.Redirect(http.StatusFound, "/admin/reports/"+url.PathEscape(c.Param("id"))+"?error="+url.QueryEscape(adminT(loc, "admin.reports.invalid_update", "Report update is invalid")))
	}
	updates := map[string]any{}
	if payload.Category != nil {
		category, ok := reportCategoryValueOK(*payload.Category)
		if !ok {
			return c.Redirect(http.StatusFound, "/admin/reports/"+url.PathEscape(c.Param("id"))+"?error="+url.QueryEscape(adminT(loc, "admin.reports.invalid_category", "Report category is invalid")))
		}
		updates["category"] = category
		if category != 2000 {
			updates["rule_ids"] = models.Int64Array(nil)
		} else if payload.RuleIDs == nil {
			updates["rule_ids"] = models.Int64Array(nil)
		}
	}
	if payload.RuleIDs != nil {
		ruleIDs := compactInt64Array(payload.RuleIDs)
		if len(ruleIDs) == 0 {
			ruleIDs = nil
		}
		category, ok := updates["category"].(int)
		if !ok {
			report, err := s.loadAdminReport(c.Param("id"))
			if err != nil {
				return echo.NewHTTPError(http.StatusNotFound, "report not found")
			}
			category = report.Category
		}
		if category == 2000 {
			updates["rule_ids"] = ruleIDs
		} else {
			updates["rule_ids"] = models.Int64Array(nil)
		}
	}
	if len(updates) == 0 {
		return c.Redirect(http.StatusFound, "/admin/reports/"+url.PathEscape(c.Param("id")))
	}
	return s.updateAdminReportWeb(c, 0, updates, adminT(loc, "admin.reports.reason_updated_msg", "Report reason updated"), "")
}

func (s *Server) assignAdminReportToSelfWeb(c *echo.Context) error {
	user, handled, err := s.requireAdminReportsWebUser(c)
	if handled || err != nil {
		return err
	}
	return s.updateAdminReportWeb(c, user.AccountID, map[string]any{"assigned_account_id": user.AccountID}, adminT(s.webLocale(c, user), "admin.reports.assigned_msg", "Report assigned"), "assigned_to_self")
}

func (s *Server) unassignAdminReportWeb(c *echo.Context) error {
	user, handled, err := s.requireAdminReportsWebUser(c)
	if handled || err != nil {
		return err
	}
	return s.updateAdminReportWeb(c, user.AccountID, map[string]any{"assigned_account_id": nil}, adminT(s.webLocale(c, user), "admin.reports.unassigned_msg", "Report unassigned"), "unassigned")
}

func (s *Server) reopenAdminReportWeb(c *echo.Context) error {
	user, handled, err := s.requireAdminReportsWebUser(c)
	if handled || err != nil {
		return err
	}
	return s.updateAdminReportWeb(c, user.AccountID, map[string]any{"action_taken_at": nil, "action_taken_by_account_id": nil}, adminT(s.webLocale(c, user), "admin.reports.reopened_msg", "Report reopened"), "reopen")
}

func (s *Server) resolveAdminReportWeb(c *echo.Context) error {
	user, handled, err := s.requireAdminReportsWebUser(c)
	if handled || err != nil {
		return err
	}
	return s.updateAdminReportWeb(c, user.AccountID, map[string]any{
		"action_taken_at":            time.Now().UTC(),
		"action_taken_by_account_id": user.AccountID,
	}, adminT(s.webLocale(c, user), "admin.reports.resolved_msg", "Report resolved"), "resolve")
}

func (s *Server) createAdminReportNoteWeb(c *echo.Context) error {
	user, handled, err := s.requireAdminReportsWebUser(c)
	if handled || err != nil {
		return err
	}
	loc := s.webLocale(c, user)
	reportID, content, err := adminReportNoteParams(c)
	if errors.Is(err, errAdminReportNoteParamsMissing) {
		return apiError(c, http.StatusBadRequest, "Malformed request")
	}
	if err != nil || reportID <= 0 {
		return c.Redirect(http.StatusFound, "/admin/reports?error="+url.QueryEscape(adminT(loc, "admin.reports.notes.invalid", "Report note is invalid")))
	}
	if strings.TrimSpace(content) == "" || len([]rune(content)) > 500 {
		return c.Redirect(http.StatusFound, "/admin/reports/"+strconv.FormatInt(reportID, 10)+"?error="+url.QueryEscape(adminT(loc, "admin.reports.notes.invalid_content", "Report note content is invalid")))
	}
	if s.db == nil {
		return c.Redirect(http.StatusFound, "/admin/reports/"+strconv.FormatInt(reportID, 10)+"?error="+url.QueryEscape(adminReportMessage(loc, "errors.database_unavailable", "DATABASE_URL is not set")))
	}
	report, err := s.loadAdminReport(strconv.FormatInt(reportID, 10))
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "report not found")
	}
	now := time.Now().UTC()
	note := models.ReportNote{ReportID: reportID, AccountID: user.AccountID, Content: content, CreatedAt: now, UpdatedAt: now}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&note).Error; err != nil {
			return err
		}
		if adminBatchFormParamExists(c, "create_and_resolve") {
			if err := tx.Model(&models.Report{}).Where("id = ?", reportID).Updates(map[string]any{
				"action_taken_at":            now,
				"action_taken_by_account_id": user.AccountID,
				"updated_at":                 now,
			}).Error; err != nil {
				return err
			}
			return logAdminAction(tx, user.AccountID, "resolve", reportAuditLogTarget(*report), now)
		}
		if adminBatchFormParamExists(c, "create_and_unresolve") {
			if err := tx.Model(&models.Report{}).Where("id = ?", reportID).Updates(map[string]any{
				"action_taken_at":            nil,
				"action_taken_by_account_id": nil,
				"updated_at":                 now,
			}).Error; err != nil {
				return err
			}
			return logAdminAction(tx, user.AccountID, "reopen", reportAuditLogTarget(*report), now)
		}
		return tx.Model(&models.Report{}).Where("id = ?", reportID).Update("updated_at", now).Error
	})
	if err != nil {
		return err
	}
	s.triggerReportUpdatedWebhook(reportID)
	if adminBatchFormParamExists(c, "create_and_resolve") {
		return c.Redirect(http.StatusFound, "/admin/reports?notice="+url.QueryEscape(adminT(loc, "admin.report_notes.created_msg", "Report note successfully created!")))
	}
	return c.Redirect(http.StatusFound, "/admin/reports/"+strconv.FormatInt(reportID, 10)+"?notice="+url.QueryEscape(adminT(loc, "admin.report_notes.created_msg", "Report note successfully created!")))
}

func (s *Server) destroyAdminReportNoteWeb(c *echo.Context) error {
	user, handled, err := s.requireAdminReportsWebUser(c)
	if handled || err != nil {
		return err
	}
	if c.Request().Method == http.MethodPost && !methodOverrideIs(c, "delete") {
		return c.Redirect(http.StatusFound, "/admin/reports")
	}
	note, err := s.findAdminReportNote(c.Param("id"))
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Delete(&models.ReportNote{}, note.ID).Error; err != nil {
			return err
		}
		return tx.Model(&models.Report{}).Where("id = ?", note.ReportID).Update("updated_at", now).Error
	}); err != nil {
		return err
	}
	s.triggerReportUpdatedWebhook(note.ReportID)
	return c.Redirect(http.StatusFound, "/admin/reports/"+strconv.FormatInt(note.ReportID, 10)+"?notice="+url.QueryEscape(adminT(s.webLocale(c, user), "admin.report_notes.destroyed_msg", "Report note successfully deleted!")))
}

var errAdminReportNoteParamsMissing = errors.New("admin report note root parameter is missing")

func adminReportNoteParams(c *echo.Context) (int64, string, error) {
	req := c.Request()
	if err := req.ParseForm(); err != nil {
		return 0, "", err
	}
	const prefix = "report_note"
	if !formHasNestedPrefix(req.Form, prefix) {
		return 0, "", errAdminReportNoteParamsMissing
	}
	reportID, err := strconv.ParseInt(strings.TrimSpace(lastFormValue(req.Form, prefix+"[report_id]")), 10, 64)
	return reportID, lastFormValue(req.Form, prefix+"[content]"), err
}

func (s *Server) previewAdminReportActionWeb(c *echo.Context) error {
	user, handled, err := s.requireAdminReportsWebUser(c)
	if handled || err != nil {
		return err
	}
	report, err := s.loadAdminReport(c.Param("report_id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "report not found")
	}
	action := adminReportActionFromRequest(c)
	if action == "" {
		return c.Redirect(http.StatusFound, "/admin/reports/"+strconv.FormatInt(report.ID, 10)+"?error="+url.QueryEscape(adminReportUnknownActionMessage(s.webLocale(c, user), c)))
	}
	statuses, err := s.reportStatuses(*report)
	if err != nil {
		return err
	}
	return c.HTML(http.StatusOK, adminReportActionPreviewHTML(*report, statuses, action, c.FormValue("text"), s.webLocale(c, user)))
}

func (s *Server) createAdminReportActionWeb(c *echo.Context) error {
	user, handled, err := s.requireAdminReportsWebUser(c)
	if handled || err != nil {
		return err
	}
	report, err := s.loadAdminReport(c.Param("report_id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "report not found")
	}
	action := adminReportActionFromRequest(c)
	if action == "" {
		return c.Redirect(http.StatusFound, "/admin/reports/"+strconv.FormatInt(report.ID, 10)+"?error="+url.QueryEscape(adminReportUnknownActionMessage(s.webLocale(c, user), c)))
	}
	now := time.Now().UTC()
	var deletedStatusIDs []int64
	var sensitiveStatusIDs []int64
	var createdWarning models.AccountWarning
	var suspendedAccount *models.Account
	err = s.db.Transaction(func(tx *gorm.DB) error {
		switch action {
		case "mark_as_sensitive":
			if len(report.StatusIDs) > 0 {
				if err := adminStatusIDsWithMediaOrPreview(tx, []int64(report.StatusIDs), &sensitiveStatusIDs); err != nil {
					return err
				}
				var statuses []models.Status
				if err := tx.Where("id IN ?", sensitiveStatusIDs).Find(&statuses).Error; err != nil {
					return err
				}
				if len(sensitiveStatusIDs) > 0 {
					if err := tx.Model(&models.Status{}).Where("id IN ? AND deleted_at IS NULL", sensitiveStatusIDs).Updates(map[string]any{"sensitive": true, "updated_at": now}).Error; err != nil {
						return err
					}
				}
				for _, status := range statuses {
					if err := logAdminAction(tx, user.AccountID, "update", statusAuditLogTarget(status), now); err != nil {
						return err
					}
				}
				warning, err := createAdminStatusBatchWarning(tx, user.AccountID, report.TargetAccountID, report.ID, "mark_statuses_as_sensitive", []int64(report.StatusIDs), c.FormValue("text"), now)
				if err != nil {
					return err
				}
				createdWarning = warning
				if len(sensitiveStatusIDs) > 0 {
					if err := resolveAdminReportTx(tx, report, user.AccountID, now); err != nil {
						return err
					}
				}
			}
		case "delete":
			if len(report.StatusIDs) > 0 {
				var statuses []models.Status
				if err := tx.Where("id IN ?", []int64(report.StatusIDs)).Find(&statuses).Error; err != nil {
					return err
				}
				if err := discardAdminStatusBatchWithReblogs(tx, []int64(report.StatusIDs), now); err != nil {
					return err
				}
				for _, status := range statuses {
					deletedStatusIDs = append(deletedStatusIDs, status.ID)
					if err := logAdminAction(tx, user.AccountID, "destroy", statusAuditLogTarget(status), now); err != nil {
						return err
					}
				}
				warning, err := createAdminStatusBatchWarning(tx, user.AccountID, report.TargetAccountID, report.ID, "delete_statuses", []int64(report.StatusIDs), c.FormValue("text"), now)
				if err != nil {
					return err
				}
				createdWarning = warning
				if err := resolveAdminReportTx(tx, report, user.AccountID, now); err != nil {
					return err
				}
			}
		case "silence":
			var account models.Account
			if err := tx.Where("id = ?", report.TargetAccountID).First(&account).Error; err != nil {
				return err
			}
			if err := tx.Model(&models.Account{}).Where("id = ?", report.TargetAccountID).Updates(map[string]any{"silenced_at": sql.NullTime{Time: now, Valid: true}, "updated_at": now}).Error; err != nil {
				return err
			}
			if err := logAdminAction(tx, user.AccountID, "silence", accountAuditLogTarget(account), now); err != nil {
				return err
			}
			warning, err := createAdminAccountActionReportWarning(tx, user.AccountID, report, action, c.FormValue("text"), now)
			if err != nil {
				return err
			}
			createdWarning = warning
			if err := s.resolveAdminAccountReports(tx, report.TargetAccountID, report.ID, user.AccountID, now, action); err != nil {
				return err
			}
		case "suspend":
			var account models.Account
			if err := tx.Where("id = ?", report.TargetAccountID).First(&account).Error; err != nil {
				return err
			}
			suspendedAccount = &account
			if err := tx.Model(&models.Account{}).Where("id = ?", report.TargetAccountID).Updates(map[string]any{"suspended_at": sql.NullTime{Time: now, Valid: true}, "suspension_origin": int64(0), "updated_at": now}).Error; err != nil {
				return err
			}
			if err := createCanonicalEmailBlockForAccountTx(tx, account, now); err != nil {
				return err
			}
			if err := logAdminAction(tx, user.AccountID, "suspend", accountAuditLogTarget(account), now); err != nil {
				return err
			}
			warning, err := createAdminAccountActionReportWarning(tx, user.AccountID, report, action, c.FormValue("text"), now)
			if err != nil {
				return err
			}
			createdWarning = warning
			if err := s.resolveAdminAccountReports(tx, report.TargetAccountID, report.ID, user.AccountID, now, action); err != nil {
				return err
			}
		default:
			return nil
		}
		return nil
	})
	if err != nil {
		return err
	}
	if action == "delete" {
		payload := s.adminStatusBatchRemovalPayload(report.TargetAccountID)
		if !s.enqueueRemovalTasksForStatusIDs(deletedStatusIDs, payload) {
			s.applyAdminDeletedStatusSideEffects(context.Background(), s.db, deletedStatusIDs)
		}
	}
	if action == "mark_as_sensitive" {
		s.fanOutAdminStatusBatchUpdates(context.Background(), sensitiveStatusIDs)
	}
	if createdWarning.ID != 0 && createdWarning.TargetAccountID.Valid && createdWarning.TargetAccountID.Int64 != 0 {
		s.publishModerationWarningNotification(createdWarning.ID)
		sendMail := report.Category != reportCategoryValue("spam")
		_ = s.sendAdminStatusBatchWarningMail(createdWarning.TargetAccountID.Int64, createdWarning, sendMail)
	}
	if action == "suspend" {
		if suspendedAccount != nil {
			s.publishStreamingKillForLocalAccount(*suspendedAccount)
		}
		if err := s.enqueueAdminSuspensionOrRun(context.Background(), s.db, report.TargetAccountID); err != nil {
			return err
		}
	}
	if action == "silence" || action == "suspend" {
		s.triggerAccountWebhook("account.updated", report.TargetAccountID)
	}
	s.triggerReportUpdatedWebhook(report.ID)
	return c.Redirect(http.StatusFound, "/admin/reports?notice="+url.QueryEscape(adminReportProcessedMessage(s.webLocale(c, user), report.ID)))
}

func resolveAdminReportTx(tx *gorm.DB, report *models.Report, actorAccountID int64, now time.Time) error {
	if report == nil || report.ID == 0 {
		return nil
	}
	if err := tx.Model(&models.Report{}).Where("id = ?", report.ID).Updates(map[string]any{
		"action_taken_at":            now,
		"action_taken_by_account_id": actorAccountID,
		"updated_at":                 now,
	}).Error; err != nil {
		return err
	}
	return logAdminAction(tx, actorAccountID, "resolve", reportAuditLogTarget(*report), now)
}

func (s *Server) updateAdminReportWeb(c *echo.Context, actorAccountID int64, updates map[string]any, notice string, action string) error {
	if s.db == nil {
		return c.Redirect(http.StatusFound, "/admin/reports/"+url.PathEscape(c.Param("id"))+"?error="+url.QueryEscape(adminReportMessage(s.webLocale(c, nil), "errors.database_unavailable", "DATABASE_URL is not set")))
	}
	now := time.Now().UTC()
	updates["updated_at"] = now
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		var report models.Report
		if err := tx.Where("id = ?", c.Param("id")).First(&report).Error; err != nil {
			return err
		}
		if err := tx.Model(&models.Report{}).Where("id = ?", report.ID).Updates(updates).Error; err != nil {
			return err
		}
		if action == "" {
			return nil
		}
		return logAdminAction(tx, actorAccountID, action, reportAuditLogTarget(report), now)
	}); err != nil {
		return err
	}
	reportID, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	if reportID != 0 {
		s.triggerReportUpdatedWebhook(reportID)
	}
	if action == "resolve" {
		return c.Redirect(http.StatusFound, "/admin/reports?notice="+url.QueryEscape(notice))
	}
	return c.Redirect(http.StatusFound, "/admin/reports/"+url.PathEscape(c.Param("id"))+"?notice="+url.QueryEscape(notice))
}

func adminStatusIDsWithMediaOrPreview(tx *gorm.DB, ids []int64, out *[]int64) error {
	if len(ids) == 0 {
		return nil
	}
	return tx.Model(&models.Status{}).
		Where("id IN ? AND deleted_at IS NULL", ids).
		Where("(EXISTS (SELECT 1 FROM media_attachments WHERE media_attachments.status_id = statuses.id) OR EXISTS (SELECT 1 FROM preview_cards_statuses WHERE preview_cards_statuses.status_id = statuses.id))").
		Pluck("id", out).Error
}

func (s *Server) fanOutAdminStatusBatchUpdates(ctx context.Context, statusIDs []int64) {
	if s == nil || s.db == nil || len(statusIDs) == 0 {
		return
	}
	var statuses []models.Status
	if err := s.statusQuery().Where("statuses.id IN ? AND statuses.deleted_at IS NULL", statusIDs).Find(&statuses).Error; err != nil {
		return
	}
	for _, status := range statuses {
		s.invalidateStatusCache(ctx, status.ID)
		s.meiliIndexStatusBestEffort(ctx, status.ID)
		_ = s.fanOutStatusUpdateToLocalRecipients(ctx, s.db, status)
		s.publishStatusUpdateEventWithContext(ctx, s.db, "status.update", status)
		_ = s.enqueueOrDeliverStatusUpdateDistribution(status)
		s.triggerStatusWebhook("status.updated", status.ID)
	}
}

func (s *Server) requireAdminReportsWebUser(c *echo.Context) (*models.User, bool, error) {
	user, _, handled, err := s.requireFunctionalWebUser(c)
	if handled || err != nil {
		return nil, handled, err
	}
	if !s.userCan(user, rolePermissionManageReports) {
		locale := s.webLocale(c, user)
		return nil, true, c.HTML(http.StatusForbidden, authPageHTML(adminT(locale, "admin.reports.title", "Reports"), "", adminT(locale, "admin.reports.not_permitted", "You are not allowed to manage reports."), "", locale))
	}
	return user, false, nil
}

func (s *Server) adminReportModels(c *echo.Context) ([]models.Report, error) {
	if s.db == nil {
		return []models.Report{}, nil
	}
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
	if queryParamPresent(c, "by_target_domain") {
		query = query.Joins("JOIN accounts report_target_accounts ON report_target_accounts.id = reports.target_account_id").
			Where("report_target_accounts.domain = ?", c.QueryParam("by_target_domain"))
	}
	if queryParamPresent(c, "target_origin") {
		switch c.QueryParam("target_origin") {
		case "local":
			query = query.Joins("JOIN accounts report_origin_accounts ON report_origin_accounts.id = reports.target_account_id").
				Where("report_origin_accounts.domain IS NULL OR report_origin_accounts.domain = ''")
		case "remote":
			query = query.Joins("JOIN accounts report_origin_accounts ON report_origin_accounts.id = reports.target_account_id").
				Where("report_origin_accounts.domain IS NOT NULL AND report_origin_accounts.domain <> ''")
		default:
			return nil, echo.NewHTTPError(http.StatusBadRequest, "unknown target origin")
		}
	}
	var reports []models.Report
	err := query.Order("reports.id DESC").Offset(adminRailsPageOffset(c)).Limit(adminRailsDefaultPageSize).Find(&reports).Error
	return reports, err
}

func (s *Server) adminReportNotes(reportID int64) ([]models.ReportNote, error) {
	if s.db == nil {
		return []models.ReportNote{}, nil
	}
	var notes []models.ReportNote
	err := s.db.Preload("Account").Where("report_id = ?", reportID).Order("id DESC").Find(&notes).Error
	return notes, err
}

func (s *Server) findAdminReportNote(rawID string) (models.ReportNote, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(rawID), 10, 64)
	if err != nil || id <= 0 {
		return models.ReportNote{}, echo.NewHTTPError(http.StatusNotFound, "report note not found")
	}
	var note models.ReportNote
	if s.db == nil {
		return note, echo.NewHTTPError(http.StatusNotFound, "report note not found")
	}
	if err := s.db.First(&note, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return note, echo.NewHTTPError(http.StatusNotFound, "report note not found")
		}
		return note, err
	}
	return note, nil
}

func adminReportActionFromRequest(c *echo.Context) string {
	for _, action := range []string{"delete", "mark_as_sensitive", "silence", "suspend"} {
		if adminBatchFormParamExists(c, action) {
			return action
		}
	}
	action := c.FormValue("moderation_action")
	switch action {
	case "delete", "mark_as_sensitive", "silence", "suspend":
		return action
	default:
		return ""
	}
}

func createAdminAccountActionReportWarning(tx *gorm.DB, actorAccountID int64, report *models.Report, action string, text string, now time.Time) (models.AccountWarning, error) {
	if tx == nil || report == nil || report.TargetAccountID == 0 {
		return models.AccountWarning{}, nil
	}
	code, ok := adminAccountActionCode(action)
	if !ok {
		return models.AccountWarning{}, nil
	}
	warning := models.AccountWarning{
		AccountID:       models.AccountWarningAccountID(actorAccountID),
		TargetAccountID: models.AccountWarningTargetAccountID(report.TargetAccountID),
		ReportID:        sql.NullInt64{Int64: report.ID, Valid: report.ID > 0},
		Action:          code,
		Text:            accountWarningText(text),
		StatusIDs:       accountWarningStatusIDStrings([]int64(report.StatusIDs)),
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := tx.Create(&warning).Error; err != nil {
		return models.AccountWarning{}, err
	}
	if err := createModerationWarningNotification(tx, warning, now); err != nil {
		return models.AccountWarning{}, err
	}
	return warning, nil
}

func adminReportUnknownActionMessage(locale string, c *echo.Context) string {
	action := c.FormValue("moderation_action")
	if action == "" {
		for _, key := range []string{"delete", "mark_as_sensitive", "silence", "suspend"} {
			if adminBatchFormParamExists(c, key) {
				action = key
				break
			}
		}
	}
	return adminTVars(locale, "admin.reports.unknown_action_msg", "Unknown action: %{action}", map[string]string{"action": action})
}

func adminReportProcessedMessage(locale string, reportID int64) string {
	return adminTVars(locale, "admin.reports.processed_msg", "Report #%{id} successfully processed", map[string]string{"id": strconv.FormatInt(reportID, 10)})
}

func adminReportMessage(locale, key, fallback string) string {
	return adminT(locale, "admin.reports."+key, fallback)
}

type adminReportFilters struct {
	Page            string
	Resolved        string
	AccountID       string
	TargetAccountID string
	ByTargetDomain  string
	TargetOrigin    string
}

func adminReportFilterValues(c *echo.Context) adminReportFilters {
	return adminReportFilters{
		Page:            adminTrendsPageValue(c),
		Resolved:        c.QueryParam("resolved"),
		AccountID:       c.QueryParam("account_id"),
		TargetAccountID: c.QueryParam("target_account_id"),
		ByTargetDomain:  c.QueryParam("by_target_domain"),
		TargetOrigin:    c.QueryParam("target_origin"),
	}
}

func adminReportFilterHiddenFields(filters adminReportFilters) string {
	values := map[string]string{
		"page":              firstNonEmpty(filters.Page, "1"),
		"resolved":          filters.Resolved,
		"account_id":        filters.AccountID,
		"target_account_id": filters.TargetAccountID,
		"target_origin":     filters.TargetOrigin,
	}
	var body strings.Builder
	for _, key := range []string{"page", "resolved", "account_id", "target_account_id", "target_origin"} {
		if value := strings.TrimSpace(values[key]); value != "" {
			body.WriteString(`<input type="hidden" name="` + key + `" value="` + html.EscapeString(value) + `">`)
		}
	}
	return body.String()
}

func adminReportsHTML(reports []models.Report, notice string, errorText string, filters adminReportFilters, locale ...string) string {
	return adminReportsHTMLWithConfig(config.Config{}, reports, notice, errorText, filters, locale...)
}

func adminReportsHTMLWithConfig(cfg config.Config, reports []models.Report, notice string, errorText string, filters adminReportFilters, locale ...string) string {
	loc := settingsLocaleArgOrEnglish(locale...)
	var body strings.Builder
	body.WriteString(`<div class="filters">`)
	body.WriteString(relationshipFilterSubsetHTML(adminT(loc, "admin.reports.status", "Status"), []relationshipFilterLink{
		{Label: adminT(loc, "admin.reports.unresolved", "Unresolved"), Href: adminReportFilterHref(filters, "resolved", ""), Active: filters.Resolved == ""},
		{Label: adminT(loc, "admin.reports.resolved", "Resolved"), Href: adminReportFilterHref(filters, "resolved", "1"), Active: filters.Resolved == "1"},
	}))
	body.WriteString(relationshipFilterSubsetHTML(adminT(loc, "admin.reports.target_origin", "Target origin"), []relationshipFilterLink{
		{Label: adminT(loc, "admin.accounts.location.all", "All"), Href: adminReportFilterHref(filters, "target_origin", ""), Active: filters.TargetOrigin == ""},
		{Label: adminT(loc, "admin.accounts.location.local", "Local"), Href: adminReportFilterHref(filters, "target_origin", "local"), Active: filters.TargetOrigin == "local"},
		{Label: adminT(loc, "admin.accounts.location.remote", "Remote"), Href: adminReportFilterHref(filters, "target_origin", "remote"), Active: filters.TargetOrigin == "remote"},
	}))
	body.WriteString(`</div><form method="get" action="/admin/reports" class="simple_form">` + adminReportFilterHiddenFields(filters) + `<div class="fields-group"><div class="input string optional"><input class="string optional" type="text" name="by_target_domain" id="by_target_domain" value="` + html.EscapeString(filters.ByTargetDomain) + `" placeholder="` + html.EscapeString(adminT(loc, "admin.reports.by_target_domain", "Target domain")) + `"></div><div class="actions"><button class="button" type="submit">` + html.EscapeString(adminT(loc, "admin.accounts.search", "Search")) + `</button> <a class="button negative" href="/admin/reports">` + html.EscapeString(adminT(loc, "admin.accounts.reset", "Reset")) + `</a></div></div></form>`)
	for _, group := range adminReportGroups(reports) {
		body.WriteString(adminReportGroupHTML(cfg, group, loc))
	}
	body.WriteString(adminRailsPaginationHTML(loc, "/admin/reports", filters.Page, adminReportFiltersQuery(filters), len(reports)))
	return authPageHTML(adminT(loc, "admin.reports.title", "Reports"), notice, errorText, body.String(), loc)
}

func adminReportFilterHref(filters adminReportFilters, key string, value string) string {
	values := adminReportFiltersQuery(filters)
	values.Del("page")
	if value == "" {
		values.Del(key)
	} else {
		values.Set(key, value)
	}
	if query := values.Encode(); query != "" {
		return "/admin/reports?" + query
	}
	return "/admin/reports"
}

func adminReportFiltersQuery(filters adminReportFilters) url.Values {
	values := url.Values{}
	for key, value := range map[string]string{
		"resolved": filters.Resolved, "account_id": filters.AccountID, "target_account_id": filters.TargetAccountID,
		"by_target_domain": filters.ByTargetDomain, "target_origin": filters.TargetOrigin,
	} {
		if strings.TrimSpace(value) != "" {
			values.Set(key, value)
		}
	}
	return values
}

func adminReportGroups(reports []models.Report) [][]models.Report {
	groups := make([][]models.Report, 0)
	indexes := map[int64]int{}
	for _, report := range reports {
		index, ok := indexes[report.TargetAccountID]
		if !ok {
			index = len(groups)
			indexes[report.TargetAccountID] = index
			groups = append(groups, nil)
		}
		groups[index] = append(groups[index], report)
	}
	return groups
}

func adminReportGroupHTML(cfg config.Config, reports []models.Report, locale string) string {
	if len(reports) == 0 {
		return ""
	}
	target := reports[0].TargetAccount
	var items strings.Builder
	for _, report := range reports {
		reporter := adminReportAccountLabel(report.Account)
		if report.Account.Domain.Valid && strings.TrimSpace(report.Account.Domain.String) != "" {
			reporter = report.Account.Domain.String
		}
		comment := firstNonEmpty(report.Comment, adminT(locale, "admin.reports.comment.none", "None"))
		assigned := "-"
		if report.AssignedAccountID.Valid {
			assigned = `<a href="/admin/accounts/` + strconv.FormatInt(report.AssignedAccount.ID, 10) + `">` + html.EscapeString(adminReportAccountLabel(report.AssignedAccount)) + `</a>`
		}
		forwarded := ""
		if report.Forwarded.Valid && report.Forwarded.Bool && target.Domain.Valid {
			forwarded = ` &middot; ` + html.EscapeString(adminTVars(locale, "admin.reports.forwarded_to", "Forwarded to %{domain}", map[string]string{"domain": target.Domain.String}))
		}
		items.WriteString(`<div class="report-card__summary__item"><div class="report-card__summary__item__reported-by">` + html.EscapeString(reporter) + `</div><div class="report-card__summary__item__content"><a href="/admin/reports/` + strconv.FormatInt(report.ID, 10) + `"><div class="one-line">` + html.EscapeString(comment) + `</div><span class="report-card__summary__item__content__icon" title="` + html.EscapeString(adminT(locale, "admin.accounts.statuses", "Posts")) + `"><i class="fa fa-comment"></i> ` + strconv.Itoa(len(report.StatusIDs)) + `</span>` + forwarded + `</a></div><div class="report-card__summary__item__assigned">` + assigned + `</div></div>`)
	}
	stateClass := "neutral"
	state := adminT(locale, "admin.accounts.no_limits_imposed", "No limits imposed")
	if target.SuspendedAt.Valid {
		stateClass, state = "red", adminT(locale, "admin.accounts.suspended", "Suspended")
	} else if target.SilencedAt.Valid {
		stateClass, state = "red", adminT(locale, "admin.accounts.silenced", "Limited")
	} else if target.User.ID != 0 && target.User.Disabled {
		stateClass, state = "red", adminT(locale, "admin.accounts.disabled", "Disabled")
	}
	return `<div class="report-card"><div class="report-card__profile">` + adminAccountLinkHTML(cfg, target) + `<div class="report-card__profile__stats"><span class="` + stateClass + `">` + html.EscapeString(state) + `</span></div></div><div class="report-card__summary">` + items.String() + `</div></div>`
}

func adminReportSummaryHTML(report models.Report, locale ...string) string {
	return adminReportGroupHTML(config.Config{}, []models.Report{report}, settingsLocaleArgOrEnglish(locale...))
}

func adminReportHTML(report models.Report, notes []models.ReportNote, statuses []models.Status, rules []models.Rule, notice string, errorText string, locale ...string) string {
	return adminReportHTMLWithConfig(config.Config{}, report, notes, statuses, rules, notice, errorText, locale...)
}

func adminReportHTMLWithConfig(cfg config.Config, report models.Report, notes []models.ReportNote, statuses []models.Status, rules []models.Rule, notice string, errorText string, locale ...string) string {
	loc := settingsLocaleArgOrEnglish(locale...)
	var body strings.Builder
	reportID := strconv.FormatInt(report.ID, 10)
	body.WriteString(`<div class="content__heading__actions">`)
	if report.ActionTakenAt.Valid {
		body.WriteString(`<a class="button" href="/admin/reports/` + reportID + `/reopen" data-method="post">` + html.EscapeString(adminT(loc, "admin.reports.mark_as_unresolved", "Mark as unresolved")) + `</a>`)
	} else {
		body.WriteString(`<a class="button" href="/admin/reports/` + reportID + `/resolve" data-method="post">` + html.EscapeString(adminT(loc, "admin.reports.mark_as_resolved", "Mark as resolved")) + `</a>`)
	}
	body.WriteString(`</div><div class="report-header"><div class="report-header__card">` + adminReportTargetAccountCardHTML(cfg, report.TargetAccount, loc) + adminReportTargetAccountDetailsHTML(report.TargetAccount, loc) + `</div><div class="report-header__details">`)
	body.WriteString(adminReportDetailItemHTML(adminT(loc, "admin.reports.created_at", "Reported"), adminAccountFormattedTime(report.CreatedAt)))
	body.WriteString(adminReportDetailItemHTML(adminT(loc, "admin.reports.reported_by", "Reported by"), adminReportReporterHTML(report.Account)))
	state := adminT(loc, "admin.reports.unresolved", "Unresolved")
	if report.ActionTakenAt.Valid {
		state = adminT(loc, "admin.reports.resolved", "Resolved")
	}
	body.WriteString(adminReportDetailItemHTML(adminT(loc, "admin.reports.status", "Status"), html.EscapeString(state)))
	if report.TargetAccount.Domain.Valid {
		forwarded := adminT(loc, "simple_form.no", "No")
		if report.Forwarded.Valid && report.Forwarded.Bool {
			forwarded = adminT(loc, "simple_form.yes", "Yes")
		}
		body.WriteString(adminReportDetailItemHTML(adminT(loc, "admin.reports.forwarded", "Forwarded"), html.EscapeString(forwarded)))
	}
	assigned := html.EscapeString(adminT(loc, "admin.reports.no_one_assigned", "No one assigned"))
	if report.AssignedAccountID.Valid {
		assigned = `<a href="/admin/accounts/` + strconv.FormatInt(report.AssignedAccount.ID, 10) + `">` + html.EscapeString(adminReportAccountLabel(report.AssignedAccount)) + `</a> &mdash; ` + adminAccountTableLink("trash", adminT(loc, "admin.reports.unassign", "Unassign"), "/admin/reports/"+reportID+"/unassign", "post")
	} else {
		assigned += ` &mdash; ` + adminAccountTableLink("user", adminT(loc, "admin.reports.assign_to_self", "Assign to self"), "/admin/reports/"+reportID+"/assign_to_self", "post")
	}
	body.WriteString(adminReportDetailItemHTML(adminT(loc, "admin.reports.assigned", "Assigned"), assigned) + `</div></div><hr class="spacer">`)
	body.WriteString(`<h3>` + html.EscapeString(adminT(loc, "admin.reports.category", "Category")) + `</h3><p>` + adminT(loc, "admin.reports.category_description_html", "Select the category that best describes this report.") + `</p>`)
	body.WriteString(adminReportReasonHTML(report, rules, loc))
	if report.Comment != "" {
		body.WriteString(`<p>` + adminT(loc, "admin.reports.comment_description_html", "The reporter supplied this comment.") + `</p><div class="report-notes">` + adminReportCommentHTML(cfg, report, loc) + `</div>`)
	}
	body.WriteString(`<hr class="spacer"><h3>` + html.EscapeString(adminT(loc, "admin.reports.statuses", "Reported posts")) + `<small class="section-skip-link"><a href="#actions"><i class="fa fa-angle-double-down"></i> ` + html.EscapeString(adminT(loc, "admin.reports.skip_to_actions", "Skip to actions")) + `</a></small></h3><p>` + adminT(loc, "admin.reports.statuses_description_html", "Posts attached to this report.") + ` &mdash; <a class="table-action-link" href="/admin/accounts/` + strconv.FormatInt(report.TargetAccountID, 10) + `/statuses?report_id=` + reportID + `"><i class="fa fa-plus"></i> ` + html.EscapeString(adminT(loc, "admin.reports.add_to_report", "Add posts")) + `</a></p>`)
	body.WriteString(`<form method="post" action="/admin/accounts/` + strconv.FormatInt(report.TargetAccountID, 10) + `/statuses/batch" class="new_admin_status_batch_action"><input type="hidden" name="report_id" value="` + reportID + `"><div class="batch-table"><div class="batch-table__toolbar"><label class="batch-table__toolbar__select batch-checkbox-all"><input type="checkbox" name="batch_checkbox_all"></label><div class="batch-table__toolbar__actions">`)
	if len(statuses) > 0 && !report.ActionTakenAt.Valid {
		body.WriteString(`<button class="table-action-link" type="submit" name="remove_from_report" value="1"><i class="fa fa-times"></i> ` + html.EscapeString(adminT(loc, "admin.statuses.batch.remove_from_report", "Remove from report")) + `</button>`)
	}
	body.WriteString(`</div></div><div class="batch-table__body">`)
	if len(statuses) == 0 {
		body.WriteString(adminNothingHereHTML(loc, "nothing-here--under-tabs"))
	} else {
		for _, status := range statuses {
			body.WriteString(adminAccountStatusRowHTML(loc, report.TargetAccountID, status))
		}
	}
	body.WriteString(`</div></div></form>`)
	if !report.ActionTakenAt.Valid {
		body.WriteString(`<hr class="spacer"><p id="actions">` + adminT(loc, "admin.reports.actions_description_html", "Choose a moderation action for this report.") + `</p>` + adminReportActionsHTML(report, statuses, loc))
	}
	body.WriteString(`<hr class="spacer"><h3>` + html.EscapeString(adminT(loc, "admin.reports.notes.title", "Notes")) + `</h3><p>` + adminT(loc, "admin.reports.notes_description_html", "Private moderation notes for this report.") + `</p><div class="report-notes">`)
	for _, note := range notes {
		body.WriteString(adminReportNoteHTML(cfg, note, loc))
	}
	body.WriteString(`</div><form method="post" action="/admin/report_notes" class="simple_form new_report_note"><input type="hidden" name="report_note[report_id]" value="` + reportID + `"><div class="field-group"><div class="input text optional report_note_content"><div class="label_input"><textarea class="text optional" name="report_note[content]" rows="6" placeholder="` + html.EscapeString(adminT(loc, "admin.reports.notes.placeholder", "Leave a note")) + `"></textarea></div></div></div><div class="actions">`)
	if !report.ActionTakenAt.Valid {
		body.WriteString(`<button class="button" type="submit" name="create_and_resolve" value="1">` + html.EscapeString(adminT(loc, "admin.reports.notes.create_and_resolve", "Create note and resolve")) + `</button>`)
	} else {
		body.WriteString(`<button class="button" type="submit" name="create_and_unresolve" value="1">` + html.EscapeString(adminT(loc, "admin.reports.notes.create_and_unresolve", "Create note and reopen")) + `</button>`)
	}
	body.WriteString(`<button class="button" type="submit">` + html.EscapeString(adminT(loc, "admin.reports.notes.create", "Create note")) + `</button></div></form>`)
	return authPageHTML(adminTVars(loc, "admin.reports.report", "Report #%{id}", map[string]string{"id": strconv.FormatInt(report.ID, 10)}), notice, errorText, body.String(), loc)
}

func adminReportTargetAccountCardHTML(cfg config.Config, account models.Account, locale string) string {
	view := serializer.AccountFromModel(cfg, account)
	avatar := firstNonEmpty(view.Avatar, view.AvatarStatic, "/avatars/original/missing.png")
	header := firstNonEmpty(view.Header, view.HeaderStatic, "/headers/original/missing.png")
	displayName := statusEmbedAccountNameHTMLWithConfig(cfg, account, account.CustomEmojis)
	if strings.TrimSpace(displayName) == "" {
		displayName = html.EscapeString(account.Username)
	}
	bio := ""
	if strings.TrimSpace(account.Note) != "" {
		bio = `<div class="account-card__bio emojify">` + sanitizeRemoteNoteContent(account.Note) + `</div>`
	}
	return `<div class="account-card"><div class="account-card__header"><img src="` + html.EscapeString(header) + `" alt=""></div><div class="account-card__title"><div class="account-card__title__avatar"><img src="` + html.EscapeString(avatar) + `" alt=""></div><div class="display-name"><bdi><strong class="emojify p-name">` + displayName + `</strong></bdi><span>@` + html.EscapeString(account.Acct()) + `</span></div></div>` + bio + `<div class="account-card__actions"><div class="account-card__counters"><div class="account-card__counters__item">` + adminInstanceCountString(account.AccountStat.StatusesCount) + `<small>` + html.EscapeString(strings.ToLower(adminT(locale, "accounts.posts.other", "posts"))) + `</small></div><div class="account-card__counters__item">` + adminInstanceCountString(account.AccountStat.FollowersCount) + `<small>` + html.EscapeString(strings.ToLower(adminT(locale, "accounts.followers.other", "followers"))) + `</small></div><div class="account-card__counters__item">` + adminInstanceCountString(account.AccountStat.FollowingCount) + `<small>` + html.EscapeString(strings.ToLower(adminT(locale, "accounts.following.other", "following"))) + `</small></div></div><div class="account-card__actions__button"><a class="button" href="/admin/accounts/` + strconv.FormatInt(account.ID, 10) + `">` + html.EscapeString(adminT(locale, "admin.reports.view_profile", "View profile")) + `</a></div></div></div>`
}

func adminReportTargetAccountDetailsHTML(account models.Account, locale string) string {
	lastActive := ""
	if account.AccountStat.LastStatusAt.Valid {
		lastActive = adminAccountFormattedTime(account.AccountStat.LastStatusAt.Time)
	}
	return `<div class="report-header__details report-header__details--horizontal">` + adminReportDetailItemHTML(adminT(locale, "admin.accounts.joined", "Joined"), adminAccountFormattedTime(account.CreatedAt)) + adminReportDetailItemHTML(adminT(locale, "accounts.last_active", "Last active"), lastActive) + `</div>`
}

func adminReportDetailItemHTML(label string, contentHTML string) string {
	return `<div class="report-header__details__item"><div class="report-header__details__item__header"><strong>` + html.EscapeString(label) + `</strong></div><div class="report-header__details__item__content">` + contentHTML + `</div></div>`
}

func adminReportReporterHTML(account models.Account) string {
	if account.ID == 0 {
		return ""
	}
	if account.Domain.Valid && strings.TrimSpace(account.Domain.String) != "" {
		return `<a href="/admin/instances/` + url.PathEscape(account.Domain.String) + `">` + html.EscapeString(account.Domain.String) + `</a>`
	}
	return `<a href="/admin/accounts/` + strconv.FormatInt(account.ID, 10) + `">` + html.EscapeString(adminReportAccountLabel(account)) + `</a>`
}

func adminReportCommentHTML(cfg config.Config, report models.Report, locale string) string {
	view := serializer.AccountFromModel(cfg, report.Account)
	avatar := firstNonEmpty(view.Avatar, view.AvatarStatic, "/avatars/original/missing.png")
	return `<div class="report-notes__item"><img class="report-notes__item__avatar" src="` + html.EscapeString(avatar) + `" alt=""><div class="report-notes__item__header"><span class="username">` + adminReportReporterHTML(report.Account) + `</span><time class="relative-formatted" datetime="` + html.EscapeString(report.CreatedAt.UTC().Format(time.RFC3339)) + `">` + html.EscapeString(report.CreatedAt.UTC().Format("2006-01-02")) + `</time></div><div class="report-notes__item__content">` + strings.ReplaceAll(html.EscapeString(report.Comment), "\n", "<br>") + `</div></div>`
}

func adminReportNoteHTML(cfg config.Config, note models.ReportNote, locale string) string {
	view := serializer.AccountFromModel(cfg, note.Account)
	avatar := firstNonEmpty(view.Avatar, view.AvatarStatic, "/avatars/original/missing.png")
	created := note.CreatedAt.UTC().Format(time.RFC3339)
	return `<div class="report-notes__item"><img class="report-notes__item__avatar" src="` + html.EscapeString(avatar) + `" alt=""><div class="report-notes__item__header"><span class="username"><a href="/admin/accounts/` + strconv.FormatInt(note.AccountID, 10) + `">` + html.EscapeString(adminReportAccountLabel(note.Account)) + `</a></span><time class="relative-formatted" datetime="` + html.EscapeString(created) + `">` + html.EscapeString(note.CreatedAt.UTC().Format("2006-01-02")) + `</time></div><div class="report-notes__item__content">` + strings.ReplaceAll(html.EscapeString(note.Content), "\n", "<br>") + `</div><div class="report-notes__item__actions">` + adminAccountTableLink("trash", adminT(locale, "admin.reports.notes.delete", "Delete note"), "/admin/report_notes/"+strconv.FormatInt(note.ID, 10), "delete") + `</div></div>`
}

func adminReportReasonHTML(report models.Report, rules []models.Rule, locale ...string) string {
	_ = locale
	_ = rules
	ruleIDs := make([]string, 0, len(report.RuleIDs))
	for _, id := range report.RuleIDs {
		ruleIDs = append(ruleIDs, strconv.FormatInt(id, 10))
	}
	return adminDashboardReactComponent("ReportReasonSelector", map[string]any{
		"id":       strconv.FormatInt(report.ID, 10),
		"category": adminReportCategoryKey(report.Category),
		"rule_ids": ruleIDs,
		"disabled": report.ActionTakenAt.Valid,
	})
}

func int64ArrayContains(values models.Int64Array, want int64) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func adminReportActionsHTML(report models.Report, statuses []models.Status, locale ...string) string {
	loc := settingsLocaleArgOrEnglish(locale...)
	action := "/admin/reports/" + strconv.FormatInt(report.ID, 10) + "/actions/preview"
	var body strings.Builder
	body.WriteString(`<form method="post" action="` + action + `" class="simple_form"><div class="report-actions">`)
	body.WriteString(adminReportActionItemHTML(`<a class="button" href="/admin/reports/`+strconv.FormatInt(report.ID, 10)+`/resolve" data-method="post">`+html.EscapeString(adminT(loc, "admin.reports.mark_as_resolved", "Mark as resolved"))+`</a>`, adminT(loc, "admin.reports.actions.resolve_description_html", "Resolve this report without taking another action.")))
	hasMedia := false
	for _, status := range statuses {
		if !status.DeletedAt.Valid && (len(snapshotMediaAttachments(status)) > 0 || len(status.PreviewCards) > 0) {
			hasMedia = true
			break
		}
	}
	if hasMedia {
		body.WriteString(adminReportActionItemHTML(`<button class="button" type="submit" name="mark_as_sensitive" value="1">`+html.EscapeString(adminReportActionLabel("mark_as_sensitive", loc))+`</button>`, adminT(loc, "admin.reports.actions.mark_as_sensitive_description_html", "Mark media in the reported posts as sensitive.")))
	}
	body.WriteString(adminReportActionItemHTML(`<button class="button button--destructive" type="submit" name="delete" value="1">`+html.EscapeString(adminT(loc, "admin.reports.delete_and_resolve", "Delete posts and resolve"))+`</button>`, adminT(loc, "admin.reports.actions.delete_description_html", "Delete the reported posts and resolve this report.")))
	body.WriteString(adminReportActionItemHTML(`<button class="button button--destructive" type="submit" name="silence" value="1">`+html.EscapeString(adminT(loc, "admin.accounts.silence", "Limit"))+`</button>`, adminT(loc, "admin.reports.actions.silence_description_html", "Limit the reported account.")))
	body.WriteString(adminReportActionItemHTML(`<button class="button button--destructive" type="submit" name="suspend" value="1">`+html.EscapeString(adminT(loc, "admin.accounts.suspend", "Suspend"))+`</button>`, adminT(loc, "admin.reports.actions.suspend_description_html", "Suspend the reported account.")))
	body.WriteString(adminReportActionItemHTML(`<a class="button" href="/admin/accounts/`+strconv.FormatInt(report.TargetAccountID, 10)+`/action/new?report_id=`+strconv.FormatInt(report.ID, 10)+`">`+html.EscapeString(adminT(loc, "admin.accounts.custom", "Custom"))+`</a>`, adminT(loc, "admin.reports.actions.other_description_html", "Choose a custom moderation action.")))
	body.WriteString(`</div></form>`)
	return body.String()
}

func adminReportActionItemHTML(buttonHTML string, descriptionHTML string) string {
	return `<div class="report-actions__item"><div class="report-actions__item__button">` + buttonHTML + `</div><div class="report-actions__item__description">` + descriptionHTML + `</div></div>`
}

func adminReportActionPreviewHTML(report models.Report, statuses []models.Status, action string, text string, locale ...string) string {
	loc := settingsLocaleArgOrEnglish(locale...)
	target := adminReportAccountLabel(report.TargetAccount)
	body := strings.Builder{}
	body.WriteString(`<form method="post" action="/admin/reports/` + strconv.FormatInt(report.ID, 10) + `/actions" class="simple_form"><input type="hidden" name="moderation_action" value="` + html.EscapeString(action) + `">`)
	body.WriteString(`<p class="hint">` + html.EscapeString(adminTVars(loc, "admin.reports.actions.preview_preamble", "Confirm %{action} for %{target}.", map[string]string{"action": adminReportActionLabel(action, loc), "target": target})) + `</p>`)
	body.WriteString(`<ul class="hint">`)
	body.WriteString(`<li class="warning-hint">` + html.EscapeString(adminReportActionSummary(action, target, loc)) + `</li>`)
	if action == "suspend" {
		body.WriteString(`<li class="warning-hint">` + html.EscapeString(adminT(loc, "admin.reports.actions.suspension_cleanup_hint", "Cached profile data and locally stored media for the account may be removed by the suspension flow.")) + `</li>`)
	}
	if action == "silence" || action == "suspend" {
		body.WriteString(`<li>` + html.EscapeString(adminT(loc, "admin.reports.actions.close_reports_hint", "Other open reports for this account may be closed by compatible account-action workers.")) + `</li>`)
	} else {
		body.WriteString(`<li>` + html.EscapeString(adminTVars(loc, "admin.reports.actions.resolve_report_hint", "Report #%{id} will be marked as resolved.", map[string]string{"id": strconv.FormatInt(report.ID, 10)})) + `</li>`)
	}
	body.WriteString(`</ul><hr class="spacer">`)
	if report.TargetAccount.Local() {
		body.WriteString(`<p class="hint">` + html.EscapeString(adminTVars(loc, "admin.reports.summary.preview_preamble_html", "This is the warning that will be sent to %{acct}.", map[string]string{"acct": target})) + `</p><div class="strike-card">`)
		if action != "none" {
			body.WriteString(`<p>` + html.EscapeString(adminReportActionSummary(action, target, loc)) + `</p>`)
		}
		body.WriteString(`<div class="fields-group"><textarea name="text" rows="4" placeholder="` + html.EscapeString(adminT(loc, "admin.reports.summary.warning_placeholder", "Optional warning text")) + `">` + html.EscapeString(text) + `</textarea></div>`)
		if len(statuses) > 0 {
			body.WriteString(`<p><strong>` + html.EscapeString(adminT(loc, "user_mailer.warning.statuses", "Posts")) + `</strong></p><div class="strike-card__statuses-list">`)
			for _, status := range statuses {
				created := status.CreatedAt.UTC().Format(time.RFC3339)
				body.WriteString(`<div class="strike-card__statuses-list__item"><div class="one-liner emojify">` + html.EscapeString(oneLineStatusText(status)) + `</div><div class="strike-card__statuses-list__item__meta"><a href="/admin/accounts/` + strconv.FormatInt(status.AccountID, 10) + `/statuses/` + strconv.FormatInt(status.ID, 10) + `"><time class="formatted" datetime="` + html.EscapeString(created) + `">` + html.EscapeString(created) + `</time></a></div></div>`)
			}
			body.WriteString(`</div>`)
		}
		body.WriteString(`</div><hr class="spacer">`)
	} else if strings.TrimSpace(text) != "" {
		body.WriteString(`<input type="hidden" name="text" value="` + html.EscapeString(text) + `">`)
	}
	body.WriteString(`<div class="actions"><a class="button button-tertiary" href="/admin/reports/` + strconv.FormatInt(report.ID, 10) + `">` + html.EscapeString(adminT(loc, "admin.reports.cancel", "Cancel")) + `</a><button class="button" type="submit" name="confirm" value="1">` + html.EscapeString(adminT(loc, "admin.reports.confirm", "Confirm")) + `</button></div></form>`)
	return authPageHTML(adminTVars(loc, "admin.reports.actions.preview_title", "Confirm action for report #%{id}", map[string]string{"id": strconv.FormatInt(report.ID, 10)}), "", "", body.String(), loc)
}

func adminReportActionLabel(action string, locale ...string) string {
	loc := settingsLocaleArgOrEnglish(locale...)
	switch action {
	case "mark_as_sensitive":
		return adminT(loc, "admin.reports.actions.mark_as_sensitive", "Mark statuses as sensitive")
	case "delete":
		return adminT(loc, "admin.reports.actions.delete_statuses", "Delete statuses")
	case "silence":
		return adminT(loc, "admin.reports.actions.silence", "Limit account")
	case "suspend":
		return adminT(loc, "admin.reports.actions.suspend", "Suspend account")
	default:
		return action
	}
}

func adminReportActionSummary(action string, target string, locale ...string) string {
	loc := settingsLocaleArgOrEnglish(locale...)
	switch action {
	case "mark_as_sensitive":
		return adminT(loc, "admin.reports.actions.mark_as_sensitive_description", "Media attached to the reported statuses will be marked as sensitive.")
	case "delete":
		return adminTVars(loc, "admin.reports.actions.delete_statuses_description", "The reported statuses by %{target} will be deleted.", map[string]string{"target": target})
	case "silence":
		return adminTVars(loc, "admin.reports.actions.silence_description", "%{target} will be limited.", map[string]string{"target": target})
	case "suspend":
		return adminTVars(loc, "admin.reports.actions.suspend_description", "%{target} will be suspended.", map[string]string{"target": target})
	default:
		return adminT(loc, "admin.reports.actions.default_description", "The selected report action will be applied.")
	}
}

func oneLineStatusText(status models.Status) string {
	text := strings.TrimSpace(firstNonEmpty(status.Text, status.SpoilerText))
	text = strings.Join(strings.Fields(text), " ")
	if len([]rune(text)) > 140 {
		runes := []rune(text)
		return string(runes[:140]) + "..."
	}
	if text == "" {
		return "(no text)"
	}
	return text
}

func adminReportAccountLabel(account models.Account) string {
	if account.ID == 0 {
		return "-"
	}
	if account.Local() {
		return "@" + account.Username
	}
	if account.Domain.Valid && account.Domain.String != "" {
		return "@" + account.Username + "@" + account.Domain.String
	}
	return "@" + account.Username
}

func adminReportCategoryKey(value int) string {
	switch value {
	case 1000:
		return "spam"
	case 1500:
		return "legal"
	case 2000:
		return "violation"
	default:
		return "other"
	}
}

func adminReportCategoryLabel(value int, locale ...string) string {
	loc := settingsLocaleArgOrEnglish(locale...)
	key := adminReportCategoryKey(value)
	return adminT(loc, "admin.reports.categories."+key, key)
}
