package api

import (
	"html"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

type adminActionLogFilter struct {
	AccountID            int64
	AccountIDPresent     bool
	AccountIDValid       bool
	TargetAccountID      int64
	TargetAccountPresent bool
	TargetAccountValid   bool
	ActionType           string
	Page                 int
}

type adminActionType struct {
	TargetType string
	Action     string
}

var adminActionLogActionTypes = map[string]adminActionType{
	"approve_appeal":                {TargetType: "Appeal", Action: "approve"},
	"reject_appeal":                 {TargetType: "Appeal", Action: "reject"},
	"assigned_to_self_report":       {TargetType: "Report", Action: "assigned_to_self"},
	"change_email_user":             {TargetType: "User", Action: "change_email"},
	"change_role_user":              {TargetType: "User", Action: "change_role"},
	"confirm_user":                  {TargetType: "User", Action: "confirm"},
	"approve_user":                  {TargetType: "User", Action: "approve"},
	"reject_user":                   {TargetType: "User", Action: "reject"},
	"create_account_warning":        {TargetType: "AccountWarning", Action: "create"},
	"create_announcement":           {TargetType: "Announcement", Action: "create"},
	"create_custom_emoji":           {TargetType: "CustomEmoji", Action: "create"},
	"create_domain_allow":           {TargetType: "DomainAllow", Action: "create"},
	"create_domain_block":           {TargetType: "DomainBlock", Action: "create"},
	"create_email_domain_block":     {TargetType: "EmailDomainBlock", Action: "create"},
	"create_ip_block":               {TargetType: "IpBlock", Action: "create"},
	"create_relay":                  {TargetType: "Relay", Action: "create"},
	"create_unavailable_domain":     {TargetType: "UnavailableDomain", Action: "create"},
	"create_user_role":              {TargetType: "UserRole", Action: "create"},
	"create_canonical_email_block":  {TargetType: "CanonicalEmailBlock", Action: "create"},
	"demote_user":                   {TargetType: "User", Action: "demote"},
	"destroy_announcement":          {TargetType: "Announcement", Action: "destroy"},
	"destroy_custom_emoji":          {TargetType: "CustomEmoji", Action: "destroy"},
	"destroy_domain_allow":          {TargetType: "DomainAllow", Action: "destroy"},
	"destroy_domain_block":          {TargetType: "DomainBlock", Action: "destroy"},
	"destroy_ip_block":              {TargetType: "IpBlock", Action: "destroy"},
	"destroy_email_domain_block":    {TargetType: "EmailDomainBlock", Action: "destroy"},
	"destroy_instance":              {TargetType: "Instance", Action: "destroy"},
	"destroy_relay":                 {TargetType: "Relay", Action: "destroy"},
	"destroy_unavailable_domain":    {TargetType: "UnavailableDomain", Action: "destroy"},
	"destroy_status":                {TargetType: "Status", Action: "destroy"},
	"destroy_user_role":             {TargetType: "UserRole", Action: "destroy"},
	"destroy_canonical_email_block": {TargetType: "CanonicalEmailBlock", Action: "destroy"},
	"disable_2fa_user":              {TargetType: "User", Action: "disable_2fa"},
	"disable_custom_emoji":          {TargetType: "CustomEmoji", Action: "disable"},
	"disable_relay":                 {TargetType: "Relay", Action: "disable"},
	"disable_user":                  {TargetType: "User", Action: "disable"},
	"enable_custom_emoji":           {TargetType: "CustomEmoji", Action: "enable"},
	"enable_relay":                  {TargetType: "Relay", Action: "enable"},
	"enable_user":                   {TargetType: "User", Action: "enable"},
	"memorialize_account":           {TargetType: "Account", Action: "memorialize"},
	"promote_user":                  {TargetType: "User", Action: "promote"},
	"remove_avatar_user":            {TargetType: "User", Action: "remove_avatar"},
	"reopen_report":                 {TargetType: "Report", Action: "reopen"},
	"resend_user":                   {TargetType: "User", Action: "resend"},
	"reset_password_user":           {TargetType: "User", Action: "reset_password"},
	"resolve_report":                {TargetType: "Report", Action: "resolve"},
	"sensitive_account":             {TargetType: "Account", Action: "sensitive"},
	"silence_account":               {TargetType: "Account", Action: "silence"},
	"suspend_account":               {TargetType: "Account", Action: "suspend"},
	"unassigned_report":             {TargetType: "Report", Action: "unassigned"},
	"unsensitive_account":           {TargetType: "Account", Action: "unsensitive"},
	"unsilence_account":             {TargetType: "Account", Action: "unsilence"},
	"unsuspend_account":             {TargetType: "Account", Action: "unsuspend"},
	"update_announcement":           {TargetType: "Announcement", Action: "update"},
	"update_custom_emoji":           {TargetType: "CustomEmoji", Action: "update"},
	"update_status":                 {TargetType: "Status", Action: "update"},
	"update_user_role":              {TargetType: "UserRole", Action: "update"},
	"update_ip_block":               {TargetType: "IpBlock", Action: "update"},
	"unblock_email_account":         {TargetType: "Account", Action: "unblock_email"},
}

var adminActionLogActionTypeOrder = []string{
	"approve_appeal",
	"reject_appeal",
	"assigned_to_self_report",
	"change_email_user",
	"change_role_user",
	"confirm_user",
	"approve_user",
	"reject_user",
	"create_account_warning",
	"create_announcement",
	"create_custom_emoji",
	"create_domain_allow",
	"create_domain_block",
	"create_email_domain_block",
	"create_ip_block",
	"create_relay",
	"create_unavailable_domain",
	"create_user_role",
	"create_canonical_email_block",
	"demote_user",
	"destroy_announcement",
	"destroy_custom_emoji",
	"destroy_domain_allow",
	"destroy_domain_block",
	"destroy_ip_block",
	"destroy_email_domain_block",
	"destroy_instance",
	"destroy_relay",
	"destroy_unavailable_domain",
	"destroy_status",
	"destroy_user_role",
	"destroy_canonical_email_block",
	"disable_2fa_user",
	"disable_custom_emoji",
	"disable_relay",
	"disable_user",
	"enable_custom_emoji",
	"enable_relay",
	"enable_user",
	"memorialize_account",
	"promote_user",
	"remove_avatar_user",
	"reopen_report",
	"resend_user",
	"reset_password_user",
	"resolve_report",
	"sensitive_account",
	"silence_account",
	"suspend_account",
	"unassigned_report",
	"unsensitive_account",
	"unsilence_account",
	"unsuspend_account",
	"update_announcement",
	"update_custom_emoji",
	"update_status",
	"update_user_role",
	"update_ip_block",
	"unblock_email_account",
}

func (s *Server) adminActionLogsPage(c *echo.Context) error {
	user, handled, err := s.requireAdminAuditLogWebUser(c)
	if handled || err != nil {
		return err
	}
	filter := parseAdminActionLogFilter(c)
	logs, err := s.adminActionLogModels(filter)
	if err != nil {
		return err
	}
	accounts, err := s.adminActionLogAuditableAccounts()
	if err != nil {
		return err
	}
	return c.HTML(http.StatusOK, adminActionLogsHTMLWithConfig(s.cfg, logs, accounts, filter, c.QueryParam("notice"), c.QueryParam("error"), s.webLocale(c, user)))
}

func (s *Server) requireAdminAuditLogWebUser(c *echo.Context) (*models.User, bool, error) {
	user, _, handled, err := s.requireFunctionalWebUser(c)
	if handled || err != nil {
		return nil, handled, err
	}
	if !s.userCan(user, rolePermissionViewAuditLog) {
		locale := s.webLocale(c, user)
		return nil, true, c.HTML(http.StatusForbidden, authPageHTML(adminT(locale, "admin.action_logs.title", "Audit log"), "", adminT(locale, "admin.action_logs.not_permitted", "You are not allowed to view audit logs."), "", locale))
	}
	return user, false, nil
}

func parseAdminActionLogFilter(c *echo.Context) adminActionLogFilter {
	accountID, accountPresent, accountValid := parseAdminActionLogIDParam(c.QueryParam("account_id"))
	targetAccountID, targetPresent, targetValid := parseAdminActionLogIDParam(c.QueryParam("target_account_id"))
	return adminActionLogFilter{
		AccountID:            accountID,
		AccountIDPresent:     accountPresent,
		AccountIDValid:       accountValid,
		TargetAccountID:      targetAccountID,
		TargetAccountPresent: targetPresent,
		TargetAccountValid:   targetValid,
		ActionType:           strings.TrimSpace(c.QueryParam("action_type")),
		Page:                 maxInt(1, int(parsePositiveInt64(c.QueryParam("page")))),
	}
}

func (s *Server) adminActionLogModels(filter adminActionLogFilter) ([]models.AdminActionLog, error) {
	if s.db == nil {
		return []models.AdminActionLog{}, nil
	}
	query := s.db.Preload("Account").Model(&models.AdminActionLog{})
	query = applyAdminActionLogFilter(query, filter)
	var logs []models.AdminActionLog
	offset := (filter.Page - 1) * adminRailsDefaultPageSize
	err := query.Order("id DESC").Limit(adminRailsDefaultPageSize).Offset(offset).Find(&logs).Error
	return logs, err
}

func applyAdminActionLogFilter(query *gorm.DB, filter adminActionLogFilter) *gorm.DB {
	if filter.AccountIDPresent && !filter.AccountIDValid {
		query = query.Where("1 = 0")
	}
	if filter.AccountIDPresent && filter.AccountIDValid {
		query = query.Where("account_id = ?", filter.AccountID)
	}
	if filter.TargetAccountPresent && !filter.TargetAccountValid {
		query = query.Where("1 = 0")
	}
	if filter.TargetAccountPresent && filter.TargetAccountValid {
		query = query.Where("(target_type = ? AND target_id = ?) OR (target_type = ? AND target_id IN (SELECT id FROM users WHERE account_id = ?))", "Account", filter.TargetAccountID, "User", filter.TargetAccountID)
	}
	if actionType, ok := adminActionLogActionTypes[filter.ActionType]; ok {
		query = query.Where("target_type = ? AND action = ?", actionType.TargetType, actionType.Action)
	}
	return query
}

func (s *Server) adminActionLogAuditableAccounts() ([]models.Account, error) {
	if s.db == nil {
		return []models.Account{}, nil
	}
	var accounts []models.Account
	err := s.db.Model(&models.Account{}).
		Where("id IN (?)", s.db.Model(&models.AdminActionLog{}).Select("distinct account_id")).
		Select("id, username").
		Order("username ASC").
		Find(&accounts).Error
	return accounts, err
}

func parsePositiveInt64(value string) int64 {
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || parsed < 1 {
		return 0
	}
	return parsed
}

func parseAdminActionLogIDParam(value string) (int64, bool, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false, false
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, true, false
	}
	return parsed, true, true
}

func maxInt(a int, b int) int {
	if a > b {
		return a
	}
	return b
}

func adminActionLogsHTML(logs []models.AdminActionLog, accounts []models.Account, filter adminActionLogFilter, notice string, errorText string, locale ...string) string {
	return adminActionLogsHTMLWithConfig(config.Config{}, logs, accounts, filter, notice, errorText, locale...)
}

func adminActionLogsHTMLWithConfig(cfg config.Config, logs []models.AdminActionLog, accounts []models.Account, filter adminActionLogFilter, notice string, errorText string, locale ...string) string {
	loc := settingsLocaleArgOrEnglish(locale...)
	var accountOptions strings.Builder
	accountOptions.WriteString(`<option value="">` + html.EscapeString(adminT(loc, "generic.all", "All")) + `</option>`)
	for _, account := range accounts {
		selected := ""
		if adminActionLogAccountIDActive(filter) && filter.AccountID == account.ID {
			selected = ` selected`
		}
		accountOptions.WriteString(`<option value="` + strconv.FormatInt(account.ID, 10) + `"` + selected + `>` + html.EscapeString(account.Username) + `</option>`)
	}
	var actionOptions strings.Builder
	actionOptions.WriteString(`<option value="">` + html.EscapeString(adminT(loc, "generic.all", "All")) + `</option>`)
	for _, key := range adminActionLogActionTypeOrder {
		selected := ""
		if filter.ActionType == key {
			selected = ` selected`
		}
		actionOptions.WriteString(`<option value="` + html.EscapeString(key) + `"` + selected + `>` + html.EscapeString(adminActionLogActionTypeLabel(loc, key)) + `</option>`)
	}
	var rows strings.Builder
	if len(logs) == 0 {
		rows.WriteString(`<div class="muted-hint center-text">` + html.EscapeString(adminT(loc, "admin.action_logs.empty", "No logs found.")) + `</div>`)
	} else {
		rows.WriteString(`<div class="report-notes">`)
		for _, log := range logs {
			avatar := statusEmbedAccountAvatarURLWithConfig(cfg, log.Account)
			rows.WriteString(`<div class="log-entry"><div class="log-entry__header"><div class="log-entry__avatar"><img src="` + html.EscapeString(avatar) + `" alt="" width="40" height="40" class="avatar"></div><div class="log-entry__content"><div class="log-entry__title">` + html.EscapeString(adminActionLogTitle(log, loc)) + `</div><div class="log-entry__timestamp"><time class="formatted" datetime="` + html.EscapeString(log.CreatedAt.Format("2006-01-02T15:04:05Z07:00")) + `">` + html.EscapeString(log.CreatedAt.Format("2006-01-02 15:04:05 UTC")) + `</time></div></div></div></div>`)
		}
		rows.WriteString(`</div>`)
	}
	targetAccountValue := ""
	if adminActionLogTargetAccountIDActive(filter) {
		targetAccountValue = strconv.FormatInt(filter.TargetAccountID, 10)
	}
	targetAccountField := ""
	if targetAccountValue != "" {
		targetAccountField = `<input type="hidden" name="target_account_id" value="` + html.EscapeString(targetAccountValue) + `">`
	}
	body := `<form method="get" action="/admin/action_logs" class="simple_form">
  ` + targetAccountField + `
  <div class="filters">
    <div class="filter-subset filter-subset--with-select"><strong>` + html.EscapeString(adminT(loc, "admin.action_logs.filter_by_user", "Filter by user")) + `</strong><div class="input select optional"><select name="account_id" id="account_id">` + accountOptions.String() + `</select></div></div>
    <div class="filter-subset filter-subset--with-select"><strong>` + html.EscapeString(adminT(loc, "admin.action_logs.filter_by_action", "Filter by action")) + `</strong><div class="input select optional"><select name="action_type" id="action_type">` + actionOptions.String() + `</select></div></div>
  </div>
</form>
` + rows.String() + adminActionLogsPaginationHTML(filter, len(logs) == adminRailsDefaultPageSize, loc)
	return authPageHTML(adminT(loc, "admin.action_logs.title", "Audit log"), notice, errorText, body, loc)
}

func adminActionLogsPaginationHTML(filter adminActionLogFilter, hasNext bool, locale ...string) string {
	loc := settingsLocaleArgOrEnglish(locale...)
	var links []string
	if filter.Page > 1 {
		params := adminActionLogPageParams(filter, filter.Page-1)
		links = append(links, `<a href="/admin/action_logs?`+html.EscapeString(params.Encode())+`">`+html.EscapeString(settingsT(loc, "pagination.prev", "Previous"))+`</a>`)
	}
	if hasNext {
		params := adminActionLogPageParams(filter, maxInt(1, filter.Page)+1)
		links = append(links, `<a href="/admin/action_logs?`+html.EscapeString(params.Encode())+`">`+html.EscapeString(settingsT(loc, "pagination.next", "Next"))+`</a>`)
	}
	if len(links) == 0 {
		return ""
	}
	return `<nav class="pagination">` + strings.Join(links, " ") + `</nav>`
}

func adminActionLogPageParams(filter adminActionLogFilter, page int) url.Values {
	params := url.Values{}
	if adminActionLogAccountIDActive(filter) {
		params.Set("account_id", strconv.FormatInt(filter.AccountID, 10))
	}
	if adminActionLogTargetAccountIDActive(filter) {
		params.Set("target_account_id", strconv.FormatInt(filter.TargetAccountID, 10))
	}
	if filter.ActionType != "" {
		params.Set("action_type", filter.ActionType)
	}
	params.Set("page", strconv.Itoa(maxInt(1, page)))
	return params
}

func adminActionLogAccountIDActive(filter adminActionLogFilter) bool {
	return (filter.AccountIDPresent && filter.AccountIDValid) || (!filter.AccountIDPresent && filter.AccountID != 0)
}

func adminActionLogTargetAccountIDActive(filter adminActionLogFilter) bool {
	return (filter.TargetAccountPresent && filter.TargetAccountValid) || (!filter.TargetAccountPresent && filter.TargetAccountID != 0)
}

func adminActionLogTitle(log models.AdminActionLog, locale ...string) string {
	loc := settingsLocaleArgOrEnglish(locale...)
	account := "unknown"
	if log.Account.Username != "" {
		account = log.Account.Username
	} else if log.AccountID.Valid {
		account = "account #" + strconv.FormatInt(log.AccountID.Int64, 10)
	}
	target := logTargetLabel(log)
	if target == "" {
		return account + " " + log.Action
	}
	actionKey := log.Action + "_" + adminActionLogLocaleTargetSuffix(log.TargetType.String)
	if log.TargetType.Valid && actionKey != log.Action+"_" {
		value := webT(loc, "admin.action_logs.actions."+actionKey+"_html", map[string]string{"name": account, "target": target})
		if value != "admin.action_logs.actions."+actionKey+"_html" && strings.TrimSpace(value) != "" {
			return value
		}
	}
	return account + " " + log.Action + " " + target
}

func adminActionLogActionTypeLabel(locale string, key string) string {
	return adminT(locale, "admin.action_logs.action_types."+key, key)
}

func adminActionLogLocaleTargetSuffix(targetType string) string {
	switch targetType {
	case "Account":
		return "account"
	case "AccountModerationNote":
		return "account_moderation_note"
	case "AccountWarning":
		return "account_warning"
	case "Announcement":
		return "announcement"
	case "Appeal":
		return "appeal"
	case "CanonicalEmailBlock":
		return "canonical_email_block"
	case "CustomEmoji":
		return "custom_emoji"
	case "DomainAllow":
		return "domain_allow"
	case "DomainBlock":
		return "domain_block"
	case "EmailDomainBlock":
		return "email_domain_block"
	case "Instance":
		return "instance"
	case "IpBlock":
		return "ip_block"
	case "PreviewCard":
		return "preview_card"
	case "PreviewCardProvider":
		return "preview_card_provider"
	case "Report":
		return "report"
	case "Status":
		return "status"
	case "UnavailableDomain":
		return "unavailable_domain"
	case "User":
		return "user"
	case "UserRole":
		return "user_role"
	default:
		return strings.ToLower(targetType)
	}
}

func logTargetLabel(log models.AdminActionLog) string {
	if log.HumanIdentifier.Valid && log.HumanIdentifier.String != "" {
		return log.HumanIdentifier.String
	}
	if log.TargetType.Valid && log.TargetID.Valid {
		return log.TargetType.String + " #" + strconv.FormatInt(log.TargetID.Int64, 10)
	}
	return ""
}
