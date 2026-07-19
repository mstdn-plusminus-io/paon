package api

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestAdminActionLogsRequiresWebSession(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/action_logs?action_type=create_announcement", nil)
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	want := "/auth/sign_in?redirect_to=" + url.QueryEscape("/admin/action_logs?action_type=create_announcement")
	if got := rec.Header().Get("Location"); got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}
}

func TestParseAdminActionLogFilter(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/admin/action_logs?account_id=4&target_account_id=5&action_type=create_announcement&page=3", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	got := parseAdminActionLogFilter(c)
	want := adminActionLogFilter{AccountID: 4, AccountIDPresent: true, AccountIDValid: true, TargetAccountID: 5, TargetAccountPresent: true, TargetAccountValid: true, ActionType: "create_announcement", Page: 3}
	if got != want {
		t.Fatalf("filter = %#v, want %#v", got, want)
	}
}

func TestAdminActionLogActionTypeMapIncludesRailsKeys(t *testing.T) {
	tests := map[string]adminActionType{
		"approve_appeal":               {TargetType: "Appeal", Action: "approve"},
		"reject_appeal":                {TargetType: "Appeal", Action: "reject"},
		"assigned_to_self_report":      {TargetType: "Report", Action: "assigned_to_self"},
		"approve_user":                 {TargetType: "User", Action: "approve"},
		"reject_user":                  {TargetType: "User", Action: "reject"},
		"create_announcement":          {TargetType: "Announcement", Action: "create"},
		"suspend_account":              {TargetType: "Account", Action: "suspend"},
		"change_email_user":            {TargetType: "User", Action: "change_email"},
		"change_role_user":             {TargetType: "User", Action: "change_role"},
		"confirm_user":                 {TargetType: "User", Action: "confirm"},
		"create_user_role":             {TargetType: "UserRole", Action: "create"},
		"destroy_status":               {TargetType: "Status", Action: "destroy"},
		"destroy_user_role":            {TargetType: "UserRole", Action: "destroy"},
		"disable_2fa_user":             {TargetType: "User", Action: "disable_2fa"},
		"resolve_report":               {TargetType: "Report", Action: "resolve"},
		"remove_avatar_user":           {TargetType: "User", Action: "remove_avatar"},
		"resend_user":                  {TargetType: "User", Action: "resend"},
		"reset_password_user":          {TargetType: "User", Action: "reset_password"},
		"silence_account":              {TargetType: "Account", Action: "silence"},
		"update_user_role":             {TargetType: "UserRole", Action: "update"},
		"update_status":                {TargetType: "Status", Action: "update"},
		"unblock_email_account":        {TargetType: "Account", Action: "unblock_email"},
		"create_canonical_email_block": {TargetType: "CanonicalEmailBlock", Action: "create"},
		"destroy_canonical_email_block": {
			TargetType: "CanonicalEmailBlock",
			Action:     "destroy",
		},
	}
	for key, want := range tests {
		if got := adminActionLogActionTypes[key]; got != want {
			t.Fatalf("%s = %#v, want %#v", key, got, want)
		}
	}
	for _, key := range []string{
		"approve_account",
		"approve_preview_card",
		"approve_preview_card_provider",
		"approve_status",
		"approve_tag",
		"create_account_moderation_note",
		"destroy_account_moderation_note",
		"reject_account",
		"reject_preview_card",
		"reject_preview_card_provider",
		"reject_status",
		"reject_tag",
		"remove_header_user",
		"update_domain_block",
	} {
		if _, ok := adminActionLogActionTypes[key]; ok {
			t.Fatalf("admin action log action_type map includes non-Rails key %q", key)
		}
	}
	if got, want := len(adminActionLogActionTypes), len(adminActionLogActionTypeOrder); got != want {
		t.Fatalf("admin action log action_type map size = %d, order size = %d", got, want)
	}
	seen := make(map[string]struct{}, len(adminActionLogActionTypeOrder))
	for _, key := range adminActionLogActionTypeOrder {
		if _, ok := adminActionLogActionTypes[key]; !ok {
			t.Fatalf("admin action log order includes unknown key %q", key)
		}
		if _, duplicate := seen[key]; duplicate {
			t.Fatalf("admin action log order includes duplicate key %q", key)
		}
		seen[key] = struct{}{}
	}
}

func TestAdminActionLogActionTypeOptionsUseConfiguredOrder(t *testing.T) {
	html := adminActionLogsHTML(nil, nil, adminActionLogFilter{}, "", "")
	first := strings.Index(html, `value="approve_appeal"`)
	second := strings.Index(html, `value="reject_appeal"`)
	third := strings.Index(html, `value="assigned_to_self_report"`)
	if first < 0 || second < 0 || third < 0 || !(first < second && second < third) {
		t.Fatalf("admin action log action_type options are not rendered in configured order: %s", html)
	}
	src, err := os.ReadFile("admin_action_logs.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, src, "adminActionLogsHTMLWithConfig", "for _, key := range adminActionLogActionTypeOrder") {
		t.Fatal("adminActionLogsHTMLWithConfig must render action_type options from the configured key slice")
	}
}

func TestAdminActionLogsHTMLIncludesFiltersAndRows(t *testing.T) {
	html := adminActionLogsHTML([]models.AdminActionLog{{
		ID:              1,
		AccountID:       sql.NullInt64{Int64: 4, Valid: true},
		Action:          "create",
		TargetType:      sql.NullString{String: "Announcement", Valid: true},
		TargetID:        sql.NullInt64{Int64: 9, Valid: true},
		HumanIdentifier: sql.NullString{String: "Maintenance", Valid: true},
		CreatedAt:       time.Date(2026, 6, 19, 1, 2, 3, 0, time.UTC),
		Account:         models.Account{ID: 4, Username: "admin"},
	}}, []models.Account{{ID: 4, Username: "admin"}}, adminActionLogFilter{AccountID: 4, ActionType: "create_announcement", TargetAccountID: 9}, "saved", "")

	for _, want := range []string{
		"Audit log",
		`action="/admin/action_logs"`,
		`name="account_id"`,
		`value="4" selected`,
		`name="action_type"`,
		`value="create_announcement" selected`,
		`type="hidden" name="target_account_id" value="9"`,
		"admin created new announcement Maintenance",
		"saved",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("action logs html missing %q: %s", want, html)
		}
	}
}

func TestAdminActionLogsHTMLRendersRailsPaginationLinks(t *testing.T) {
	logs := make([]models.AdminActionLog, adminRailsDefaultPageSize)
	for i := range logs {
		logs[i] = models.AdminActionLog{ID: int64(i + 1), Action: "create"}
	}
	filter := adminActionLogFilter{AccountID: 4, TargetAccountID: 9, ActionType: "create_announcement", Page: 2}
	html := adminActionLogsHTML(logs, []models.Account{{ID: 4, Username: "admin"}}, filter, "", "")

	for _, want := range []string{
		`href="/admin/action_logs?account_id=4&amp;action_type=create_announcement&amp;page=1&amp;target_account_id=9"`,
		`href="/admin/action_logs?account_id=4&amp;action_type=create_announcement&amp;page=3&amp;target_account_id=9"`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("action logs pagination missing %q: %s", want, html)
		}
	}

	partialHTML := adminActionLogsHTML(logs[:adminRailsDefaultPageSize-1], nil, filter, "", "")
	if strings.Contains(partialHTML, `page=3`) {
		t.Fatalf("partial action log page should not render next link: %s", partialHTML)
	}
}

func TestAdminActionLogModelsUseRailsPageSizeAndOffset(t *testing.T) {
	src, err := os.ReadFile("admin_action_logs.go")
	if err != nil {
		t.Fatal(err)
	}
	code := string(src)
	for _, want := range []string{
		"offset := (filter.Page - 1) * adminRailsDefaultPageSize",
		"Limit(adminRailsDefaultPageSize).Offset(offset)",
	} {
		if !strings.Contains(code, want) {
			t.Fatalf("admin action log source missing %q", want)
		}
	}
}

func TestAdminActionLogTitleFallbacks(t *testing.T) {
	log := models.AdminActionLog{
		AccountID:  sql.NullInt64{Int64: 5, Valid: true},
		Action:     "destroy",
		TargetType: sql.NullString{String: "Status", Valid: true},
		TargetID:   sql.NullInt64{Int64: 10, Valid: true},
	}
	if got, want := adminActionLogTitle(log), "account #5 removed post by Status #10"; got != want {
		t.Fatalf("title = %q, want %q", got, want)
	}
}

func TestAdminActionLogForTargetUsesRailsColumns(t *testing.T) {
	at := time.Date(2026, 6, 19, 1, 2, 3, 0, time.UTC)
	log := adminActionLogForTarget(7, "create", adminAuditLogTarget{
		Type:            "CustomEmoji",
		ID:              42,
		HumanIdentifier: "party",
		RouteParam:      "party",
		Permalink:       "https://example.com/emoji/party",
	}, at)

	if !log.AccountID.Valid || log.AccountID.Int64 != 7 {
		t.Fatalf("account_id = %#v", log.AccountID)
	}
	if log.Action != "create" {
		t.Fatalf("action = %q", log.Action)
	}
	if !log.TargetType.Valid || log.TargetType.String != "CustomEmoji" {
		t.Fatalf("target_type = %#v", log.TargetType)
	}
	if !log.TargetID.Valid || log.TargetID.Int64 != 42 {
		t.Fatalf("target_id = %#v", log.TargetID)
	}
	if !log.HumanIdentifier.Valid || log.HumanIdentifier.String != "party" {
		t.Fatalf("human_identifier = %#v", log.HumanIdentifier)
	}
	if !log.RouteParam.Valid || log.RouteParam.String != "party" {
		t.Fatalf("route_param = %#v", log.RouteParam)
	}
	if !log.Permalink.Valid || log.Permalink.String != "https://example.com/emoji/party" {
		t.Fatalf("permalink = %#v", log.Permalink)
	}
	if !log.CreatedAt.Equal(at) || !log.UpdatedAt.Equal(at) {
		t.Fatalf("timestamps = %s / %s", log.CreatedAt, log.UpdatedAt)
	}
}

func TestDomainBlockAuditLogTargetUsesDomainHumanIdentifier(t *testing.T) {
	target := domainBlockAuditLogTarget(models.DomainBlock{ID: 42, Domain: "remote.example"})
	if target.Type != "DomainBlock" {
		t.Fatalf("type = %q", target.Type)
	}
	if target.ID != 42 {
		t.Fatalf("id = %d", target.ID)
	}
	if target.HumanIdentifier != "remote.example" {
		t.Fatalf("human identifier = %q", target.HumanIdentifier)
	}
}

func TestAppealAuditLogTargetUsesRailsType(t *testing.T) {
	target := appealAuditLogTarget(models.Appeal{ID: 42, AccountWarningID: 9})
	if target.Type != "Appeal" {
		t.Fatalf("type = %q", target.Type)
	}
	if target.ID != 42 {
		t.Fatalf("id = %d", target.ID)
	}
	if target.RouteParam != "9" {
		t.Fatalf("route param = %q", target.RouteParam)
	}
}

func TestStatusAndReportAuditLogTargetsUseRailsTypes(t *testing.T) {
	statusTarget := statusAuditLogTarget(models.Status{ID: 11, Account: models.Account{ID: 2, Username: "alice"}})
	if statusTarget.Type != "Status" || statusTarget.ID != 11 || statusTarget.HumanIdentifier != "alice" {
		t.Fatalf("status target = %#v", statusTarget)
	}
	reportTarget := reportAuditLogTarget(models.Report{ID: 12})
	if reportTarget.Type != "Report" || reportTarget.ID != 12 {
		t.Fatalf("report target = %#v", reportTarget)
	}
}

func TestUserRoleAuditLogTargetUsesRailsTypeAndName(t *testing.T) {
	target := userRoleAuditLogTarget(models.UserRole{ID: 3, Name: "Moderators"})
	if target.Type != "UserRole" || target.ID != 3 || target.HumanIdentifier != "Moderators" {
		t.Fatalf("user role target = %#v", target)
	}
}

func TestAnnouncementAuditLogTargetUsesTextHumanIdentifier(t *testing.T) {
	target := announcementAuditLogTarget(models.Announcement{ID: 4, Text: "Maintenance window"})
	if target.Type != "Announcement" || target.ID != 4 || target.HumanIdentifier != "Maintenance window" {
		t.Fatalf("announcement target = %#v", target)
	}
}

func TestUnavailableDomainAuditLogTargetUsesDomainHumanIdentifier(t *testing.T) {
	target := unavailableDomainAuditLogTarget(models.UnavailableDomain{ID: 5, Domain: "remote.example"})
	if target.Type != "UnavailableDomain" || target.ID != 5 || target.HumanIdentifier != "remote.example" {
		t.Fatalf("unavailable domain target = %#v", target)
	}
}

func TestInstanceAuditLogTargetUsesDomainHumanIdentifier(t *testing.T) {
	target := instanceAuditLogTarget("remote.example")
	if target.Type != "Instance" || target.ID != 0 || target.HumanIdentifier != "remote.example" {
		t.Fatalf("instance target = %#v", target)
	}
}

func TestAdminAccountAuditActionUsesRailsTargets(t *testing.T) {
	account := &models.Account{ID: 7, Username: "alice", User: models.User{ID: 9}}

	action, target, ok := adminAccountAuditAction(account, "suspend")
	if !ok || action != "suspend" || target.Type != "Account" || target.ID != 7 || target.HumanIdentifier != "alice" {
		t.Fatalf("suspend target = %q %#v ok=%v", action, target, ok)
	}

	action, target, ok = adminAccountAuditAction(account, "enable")
	if !ok || action != "enable" || target.Type != "User" || target.ID != 9 {
		t.Fatalf("enable target = %q %#v ok=%v", action, target, ok)
	}

	action, target, ok = adminAccountAuditAction(account, "remove_header")
	if !ok || action != "remove_header" || target.Type != "User" || target.ID != 9 {
		t.Fatalf("remove_header target = %q %#v ok=%v", action, target, ok)
	}

	_, _, ok = adminAccountAuditAction(account, "redownload")
	if ok {
		t.Fatal("redownload should not create an audit target without a Rails action type")
	}
}

func TestBlockAuditLogTargetsUseRailsTypesAndHumanIdentifiers(t *testing.T) {
	tests := []struct {
		name  string
		got   adminAuditLogTarget
		typ   string
		human string
	}{
		{name: "domain allow", got: domainAllowAuditLogTarget(models.DomainAllow{ID: 1, Domain: "allowed.example"}), typ: "DomainAllow", human: "allowed.example"},
		{name: "account moderation note", got: accountModerationNoteAuditLogTarget(models.AccountModerationNote{ID: 8}), typ: "AccountModerationNote", human: ""},
		{name: "email domain block", got: emailDomainBlockAuditLogTarget(models.EmailDomainBlock{ID: 2, Domain: "mail.example"}), typ: "EmailDomainBlock", human: "mail.example"},
		{name: "canonical email block", got: canonicalEmailBlockAuditLogTarget(models.CanonicalEmailBlock{ID: 3, CanonicalEmailHash: "abc123"}), typ: "CanonicalEmailBlock", human: "abc123"},
		{name: "ip block", got: ipBlockAuditLogTarget(models.IPBlock{ID: 4, IP: "192.0.2.0/24"}), typ: "IpBlock", human: "192.0.2.0/24"},
		{name: "tag", got: tagAuditLogTarget(models.Tag{ID: 5, Name: "golang"}), typ: "Tag", human: "golang"},
		{name: "preview card", got: previewCardAuditLogTarget(models.PreviewCard{ID: 6, Title: "Article", URL: "https://example.com/a"}), typ: "PreviewCard", human: "Article"},
		{name: "preview card provider", got: previewCardProviderAuditLogTarget(models.PreviewCardProvider{ID: 7, Domain: "news.example"}), typ: "PreviewCardProvider", human: "news.example"},
	}
	for _, tt := range tests {
		if tt.got.Type != tt.typ || tt.got.HumanIdentifier != tt.human {
			t.Fatalf("%s target = %#v", tt.name, tt.got)
		}
	}
}
