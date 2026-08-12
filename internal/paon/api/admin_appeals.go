package api

import (
	"context"
	"html"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

func (s *Server) adminAppealsPage(c *echo.Context) error {
	if err := requireHTMLOnlyOptionalFormat(c); err != nil {
		return err
	}
	user, handled, err := s.requireAdminAppealsWebUser(c)
	if handled || err != nil {
		return err
	}
	appeals, pending, err := s.adminAppealModels(c)
	if err != nil {
		return err
	}
	return c.HTML(http.StatusOK, adminAppealsHTMLWithConfig(s.cfg, appeals, pending, adminAppealFilters{Page: adminTrendsPageValue(c), Status: adminAppealStatus(c)}, c.QueryParam("notice"), c.QueryParam("error"), s.webLocale(c, user)))
}

func (s *Server) approveAdminAppealWeb(c *echo.Context) error {
	user, handled, err := s.requireAdminAppealsWebUser(c)
	if handled || err != nil {
		return err
	}
	strikeID, err := s.applyAdminAppealDecision(c.Param("id"), user.AccountID, true)
	if err != nil {
		return err
	}
	return c.Redirect(http.StatusFound, "/disputes/strikes/"+strconv.FormatInt(strikeID, 10))
}

func (s *Server) rejectAdminAppealWeb(c *echo.Context) error {
	user, handled, err := s.requireAdminAppealsWebUser(c)
	if handled || err != nil {
		return err
	}
	strikeID, err := s.applyAdminAppealDecision(c.Param("id"), user.AccountID, false)
	if err != nil {
		return err
	}
	return c.Redirect(http.StatusFound, "/disputes/strikes/"+strconv.FormatInt(strikeID, 10))
}

func (s *Server) requireAdminAppealsWebUser(c *echo.Context) (*models.User, bool, error) {
	user, _, handled, err := s.requireFunctionalWebUser(c)
	if handled || err != nil {
		return nil, handled, err
	}
	if !s.userCan(user, rolePermissionManageAppeals) {
		locale := s.webLocale(c, user)
		return nil, true, c.HTML(http.StatusForbidden, authPageHTML(adminT(locale, "admin.disputes.appeals.title", "Appeals"), "", adminT(locale, "admin.disputes.appeals.not_permitted", "You are not allowed to manage appeals."), "", locale))
	}
	return user, false, nil
}

func (s *Server) adminAppealModels(c *echo.Context) ([]models.Appeal, int64, error) {
	if s.db == nil {
		return []models.Appeal{}, 0, nil
	}
	var pending int64
	if err := s.db.Model(&models.Appeal{}).Where("approved_at IS NULL AND rejected_at IS NULL").Count(&pending).Error; err != nil {
		return nil, 0, err
	}
	query := s.db.Preload("Account").Preload("Strike.TargetAccount").Model(&models.Appeal{})
	status, apply, err := adminAppealStatusFilter(c)
	if err != nil {
		return nil, 0, err
	}
	if apply {
		switch status {
		case "approved":
			query = query.Where("approved_at IS NOT NULL")
		case "rejected":
			query = query.Where("rejected_at IS NOT NULL")
		case "pending":
			query = query.Where("approved_at IS NULL AND rejected_at IS NULL")
		}
	}
	var appeals []models.Appeal
	err = query.Order("appeals.id DESC").Offset(adminRailsPageOffset(c)).Limit(adminRailsDefaultPageSize).Find(&appeals).Error
	return appeals, pending, err
}

func adminAppealStatus(c *echo.Context) string {
	if !queryParamPresent(c, "status") {
		return "pending"
	}
	return strings.TrimSpace(c.QueryParam("status"))
}

func adminAppealStatusFilter(c *echo.Context) (string, bool, error) {
	if !queryParamPresent(c, "status") {
		return "pending", true, nil
	}
	status := strings.TrimSpace(c.QueryParam("status"))
	if status == "" {
		return "", false, nil
	}
	switch status {
	case "approved", "rejected", "pending":
		return status, true, nil
	default:
		return "", false, echo.NewHTTPError(http.StatusBadRequest, "Unknown status: "+status)
	}
}

func (s *Server) applyAdminAppealDecision(rawID string, actorAccountID int64, approve bool) (int64, error) {
	if s.db == nil {
		return 0, echo.NewHTTPError(http.StatusUnprocessableEntity, "DATABASE_URL is not set")
	}
	var strikeID int64
	var decidedAppeal models.Appeal
	var updateStatusIDs []int64
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var appeal models.Appeal
		if err := tx.Preload("Strike").Where("id = ?", rawID).First(&appeal).Error; err != nil {
			return echo.NewHTTPError(http.StatusNotFound, "appeal not found")
		}
		strikeID = appeal.AccountWarningID
		decidedAppeal = appeal
		if appeal.ApprovedAt.Valid || appeal.RejectedAt.Valid {
			return echo.NewHTTPError(http.StatusUnprocessableEntity, "appeal is not pending")
		}
		now := time.Now().UTC()
		if approve {
			if err := tx.Model(&models.Appeal{}).Where("id = ?", appeal.ID).Updates(map[string]any{
				"approved_at":            now,
				"approved_by_account_id": actorAccountID,
				"updated_at":             now,
			}).Error; err != nil {
				return err
			}
			if err := logAdminAction(tx, actorAccountID, "approve", appealAuditLogTarget(appeal), now); err != nil {
				return err
			}
			if err := tx.Model(&models.AccountWarning{}).Where("id = ?", appeal.AccountWarningID).Updates(map[string]any{"overruled_at": now, "updated_at": now}).Error; err != nil {
				return err
			}
			if err := s.undoAdminAppealStrikeAction(tx, appeal.Strike, now); err != nil {
				return err
			}
			if appeal.Strike.Action == 1250 {
				updateStatusIDs = accountWarningStatusIDs(appeal.Strike)
			}
			return nil
		}
		if err := tx.Model(&models.Appeal{}).Where("id = ?", appeal.ID).Updates(map[string]any{
			"rejected_at":            now,
			"rejected_by_account_id": actorAccountID,
			"updated_at":             now,
		}).Error; err != nil {
			return err
		}
		return logAdminAction(tx, actorAccountID, "reject", appealAuditLogTarget(appeal), now)
	})
	if err != nil {
		return strikeID, err
	}
	targetAccountID := int64(0)
	if decidedAppeal.Strike.TargetAccountID.Valid {
		targetAccountID = decidedAppeal.Strike.TargetAccountID.Int64
	}
	if approve && targetAccountID != 0 && adminAppealStrikeUpdatesAccount(decidedAppeal.Strike.Action) {
		s.triggerAccountWebhook("account.updated", targetAccountID)
		_ = s.enqueueFASPAccountLifecycleByID(context.Background(), targetAccountID, "update")
	}
	if approve && targetAccountID != 0 && decidedAppeal.Strike.Action == 4000 {
		if err := s.enqueueAdminUnsuspensionOrRun(s.db, targetAccountID); err != nil {
			return strikeID, err
		}
	}
	if approve && len(updateStatusIDs) > 0 {
		s.fanOutAdminAppealStatusUpdates(context.Background(), updateStatusIDs)
	}
	if approve {
		_ = s.sendAppealDecisionMail(decidedAppeal, true)
	}
	return strikeID, nil
}

func adminAppealStrikeUpdatesAccount(action int) bool {
	switch action {
	case 2000, 3000, 4000:
		return true
	default:
		return false
	}
}

func (s *Server) undoAdminAppealStrikeAction(tx *gorm.DB, strike models.AccountWarning, now time.Time) error {
	if !strike.TargetAccountID.Valid || strike.TargetAccountID.Int64 == 0 {
		return nil
	}
	targetAccountID := strike.TargetAccountID.Int64
	switch strike.Action {
	case 1000:
		return tx.Model(&models.User{}).Where("account_id = ?", targetAccountID).Updates(map[string]any{"disabled": false, "updated_at": now}).Error
	case 1250:
		ids := accountWarningStatusIDs(strike)
		if len(ids) == 0 {
			return nil
		}
		return tx.Model(&models.Status{}).
			Where("id IN ?", ids).
			Where("EXISTS (SELECT 1 FROM media_attachments WHERE media_attachments.status_id = statuses.id)").
			Updates(map[string]any{"sensitive": false, "updated_at": now}).Error
	case 2000:
		return tx.Model(&models.Account{}).Where("id = ?", targetAccountID).Updates(map[string]any{"sensitized_at": nil, "updated_at": now}).Error
	case 3000:
		return tx.Model(&models.Account{}).Where("id = ?", targetAccountID).Updates(map[string]any{"silenced_at": nil, "updated_at": now}).Error
	case 4000:
		if err := tx.Model(&models.Account{}).Where("id = ?", targetAccountID).Updates(map[string]any{"suspended_at": nil, "suspension_origin": nil, "updated_at": now}).Error; err != nil {
			return err
		}
		if err := tx.Where("account_id = ?", targetAccountID).Delete(&models.AccountDeletionRequest{}).Error; err != nil {
			return err
		}
		return destroyCanonicalEmailBlocksForAccountTx(tx, targetAccountID)
	default:
		return nil
	}
}

func (s *Server) fanOutAdminAppealStatusUpdates(ctx context.Context, statusIDs []int64) {
	s.fanOutAdminStatusBatchUpdates(ctx, statusIDs)
}

func accountWarningStatusIDs(strike models.AccountWarning) []int64 {
	ids := make([]int64, 0, len(strike.StatusIDs))
	for _, raw := range strike.StatusIDs {
		id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err == nil && id > 0 {
			ids = append(ids, id)
		}
	}
	return ids
}

type adminAppealFilters struct {
	Page   string
	Status string
}

func adminAppealsHTML(appeals []models.Appeal, pending int64, filters adminAppealFilters, notice string, errorText string, locale ...string) string {
	return adminAppealsHTMLWithConfig(config.Config{}, appeals, pending, filters, notice, errorText, locale...)
}

func adminAppealsHTMLWithConfig(cfg config.Config, appeals []models.Appeal, pending int64, filters adminAppealFilters, notice string, errorText string, locale ...string) string {
	loc := settingsLocaleArgOrEnglish(locale...)
	var body strings.Builder
	statusFilters := adminTrendsFilterValues("status", filters.Status)
	pendingLabel := adminT(loc, "admin.accounts.moderation.pending", "Pending") + " (" + strconv.FormatInt(pending, 10) + ")"
	body.WriteString(`<div class="filters">` + relationshipFilterSubsetHTML(adminT(loc, "admin.tags.review", "Review status"), []relationshipFilterLink{
		{Label: pendingLabel, Href: adminTrendsWebFilterHref("/admin/disputes/appeals", statusFilters, "status", "pending"), Active: filters.Status == "pending" || filters.Status == ""},
		{Label: adminT(loc, "admin.trends.approved", "Approved"), Href: adminTrendsWebFilterHref("/admin/disputes/appeals", statusFilters, "status", "approved"), Active: filters.Status == "approved"},
		{Label: adminT(loc, "admin.trends.rejected", "Rejected"), Href: adminTrendsWebFilterHref("/admin/disputes/appeals", statusFilters, "status", "rejected"), Active: filters.Status == "rejected"},
	}) + `</div>`)
	if len(appeals) == 0 {
		body.WriteString(`<div class="muted-hint center-text">` + html.EscapeString(adminT(loc, "admin.disputes.appeals.empty", "No appeals found.")) + `</div>`)
	} else {
		body.WriteString(`<div class="announcements-list">`)
		for _, appeal := range appeals {
			body.WriteString(adminAppealRowHTMLWithConfig(cfg, appeal, loc))
		}
		body.WriteString(`</div>`)
	}
	body.WriteString(adminRailsPaginationHTML(loc, "/admin/disputes/appeals", filters.Page, statusFilters, len(appeals)))
	return authPageHTML(adminT(loc, "admin.disputes.appeals.title", "Appeals"), notice, errorText, body.String(), loc)
}

func adminAppealRowHTML(appeal models.Appeal, locale ...string) string {
	return adminAppealRowHTMLWithConfig(config.Config{}, appeal, settingsLocaleArgOrEnglish(locale...))
}

func adminAppealRowHTMLWithConfig(cfg config.Config, appeal models.Appeal, loc string) string {
	strikeID := strconv.FormatInt(appeal.AccountWarningID, 10)
	state := adminAppealState(appeal)
	stateClass := "warning-hint"
	if state == "approved" {
		stateClass = "positive-hint"
	} else if state == "rejected" {
		stateClass = "negative-hint"
	}
	avatar := statusEmbedAccountAvatarURLWithConfig(cfg, appeal.Account)
	title := accountWarningActionLabel(appeal.Strike.Action, loc) + ": " + adminAppealAccountLabel(appeal.Account)
	report := ""
	if appeal.Strike.ReportID.Valid {
		report = ` &middot; ` + html.EscapeString(adminTVars(loc, "admin.reports.report", "Report #%{id}", map[string]string{"id": strconv.FormatInt(appeal.Strike.ReportID.Int64, 10)}))
	}
	stamp := appeal.Strike.CreatedAt
	if stamp.IsZero() {
		stamp = appeal.CreatedAt
	}
	return `<a href="/disputes/strikes/` + strikeID + `" class="log-entry"><div class="log-entry__header"><div class="log-entry__avatar"><img src="` + html.EscapeString(avatar) + `" alt="" width="40" height="40" class="avatar"></div><div class="log-entry__content"><div class="log-entry__title">` + html.EscapeString(title) + `</div><div class="log-entry__timestamp"><time class="formatted" datetime="` + html.EscapeString(stamp.UTC().Format(time.RFC3339)) + `">` + html.EscapeString(stamp.UTC().Format("2006-01-02 15:04")) + `</time>` + report + ` &middot; <span class="` + stateClass + `">` + html.EscapeString(adminT(loc, "admin.strikes.appeal_"+state, state)) + `</span></div></div></div></a>`
}

func adminAppealState(appeal models.Appeal) string {
	switch {
	case appeal.ApprovedAt.Valid:
		return "approved"
	case appeal.RejectedAt.Valid:
		return "rejected"
	default:
		return "pending"
	}
}

func adminAppealAccountLabel(account models.Account) string {
	if account.ID == 0 {
		return "unknown"
	}
	return account.Acct()
}
