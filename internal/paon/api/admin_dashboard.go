package api

import (
	"encoding/json"
	"html"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	paondb "github.com/mstdn-plusminus-io/paon/internal/paon/db"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

type adminDashboardCounts struct {
	PendingUsers   int64
	PendingReports int64
	PendingTags    int64
	PendingAppeals int64
}

type adminDashboardSystemCheck struct {
	Key      string
	Value    string
	Action   string
	Critical bool
}

type adminDashboardPermissions struct {
	ManageUsers   bool
	ManageReports bool
}

func (s *Server) adminDashboardPage(c *echo.Context) error {
	user, _, handled, err := s.requireFunctionalWebUser(c)
	if handled || err != nil {
		return err
	}
	if !s.userCan(user, rolePermissionViewDashboard) {
		locale := s.webLocale(c, user)
		return c.HTML(http.StatusForbidden, authPageHTML(adminT(locale, "admin.dashboard.title", "Admin dashboard"), "", adminT(locale, "admin.dashboard.not_permitted", "You are not allowed to view the dashboard."), "", locale))
	}
	counts, err := s.adminDashboardCounts()
	if err != nil {
		return err
	}
	locale := s.webLocale(c, user)
	theme := settingsWebTheme(decodeUserSettings(user.Settings.String))
	checks := s.adminDashboardSystemChecks(user)
	permissions := adminDashboardPermissions{
		ManageUsers:   s.userCan(user, rolePermissionManageUsers),
		ManageReports: s.userCan(user, rolePermissionManageReports),
	}
	return c.HTML(http.StatusOK, adminDashboardHTMLWithChecksAndPermissions(counts, checks, permissions, locale, theme))
}

func (s *Server) adminDashboardCounts() (adminDashboardCounts, error) {
	counts := adminDashboardCounts{}
	if s.db == nil {
		return counts, nil
	}
	if err := s.db.Model(&models.User{}).Where("approved = false").Count(&counts.PendingUsers).Error; err != nil {
		return counts, err
	}
	if err := s.db.Model(&models.Report{}).Where("action_taken_at IS NULL").Count(&counts.PendingReports).Error; err != nil {
		return counts, err
	}
	if err := s.db.Model(&models.Tag{}).
		Joins("JOIN tag_trends ON tag_trends.tag_id = tags.id").
		Where("tags.reviewed_at IS NULL AND tags.requested_review_at IS NOT NULL").
		Count(&counts.PendingTags).Error; err != nil {
		return counts, err
	}
	if err := s.db.Model(&models.Appeal{}).Where("approved_at IS NULL AND rejected_at IS NULL").Count(&counts.PendingAppeals).Error; err != nil {
		return counts, err
	}
	return counts, nil
}

func (s *Server) adminDashboardSystemChecks(user *models.User) []adminDashboardSystemCheck {
	if s.db == nil {
		return nil
	}
	checks := make([]adminDashboardSystemCheck, 0, 3)
	if s.userCan(user, rolePermissionViewDevops) && !s.adminDashboardSchemaUpToDate() {
		checks = append(checks, adminDashboardSystemCheck{Key: "database_schema_check"})
	}
	if s.userCan(user, rolePermissionViewDevops) {
		if check, ok := s.adminDashboardMediaPrivacyCheck(); ok {
			checks = append(checks, check)
		}
	}
	if s.userCan(user, rolePermissionViewDevops) {
		if check, ok := s.adminDashboardSidekiqProcessCheck(); ok {
			checks = append(checks, check)
		}
	}
	if s.userCan(user, rolePermissionViewDevops) && s.softwareUpdateCheckEnabled() {
		if check, ok := s.adminDashboardSoftwareVersionCheck(); ok {
			checks = append(checks, check)
		}
	}
	if s.userCan(user, rolePermissionManageRules) && !s.adminDashboardRulesPresent() {
		checks = append(checks, adminDashboardSystemCheck{Key: "rules_check", Action: "/admin/rules"})
	}
	return checks
}

func (s *Server) adminDashboardRulesPresent() bool {
	var count int64
	if err := s.db.Model(&models.Rule{}).Where("deleted_at IS NULL").Count(&count).Error; err != nil {
		return true
	}
	return count > 0
}

func (s *Server) adminDashboardSchemaUpToDate() bool {
	version := adminDashboardRailsSchemaVersion()
	if version == "" {
		return true
	}
	var found string
	if err := s.db.Raw("SELECT version FROM schema_migrations WHERE version = ? LIMIT 1", version).Scan(&found).Error; err != nil {
		return true
	}
	return found == version
}

func adminDashboardRailsSchemaVersion() string {
	return paondb.RequiredMastodonSchemaVersion()
}

func (s *Server) adminDashboardSoftwareVersionCheck() (adminDashboardSystemCheck, bool) {
	updates, err := s.softwareUpdates()
	if err != nil {
		return adminDashboardSystemCheck{}, false
	}
	return s.adminDashboardSoftwareVersionCheckFromUpdates(updates)
}

func (s *Server) adminDashboardSoftwareVersionCheckFromUpdates(updates []models.SoftwareUpdate) (adminDashboardSystemCheck, bool) {
	pending := make([]models.SoftwareUpdate, 0, len(updates))
	current := s.currentSoftwareUpdateVersion()
	for _, update := range updates {
		if compareSoftwareVersions(update.Version, current) > 0 {
			pending = append(pending, update)
		}
	}
	if len(pending) == 0 {
		return adminDashboardSystemCheck{}, false
	}
	check := adminDashboardSystemCheck{Key: "software_version_check", Action: "/admin/software_updates"}
	for _, update := range pending {
		if update.Urgent {
			return adminDashboardSystemCheck{Key: "software_version_critical_check", Action: "/admin/software_updates", Critical: true}, true
		}
		if update.Type == 0 {
			check.Key = "software_version_patch_check"
		}
	}
	return check, true
}

func adminDashboardHTML(counts adminDashboardCounts, localeAndTheme ...string) string {
	return adminDashboardHTMLWithChecksAndPermissions(counts, nil, adminDashboardPermissions{ManageUsers: true, ManageReports: true}, localeAndTheme...)
}

func adminDashboardHTMLWithChecks(counts adminDashboardCounts, checks []adminDashboardSystemCheck, localeAndTheme ...string) string {
	return adminDashboardHTMLWithChecksAndPermissions(counts, checks, adminDashboardPermissions{ManageUsers: true, ManageReports: true}, localeAndTheme...)
}

func adminDashboardHTMLWithChecksAndPermissions(counts adminDashboardCounts, checks []adminDashboardSystemCheck, permissions adminDashboardPermissions, localeAndTheme ...string) string {
	loc := settingsLocaleArgOrEnglish(localeAndTheme...)
	theme := settingsThemeArg(localeAndTheme...)
	title := adminT(loc, "admin.dashboard.title", "Admin dashboard")
	now := time.Now().UTC()
	startAt := now.AddDate(0, 0, -29).Format("2006-01-02")
	endAt := now.Format("2006-01-02")
	retentionStartAt := now.AddDate(0, -6, 0).Format("2006-01-02")
	var body strings.Builder
	body.WriteString(`<div class="content__heading__actions"><span>` + html.EscapeString(startAt) + ` - ` + html.EscapeString(endAt) + `</span></div>`)
	body.WriteString(adminDashboardSystemChecksHTML(checks, loc))
	body.WriteString(`<div class="dashboard">`)
	body.WriteString(`<div class="dashboard__item">` + adminDashboardReactComponent("counter", adminDashboardCounterProps("new_users", startAt, endAt, adminT(loc, "admin.dashboard.new_users", "new users"), "/admin/accounts?origin=local", permissions.ManageUsers)) + `</div>`)
	body.WriteString(`<div class="dashboard__item">` + adminDashboardReactComponent("counter", adminDashboardCounterProps("active_users", startAt, endAt, adminT(loc, "admin.dashboard.active_users", "active users"), "/admin/accounts?origin=local", permissions.ManageUsers)) + `</div>`)
	body.WriteString(`<div class="dashboard__item">` + adminDashboardReactComponent("counter", map[string]any{"measure": "interactions", "start_at": startAt, "end_at": endAt, "label": adminT(loc, "admin.dashboard.interactions", "interactions")}) + `</div>`)
	body.WriteString(`<div class="dashboard__item">` + adminDashboardReactComponent("counter", adminDashboardCounterProps("opened_reports", startAt, endAt, adminT(loc, "admin.dashboard.opened_reports", "reports opened"), "/admin/reports", permissions.ManageReports)) + `</div>`)
	body.WriteString(`<div class="dashboard__item">` + adminDashboardReactComponent("counter", adminDashboardCounterProps("resolved_reports", startAt, endAt, adminT(loc, "admin.dashboard.resolved_reports", "reports resolved"), "/admin/reports?resolved=1", permissions.ManageReports)) + `</div>`)
	body.WriteString(`<div class="dashboard__item">`)
	body.WriteString(adminDashboardQuickAccess("/admin/reports", adminDashboardPendingHTML(loc, "admin.dashboard.pending_reports_html", counts.PendingReports, "pending reports")))
	body.WriteString(adminDashboardQuickAccess("/admin/accounts?status=pending", adminDashboardPendingHTML(loc, "admin.dashboard.pending_users_html", counts.PendingUsers, "pending users")))
	body.WriteString(adminDashboardQuickAccess("/admin/trends/tags?status=pending_review", adminDashboardPendingHTML(loc, "admin.dashboard.pending_tags_html", counts.PendingTags, "pending hashtags")))
	body.WriteString(adminDashboardQuickAccess("/admin/disputes/appeals?status=pending", adminDashboardPendingHTML(loc, "admin.dashboard.pending_appeals_html", counts.PendingAppeals, "pending appeals")))
	body.WriteString(`</div>`)
	body.WriteString(`<div class="dashboard__item">` + adminDashboardReactComponent("dimension", map[string]any{"dimension": "sources", "start_at": startAt, "end_at": endAt, "limit": 8, "label": adminT(loc, "admin.dashboard.sources", "Sign-up sources")}) + `</div>`)
	body.WriteString(`<div class="dashboard__item">` + adminDashboardReactComponent("dimension", map[string]any{"dimension": "languages", "start_at": startAt, "end_at": endAt, "limit": 8, "label": adminT(loc, "admin.dashboard.top_languages", "Top active languages")}) + `</div>`)
	body.WriteString(`<div class="dashboard__item">` + adminDashboardReactComponent("dimension", map[string]any{"dimension": "servers", "start_at": startAt, "end_at": endAt, "limit": 8, "label": adminT(loc, "admin.dashboard.top_servers", "Top active servers")}) + `</div>`)
	body.WriteString(`<div class="dashboard__item dashboard__item--span-double-column">` + adminDashboardReactComponent("retention", map[string]any{"start_at": retentionStartAt, "end_at": endAt, "frequency": "month"}) + `</div>`)
	body.WriteString(`<div class="dashboard__item dashboard__item--span-double-row">` + adminDashboardReactComponent("trends", map[string]any{"limit": 7}) + `</div>`)
	body.WriteString(`<div class="dashboard__item">` + adminDashboardReactComponent("dimension", map[string]any{"dimension": "software_versions", "start_at": startAt, "end_at": endAt, "limit": 4, "label": adminT(loc, "admin.dashboard.software", "Software")}) + `</div>`)
	body.WriteString(`<div class="dashboard__item">` + adminDashboardReactComponent("dimension", map[string]any{"dimension": "space_usage", "start_at": startAt, "end_at": endAt, "limit": 3, "label": adminT(loc, "admin.dashboard.space", "Space usage")}) + `</div>`)
	body.WriteString(`</div>`)
	return adminDashboardShell(title, body.String(), loc, theme)
}

func adminDashboardCounterProps(measure string, startAt string, endAt string, label string, href string, permitted bool) map[string]any {
	props := map[string]any{"measure": measure, "start_at": startAt, "end_at": endAt, "label": label}
	if permitted && href != "" {
		props["href"] = href
	}
	return props
}

func adminDashboardSystemChecksHTML(checks []adminDashboardSystemCheck, locale string) string {
	if len(checks) == 0 {
		return ""
	}
	var out strings.Builder
	out.WriteString(`<div class="flash-message-stack">`)
	for _, check := range checks {
		className := "warning"
		if check.Critical {
			className = "alert"
		}
		value := ""
		if strings.TrimSpace(check.Value) != "" {
			value = `<strong>` + html.EscapeString(check.Value) + `</strong>`
		}
		message := adminTVars(locale, "admin.system_checks."+check.Key+".message_html", check.Key, map[string]string{"value": value})
		out.WriteString(`<div class="flash-message ` + className + `">` + message)
		if strings.TrimSpace(check.Action) != "" {
			label := adminT(locale, "admin.system_checks."+check.Key+".action", "Review")
			out.WriteString(` <a href="` + html.EscapeString(check.Action) + `">` + html.EscapeString(label) + `</a>`)
		}
		out.WriteString(`</div>`)
	}
	out.WriteString(`</div>`)
	return out.String()
}

func adminDashboardReactComponent(name string, props map[string]any) string {
	raw, err := json.Marshal(props)
	if err != nil {
		raw = []byte(`{}`)
	}
	return `<div data-admin-component="` + html.EscapeString(adminDashboardComponentName(name)) + `" data-props="` + html.EscapeString(string(raw)) + `"></div>`
}

func adminDashboardComponentName(name string) string {
	switch name {
	case "counter":
		return "Counter"
	case "dimension":
		return "Dimension"
	case "retention":
		return "Retention"
	case "trends":
		return "Trends"
	default:
		return name
	}
}

func adminDashboardQuickAccess(href string, labelHTML string) string {
	return `<a class="dashboard__quick-access" href="` + html.EscapeString(href) + `"><span>` + labelHTML + `</span><i class="fa fa-chevron-right fw" aria-hidden="true"></i></a>`
}

func adminDashboardPendingHTML(locale string, key string, count int64, fallback string) string {
	text := adminTVars(locale, key+".other", `<strong>%{count}</strong> `+fallback, map[string]string{"count": html.EscapeString(strconv.FormatInt(count, 10))})
	if count == 1 {
		text = adminTVars(locale, key+".one", `<strong>%{count}</strong> `+strings.TrimSuffix(fallback, "s"), map[string]string{"count": "1"})
	}
	return text
}

func adminDashboardShell(title string, body string, locale string, theme string) string {
	a := currentAppAssets()
	if strings.TrimSpace(locale) == "" {
		locale = webDefaultLocaleValue()
	}
	return `<!DOCTYPE html>
<html lang="` + html.EscapeString(locale) + `">
  <head>
    ` + buildAppHead(title, theme) + `
    <script src="` + html.EscapeString(a.adminJS) + `" crossorigin="anonymous" defer></script>
  </head>
  <body class="admin">
    <main role="main">
      <h1>` + html.EscapeString(title) + `</h1>
      ` + body + `
    </main>
    <div class="logo-resources" tabindex="-1" aria-hidden="true"></div>
  </body>
</html>`
}
