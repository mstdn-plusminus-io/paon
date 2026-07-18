package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"github.com/mstdn-plusminus-io/paon/internal/paon/serializer"
	"gorm.io/gorm"
)

type reportPayload struct {
	AccountID        string   `json:"account_id" form:"account_id"`
	StatusIDs        []string `json:"status_ids" form:"status_ids"`
	RuleIDs          []string `json:"rule_ids" form:"rule_ids"`
	Comment          string   `json:"comment" form:"comment"`
	Category         string   `json:"category" form:"category"`
	Forward          bool     `json:"forward" form:"forward"`
	ForwardToDomains []string `json:"forward_to_domains" form:"forward_to_domains"`
}

func (s *Server) createReport(c *echo.Context) error {
	account, _, err := s.requireAccountScope(c, "write", "write:reports")
	if err != nil {
		return err
	}
	c.Response().Header().Set("Vary", "Authorization")
	setReportsFamilyRateLimitHeaders(c, railsReportsFamilyLimit-1)
	payload, err := parseReportPayload(c)
	if err != nil {
		return apiError(c, http.StatusBadRequest, "Malformed request")
	}
	target, err := s.findAccountByID(payload.AccountID)
	if err != nil || target.SuspendedAt.Valid {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	comment := reportComment(payload.Comment)
	if len([]rune(comment)) > 1000 {
		return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Comment is too long")
	}

	statusIDs := compactInt64Array(payload.StatusIDs)
	if len(statusIDs) > 0 {
		statusIDs, err = s.reportableStatusIDs(*account, *target, statusIDs)
		if err != nil {
			return err
		}
	}
	ruleIDs := compactInt64Array(payload.RuleIDs)
	if len(ruleIDs) == 0 {
		ruleIDs = nil
	} else if err := s.validateReportRuleIDs(ruleIDs); err != nil {
		return err
	}
	category := reportCategoryValue(payload.Category)
	if category == 2000 && len(ruleIDs) == 0 {
		return reportInvalidRuleIDsError()
	}
	if len(ruleIDs) > 0 {
		category = 2000
	}
	forwardDomains := reportForwardDomains(payload, *target)
	forwarded := payload.Forward && !target.Local() && reportForwardDomainsIncludeTarget(forwardDomains, *target)
	now := time.Now().UTC()
	var staffNotificationPayloads []asynqLocalNotificationPayload
	report := models.Report{
		StatusIDs:       statusIDs,
		Comment:         comment,
		CreatedAt:       now,
		UpdatedAt:       now,
		AccountID:       account.ID,
		TargetAccountID: target.ID,
		URI:             s.reportURIForAccount(*account),
		Forwarded:       sql.NullBool{Bool: forwarded, Valid: true},
		Category:        category,
		RuleIDs:         ruleIDs,
	}
	rateLimitRecorded, err := s.consumeRailsFamilyRateLimit(c, *account, railsRateLimitFamilyReports, now)
	if err != nil {
		return err
	}
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&report).Error; err != nil {
			return err
		}
		payloads, err := s.createStaffReportNotificationPayloads(tx, report, *account)
		staffNotificationPayloads = payloads
		return err
	}); err != nil {
		if rateLimitRecorded {
			s.rollbackRailsFamilyRateLimit(c.Request().Context(), *account, railsRateLimitFamilyReports, now)
		}
		return err
	}
	if err := s.db.Preload("Account").Preload("TargetAccount.AccountStat").Preload("TargetAccount.User.Role").First(&report, report.ID).Error; err != nil {
		return err
	}
	s.triggerReportCreatedWebhook(report)
	if payload.Forward && !target.Local() {
		_ = s.forwardActivityPubReport(report, *target, forwardDomains)
	}
	if len(staffNotificationPayloads) > 0 {
		if _, err := s.enqueueOrCreateLocalNotifications(c.Request().Context(), staffNotificationPayloads); err != nil {
			return err
		}
		_ = s.sendStaffNewReportMails(report)
	}
	return c.JSON(http.StatusOK, serializer.ReportFromModel(s.cfg, report))
}

func reportComment(comment string) string {
	if strings.TrimSpace(comment) == "" {
		return ""
	}
	return comment
}

func (s *Server) triggerReportCreatedWebhook(report models.Report) {
	s.triggerReportWebhook("report.created", report.ID)
}

func (s *Server) triggerReportUpdatedWebhook(reportID int64) {
	s.triggerReportWebhook("report.updated", reportID)
}

func (s *Server) triggerReportWebhook(event string, reportID int64) {
	if s == nil || s.db == nil {
		return
	}
	if s.enqueueTriggerWebhookTask(event, "Report", reportID) {
		return
	}
	_ = s.triggerReportWebhookNow(event, reportID)
}

func (s *Server) triggerReportWebhookNow(event string, reportID int64) error {
	adminReport, err := s.loadAdminReport(strconv.FormatInt(reportID, 10))
	if err != nil {
		return err
	}
	statuses, err := s.reportStatuses(*adminReport)
	if err != nil {
		return err
	}
	s.triggerWebhookEvent(event, serializer.AdminReportFromModel(s.cfg, *adminReport, statuses))
	return nil
}

func (s *Server) reportURIForAccount(account models.Account) sql.NullString {
	if !account.Local() {
		return sql.NullString{}
	}
	return sql.NullString{String: activityPubGeneratedPayloadURI(s), Valid: true}
}

func (s *Server) reportURIForAccountID(tx *gorm.DB, accountID int64) (sql.NullString, error) {
	if accountID == 0 {
		return sql.NullString{}, nil
	}
	db := s.db
	if tx != nil {
		db = tx
	}
	if db == nil {
		return sql.NullString{}, nil
	}
	var account models.Account
	if err := db.Select("id", "domain").Where("id = ?", accountID).First(&account).Error; err != nil {
		return sql.NullString{}, err
	}
	return s.reportURIForAccount(account), nil
}

func (s *Server) createStaffReportNotificationPayloads(tx *gorm.DB, report models.Report, source models.Account) ([]asynqLocalNotificationPayload, error) {
	if hasSibling, err := s.reportHasUnresolvedSibling(tx, report); err != nil || hasSibling {
		return nil, err
	}
	users, err := s.staffUsersWithPermission(tx, rolePermissionManageReports)
	if err != nil || len(users) == 0 {
		return nil, err
	}
	payloads := make([]asynqLocalNotificationPayload, 0, len(users))
	for _, user := range users {
		if user.AccountID == 0 {
			continue
		}
		payloads = append(payloads, asynqLocalNotificationPayload{
			ReceiverAccountID: user.AccountID,
			FromAccountID:     source.ID,
			ActivityID:        report.ID,
			ActivityType:      "Report",
			Type:              "admin.report",
		})
	}
	return payloads, nil
}

func (s *Server) staffUsersWithPermission(db *gorm.DB, permission int64) ([]models.User, error) {
	roleIDs, err := s.roleIDsWithPermission(db, permission)
	if err != nil || len(roleIDs) == 0 {
		return nil, err
	}
	query := db.Preload("Account")
	if roleIDsIncludeEveryone(roleIDs) {
		filtered := roleIDsWithoutEveryone(roleIDs)
		if len(filtered) > 0 {
			query = query.Where("role_id IN ? OR role_id IS NULL", filtered)
		} else {
			query = query.Where("role_id IS NULL")
		}
	} else {
		query = query.Where("role_id IN ?", roleIDs)
	}
	var users []models.User
	if err := query.Find(&users).Error; err != nil {
		return nil, err
	}
	return users, nil
}

func (s *Server) reportHasUnresolvedSibling(tx *gorm.DB, report models.Report) (bool, error) {
	var count int64
	if err := tx.Model(&models.Report{}).
		Where("id <> ? AND target_account_id = ? AND action_taken_at IS NULL", report.ID, report.TargetAccountID).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func roleIDsIncludeEveryone(roleIDs []int64) bool {
	for _, id := range roleIDs {
		if id == -99 {
			return true
		}
	}
	return false
}

func roleIDsWithoutEveryone(roleIDs []int64) []int64 {
	out := make([]int64, 0, len(roleIDs))
	for _, id := range roleIDs {
		if id != -99 {
			out = append(out, id)
		}
	}
	return out
}

func parseReportPayload(c *echo.Context) (reportPayload, error) {
	var payload reportPayload
	if requestIsJSON(c) {
		var raw map[string]any
		decoder := json.NewDecoder((*c).Request().Body)
		decoder.UseNumber()
		if err := decoder.Decode(&raw); err != nil {
			return payload, err
		}
		payload.AccountID = rawString(raw["account_id"])
		payload.StatusIDs = rawStringSlice(raw["status_ids"])
		payload.RuleIDs = rawStringSlice(raw["rule_ids"])
		payload.Comment = rawString(raw["comment"])
		payload.Category = rawString(raw["category"])
		payload.Forward = railsBool(raw["forward"], false)
		payload.ForwardToDomains = rawStringSlice(raw["forward_to_domains"])
		return payload, nil
	}
	if values, err := c.FormValues(); err == nil {
		payload.StatusIDs = append(payload.StatusIDs, values["status_ids[]"]...)
		payload.RuleIDs = append(payload.RuleIDs, values["rule_ids[]"]...)
		payload.ForwardToDomains = append(payload.ForwardToDomains, values["forward_to_domains[]"]...)
		if payload.AccountID == "" {
			payload.AccountID = values.Get("account_id")
		}
		if payload.Comment == "" {
			payload.Comment = values.Get("comment")
		}
		if payload.Category == "" {
			payload.Category = values.Get("category")
		}
		if value := values.Get("forward"); value != "" {
			payload.Forward = truthy(value)
		}
	}
	return payload, nil
}

func (s *Server) validateReportRuleIDs(ruleIDs models.Int64Array) error {
	if s == nil || s.db == nil || len(ruleIDs) == 0 {
		return nil
	}
	var count int64
	if err := s.db.Model(&models.Rule{}).Where("id IN ?", []int64(ruleIDs)).Count(&count).Error; err != nil {
		return err
	}
	if count != int64(len(ruleIDs)) {
		return reportInvalidRuleIDsError()
	}
	return nil
}

func reportInvalidRuleIDsError() error {
	return apiHTTPError{status: http.StatusUnprocessableEntity, message: "Validation failed: Rule ids is invalid"}
}

func reportForwardDomains(payload reportPayload, target models.Account) []string {
	values := payload.ForwardToDomains
	if len(values) == 0 && target.Domain.Valid {
		values = []string{target.Domain.String}
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		domain := normalizeDomain(value)
		if domain == "" {
			continue
		}
		if _, ok := seen[domain]; ok {
			continue
		}
		seen[domain] = struct{}{}
		out = append(out, domain)
	}
	return out
}

func reportForwardDomainsIncludeTarget(domains []string, target models.Account) bool {
	if !target.Domain.Valid {
		return false
	}
	targetDomain := normalizeDomain(target.Domain.String)
	for _, domain := range domains {
		if domain == targetDomain {
			return true
		}
	}
	return false
}

func (s *Server) forwardActivityPubReport(report models.Report, target models.Account, forwardDomains []string) error {
	if s == nil || s.db == nil || target.Local() || len(forwardDomains) == 0 {
		return nil
	}
	local, err := s.representativeActivityPubAccount()
	if err != nil {
		return err
	}
	statuses, err := s.reportForwardStatuses(report.StatusIDs)
	if err != nil {
		return err
	}
	body, err := json.Marshal(activityPubFlagReport(s, report, target, statuses, *local))
	if err != nil {
		return err
	}
	inboxes, err := s.reportForwardInboxes(target, report.StatusIDs, forwardDomains)
	if err != nil {
		return err
	}
	var lastErr error
	for _, inboxURL := range inboxes {
		if err := s.deliverActivityPub(*local, inboxURL, body); err != nil {
			lastErr = err
		}
	}
	return lastErr
}

func (s *Server) reportForwardStatuses(statusIDs models.Int64Array) ([]models.Status, error) {
	if len(statusIDs) == 0 {
		return nil, nil
	}
	var statuses []models.Status
	if err := s.db.Preload("Account").Where("id IN ?", []int64(statusIDs)).Find(&statuses).Error; err != nil {
		return nil, err
	}
	return orderReportForwardStatuses(statusIDs, statuses), nil
}

func (s *Server) reportForwardInboxes(target models.Account, statusIDs models.Int64Array, forwardDomains []string) ([]string, error) {
	inboxes := []string{}
	if reportForwardDomainsIncludeTarget(forwardDomains, target) {
		inboxes = append(inboxes, target.InboxURL)
	}
	if len(statusIDs) > 0 {
		rows := []struct {
			InboxURL       string `gorm:"column:inbox_url"`
			SharedInboxURL string `gorm:"column:shared_inbox_url"`
		}{}
		if err := s.db.Model(&models.Account{}).
			Select("accounts.inbox_url, accounts.shared_inbox_url").
			Joins("JOIN statuses ON statuses.in_reply_to_account_id = accounts.id").
			Where("statuses.id IN ?", []int64(statusIDs)).
			Where("accounts.domain IS NOT NULL AND accounts.domain <> '' AND accounts.protocol = ?", 1).
			Where("lower(accounts.domain) IN ?", forwardDomains).
			Find(&rows).Error; err != nil {
			return nil, err
		}
		inboxes = append(inboxes, s.compactAvailableActivityPubInboxes(activityPubInboxesFromRows(rows))...)
	}
	excluded := map[string]struct{}{}
	for _, value := range []string{target.InboxURL, target.SharedInboxURL} {
		value = strings.TrimSpace(value)
		if value != "" {
			excluded[value] = struct{}{}
		}
	}
	out := make([]string, 0, len(inboxes))
	for _, inboxURL := range compactActivityPubInboxes(inboxes) {
		if _, ok := excluded[inboxURL]; ok && inboxURL != strings.TrimSpace(target.InboxURL) {
			continue
		}
		out = append(out, inboxURL)
	}
	return out, nil
}

func activityPubFlagReport(s *Server, report models.Report, target models.Account, statuses []models.Status, actor models.Account) map[string]any {
	objects := []string{activityPubAccountTagManagerURI(s, target)}
	for _, status := range statuses {
		objects = append(objects, activityPubStatusURI(s, status))
	}
	reportID := report.URI.String
	if strings.TrimSpace(reportID) == "" {
		reportID = activityPubGeneratedPayloadURI(s)
	}
	return map[string]any{
		"@context": activityPubActivityStreamsContext(),
		"id":       reportID,
		"type":     "Flag",
		"actor":    activityPubActorID(s, actor),
		"content":  report.Comment,
		"object":   objects,
	}
}

func orderReportForwardStatuses(statusIDs models.Int64Array, statuses []models.Status) []models.Status {
	byID := make(map[int64]models.Status, len(statuses))
	for _, status := range statuses {
		byID[status.ID] = status
	}
	out := make([]models.Status, 0, len(statuses))
	seen := map[int64]struct{}{}
	for _, id := range statusIDs {
		if _, ok := seen[id]; ok {
			continue
		}
		status, ok := byID[id]
		if !ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, status)
	}
	return out
}

func (s *Server) reportableStatusIDs(source models.Account, target models.Account, statusIDs models.Int64Array) (models.Int64Array, error) {
	if source.ID != target.ID {
		var blocks int64
		if err := s.db.Model(&models.Block{}).
			Where("account_id = ? AND target_account_id = ?", target.ID, source.ID).
			Count(&blocks).Error; err != nil || blocks > 0 {
			return nil, err
		}
	}
	var rows []models.Status
	query := s.db.Model(&models.Status{}).
		Where("statuses.account_id = ? AND statuses.id IN ?", target.ID, statusIDs)
	if source.ID != target.ID {
		visible := []int{0, 1}
		var follows int64
		if err := s.db.Model(&models.Follow{}).
			Where("account_id = ? AND target_account_id = ?", source.ID, target.ID).
			Count(&follows).Error; err != nil {
			return nil, err
		}
		if follows > 0 {
			visible = append(visible, 2)
		}
		query = query.
			Joins("LEFT JOIN mentions report_status_mentions ON report_status_mentions.status_id = statuses.id AND report_status_mentions.account_id = ?", source.ID).
			Where("(statuses.visibility IN ? OR report_status_mentions.id IS NOT NULL)", visible).
			Group("statuses.id")
		query = s.applyActivityPubOutboxReblogExclusions(query, &source)
	}
	if err := query.Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make(models.Int64Array, 0, len(rows))
	for _, row := range orderReportForwardStatuses(statusIDs, rows) {
		out = append(out, row.ID)
	}
	if len(out) != len(statusIDs) {
		return nil, apiHTTPError{status: http.StatusNotFound, message: "Record not found"}
	}
	return out, nil
}

func compactInt64Array(values []string) models.Int64Array {
	seen := map[int64]struct{}{}
	out := make(models.Int64Array, 0, len(values))
	for _, value := range values {
		id, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err != nil || id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func reportCategoryValue(value string) int {
	category, ok := reportCategoryValueOK(value)
	if !ok {
		return 0
	}
	return category
}

func reportCategoryValueOK(value string) (int, bool) {
	switch value {
	case "", "other":
		return 0, true
	case "spam":
		return 1000, true
	case "legal":
		return 1500, true
	case "violation":
		return 2000, true
	default:
		return 0, false
	}
}
