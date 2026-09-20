package api

import (
	"encoding/json"
	"errors"
	"html"
	"strconv"
	"strings"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/i18n"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"github.com/mstdn-plusminus-io/paon/internal/paon/serializer"
	"gorm.io/gorm"
)

type settingsHTMLOptions struct {
	Notice                     string
	ErrorText                  string
	Permissions                int64
	SoftwareUpdateCheckEnabled bool
	Functional                 bool
	FunctionalOrMoved          bool
	LimitedFederationMode      bool
	ApplicationName            string
}

func (s *Server) settingsHTML(path string, user models.User, account models.Account) (string, bool, error) {
	user.Account = &account
	return s.settingsHTMLWithOptions(path, user, account, settingsHTMLOptions{
		Functional:            webUserFunctional(user, false),
		FunctionalOrMoved:     webUserFunctional(user, true),
		LimitedFederationMode: s.cfg.LimitedFederationMode,
		ApplicationName:       firstNonEmpty(s.cfg.Title, "Mastodon"),
	})
}

func (s *Server) settingsNavigationForUser(path string, locale string, user *models.User, account *models.Account) (string, error) {
	if user == nil {
		return settingsNavigationHTML(path, locale, settingsHTMLOptions{}), nil
	}
	if s == nil || s.db == nil {
		return settingsNavForLocale(locale), nil
	}
	userCopy := *user
	if account == nil && userCopy.Account != nil {
		account = userCopy.Account
	}
	if account == nil && s != nil && userCopy.AccountID != 0 {
		loaded, err := s.accountForUser(&userCopy)
		if err != nil {
			return "", err
		}
		account = loaded
	}
	if account != nil {
		userCopy.Account = account
	}
	permissions, err := s.computedUserPermissions(&userCopy)
	if err != nil {
		return "", err
	}
	return settingsNavigationHTML(path, locale, settingsHTMLOptions{
		Permissions:           permissions,
		Functional:            webUserFunctional(userCopy, false),
		FunctionalOrMoved:     webUserFunctional(userCopy, true),
		LimitedFederationMode: s.cfg.LimitedFederationMode,
		ApplicationName:       firstNonEmpty(s.settingStringValue("site_title", s.cfg.Title), s.cfg.Title, "Mastodon"),
	}), nil
}

func (s *Server) settingsRenderArgs(path string, locale string, theme string, user *models.User, account *models.Account) ([]string, error) {
	navigation, err := s.settingsNavigationForUser(path, locale, user, account)
	if err != nil {
		return nil, err
	}
	applicationName := firstNonEmpty(s.settingStringValue("site_title", s.cfg.Title), s.cfg.Title, "Mastodon")
	return []string{locale, theme, navigation, applicationName}, nil
}

func (s *Server) settingsHTMLWithOptions(path string, user models.User, account models.Account, options settingsHTMLOptions) (string, bool, error) {
	settings := decodeUserSettings(user.Settings.String)
	locale := i18n.Resolve(firstNonEmpty(userSettingString(user, "locale", ""), nullStringValue(user.Locale, "")), "", s.cfg.Locale())
	theme := settingsWebTheme(settings)
	navigation := settingsNavigationHTML(path, locale, options)
	renderContext := []string{locale, theme, navigation, options.ApplicationName}
	switch path {
	case "/settings/profile":
		return s.settingsProfileHTMLWithMessages(account, options.Notice, options.ErrorText, renderContext...), true, nil
	case "/settings/preferences/appearance":
		return settingsPreferencesAppearanceHTMLWithMessages(user, settings, options.Notice, options.ErrorText, renderContext...), true, nil
	case "/settings/preferences/notifications":
		return settingsPreferencesNotificationsHTMLWithOptions(settings, options.Notice, options.ErrorText, options, renderContext...), true, nil
	case "/settings/preferences/other":
		return settingsPreferencesOtherHTMLWithMessages(user, account, settings, options.Notice, options.ErrorText, renderContext...), true, nil
	case "/settings/privacy":
		return settingsPrivacyHTMLWithMessages(account, settings, options.Notice, options.ErrorText, renderContext...), true, nil
	case "/settings/export":
		backups, err := s.settingsBackups(user.ID)
		if err != nil {
			return "", true, err
		}
		totals, err := s.settingsExportTotals(account)
		if err != nil {
			return "", true, err
		}
		canCreateBackup, err := s.backupCreateAllowed(nil, user.ID, time.Now().UTC())
		return settingsExportHTML(totals, backups, canCreateBackup, renderContext...), true, err
	case "/settings/two_factor_authentication_methods":
		credentials, err := s.settingsWebauthnCredentials(user.ID)
		return settingsTwoFactorMethodsHTML(user, credentials, renderContext...), true, err
	default:
		return "", false, nil
	}
}

func settingsPageShell(title string, nav string, body string, locale string, theme string, scripts ...string) string {
	return settingsPageShellWithHeading(title, nav, body, locale, theme, "", settingsHeadingActions(body, locale), scripts...)
}

func settingsPageShellWithHeading(title string, nav string, body string, locale string, theme string, headingTabs string, headingActions string, scripts ...string) string {
	return settingsPageShellWithHeadingTitle(title, title, nav, body, locale, theme, headingTabs, headingActions, scripts...)
}

func settingsPageShellWithHeadingTitle(pageTitle string, headingTitle string, nav string, body string, locale string, theme string, headingTabs string, headingActions string, scripts ...string) string {
	assets := currentAppAssets()
	scriptHTML := ""
	seenScripts := map[string]bool{}
	for _, script := range append([]string{assets.publicJS, assets.adminJS}, scripts...) {
		if strings.TrimSpace(script) == "" {
			continue
		}
		if seenScripts[script] {
			continue
		}
		seenScripts[script] = true
		scriptHTML += `<script src="` + html.EscapeString(script) + `" crossorigin="anonymous" defer></script>`
	}
	if strings.TrimSpace(locale) == "" {
		locale = webDefaultLocaleValue()
	}
	return `<!DOCTYPE html>
<html lang="` + html.EscapeString(locale) + `">
  <head>
    ` + buildAppHead(pageTitle, theme) + `
  </head>
  <body class="admin theme-` + html.EscapeString(normalizedWebTheme(theme)) + ` no-reduce-motion">
    <div class="admin-wrapper">
      <div class="sidebar-wrapper">
        <div class="sidebar-wrapper__inner">
          <div class="sidebar">
            <a href="/"><img class="logo logo--icon" alt="Mastodon" src="` + html.EscapeString(assets.logoDesktopSVG) + `"></a>
            <div class="sidebar__toggle">
              <div class="sidebar__toggle__logo"><a href="/"><img class="logo logo--wordmark" alt="Mastodon" src="` + html.EscapeString(assets.logoWordmarkSVG) + `"></a></div>
              <a href="#" class="sidebar__toggle__icon" aria-label="Menu" aria-expanded="false"><i class="fa fa-bars" aria-hidden="true"></i><i class="fa fa-times" aria-hidden="true"></i></a>
            </div>
            ` + nav + `
          </div>
        </div>
      </div>
      <div class="content-wrapper">
        <main class="content" role="main">
          <div class="content__heading"><div class="content__heading__row"><h2>` + html.EscapeString(headingTitle) + `</h2>` + headingActions + `</div>` + headingTabs + `</div>
          ` + body + `
        </main>
      </div>
      <div class="sidebar-wrapper sidebar-wrapper--empty"></div>
    </div>
    ` + scriptHTML + `
    <div class="logo-resources" tabindex="-1" aria-hidden="true"></div>
  </body>
</html>`
}

func settingsHeadingActions(body string, locale string) string {
	for _, formID := range []string{"edit_user", "edit_profile", "edit_notification", "edit_preferences", "edit_account", "edit_policy"} {
		if strings.Contains(body, `id="`+formID+`"`) {
			return `<div class="content__heading__actions"><button type="submit" class="button" form="` + formID + `">` + html.EscapeString(settingsT(locale, "generic.save_changes", "Save changes")) + `</button></div>`
		}
	}
	return ""
}

func settingsNav() string {
	return settingsNavForLocale(webDefaultLocaleValue())
}

func settingsNavForLocale(locale string) string {
	return settingsNavigationHTML("", locale, settingsHTMLOptions{Functional: true, FunctionalOrMoved: true, ApplicationName: "Mastodon"})
}

type settingsNavigationItem struct {
	ID         string
	Href       string
	Icon       string
	Label      string
	Target     string
	DataMethod string
	Selected   bool
	Leaf       bool
	Warning    bool
	Children   []settingsNavigationItem
}

func settingsNavigationHTML(path string, locale string, options settingsHTMLOptions) string {
	has := func(permission int64) bool { return options.Permissions&permission == permission }
	hasAny := func(permissions ...int64) bool {
		for _, permission := range permissions {
			if has(permission) {
				return true
			}
		}
		return false
	}
	selected := func(prefixes ...string) bool {
		for _, prefix := range prefixes {
			if path == prefix || strings.HasPrefix(path, prefix+"/") {
				return true
			}
		}
		return false
	}
	item := func(id, href, icon, key, fallback string) settingsNavigationItem {
		return settingsNavigationItem{ID: id, Href: href, Icon: icon, Label: settingsT(locale, key, fallback)}
	}
	backLabel := settingsT(locale, "settings.back", "Back to Mastodon")
	if name := strings.TrimSpace(options.ApplicationName); name != "" && name != "Mastodon" {
		backLabel = strings.ReplaceAll(backLabel, "Mastodon", name)
	}
	items := []settingsNavigationItem{{ID: "web", Href: "/", Icon: "chevron-left", Label: backLabel}}

	if options.Functional {
		profile := item("profile", "/settings/profile", "user", "settings.profile", "Profile")
		profile.Selected = selected("/settings/profile", "/settings/featured_tags", "/settings/verification", "/settings/privacy")
		profile.Leaf = profile.Selected
		items = append(items, profile)

		preferences := item("preferences", "/settings/preferences", "cog", "settings.preferences", "Preferences")
		preferences.Selected = selected("/settings/preferences")
		if preferences.Selected {
			preferences.Children = []settingsNavigationItem{
				settingsNavigationLeaf("appearance", "/settings/preferences/appearance", "desktop", settingsT(locale, "settings.appearance", "Appearance"), path),
				settingsNavigationLeaf("notifications", "/settings/preferences/notifications", "bell", settingsT(locale, "settings.notifications", "Notifications"), path),
				settingsNavigationLeaf("other", "/settings/preferences/other", "cog", settingsT(locale, "preferences.other", "Other"), path),
			}
		}
		items = append(items, preferences)
		relationships := item("relationships", "/relationships", "users", "settings.relationships", "Follows and followers")
		relationships.Selected = selected("/relationships", "/severed_relationships")
		if relationships.Selected {
			relationships.Children = []settingsNavigationItem{
				settingsNavigationLeaf("current_relationships", "/relationships", "users", settingsT(locale, "settings.relationships", "Follows and followers"), path),
				settingsNavigationLeaf("severed_relationships", "/severed_relationships", "unlink", settingsT(locale, "settings.severed_relationships", "Severed relationships"), path),
			}
		}
		filters := item("filters", "/filters", "filter", "filters.index.title", "Filters")
		filters.Selected, filters.Leaf = selected("/filters"), selected("/filters")
		items = append(items, relationships, filters)
	}
	if options.FunctionalOrMoved {
		cleanup := item("statuses_cleanup", "/statuses_cleanup", "history", "settings.statuses_cleanup", "Automated post deletion")
		cleanup.Selected, cleanup.Leaf = selected("/statuses_cleanup"), selected("/statuses_cleanup")
		items = append(items, cleanup)
	}

	security := item("security", "/auth/edit", "lock", "settings.account", "Account")
	security.Selected = selected("/auth/edit", "/settings/delete", "/settings/migration", "/settings/aliases", "/settings/login_activities", "/disputes", "/settings/two_factor_authentication_methods", "/settings/otp_authentication", "/settings/security_keys", "/oauth/authorized_applications")
	if security.Selected {
		password := settingsNavigationLeaf("password", "/auth/edit", "lock", settingsT(locale, "settings.account_settings", "Account settings"), path)
		password.Selected = selected("/auth/edit", "/settings/delete", "/settings/migration", "/settings/aliases", "/settings/login_activities", "/disputes")
		password.Leaf = password.Selected
		twoFactor := settingsNavigationLeaf("two_factor_authentication", "/settings/two_factor_authentication_methods", "mobile", settingsT(locale, "settings.two_factor_authentication", "Two-factor authentication"), path)
		twoFactor.Selected = selected("/settings/two_factor_authentication_methods", "/settings/otp_authentication", "/settings/security_keys")
		twoFactor.Leaf = twoFactor.Selected
		security.Children = []settingsNavigationItem{
			password,
			twoFactor,
			settingsNavigationLeaf("authorized_apps", "/oauth/authorized_applications", "list", settingsT(locale, "settings.authorized_apps", "Authorized apps"), path),
		}
	}
	items = append(items, security)

	data := item("data", "/settings/export", "cloud-download", "settings.import_and_export", "Import and export")
	data.Selected = selected("/settings/export", "/settings/imports")
	if data.Selected {
		if options.Functional {
			data.Children = append(data.Children, settingsNavigationLeaf("import", "/settings/imports", "cloud-upload", settingsT(locale, "settings.import", "Import"), path))
		}
		data.Children = append(data.Children, settingsNavigationLeaf("export", "/settings/export", "cloud-download", settingsT(locale, "settings.export", "Export"), path))
	}
	items = append(items, data)

	if options.Functional && has(rolePermissionInviteUsers) {
		invites := item("invites", "/invites", "user-plus", "invites.title", "Invite people")
		invites.Selected, invites.Leaf = selected("/invites"), selected("/invites")
		items = append(items, invites)
	}
	if options.Functional {
		development := item("development", "/settings/applications", "code", "settings.development", "Development")
		development.Selected, development.Leaf = selected("/settings/applications"), selected("/settings/applications")
		items = append(items, development)
	}

	if has(rolePermissionManageTaxonomies) {
		trends := item("trends", "/admin/trends/statuses", "fire", "admin.trends.title", "Trends")
		trends.Selected = selected("/admin/trends", "/admin/follow_recommendations")
		if trends.Selected {
			trends.Children = []settingsNavigationItem{
				settingsNavigationLeaf("statuses", "/admin/trends/statuses", "comments-o", settingsT(locale, "admin.trends.statuses.title", "Posts"), path),
				settingsNavigationLeaf("tags", "/admin/trends/tags", "hashtag", settingsT(locale, "admin.trends.tags.title", "Hashtags"), path),
				settingsNavigationLeaf("links", "/admin/trends/links", "newspaper-o", settingsT(locale, "admin.trends.links.title", "Links"), path),
				settingsNavigationLeaf("follow_recommendations", "/admin/follow_recommendations", "user-plus", settingsT(locale, "admin.follow_recommendations.title", "Follow recommendations"), path),
			}
		}
		items = append(items, trends)
	}

	moderationPermissions := []int64{rolePermissionManageReports, rolePermissionManageAppeals, rolePermissionViewAuditLog, rolePermissionManageUsers, rolePermissionManageInvites, rolePermissionManageTaxonomies, rolePermissionManageFederation, rolePermissionManageBlocks}
	if hasAny(moderationPermissions...) {
		moderation := item("moderation", settingsModerationNavigationHref(options.Permissions), "gavel", "moderation.title", "Moderation")
		moderation.Selected = selected("/admin/reports", "/admin/accounts", "/admin/pending_accounts", "/admin/disputes", "/admin/users", "/admin/tags", "/admin/invites", "/admin/instances", "/admin/domain_blocks", "/admin/domain_allows", "/admin/email_domain_blocks", "/admin/ip_blocks", "/admin/action_logs")
		if moderation.Selected {
			moderation.Children = settingsModerationNavigationChildren(path, locale, options)
		}
		items = append(items, moderation)
	}

	adminPermissions := []int64{rolePermissionViewDashboard, rolePermissionManageSettings, rolePermissionManageRules, rolePermissionManageRoles, rolePermissionManageAnnouncements, rolePermissionManageCustomEmojis, rolePermissionManageWebhooks, rolePermissionManageFederation}
	if hasAny(adminPermissions...) {
		admin := item("admin", settingsAdminNavigationHref(options.Permissions), "cogs", "admin.title", "Administration")
		admin.Selected = selected("/admin/dashboard", "/admin/settings", "/admin/rules", "/admin/warning_presets", "/admin/roles", "/admin/announcements", "/admin/custom_emojis", "/admin/webhooks", "/admin/relays")
		if admin.Selected {
			admin.Children = settingsAdminNavigationChildren(path, locale, options)
		}
		items = append(items, admin)
	}
	if has(rolePermissionViewDevops) {
		asynq := settingsNavigationItem{ID: "asynq", Href: "/asynq", Icon: "tasks", Label: "Asynq"}
		asynq.Target = "_blank"
		asynq.Selected = selected("/asynq")
		asynq.Leaf = asynq.Selected
		items = append(items, asynq)
	}
	items = append(items, settingsNavigationItem{ID: "logout", Href: "/auth/sign_out", Icon: "sign-out", Label: settingsT(locale, "auth.logout", "Logout"), DataMethod: "delete"})

	var out strings.Builder
	out.WriteString(`<ul>`)
	for _, navigationItem := range items {
		settingsWriteNavigationItem(&out, navigationItem)
	}
	out.WriteString(`</ul>`)
	return out.String()
}

func settingsNavigationLeaf(id, href, icon, label, path string) settingsNavigationItem {
	matchHref := strings.SplitN(href, "?", 2)[0]
	selected := path == matchHref || strings.HasPrefix(path, matchHref+"/")
	return settingsNavigationItem{ID: id, Href: href, Icon: icon, Label: label, Selected: selected, Leaf: selected}
}

func settingsWriteNavigationItem(out *strings.Builder, item settingsNavigationItem) {
	classes := make([]string, 0, 3)
	if item.Selected {
		classes = append(classes, "selected")
	}
	if item.Leaf {
		classes = append(classes, "simple-navigation-active-leaf")
	}
	if item.Warning {
		classes = append(classes, "warning")
	}
	out.WriteString(`<li id="` + html.EscapeString(item.ID) + `"`)
	if len(classes) > 0 {
		out.WriteString(` class="` + html.EscapeString(strings.Join(classes, " ")) + `"`)
	}
	out.WriteString(`><a`)
	if item.Selected {
		out.WriteString(` class="selected"`)
	}
	if item.Target != "" {
		out.WriteString(` target="` + html.EscapeString(item.Target) + `"`)
	}
	if item.DataMethod != "" {
		out.WriteString(` data-method="` + html.EscapeString(item.DataMethod) + `"`)
	}
	out.WriteString(` href="` + html.EscapeString(item.Href) + `"><i class="fa fa-` + html.EscapeString(item.Icon) + ` fa-fw"></i>` + html.EscapeString(item.Label) + `</a>`)
	if len(item.Children) > 0 {
		out.WriteString(`<ul>`)
		for _, child := range item.Children {
			settingsWriteNavigationItem(out, child)
		}
		out.WriteString(`</ul>`)
	}
	out.WriteString(`</li>`)
}

func settingsNavigationArg(localeAndTheme []string, locale string) string {
	if len(localeAndTheme) > 2 && strings.TrimSpace(localeAndTheme[2]) != "" {
		return localeAndTheme[2]
	}
	return settingsNavForLocale(locale)
}

func settingsApplicationNameArg(localeAndTheme []string) string {
	if len(localeAndTheme) > 3 && strings.TrimSpace(localeAndTheme[3]) != "" {
		return strings.TrimSpace(localeAndTheme[3])
	}
	return "Mastodon"
}

func settingsModerationNavigationHref(permissions int64) string {
	for _, candidate := range []struct {
		permission int64
		href       string
	}{
		{rolePermissionManageReports, "/admin/reports"},
		{rolePermissionManageAppeals, "/admin/disputes/appeals"},
		{rolePermissionManageUsers, "/admin/accounts?origin=local"},
		{rolePermissionManageTaxonomies, "/admin/tags"},
		{rolePermissionManageInvites, "/admin/invites"},
		{rolePermissionManageFederation, "/admin/instances?limited=1"},
		{rolePermissionManageBlocks, "/admin/email_domain_blocks"},
		{rolePermissionViewAuditLog, "/admin/action_logs"},
	} {
		if permissions&candidate.permission == candidate.permission {
			return candidate.href
		}
	}
	return "/admin/dashboard"
}

func settingsModerationNavigationChildren(path, locale string, options settingsHTMLOptions) []settingsNavigationItem {
	has := func(permission int64) bool { return options.Permissions&permission == permission }
	children := make([]settingsNavigationItem, 0, 8)
	add := func(permission int64, id, href, icon, key, fallback string) {
		if has(permission) {
			children = append(children, settingsNavigationLeaf(id, href, icon, settingsT(locale, key, fallback), path))
		}
	}
	add(rolePermissionManageReports, "reports", "/admin/reports", "flag", "admin.reports.title", "Reports")
	add(rolePermissionManageAppeals, "appeals", "/admin/disputes/appeals", "commenting-o", "admin.disputes.appeals.title", "Appeals")
	add(rolePermissionManageUsers, "accounts", "/admin/accounts?origin=local", "users", "admin.accounts.title", "Accounts")
	add(rolePermissionManageTaxonomies, "moderated_tags", "/admin/tags", "hashtag", "admin.tags.title", "Hashtags")
	add(rolePermissionManageInvites, "invites", "/admin/invites", "user-plus", "admin.invites.title", "Invites")
	instancesHref := "/admin/instances?limited=1"
	if options.LimitedFederationMode {
		instancesHref = "/admin/instances"
	}
	add(rolePermissionManageFederation, "instances", instancesHref, "cloud", "admin.instances.title", "Federation")
	add(rolePermissionManageBlocks, "email_domain_blocks", "/admin/email_domain_blocks", "envelope", "admin.email_domain_blocks.title", "E-mail domain blocks")
	add(rolePermissionManageBlocks, "ip_blocks", "/admin/ip_blocks", "ban", "admin.ip_blocks.title", "IP blocks")
	add(rolePermissionViewAuditLog, "action_logs", "/admin/action_logs", "bars", "admin.action_logs.title", "Audit log")
	return children
}

func settingsAdminNavigationHref(permissions int64) string {
	for _, candidate := range []struct {
		permission int64
		href       string
	}{
		{rolePermissionViewDashboard, "/admin/dashboard"},
		{rolePermissionManageSettings, "/admin/settings"},
		{rolePermissionManageRules, "/admin/rules"},
		{rolePermissionManageRoles, "/admin/roles"},
		{rolePermissionManageAnnouncements, "/admin/announcements"},
		{rolePermissionManageCustomEmojis, "/admin/custom_emojis"},
		{rolePermissionManageWebhooks, "/admin/webhooks"},
		{rolePermissionManageFederation, "/admin/relays"},
	} {
		if permissions&candidate.permission == candidate.permission {
			return candidate.href
		}
	}
	return "/admin/dashboard"
}

func settingsAdminNavigationChildren(path, locale string, options settingsHTMLOptions) []settingsNavigationItem {
	has := func(permission int64) bool { return options.Permissions&permission == permission }
	children := make([]settingsNavigationItem, 0, 8)
	add := func(permission int64, id, href, icon, key, fallback string) {
		if has(permission) {
			children = append(children, settingsNavigationLeaf(id, href, icon, settingsT(locale, key, fallback), path))
		}
	}
	add(rolePermissionViewDashboard, "dashboard", "/admin/dashboard", "tachometer", "admin.dashboard.title", "Dashboard")
	add(rolePermissionManageSettings, "settings", "/admin/settings", "cogs", "admin.settings.title", "Server settings")
	add(rolePermissionManageRules, "rules", "/admin/rules", "gavel", "admin.rules.title", "Server rules")
	add(rolePermissionManageSettings, "warning_presets", "/admin/warning_presets", "warning", "admin.warning_presets.title", "Warning presets")
	add(rolePermissionManageRoles, "roles", "/admin/roles", "vcard", "admin.roles.title", "Roles")
	add(rolePermissionManageAnnouncements, "announcements", "/admin/announcements", "bullhorn", "admin.announcements.title", "Announcements")
	add(rolePermissionManageCustomEmojis, "custom_emojis", "/admin/custom_emojis", "smile-o", "admin.custom_emojis.title", "Custom emojis")
	add(rolePermissionManageWebhooks, "webhooks", "/admin/webhooks", "inbox", "admin.webhooks.title", "Webhooks")
	if !options.LimitedFederationMode {
		add(rolePermissionManageFederation, "relays", "/admin/relays", "exchange", "admin.relays.title", "Relays")
	}
	return children
}

func (s *Server) settingsProfileHTML(account models.Account, locale string, theme string) string {
	return settingsProfileHTMLWithConfigMessages(s.cfg, account, s.packAssetPath("public.js"), "", "", locale, theme)
}

func (s *Server) settingsProfileHTMLWithMessages(account models.Account, notice string, errorText string, locale ...string) string {
	return settingsProfileHTMLWithConfigMessages(s.cfg, account, s.packAssetPath("public.js"), notice, errorText, locale...)
}

func settingsProfileHTMLWithConfig(cfg config.Config, account models.Account, publicScriptPath string, locale ...string) string {
	return settingsProfileHTMLWithConfigMessages(cfg, account, publicScriptPath, "", "", locale...)
}

func settingsProfileHTMLWithConfigMessages(cfg config.Config, account models.Account, publicScriptPath string, notice string, errorText string, locale ...string) string {
	loc := settingsLocaleArg(locale...)
	theme := settingsThemeArg(locale...)
	view := serializer.AccountFromModel(cfg, account)
	fields := settingsProfileFields(account.Fields)
	for len(fields) < 4 {
		fields = append(fields, profileField{})
	}
	body := settingsInlineFlashHTML(notice, errorText) + `<form class="simple_form edit_account" novalidate="novalidate" method="post" action="/settings/profile" enctype="multipart/form-data" id="edit_profile"><input type="hidden" name="_method" value="put">`
	body += `<p class="lead">` + settingsT(loc, "edit_profile.hint_html", "<strong>Customize what people see on your public profile and next to your posts.</strong> Other people are more likely to follow you back and interact with you when you have a filled out profile and a profile picture.") + `</p>`
	body += `<h4>` + html.EscapeString(settingsT(loc, "edit_profile.basic_information", "Basic information")) + `</h4><div class="fields-row"><div class="fields-row__column fields-row__column-6">`
	body += settingsBlockTextInput(settingsT(loc, "simple_form.labels.defaults.display_name", "Display name"), settingsT(loc, "simple_form.hints.account.display_name", "Your full name or your fun name."), "account[display_name]", account.DisplayName, `maxlength="30" data-default="`+html.EscapeString(account.Username)+`"`)
	body += settingsBlockTextarea(settingsT(loc, "simple_form.labels.defaults.note", "Bio"), settingsT(loc, "simple_form.hints.account.note", "You can @mention other people or #hashtags."), "account[note]", account.Note, `maxlength="500"`)
	body += `</div><div class="fields-row__column fields-group fields-row__column-6"><div class="input with_block_label"><label>` + html.EscapeString(settingsT(loc, "simple_form.labels.defaults.fields", "Extra fields")) + `</label><span class="hint">` + html.EscapeString(settingsT(loc, "simple_form.hints.account.fields", "Your homepage, pronouns, age, anything you want.")) + `</span>`
	for i := 0; i < 4; i++ {
		body += `<div class="row">` + settingsCompactTextInput(settingsT(loc, "simple_form.labels.account.fields.name", "Label"), "account[fields_attributes]["+strconv.Itoa(i)+"][name]", fields[i].Name, `maxlength="255"`) + settingsCompactTextInput(settingsT(loc, "simple_form.labels.account.fields.value", "Content"), "account[fields_attributes]["+strconv.Itoa(i)+"][value]", fields[i].Value, `maxlength="255"`) + `</div>`
	}
	body += `</div></div></div>`
	body += settingsProfileImageRow("avatar", settingsT(loc, "simple_form.labels.defaults.avatar", "Avatar"), settingsTVars(loc, "simple_form.hints.defaults.avatar", "PNG, GIF or JPG. At most %{size}. Will be downscaled to %{dimensions}px", map[string]string{"size": "2 MB", "dimensions": "400x400"}), view.Avatar, account.AvatarFileName.Valid && strings.TrimSpace(account.AvatarFileName.String) != "", loc)
	body += settingsProfileImageRow("header", settingsT(loc, "simple_form.labels.defaults.header", "Header"), settingsTVars(loc, "simple_form.hints.defaults.header", "PNG, GIF or JPG. At most %{size}. Will be downscaled to %{dimensions}px", map[string]string{"size": "2 MB", "dimensions": "1500x500"}), view.Header, account.HeaderFileName.Valid && strings.TrimSpace(account.HeaderFileName.String) != "", loc)
	body += `<h4>` + html.EscapeString(settingsT(loc, "edit_profile.other", "Other")) + `</h4>`
	body += settingsCheckboxFieldWithHint(settingsT(loc, "simple_form.labels.defaults.bot", "This is a bot account"), settingsT(loc, "simple_form.hints.defaults.bot", "This account mainly performs automated actions and might not be monitored."), "account[bot]", account.ActorType.Valid && (account.ActorType.String == "Service" || account.ActorType.String == "Application"))
	body += settingsSubmitButton(settingsT(loc, "generic.save_changes", "Save changes")) + `</form>`
	pageTitle := settingsT(loc, "settings.edit_profile", "Edit profile")
	headingTitle := settingsT(loc, "settings.profile", "Profile")
	return settingsPageShellWithHeadingTitle(pageTitle, headingTitle, settingsNavigationArg(locale, loc), body, loc, theme, settingsProfileTabsHTML("profile", loc), "", publicScriptPath)
}

func settingsInlineFlashHTML(notice string, errorText string) string {
	return settingsFlashHTML(notice, errorText)
}

func settingsTextInput(label string, name string, value string, attrs string) string {
	return `<div class="fields-group"><div class="input with_label"><div class="label_input"><label>` + html.EscapeString(label) + `</label><input name="` + html.EscapeString(name) + `" value="` + html.EscapeString(value) + `" ` + attrs + `></div></div></div>`
}

func settingsTextarea(label string, name string, value string, attrs string) string {
	return `<div class="fields-group"><div class="input with_label"><div class="label_input"><label>` + html.EscapeString(label) + `</label><textarea name="` + html.EscapeString(name) + `" ` + attrs + `>` + html.EscapeString(value) + `</textarea></div></div></div>`
}

func settingsFileInput(label string, name string, id string, previewURL string) string {
	escapedURL := html.EscapeString(previewURL)
	return `<div class="fields-group"><div class="input with_label"><div class="label_input"><label>` + html.EscapeString(label) + `</label><input type="file" id="` + html.EscapeString(id) + `" name="` + html.EscapeString(name) + `" accept="image/*"><img class="fields-group__thumbnail" id="` + html.EscapeString(id) + `-preview" src="` + escapedURL + `" data-original-src="` + escapedURL + `" alt=""></div></div></div>`
}

func settingsDeletePictureForm(kind string, present bool, locale ...string) string {
	if !present {
		return ""
	}
	loc := settingsLocaleArg(locale...)
	escapedKind := html.EscapeString(kind)
	label := settingsT(loc, "generic.delete", "Delete") + " " + kind
	return `<form class="simple_form" method="post" action="/settings/profile/pictures/` + escapedKind + `"><input type="hidden" name="_method" value="delete"><div class="actions"><button type="submit" class="button">` + html.EscapeString(label) + `</button></div></form>`
}

func settingsBlockTextInput(label string, hint string, name string, value string, attrs string) string {
	id := settingsFieldID(name)
	return `<div class="fields-group"><div class="input with_block_label string optional field_with_hint"><label class="string optional" for="` + id + `">` + html.EscapeString(label) + `</label><span class="hint">` + html.EscapeString(hint) + `</span><div class="label_input"><input class="string optional" type="text" id="` + id + `" name="` + html.EscapeString(name) + `" value="` + html.EscapeString(value) + `" ` + attrs + `></div></div></div>`
}

func settingsBlockTextarea(label string, hint string, name string, value string, attrs string) string {
	id := settingsFieldID(name)
	return `<div class="fields-group"><div class="input with_block_label text optional field_with_hint"><label class="text optional" for="` + id + `">` + html.EscapeString(label) + `</label><span class="hint">` + html.EscapeString(hint) + `</span><div class="label_input"><textarea class="text optional" id="` + id + `" name="` + html.EscapeString(name) + `" ` + attrs + `>` + html.EscapeString(value) + `</textarea></div></div></div>`
}

func settingsCompactTextInput(placeholder string, name string, value string, attrs string) string {
	id := settingsFieldID(name)
	return `<div class="input string optional"><input class="string optional" type="text" id="` + id + `" name="` + html.EscapeString(name) + `" value="` + html.EscapeString(value) + `" placeholder="` + html.EscapeString(placeholder) + `" ` + attrs + `></div>`
}

func settingsProfileImageRow(kind string, label string, hint string, previewURL string, present bool, locale string) string {
	id := "account_" + kind
	body := `<div class="fields-row"><div class="fields-row__column fields-row__column-6"><div class="fields-group"><div class="input with_block_label file optional field_with_hint"><label class="file optional" for="` + id + `">` + html.EscapeString(label) + `</label><span class="hint">` + html.EscapeString(hint) + `</span><div class="label_input"><input class="file optional" type="file" id="` + id + `" name="account[` + kind + `]" accept="image/jpeg,image/png,image/gif,image/webp"></div></div></div></div>`
	body += `<div class="fields-row__column fields-row__column-6"><div class="fields-group"><img class="fields-group__thumbnail" id="` + id + `-preview" src="` + html.EscapeString(previewURL) + `" data-original-src="` + html.EscapeString(previewURL) + `" alt="">`
	if present {
		body += `<a data-method="delete" class="link-button link-button--destructive" href="/settings/profile/pictures/` + kind + `"><i class="fa fa-trash fa-fw"></i>` + html.EscapeString(settingsT(locale, "generic.delete", "Delete")) + `</a>`
	}
	return body + `</div></div></div>`
}

func settingsProfileTabsHTML(active string, locale string) string {
	tabs := []struct {
		id    string
		href  string
		icon  string
		key   string
		label string
	}{
		{"profile", "/settings/profile", "user", "settings.edit_profile", "Edit profile"},
		{"privacy", "/settings/privacy", "lock", "privacy.title", "Privacy and reach"},
		{"verification", "/settings/verification", "check", "verification.verification", "Verification"},
		{"featured_tags", "/settings/featured_tags", "hashtag", "settings.featured_tags", "Featured hashtags"},
	}
	var out strings.Builder
	out.WriteString(`<div class="content__heading__tabs"><div>`)
	for _, tab := range tabs {
		out.WriteString(`<a id="` + tab.id + `"`)
		if tab.id == active {
			out.WriteString(` class="selected simple-navigation-active-leaf"`)
		}
		out.WriteString(` href="` + tab.href + `"><i class="fa fa-` + tab.icon + ` fa-fw"></i>` + html.EscapeString(settingsT(locale, tab.key, tab.label)) + `</a>`)
	}
	out.WriteString(`</div></div>`)
	return out.String()
}

func settingsProfileFields(raw []byte) []profileField {
	if len(raw) == 0 {
		return nil
	}
	var decoded []profileField
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil
	}
	return decoded
}

func settingsPreferencesAppearanceHTML(user models.User, settings map[string]any, locale ...string) string {
	return settingsPreferencesAppearanceHTMLWithMessages(user, settings, "", "", locale...)
}

func settingsPreferencesAppearanceHTMLWithMessages(user models.User, settings map[string]any, notice string, errorText string, locale ...string) string {
	loc := settingsLocaleArg(locale...)
	theme := settingsThemeArg(locale...)
	applicationName := settingsApplicationNameArg(locale)
	body := settingsInlineFlashHTML(notice, errorText) + `<form class="simple_form edit_user" novalidate="novalidate" method="post" action="/settings/preferences/appearance" id="edit_user"><input type="hidden" name="_method" value="put">`
	body += `<div class="fields-row">` + settingsFieldRowColumn(settingsLocaleSelectField(user.Locale.String, loc)) + settingsFieldRowColumn(settingsTimeZoneSelectField(user.TimeZone.String, loc)) + `</div>`
	themeField := settingsPreferenceSelectField(settingsT(loc, "simple_form.labels.defaults.setting_theme", "Theme"), "theme", stringSetting(settings, "theme", "system"), []string{"system", "default", "contrast", "mastodon-light", "single-column-chat-dark"}, true, loc)
	body += strings.ReplaceAll(themeField, "Mastodon", applicationName)
	if loc != "en" {
		guideText := settingsT(loc, "appearance.localization.guide_link_text", "Join the translation effort")
		guideURL := settingsT(loc, "appearance.localization.guide_link", "https://crowdin.com/project/mastodon")
		localizationBody := strings.ReplaceAll(settingsT(loc, "appearance.localization.body", "Mastodon is translated by volunteers."), "Mastodon", applicationName)
		body += `<div class="flash-message translation-prompt">` + html.EscapeString(localizationBody) + ` <a href="` + html.EscapeString(guideURL) + `" target="_blank" rel="noopener">` + html.EscapeString(guideText) + `</a></div>`
	}
	body += settingsAppearanceSectionHeading(settingsT(loc, "appearance.advanced_web_interface", "Advanced web interface"))
	body += `<p class="hint">` + html.EscapeString(settingsT(loc, "appearance.advanced_web_interface_hint", "Use the advanced web interface to show multiple columns at once.")) + `</p>`
	body += settingsPreferenceCheckboxField(settingsT(loc, "simple_form.labels.defaults.setting_advanced_layout", "Advanced web interface"), "web.advanced_layout", rawBool(settings["web.advanced_layout"], false))
	body += settingsAppearanceSectionHeading(settingsT(loc, "appearance.animations_and_accessibility", "Animations and accessibility"))
	body += settingsPreferenceCheckboxFieldWithHint(settingsT(loc, "simple_form.labels.defaults.setting_use_pending_items", "Use pending items"), settingsT(loc, "simple_form.hints.defaults.setting_use_pending_items", "Hide timeline updates behind a click instead of automatically scrolling the feed"), "web.use_pending_items", rawBool(settings["web.use_pending_items"], false))
	body += settingsCheckboxGroupHTML(
		settingsCheckboxInputHTML(settingsT(loc, "simple_form.labels.defaults.setting_auto_play_gif", "Auto-play animated GIFs"), "", settingsPreferenceFieldName("web.auto_play"), rawBool(settings["web.auto_play"], false), settingsT(loc, "simple_form.recommended", "Recommended")),
		settingsCheckboxInputHTML(settingsT(loc, "simple_form.labels.defaults.setting_reduce_motion", "Reduce motion"), "", settingsPreferenceFieldName("web.reduce_motion"), rawBool(settings["web.reduce_motion"], false), ""),
		settingsCheckboxInputHTML(settingsT(loc, "simple_form.labels.defaults.setting_disable_swiping", "Disable swiping motions"), "", settingsPreferenceFieldName("web.disable_swiping"), rawBool(settings["web.disable_swiping"], false), ""),
		settingsCheckboxInputHTML(settingsT(loc, "simple_form.labels.defaults.setting_system_font_ui", "Use system default font"), "", settingsPreferenceFieldName("web.use_system_font"), rawBool(settings["web.use_system_font"], false), ""),
	)
	body += settingsAppearanceSectionHeading(settingsT(loc, "appearance.toot_layout", "Post layout"))
	body += settingsPreferenceCheckboxField(settingsT(loc, "simple_form.labels.defaults.setting_crop_images", "Crop images in non-expanded posts"), "web.crop_images", rawBool(settings["web.crop_images"], true))
	body += settingsAppearanceSectionHeading(settingsT(loc, "appearance.discovery", "Discovery"))
	body += settingsPreferenceCheckboxField(settingsT(loc, "simple_form.labels.defaults.setting_trends", "Show trends"), "web.trends", rawBool(settings["web.trends"], true))
	body += settingsPreferenceCheckboxField(settingsT(loc, "simple_form.labels.defaults.setting_disable_hover_cards", "Disable profile preview on hover"), "web.disable_hover_cards", rawBool(settings["web.disable_hover_cards"], false))
	body += settingsAppearanceSectionHeading(settingsT(loc, "appearance.confirmation_dialogs", "Confirmation dialogs"))
	body += settingsCheckboxGroupHTML(
		settingsCheckboxInputHTML(settingsT(loc, "simple_form.labels.defaults.setting_boost_modal", "Confirm before boosting"), "", settingsPreferenceFieldName("web.reblog_modal"), rawBool(settings["web.reblog_modal"], false), ""),
		settingsCheckboxInputHTML(settingsT(loc, "simple_form.labels.defaults.setting_delete_modal", "Confirm before deleting"), "", settingsPreferenceFieldName("web.delete_modal"), rawBool(settings["web.delete_modal"], true), ""),
	)
	body += settingsAppearanceSectionHeading(settingsT(loc, "appearance.sensitive_content", "Sensitive content"))
	body += settingsDisplayMediaField(stringSetting(settings, "web.display_media", "default"), loc)
	body += settingsPreferenceCheckboxFieldWithHint(settingsT(loc, "simple_form.labels.defaults.setting_use_blurhash", "Show colorful gradients for hidden media"), settingsT(loc, "simple_form.hints.defaults.setting_use_blurhash", "Gradients are based on the colors of the hidden visuals but obfuscate any details"), "web.use_blurhash", rawBool(settings["web.use_blurhash"], true))
	body += settingsPreferenceCheckboxField(settingsT(loc, "simple_form.labels.defaults.setting_expand_spoilers", "Always expand posts marked with content warnings"), "web.expand_content_warnings", rawBool(settings["web.expand_content_warnings"], false))
	body += settingsSubmitButton(settingsT(loc, "generic.save_changes", "Save changes")) + `</form>`
	title := settingsT(loc, "settings.appearance", "Appearance preferences")
	return settingsPageShellWithHeading(title, settingsNavigationArg(locale, loc), body, loc, theme, "", settingsHeadingActions(body, loc))
}

func settingsPreferencesNotificationsHTML(settings map[string]any, locale ...string) string {
	return settingsPreferencesNotificationsHTMLWithMessages(settings, "", "", locale...)
}

func settingsPreferencesNotificationsHTMLWithMessages(settings map[string]any, notice string, errorText string, locale ...string) string {
	return settingsPreferencesNotificationsHTMLWithOptions(settings, notice, errorText, settingsHTMLOptions{}, locale...)
}

func settingsPreferencesNotificationsHTMLWithOptions(settings map[string]any, notice string, errorText string, options settingsHTMLOptions, locale ...string) string {
	loc := settingsLocaleArg(locale...)
	theme := settingsThemeArg(locale...)
	applicationName := settingsApplicationNameArg(locale)
	body := settingsInlineFlashHTML(notice, errorText) + `<form class="simple_form edit_user" novalidate="novalidate" method="post" action="/settings/preferences/notifications" id="edit_notification"><input type="hidden" name="_method" value="put">`
	body += `<h4>` + html.EscapeString(settingsT(loc, "notifications.email_events", "Events for e-mail notifications")) + `</h4><p class="hint">` + html.EscapeString(settingsT(loc, "notifications.email_events_hint", "Select the events you want to receive e-mail notifications for.")) + `</p>`
	var eventInputs strings.Builder
	for _, item := range []struct {
		key      string
		labelKey string
		fallback string
		def      bool
	}{
		{"notification_emails.follow", "simple_form.labels.notification_emails.follow", "New followers", true},
		{"notification_emails.follow_request", "simple_form.labels.notification_emails.follow_request", "New follow requests", true},
		{"notification_emails.reblog", "simple_form.labels.notification_emails.reblog", "Boosts", false},
		{"notification_emails.favourite", "simple_form.labels.notification_emails.favourite", "Favourites", false},
		{"notification_emails.mention", "simple_form.labels.notification_emails.mention", "Mentions", true},
	} {
		eventInputs.WriteString(settingsCheckboxInputHTML(settingsT(loc, item.labelKey, item.fallback), "", settingsPreferenceFieldName(item.key), rawBool(settings[item.key], item.def), ""))
	}
	body += settingsCheckboxGroupHTML(eventInputs.String())
	alwaysSendHint := strings.ReplaceAll(settingsT(loc, "simple_form.hints.defaults.setting_always_send_emails", "Normally e-mail notifications will not be sent while you are actively using Mastodon"), "Mastodon", applicationName)
	body += settingsPreferenceCheckboxFieldWithHint(settingsT(loc, "simple_form.labels.defaults.setting_always_send_emails", "Always send e-mail notifications"), alwaysSendHint, "always_send_emails", rawBool(settings["always_send_emails"], false))
	adminFields := []struct {
		permission int64
		key        string
		labelKey   string
		fallback   string
		def        bool
	}{
		{rolePermissionManageReports, "notification_emails.report", "simple_form.labels.notification_emails.report", "New reports", true},
		{rolePermissionManageAppeals, "notification_emails.appeal", "simple_form.labels.notification_emails.appeal", "Appeals", true},
		{rolePermissionManageUsers, "notification_emails.pending_account", "simple_form.labels.notification_emails.pending_account", "New pending accounts", true},
		{rolePermissionManageTaxonomies, "notification_emails.trends", "simple_form.labels.notification_emails.trending_tag", "Trends requiring review", true},
	}
	var adminInputs strings.Builder
	for _, item := range adminFields {
		if options.Permissions&item.permission == item.permission {
			adminInputs.WriteString(settingsCheckboxInputHTML(settingsT(loc, item.labelKey, item.fallback), "", settingsPreferenceFieldName(item.key), rawBool(settings[item.key], item.def), ""))
		}
	}
	softwareUpdatesVisible := options.SoftwareUpdateCheckEnabled && options.Permissions&rolePermissionViewDevops == rolePermissionViewDevops
	if adminInputs.Len() > 0 || softwareUpdatesVisible {
		body += `<h4>` + html.EscapeString(settingsT(loc, "notifications.administration_emails", "Administration e-mail notifications")) + `</h4>`
	}
	if adminInputs.Len() > 0 {
		body += settingsCheckboxGroupHTML(adminInputs.String())
	}
	if softwareUpdatesVisible {
		body += settingsPreferenceSelectField(settingsT(loc, "simple_form.labels.notification_emails.software_updates.label", "Software update e-mails"), "notification_emails.software_updates", stringSetting(settings, "notification_emails.software_updates", "critical"), []string{"none", "critical", "patch", "all"}, true, loc)
	}
	body += `<h4>` + html.EscapeString(settingsT(loc, "notifications.other_settings", "Other notification settings")) + `</h4>`
	var otherInputs strings.Builder
	for _, item := range []struct {
		key      string
		labelKey string
		fallback string
		def      bool
	}{
		{"interactions.must_be_follower", "simple_form.labels.interactions.must_be_follower", "Block notifications from non-followers", false},
		{"interactions.must_be_following", "simple_form.labels.interactions.must_be_following", "Block notifications from people you do not follow", false},
		{"interactions.must_be_following_dm", "simple_form.labels.interactions.must_be_following_dm", "Block direct messages from people you do not follow", false},
		{"interactions.must_be_human", "simple_form.labels.interactions.must_be_human", "Block notifications from automated accounts", false},
	} {
		otherInputs.WriteString(settingsCheckboxInputHTML(settingsT(loc, item.labelKey, item.fallback), "", settingsPreferenceFieldName(item.key), rawBool(settings[item.key], item.def), ""))
	}
	body += settingsCheckboxGroupHTML(otherInputs.String())
	body += settingsSubmitButton(settingsT(loc, "generic.save_changes", "Save changes")) + `</form>`
	title := settingsT(loc, "settings.notifications", "Notification preferences")
	return settingsPageShellWithHeading(title, settingsNavigationArg(locale, loc), body, loc, theme, "", settingsHeadingActions(body, loc))
}

func settingsPreferencesOtherHTML(user models.User, account models.Account, settings map[string]any, locale ...string) string {
	return settingsPreferencesOtherHTMLWithMessages(user, account, settings, "", "", locale...)
}

func settingsPreferencesOtherHTMLWithMessages(user models.User, account models.Account, settings map[string]any, notice string, errorText string, locale ...string) string {
	loc := settingsLocaleArg(locale...)
	theme := settingsThemeArg(locale...)
	body := settingsInlineFlashHTML(notice, errorText) + `<form class="simple_form edit_user" novalidate="novalidate" method="post" action="/settings/preferences/other" id="edit_preferences"><input type="hidden" name="_method" value="put">`
	body += settingsPreferenceRecommendedCheckboxFieldWithHint(settingsT(loc, "simple_form.labels.defaults.setting_aggregate_reblogs", "Group boosts in timelines"), settingsT(loc, "simple_form.hints.defaults.setting_aggregate_reblogs", "Do not show new boosts for posts that have been recently boosted"), "aggregate_reblogs", rawBool(settings["aggregate_reblogs"], true), settingsT(loc, "simple_form.recommended", "Recommended"))
	body += `<h4>` + html.EscapeString(settingsT(loc, "preferences.posting_defaults", "Posting defaults")) + `</h4><div class="fields-row">`
	body += settingsFieldRowColumn(settingsPreferenceSelectField(settingsT(loc, "simple_form.labels.defaults.setting_default_privacy", "Default post privacy"), "default_privacy", settingsDefaultPrivacy(settings, account), []string{"public", "unlisted", "private"}, false, loc))
	languages := []string{""}
	for _, language := range serializer.SupportedLanguages() {
		languages = append(languages, language.Code)
	}
	body += settingsFieldRowColumn(settingsPreferenceSelectField(settingsT(loc, "simple_form.labels.defaults.setting_default_language", "Default post language"), "default_language", stringSetting(settings, "default_language", ""), languages, false, loc)) + `</div>`
	body += settingsPreferenceCheckboxFieldWithHint(settingsT(loc, "simple_form.labels.defaults.setting_default_sensitive", "Mark media as sensitive by default"), settingsT(loc, "simple_form.hints.defaults.setting_default_sensitive", "Sensitive media is hidden by default and can be revealed with a click"), "default_sensitive", rawBool(settings["default_sensitive"], false))
	body += `<h4>` + html.EscapeString(settingsT(loc, "preferences.public_timelines", "Public timelines")) + `</h4>`
	body += settingsChosenLanguagesHTML(user.ChosenLanguages, loc)
	body += settingsSubmitButton(settingsT(loc, "generic.save_changes", "Save changes")) + `</form>`
	title := settingsT(loc, "preferences.other", "Other preferences")
	return settingsPageShellWithHeading(title, settingsNavigationArg(locale, loc), body, loc, theme, "", settingsHeadingActions(body, loc))
}

func settingsChosenLanguagesHTML(chosen models.StringArray, locale string) string {
	selected := map[string]bool{}
	for _, value := range chosen {
		value = strings.TrimSpace(value)
		if value != "" {
			selected[value] = true
		}
	}
	languages := serializer.SupportedLanguages()
	for value := range selected {
		if !settingsSupportedLanguageContains(languages, value) {
			languages = append(languages, serializer.SupportedLanguage{Code: value, Name: value, NativeName: value})
		}
	}
	var b strings.Builder
	b.WriteString(`<div class="fields-group"><div class="input with_block_label check_boxes optional user_chosen_languages field_with_hint"><label class="check_boxes optional">` + html.EscapeString(settingsT(locale, "simple_form.labels.defaults.chosen_languages", "Filter languages")) + `</label><span class="hint">` + html.EscapeString(settingsT(locale, "simple_form.hints.user.chosen_languages", "Only posts in the selected languages will be displayed in public timelines")) + `</span><div class="label_input"><ul><input type="hidden" name="user[chosen_languages][]" value="">`)
	for _, language := range languages {
		checked := ""
		if selected[language.Code] {
			checked = ` checked`
		}
		b.WriteString(`<li class="checkbox"><label><input class="check_boxes optional" type="checkbox" id="user_chosen_languages_` + html.EscapeString(language.Code) + `" name="user[chosen_languages][]" value="` + html.EscapeString(language.Code) + `"` + checked + `> ` + html.EscapeString(language.NativeName) + `</label></li>`)
	}
	b.WriteString(`</ul></div></div></div>`)
	return b.String()
}

func settingsSupportedLanguageContains(values []serializer.SupportedLanguage, want string) bool {
	for _, value := range values {
		if value.Code == want {
			return true
		}
	}
	return false
}

func settingsPrivacyHTML(account models.Account, settings map[string]any, locale ...string) string {
	return settingsPrivacyHTMLWithMessages(account, settings, "", "", locale...)
}

func settingsPrivacyHTMLWithMessages(account models.Account, settings map[string]any, notice string, errorText string, locale ...string) string {
	loc := settingsLocaleArg(locale...)
	theme := settingsThemeArg(locale...)
	applicationName := settingsApplicationNameArg(locale)
	discoverable := !account.Discoverable.Valid || account.Discoverable.Bool
	showCollections := !account.HideCollections.Valid || !account.HideCollections.Bool
	body := settingsInlineFlashHTML(notice, errorText) + `<form class="simple_form edit_account" novalidate="novalidate" method="post" action="/settings/privacy" id="edit_account_` + strconv.FormatInt(account.ID, 10) + `"><input type="hidden" name="_method" value="put">`
	body += `<p class="lead">` + settingsT(loc, "privacy.hint_html", "<strong>Customize how you want your profile and your posts to be found.</strong> Review these settings to make sure they fit your use case.") + `</p>`
	body += `<h4>` + html.EscapeString(settingsT(loc, "privacy.reach", "Reach")) + `</h4><p class="lead">` + settingsT(loc, "privacy.reach_hint_html", "Control whether you want to be discovered and followed by new people.") + `</p>`
	body += settingsRecommendedCheckboxFieldWithHint(settingsT(loc, "simple_form.labels.account.discoverable", "Feature profile and posts in discovery algorithms"), settingsT(loc, "simple_form.hints.account.discoverable", "Your public posts and profile may be featured or recommended."), "account[discoverable]", discoverable, settingsT(loc, "simple_form.recommended", "Recommended"))
	body += settingsCheckboxFieldWithHint(settingsT(loc, "simple_form.labels.account.unlocked", "Automatically accept new followers"), settingsT(loc, "simple_form.hints.account.unlocked", "People will be able to follow you without requesting approval."), "account[unlocked]", !account.Locked)
	searchHint := strings.ReplaceAll(settingsT(loc, "privacy.search_hint_html", "Control how you want to be found in Mastodon and web search results."), "Mastodon", applicationName)
	body += `<h4>` + html.EscapeString(settingsT(loc, "privacy.search", "Search")) + `</h4><p class="lead">` + searchHint + `</p>`
	body += settingsCheckboxFieldWithHint(settingsT(loc, "simple_form.labels.account.indexable", "Include public posts in search results"), settingsT(loc, "simple_form.hints.account.indexable", "Your public posts may appear in search results."), "account[indexable]", account.Indexable)
	body += settingsCheckboxFieldWithHint(settingsT(loc, "simple_form.labels.settings.indexable", "Include profile page in search engines"), settingsT(loc, "simple_form.hints.settings.indexable", "Your profile page may appear in results from web search engines."), "account[settings][indexable]", !rawBool(settings["noindex"], false))
	body += `<h4>` + html.EscapeString(settingsT(loc, "privacy.privacy", "Privacy")) + `</h4><p class="lead">` + settingsT(loc, "privacy.privacy_hint_html", "Control how much information you disclose for the benefit of others.") + `</p>`
	body += settingsCheckboxFieldWithHint(settingsT(loc, "simple_form.labels.account.show_collections", "Show follows and followers on profile"), settingsT(loc, "simple_form.hints.account.show_collections", "People will be able to browse through your follows and followers."), "account[show_collections]", showCollections)
	body += settingsCheckboxFieldWithHint(settingsT(loc, "simple_form.labels.settings.show_application", "Display from which app you sent a post"), settingsT(loc, "simple_form.hints.settings.show_application", "You will always be able to see which app published your post regardless."), "account[settings][show_application]", rawBool(settings["show_application"], true))
	body += settingsSubmitButton(settingsT(loc, "generic.save_changes", "Save changes")) + `</form>`
	title := settingsT(loc, "privacy.title", "Privacy settings")
	return settingsPageShellWithHeadingTitle(title, settingsT(loc, "settings.profile", "Profile"), settingsNavigationArg(locale, loc), body, loc, theme, settingsProfileTabsHTML("privacy", loc), "")
}

func (s *Server) settingsBackups(userID int64) ([]models.Backup, error) {
	if s == nil || s.db == nil || userID == 0 {
		return nil, nil
	}
	var backups []models.Backup
	err := s.db.
		Where("user_id = ?", userID).
		Order("id DESC").
		Find(&backups).Error
	return backups, err
}

type settingsExportTotals struct {
	Storage      int64
	Statuses     int64
	Follows      int64
	Lists        int64
	Followers    int64
	Blocks       int64
	Mutes        int64
	DomainBlocks int64
	Bookmarks    int64
}

func (s *Server) settingsExportTotals(account models.Account) (settingsExportTotals, error) {
	totals := settingsExportTotals{
		Statuses:  account.AccountStat.StatusesCount,
		Follows:   account.AccountStat.FollowingCount,
		Followers: account.AccountStat.FollowersCount,
	}
	if s == nil || s.db == nil || account.ID == 0 {
		return totals, nil
	}
	var stat models.AccountStat
	if err := s.db.Where("account_id = ?", account.ID).First(&stat).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return settingsExportTotals{}, err
		}
	} else {
		totals.Statuses = stat.StatusesCount
		totals.Follows = stat.FollowingCount
		totals.Followers = stat.FollowersCount
	}
	if err := s.db.Model(&models.MediaAttachment{}).
		Where("account_id = ?", account.ID).
		Select("COALESCE(SUM(file_file_size), 0)").
		Scan(&totals.Storage).Error; err != nil {
		return settingsExportTotals{}, err
	}
	for _, item := range []struct {
		value *int64
		model any
	}{
		{&totals.Lists, &models.List{}},
		{&totals.Blocks, &models.Block{}},
		{&totals.Mutes, &models.Mute{}},
		{&totals.DomainBlocks, &models.AccountDomainBlock{}},
		{&totals.Bookmarks, &models.Bookmark{}},
	} {
		if err := s.db.Model(item.model).Where("account_id = ?", account.ID).Count(item.value).Error; err != nil {
			return settingsExportTotals{}, err
		}
	}
	return totals, nil
}

func settingsExportHTML(totals settingsExportTotals, backups []models.Backup, canCreateBackup bool, locale ...string) string {
	loc := settingsLocaleArg(locale...)
	theme := settingsThemeArg(locale...)
	body := `<div class="table-wrapper"><table class="table"><tbody>`
	for _, item := range []struct {
		labelKey string
		fallback string
		count    int64
		path     string
	}{
		{"exports.storage", "Media storage", totals.Storage, ""},
		{"accounts.posts_tab_heading", "Posts", totals.Statuses, ""},
		{"admin.accounts.follows", "Follows", totals.Follows, "/settings/exports/follows.csv"},
		{"exports.lists", "Lists", totals.Lists, "/settings/exports/lists.csv"},
		{"admin.accounts.followers", "Followers", totals.Followers, ""},
		{"exports.blocks", "Blocks", totals.Blocks, "/settings/exports/blocks.csv"},
		{"exports.mutes", "Mutes", totals.Mutes, "/settings/exports/mutes.csv"},
		{"exports.domain_blocks", "Domain blocks", totals.DomainBlocks, "/settings/exports/domain_blocks.csv"},
		{"exports.bookmarks", "Bookmarks", totals.Bookmarks, "/settings/exports/bookmarks.csv"},
	} {
		link := ""
		if item.path != "" {
			link = `<a class="table-action-link" href="` + item.path + `"><i class="fa fa-download fa-fw"></i> ` + html.EscapeString(settingsT(loc, "exports.csv", "CSV")) + `</a>`
		}
		count := formatRailsInteger(item.count)
		if item.labelKey == "exports.storage" {
			count = formatRailsHumanSize(item.count)
		}
		body += `<tr><th>` + html.EscapeString(settingsT(loc, item.labelKey, item.fallback)) + `</th><td>` + html.EscapeString(count) + `</td><td>` + link + `</td></tr>`
	}
	body += `</tbody></table></div><hr class="spacer"><p class="muted-hint">` + settingsT(loc, "exports.archive_takeout.hint_html", "You can request an archive of your <strong>posts and uploaded media</strong>.") + `</p>`
	if canCreateBackup {
		body += `<p><a class="button" data-method="post" href="/settings/export">` + html.EscapeString(settingsT(loc, "exports.archive_takeout.request", "Request archive")) + `</a></p>`
	}
	if len(backups) > 0 {
		body += `<hr class="spacer"><div class="table-wrapper"><table class="table"><thead><tr><th>` + html.EscapeString(settingsT(loc, "exports.archive_takeout.date", "Created")) + `</th><th>` + html.EscapeString(settingsT(loc, "exports.archive_takeout.size", "Size")) + `</th><th></th></tr></thead><tbody>`
		for _, backup := range backups {
			stamp := backup.CreatedAt.UTC().Format(time.RFC3339)
			body += `<tr><td><time class="formatted" datetime="` + html.EscapeString(stamp) + `" title="` + html.EscapeString(stamp) + `">` + html.EscapeString(stamp) + `</time></td>`
			if backup.Processed && backup.DumpFileName.Valid && strings.TrimSpace(backup.DumpFileName.String) != "" {
				size := int64(0)
				if backup.DumpFileSize.Valid {
					size = backup.DumpFileSize.Int64
				}
				body += `<td>` + html.EscapeString(formatRailsHumanSize(size)) + `</td><td><a class="table-action-link" href="/backups/` + strconv.FormatInt(backup.ID, 10) + `/download"><i class="fa fa-download fa-fw"></i> ` + html.EscapeString(settingsT(loc, "exports.archive_takeout.download", "Download")) + `</a></td>`
			} else {
				body += `<td colspan="2">` + html.EscapeString(settingsT(loc, "exports.archive_takeout.in_progress", "Processing")) + `</td>`
			}
			body += `</tr>`
		}
		body += `</tbody></table></div>`
	}
	title := settingsT(loc, "settings.export", "Export")
	return settingsPageShell(title, settingsNavigationArg(locale, loc), body, loc, theme)
}

func formatRailsInteger(value int64) string {
	negative := value < 0
	digits := strconv.FormatInt(value, 10)
	if negative {
		digits = strings.TrimPrefix(digits, "-")
	}
	for i := len(digits) - 3; i > 0; i -= 3 {
		digits = digits[:i] + "," + digits[i:]
	}
	if negative {
		return "-" + digits
	}
	return digits
}

func formatRailsHumanSize(size int64) string {
	if size < 1024 {
		if size == 1 {
			return "1 Byte"
		}
		return strconv.FormatInt(size, 10) + " Bytes"
	}
	units := []string{"KB", "MB", "GB", "TB", "PB"}
	value := float64(size)
	for _, unit := range units {
		value /= 1024
		if value < 1024 {
			return strconv.FormatFloat(value, 'g', 3, 64) + unit
		}
	}
	return strconv.FormatFloat(value, 'g', 3, 64) + "PB"
}

func formatRailsLocalizedTime(locale string, value time.Time) string {
	if value.IsZero() {
		return ""
	}
	value = value.UTC()
	format := settingsT(locale, "time.formats.default", "%b %d, %Y, %H:%M")
	return strings.NewReplacer(
		"%Y", value.Format("2006"),
		"%m", value.Format("01"),
		"%d", value.Format("02"),
		"%e", value.Format("_2"),
		"%H", value.Format("15"),
		"%M", value.Format("04"),
		"%S", value.Format("05"),
		"%Z", "UTC",
		"%b", value.Format("Jan"),
		"%B", value.Format("January"),
	).Replace(format)
}

func (s *Server) settingsWebauthnCredentials(userID int64) ([]models.WebauthnCredential, error) {
	if s == nil || s.db == nil || userID == 0 {
		return nil, nil
	}
	return s.webauthnCredentialsForUser(userID)
}

func settingsTwoFactorMethodsHTML(user models.User, credentials []models.WebauthnCredential, locale ...string) string {
	loc := settingsLocaleArg(locale...)
	theme := settingsThemeArg(locale...)
	webauthnAction := `<a class="table-action-link" data-method="get" href="/settings/security_keys/new"><i class="fa fa-key fa-fw"></i> ` + html.EscapeString(settingsT(loc, "two_factor_authentication.add", "Add")) + `</a>`
	if len(credentials) > 0 {
		webauthnAction = `<a class="table-action-link" data-method="get" href="/settings/security_keys"><i class="fa fa-pencil fa-fw"></i> ` + html.EscapeString(settingsT(loc, "two_factor_authentication.edit", "Edit")) + `</a>`
	}
	body := `<p class="hint"><span class="positive-hint"><i class="fa fa-check fa-fw"></i> ` + html.EscapeString(settingsT(loc, "two_factor_authentication.enabled", "Two-factor authentication is enabled")) + `</span></p><div class="table-wrapper"><table class="table"><thead><tr><th>` + html.EscapeString(settingsT(loc, "two_factor_authentication.methods", "Methods")) + `</th><th></th></tr></thead><tbody><tr><td>` + html.EscapeString(settingsT(loc, "two_factor_authentication.otp", "Authenticator app")) + `</td><td><a class="table-action-link" data-method="post" href="/settings/otp_authentication"><i class="fa fa-pencil fa-fw"></i> ` + html.EscapeString(settingsT(loc, "two_factor_authentication.edit", "Edit")) + `</a></td></tr><tr><td>` + html.EscapeString(settingsT(loc, "two_factor_authentication.webauthn", "Security key")) + `</td><td>` + webauthnAction + `</td></tr></tbody></table></div><hr class="spacer"><h3>` + html.EscapeString(settingsT(loc, "two_factor_authentication.recovery_codes", "Recovery codes")) + `</h3><p class="muted-hint">` + html.EscapeString(settingsT(loc, "two_factor_authentication.lost_recovery_codes", "Generate new recovery codes if you have lost yours.")) + `</p><hr class="spacer"><div class="simple_form"><a class="block-button" data-method="post" href="/settings/two_factor_authentication/recovery_codes">` + html.EscapeString(settingsT(loc, "two_factor_authentication.generate_recovery_codes", "Generate recovery codes")) + `</a></div>`
	title := settingsT(loc, "settings.two_factor_authentication", "Two-factor authentication")
	headingAction := `<div class="content__heading__actions"><a class="button button--destructive" data-method="post" href="/settings/two_factor_authentication_methods/disable">` + html.EscapeString(settingsT(loc, "two_factor_authentication.disable", "Disable")) + `</a></div>`
	return settingsPageShellWithHeading(title, settingsNavigationArg(locale, loc), body, loc, theme, "", headingAction)
}

func settingsCheckboxField(label string, name string, checked bool) string {
	return settingsCheckboxFieldHTML(label, "", name, checked, "")
}

func settingsCheckboxFieldWithHint(label string, hint string, name string, checked bool) string {
	return settingsCheckboxFieldHTML(label, hint, name, checked, "")
}

func settingsRecommendedCheckboxField(label string, name string, checked bool, recommendedLabel string) string {
	return settingsCheckboxFieldHTML(label, "", name, checked, recommendedLabel)
}

func settingsRecommendedCheckboxFieldWithHint(label string, hint string, name string, checked bool, recommendedLabel string) string {
	return settingsCheckboxFieldHTML(label, hint, name, checked, recommendedLabel)
}

func settingsCheckboxFieldHTML(label string, hint string, name string, checked bool, recommendedLabel string) string {
	return settingsCheckboxGroupHTML(settingsCheckboxInputHTML(label, hint, name, checked, recommendedLabel))
}

func settingsCheckboxGroupHTML(inputs ...string) string {
	return `<div class="fields-group">` + strings.Join(inputs, "") + `</div>`
}

func settingsCheckboxInputHTML(label string, hint string, name string, checked bool, recommendedLabel string) string {
	escapedName := html.EscapeString(name)
	id := settingsFieldID(name)
	if key, ok := settingsPreferenceKeyFromFieldName(name); ok {
		id = settingsPreferenceFieldID(key)
	}
	inputClasses := "input with_label boolean optional " + settingsSimpleFormObjectClass(name)
	if strings.TrimSpace(hint) != "" {
		inputClasses += " field_with_hint"
	}
	out := `<div class="` + inputClasses + `"><div class="label_input"><label class="boolean optional" for="` + id + `">` + html.EscapeString(label)
	if strings.TrimSpace(recommendedLabel) != "" {
		out += ` <span class="recommended">` + html.EscapeString(recommendedLabel) + `</span>`
	}
	out += `</label><div class="label_input__wrapper"><input type="hidden" name="` + escapedName + `" value="0"><label class="checkbox"><input class="boolean optional" type="checkbox" id="` + id + `" name="` + escapedName + `" value="1"`
	if checked {
		out += ` checked`
	}
	out += `></label></div></div>`
	if strings.TrimSpace(hint) != "" {
		out += `<span class="hint">` + html.EscapeString(hint) + `</span>`
	}
	return out + `</div>`
}

func settingsPreferenceFieldName(key string) string {
	return "user[settings_attributes][" + key + "]"
}

func settingsPreferenceCheckboxField(label string, key string, checked bool) string {
	return settingsCheckboxField(label, settingsPreferenceFieldName(key), checked)
}

func settingsPreferenceCheckboxFieldWithHint(label string, hint string, key string, checked bool) string {
	return settingsCheckboxFieldWithHint(label, hint, settingsPreferenceFieldName(key), checked)
}

func settingsPreferenceRecommendedCheckboxFieldWithHint(label string, hint string, key string, checked bool, recommendedLabel string) string {
	return settingsRecommendedCheckboxFieldWithHint(label, hint, settingsPreferenceFieldName(key), checked, recommendedLabel)
}

func settingsSimpleFormObjectClass(name string) string {
	if key, ok := settingsPreferenceKeyFromFieldName(name); ok {
		return "user_settings_" + html.EscapeString(key)
	}
	return settingsFieldID(name)
}

func settingsPreferenceKeyFromFieldName(name string) (string, bool) {
	const prefix = "user[settings_attributes]["
	if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, "]") {
		return "", false
	}
	return strings.TrimSuffix(strings.TrimPrefix(name, prefix), "]"), true
}

func settingsAppearanceSectionHeading(label string) string {
	return `<h4>` + html.EscapeString(label) + `</h4>`
}

func settingsFieldRowColumn(field string) string {
	return strings.Replace(field, `class="fields-group"`, `class="fields-group fields-row__column fields-row__column-6"`, 1)
}

func settingsLocaleSelectField(current string, locale string) string {
	id := settingsFieldID("user[locale]")
	label := settingsT(locale, "simple_form.labels.defaults.locale", "Interface locale")
	var out strings.Builder
	out.WriteString(`<div class="fields-group"><div class="input with_label select optional user_locale"><div class="label_input"><label class="select optional" for="` + id + `">` + html.EscapeString(label) + `</label><div class="label_input__wrapper"><select class="select optional" id="` + id + `" name="user[locale]">`)
	for _, value := range railsI18nAvailableLocales {
		out.WriteString(`<option value="` + html.EscapeString(value) + `"`)
		if value == current {
			out.WriteString(` selected`)
		}
		out.WriteString(`>` + html.EscapeString(settingsNativeLocaleName(value)) + `</option>`)
	}
	out.WriteString(`</select></div></div></div></div>`)
	return out.String()
}

func settingsNativeLocaleName(locale string) string {
	if name, ok := railsNativeLocaleNames[locale]; ok {
		return name
	}
	for _, language := range serializer.SupportedLanguages() {
		if language.Code == locale {
			return language.NativeName
		}
	}
	return railsStandardLocaleName(locale)
}

func settingsTimeZoneSelectField(current string, locale string) string {
	id := settingsFieldID("user[time_zone]")
	label := settingsT(locale, "simple_form.labels.user.time_zone", "Time zone")
	var out strings.Builder
	out.WriteString(`<div class="fields-group"><div class="input with_label select optional user_time_zone"><div class="label_input"><label class="select optional" for="` + id + `">` + html.EscapeString(label) + `</label><div class="label_input__wrapper"><select class="select optional" id="` + id + `" name="user[time_zone]">`)
	for _, option := range railsTimeZoneOptions {
		out.WriteString(`<option value="` + html.EscapeString(option.Value) + `"`)
		if option.Value == current {
			out.WriteString(` selected`)
		}
		out.WriteString(`>` + html.EscapeString(option.Label) + `</option>`)
	}
	out.WriteString(`</select></div></div></div></div>`)
	return out.String()
}

func settingsDisplayMediaField(current string, locale string) string {
	name := settingsPreferenceFieldName("web.display_media")
	label := settingsT(locale, "simple_form.labels.defaults.setting_display_media", "Media display")
	var out strings.Builder
	out.WriteString(`<div class="fields-group"><div class="input with_floating_label radio_buttons required user_settings_web.display_media"><div class="label_input"><label class="radio_buttons required">` + html.EscapeString(label) + ` <abbr title="` + html.EscapeString(settingsT(locale, "simple_form.required.text", "required")) + `">*</abbr></label><ul>`)
	for _, value := range []string{"default", "show_all", "hide_all"} {
		id := settingsPreferenceFieldID("web.display_media") + "_" + value
		optionLabel := settingsT(locale, "simple_form.hints.defaults.setting_display_media_"+value, value)
		out.WriteString(`<li><label for="` + id + `"><input class="radio_buttons required" type="radio" id="` + id + `" name="` + html.EscapeString(name) + `" value="` + value + `"`)
		if value == current {
			out.WriteString(` checked`)
		}
		out.WriteString(`> ` + html.EscapeString(optionLabel) + `</label></li>`)
	}
	out.WriteString(`</ul></div></div></div>`)
	return out.String()
}

func settingsSelectField(label string, name string, current string, options []string, locale ...string) string {
	loc := settingsLocaleArg(locale...)
	id := settingsFieldID(name)
	out := `<div class="fields-group"><div class="input select with_label"><div class="label_input"><label class="select" for="` + id + `">` + html.EscapeString(label) + `</label><div class="label_input__wrapper"><select class="select" id="` + id + `" name="` + html.EscapeString(name) + `">`
	for _, option := range options {
		display := settingsOptionLabel(loc, name, option)
		out += `<option value="` + html.EscapeString(option) + `"`
		if option == current {
			out += ` selected`
		}
		out += `>` + html.EscapeString(display) + `</option>`
	}
	return out + `</select></div></div></div></div>`
}

func settingsPreferenceSelectField(label string, key string, current string, options []string, required bool, locale string) string {
	name := settingsPreferenceFieldName(key)
	id := settingsPreferenceFieldID(key)
	requirement := "optional"
	if required {
		requirement = "required"
	}
	out := `<div class="fields-group"><div class="input with_label select ` + requirement + ` user_settings_` + html.EscapeString(key) + `"><div class="label_input"><label class="select ` + requirement + `" for="` + id + `">` + html.EscapeString(label)
	if required {
		out += ` <abbr title="` + html.EscapeString(settingsT(locale, "simple_form.required.text", "required")) + `">*</abbr>`
	}
	out += `</label><div class="label_input__wrapper"><select class="select ` + requirement + `" id="` + id + `" name="` + html.EscapeString(name) + `">`
	for _, option := range options {
		display := settingsOptionLabel(locale, name, option)
		out += `<option value="` + html.EscapeString(option) + `"`
		if option == current {
			out += ` selected`
		}
		out += `>` + html.EscapeString(display) + `</option>`
	}
	return out + `</select></div></div></div></div>`
}

func settingsPreferenceFieldID(key string) string {
	return "user_settings_attributes_" + html.EscapeString(key)
}

func settingsOptionLabel(locale string, name string, option string) string {
	switch {
	case strings.HasSuffix(name, "[theme]"):
		return settingsT(locale, "themes."+option, option)
	case strings.HasSuffix(name, "[web.display_media]"):
		return settingsT(locale, "simple_form.labels.defaults.setting_display_media_"+strings.ReplaceAll(option, "-", "_"), option)
	case strings.HasSuffix(name, "[notification_emails.software_updates]"):
		return settingsT(locale, "simple_form.labels.notification_emails.software_updates."+option, option)
	case strings.HasSuffix(name, "[default_privacy]"):
		short := settingsT(locale, "statuses.visibilities."+option, option)
		long := settingsT(locale, "statuses.visibilities."+option+"_long", "")
		if strings.TrimSpace(long) == "" {
			return short
		}
		return short + " - " + long
	case strings.HasSuffix(name, "[default_language]"):
		if option == "" {
			return settingsT(locale, "statuses.default_language", "Same as interface language")
		}
		return settingsNativeLocaleName(option)
	default:
		if option == "" {
			return settingsT(locale, "simple_form.labels.defaults.setting_display_media_default", "Default")
		}
		return option
	}
}

func settingsLocaleArg(locale ...string) string {
	if len(locale) > 0 && strings.TrimSpace(locale[0]) != "" {
		return locale[0]
	}
	return webDefaultLocaleValue()
}

func settingsLocaleArgOrEnglish(locale ...string) string {
	if len(locale) > 0 && strings.TrimSpace(locale[0]) != "" {
		return locale[0]
	}
	return "en"
}

func settingsThemeArg(localeAndTheme ...string) string {
	if len(localeAndTheme) > 1 && strings.TrimSpace(localeAndTheme[1]) != "" {
		return normalizedWebTheme(localeAndTheme[1])
	}
	return "system"
}

func settingsT(locale string, key string, fallback string) string {
	value := webT(locale, key)
	if value == key || strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func adminT(locale string, key string, fallback string) string {
	return settingsT(locale, key, fallback)
}

func adminNothingHereHTML(locale string, extraClasses ...string) string {
	classes := "nothing-here"
	if len(extraClasses) > 0 && strings.TrimSpace(extraClasses[0]) != "" {
		classes += " " + strings.TrimSpace(extraClasses[0])
	}
	return `<div class="` + html.EscapeString(classes) + `">` + html.EscapeString(adminT(locale, "accounts.nothing_here", "There is nothing here!")) + `</div>`
}

func adminTVars(locale string, key string, fallback string, vars map[string]string) string {
	value := webT(locale, key, vars)
	if value == key || strings.TrimSpace(value) == "" {
		value = fallback
		for name, replacement := range vars {
			value = strings.ReplaceAll(value, "%{"+name+"}", replacement)
		}
	}
	return value
}

func settingsTVars(locale string, key string, fallback string, vars map[string]string) string {
	return adminTVars(locale, key, fallback, vars)
}

func settingsWebTheme(settings map[string]any) string {
	return normalizedWebTheme(stringSetting(settings, "theme", "system"))
}

func settingsDefaultPrivacy(settings map[string]any, account models.Account) string {
	if value := stringSetting(settings, "default_privacy", ""); value != "" {
		return value
	}
	if account.Locked {
		return "private"
	}
	return "public"
}

func settingsTextField(label string, name string, value string) string {
	return simpleTextInput(label, name, value, "text", "")
}

func settingsFieldID(name string) string {
	return html.EscapeString(strings.NewReplacer("[", "_", "]", "", ".", "_").Replace(name))
}

func settingsSubmitButton(label string) string {
	return `<div class="actions"><button type="submit" class="button">` + html.EscapeString(label) + `</button></div>`
}

func stringSetting(settings map[string]any, key string, fallback string) string {
	value := strings.TrimSpace(rawString(settings[key]))
	if value == "" {
		return fallback
	}
	return value
}
