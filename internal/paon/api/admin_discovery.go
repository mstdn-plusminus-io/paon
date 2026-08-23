package api

import (
	"html"
	"net/http"
	"net/url"
	"strings"

	"github.com/labstack/echo/v5"
	"gorm.io/gorm"
)

type adminDiscoverySettings struct {
	Trends                    bool
	TrendableByDefault        bool
	LocalLiveFeedAccess       string
	RemoteLiveFeedAccess      string
	LocalTopicFeedAccess      string
	RemoteTopicFeedAccess     string
	NoIndex                   bool
	AllowReferrerOrigin       bool
	ActivityAPIEnabled        bool
	PeersAPIEnabled           bool
	AuthorizedFetch           bool
	AuthorizedFetchOverridden bool
	BootstrapTimelineAccounts string
	ProfileDirectory          bool
}

func (s *Server) adminSettingsDiscoveryPage(c *echo.Context) error {
	user, _, handled, err := s.requireAdminSettingsWebUser(c)
	if handled || err != nil {
		return err
	}
	locale := s.webLocale(c, user)
	theme := settingsWebTheme(decodeUserSettings(user.Settings.String))
	return c.HTML(http.StatusOK, adminSettingsDiscoveryHTML(s.adminDiscoverySettings(), c.QueryParam("notice"), c.QueryParam("error"), locale, theme))
}

func (s *Server) updateAdminSettingsDiscovery(c *echo.Context) error {
	user, _, handled, err := s.requireAdminSettingsWebUser(c)
	if handled || err != nil {
		return err
	}
	locale := s.webLocale(c, user)
	theme := settingsWebTheme(decodeUserSettings(user.Settings.String))
	if c.Request().Method == http.MethodPost && !methodOverrideIs(c, "put", "patch") {
		return c.Redirect(http.StatusFound, "/admin/settings/discovery")
	}
	settings, err := parseAdminDiscoverySettings(c)
	if err != nil {
		if adminSettingsParamsMissing(err) {
			return apiError(c, http.StatusBadRequest, "Malformed request")
		}
		return c.HTML(http.StatusOK, adminSettingsDiscoveryHTML(s.adminDiscoverySettings(), "", adminSettingsInvalidMessage(locale, "discovery"), locale, theme))
	}
	if err := validateAdminDiscoverySettings(settings); err != nil {
		return c.HTML(http.StatusOK, adminSettingsDiscoveryHTML(settings, "", adminSettingErrorText(locale, err), locale, theme))
	}
	if s.db == nil {
		return c.HTML(http.StatusOK, adminSettingsDiscoveryHTML(settings, "", adminSettingsDatabaseUnavailableMessage(locale), locale, theme))
	}
	authorizedFetchOverridden := s.authorizedFetchOverridden()
	if authorizedFetchOverridden {
		settings.AuthorizedFetch = s.authorizedFetchMode()
		settings.AuthorizedFetchOverridden = true
	}
	if err := s.validateExistingAdminSettingUsernames(settings.BootstrapTimelineAccounts, true); err != nil {
		return c.HTML(http.StatusOK, adminSettingsDiscoveryHTML(settings, "", adminSettingErrorText(locale, err), locale, theme))
	}
	if err := s.updateAdminDiscoverySettings(settings, authorizedFetchOverridden); err != nil {
		return err
	}
	return c.Redirect(http.StatusFound, "/admin/settings/discovery?notice="+url.QueryEscape(adminSettingsSavedMessage(locale, "discovery")))
}

func (s *Server) adminDiscoverySettings() adminDiscoverySettings {
	return adminDiscoverySettings{
		Trends:                    s.settingBoolValue("trends", true),
		TrendableByDefault:        s.settingBoolValue("trendable_by_default", false),
		LocalLiveFeedAccess:       normalizeTimelineAccess(s.settingStringValue("local_live_feed_access", timelineAccessPublic)),
		RemoteLiveFeedAccess:      normalizeTimelineAccess(s.settingStringValue("remote_live_feed_access", timelineAccessPublic)),
		LocalTopicFeedAccess:      normalizeTimelineAccess(s.settingStringValue("local_topic_feed_access", timelineAccessPublic)),
		RemoteTopicFeedAccess:     normalizeTimelineAccess(s.settingStringValue("remote_topic_feed_access", timelineAccessPublic)),
		NoIndex:                   s.settingBoolValue("noindex", false),
		AllowReferrerOrigin:       s.settingBoolValue("allow_referrer_origin", false),
		ActivityAPIEnabled:        s.settingBoolValue("activity_api_enabled", true),
		PeersAPIEnabled:           s.settingBoolValue("peers_api_enabled", true),
		AuthorizedFetch:           s.authorizedFetchMode(),
		AuthorizedFetchOverridden: s.authorizedFetchOverridden(),
		BootstrapTimelineAccounts: s.settingStringValue("bootstrap_timeline_accounts", ""),
		ProfileDirectory:          s.settingBoolValue("profile_directory", true),
	}
}

func parseAdminDiscoverySettings(c *echo.Context) (adminDiscoverySettings, error) {
	req := c.Request()
	if err := parseAdminSettingsFormRoot(req, 8<<20); err != nil {
		return adminDiscoverySettings{}, err
	}
	return adminDiscoverySettings{
		Trends:                    adminSettingsCheckbox(req.Form, "form_admin_settings[trends]"),
		TrendableByDefault:        adminSettingsCheckbox(req.Form, "form_admin_settings[trendable_by_default]"),
		LocalLiveFeedAccess:       lastFormValue(req.Form, "form_admin_settings[local_live_feed_access]"),
		RemoteLiveFeedAccess:      lastFormValue(req.Form, "form_admin_settings[remote_live_feed_access]"),
		LocalTopicFeedAccess:      lastFormValue(req.Form, "form_admin_settings[local_topic_feed_access]"),
		RemoteTopicFeedAccess:     lastFormValue(req.Form, "form_admin_settings[remote_topic_feed_access]"),
		NoIndex:                   adminSettingsCheckbox(req.Form, "form_admin_settings[noindex]"),
		AllowReferrerOrigin:       adminSettingsCheckbox(req.Form, "form_admin_settings[allow_referrer_origin]"),
		ActivityAPIEnabled:        adminSettingsCheckbox(req.Form, "form_admin_settings[activity_api_enabled]"),
		PeersAPIEnabled:           adminSettingsCheckbox(req.Form, "form_admin_settings[peers_api_enabled]"),
		AuthorizedFetch:           adminSettingsCheckbox(req.Form, "form_admin_settings[authorized_fetch]"),
		BootstrapTimelineAccounts: lastFormValue(req.Form, "form_admin_settings[bootstrap_timeline_accounts]"),
		ProfileDirectory:          adminSettingsCheckbox(req.Form, "form_admin_settings[profile_directory]"),
	}, nil
}

func validateAdminDiscoverySettings(settings adminDiscoverySettings) error {
	valid := func(value string, allowDisabled bool) bool {
		value = strings.TrimSpace(value)
		return value == timelineAccessPublic || value == timelineAccessAuthenticated || (allowDisabled && value == timelineAccessDisabled)
	}
	if !valid(settings.LocalLiveFeedAccess, true) || !valid(settings.RemoteLiveFeedAccess, true) || !valid(settings.LocalTopicFeedAccess, false) || !valid(settings.RemoteTopicFeedAccess, true) {
		return errAdminSetting("Feed access mode is invalid")
	}
	return nil
}

func (s *Server) updateAdminDiscoverySettings(settings adminDiscoverySettings, skipAuthorizedFetch bool) error {
	values := map[string]string{
		"trends":                      boolSettingValue(settings.Trends),
		"trendable_by_default":        boolSettingValue(settings.TrendableByDefault),
		"local_live_feed_access":      settings.LocalLiveFeedAccess,
		"remote_live_feed_access":     settings.RemoteLiveFeedAccess,
		"local_topic_feed_access":     settings.LocalTopicFeedAccess,
		"remote_topic_feed_access":    settings.RemoteTopicFeedAccess,
		"noindex":                     boolSettingValue(settings.NoIndex),
		"allow_referrer_origin":       boolSettingValue(settings.AllowReferrerOrigin),
		"activity_api_enabled":        boolSettingValue(settings.ActivityAPIEnabled),
		"peers_api_enabled":           boolSettingValue(settings.PeersAPIEnabled),
		"bootstrap_timeline_accounts": settings.BootstrapTimelineAccounts,
		"profile_directory":           boolSettingValue(settings.ProfileDirectory),
	}
	if !skipAuthorizedFetch {
		values["authorized_fetch"] = boolSettingValue(settings.AuthorizedFetch)
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		for name, value := range values {
			if err := upsertGlobalSetting(tx, name, value); err != nil {
				return err
			}
		}
		return nil
	})
}

func adminSettingsDiscoveryHTML(settings adminDiscoverySettings, notice string, errorText string, localeAndTheme ...string) string {
	loc := settingsLocaleArgOrEnglish(localeAndTheme...)
	theme := settingsThemeArg(localeAndTheme...)
	checkbox := func(key string, label string, checked bool) string {
		attr := ""
		if checked {
			attr = " checked"
		}
		return `<input type="hidden" name="form_admin_settings[` + key + `]" value="0">
  <label><input type="checkbox" name="form_admin_settings[` + key + `]" value="1"` + attr + `> ` + html.EscapeString(label) + `</label>`
	}
	selectAccess := func(key string, label string, value string, allowDisabled bool) string {
		modes := []string{timelineAccessPublic, timelineAccessAuthenticated}
		if allowDisabled {
			modes = append(modes, timelineAccessDisabled)
		}
		var options strings.Builder
		for _, mode := range modes {
			selected := ""
			if mode == value {
				selected = " selected"
			}
			label := map[string]string{
				timelineAccessPublic:        adminT(loc, "admin.settings.feed_access.modes.public", "Everyone"),
				timelineAccessAuthenticated: adminT(loc, "admin.settings.feed_access.modes.authenticated", "Logged-in local users"),
				timelineAccessDisabled:      adminT(loc, "admin.settings.feed_access.modes.disabled", "Moderators only"),
			}[mode]
			options.WriteString(`<option value="` + mode + `"` + selected + `>` + html.EscapeString(label) + `</option>`)
		}
		return `<label>` + html.EscapeString(label) + ` <select name="form_admin_settings[` + key + `]">` + options.String() + `</select></label>`
	}
	title := adminT(loc, "admin.settings.discovery.title", "Discovery")
	body := `<p class="lead">` + html.EscapeString(adminT(loc, "admin.settings.discovery.preamble", "Tune public discovery surfaces, searchability, public APIs, and ActivityPub fetch policy.")) + `</p>
<form class="simple_form" method="post" action="/admin/settings/discovery">
  <input type="hidden" name="_method" value="patch">
  <h2>` + html.EscapeString(adminT(loc, "admin.settings.discovery.trends", "Trends")) + `</h2>
  <div class="fields-group">` + checkbox("trends", adminSettingsLabel(loc, "trends", "Enable trends"), settings.Trends) + `</div>
  <div class="fields-group">` + checkbox("trendable_by_default", adminSettingsLabel(loc, "trendable_by_default", "Allow trends without prior review"), settings.TrendableByDefault) + `</div>
  <h2>` + html.EscapeString(adminT(loc, "admin.settings.discovery.public_timelines", "Public timelines")) + `</h2>
  <div class="fields-row"><div class="fields-group">` + selectAccess("local_live_feed_access", adminSettingsLabel(loc, "local_live_feed_access", "Access to live feeds featuring local posts"), settings.LocalLiveFeedAccess, true) + `</div>
  <div class="fields-group">` + selectAccess("remote_live_feed_access", adminSettingsLabel(loc, "remote_live_feed_access", "Access to live feeds featuring remote posts"), settings.RemoteLiveFeedAccess, true) + `</div></div>
  <div class="fields-row"><div class="fields-group">` + selectAccess("local_topic_feed_access", adminSettingsLabel(loc, "local_topic_feed_access", "Access to hashtag feeds featuring local posts"), settings.LocalTopicFeedAccess, false) + `</div>
  <div class="fields-group">` + selectAccess("remote_topic_feed_access", adminSettingsLabel(loc, "remote_topic_feed_access", "Access to hashtag feeds featuring remote posts"), settings.RemoteTopicFeedAccess, true) + `</div></div>
  <div class="fields-group">` + checkbox("noindex", adminT(loc, "admin.settings.default_noindex.title", "Opt users out of search engine indexing by default"), settings.NoIndex) + `</div>
  <div class="fields-group">` + checkbox("allow_referrer_origin", adminT(loc, "admin.settings.allow_referrer_origin.title", "Allow origin referrer information for external links"), settings.AllowReferrerOrigin) + `<p class="hint">` + html.EscapeString(adminT(loc, "admin.settings.allow_referrer_origin.desc", "When enabled, external sites can see this server as the source of traffic, but never the full page URL.")) + `</p></div>
  <h2>` + html.EscapeString(adminT(loc, "admin.settings.discovery.public_apis", "Public APIs")) + `</h2>
  <div class="fields-group">` + checkbox("activity_api_enabled", adminSettingsLabel(loc, "activity_api_enabled", "Publish aggregate statistics about user activity in the API"), settings.ActivityAPIEnabled) + `</div>
  <div class="fields-group">` + checkbox("peers_api_enabled", adminSettingsLabel(loc, "peers_api_enabled", "Publish list of discovered servers in the API"), settings.PeersAPIEnabled) + `</div>
  <div class="fields-group">` + adminAuthorizedFetchCheckboxHTML(settings, loc) + `</div>
  <h2>` + html.EscapeString(adminT(loc, "admin.settings.discovery.follow_recommendations", "Follow recommendations")) + `</h2>
  <div class="fields-group"><label>` + html.EscapeString(adminSettingsLabel(loc, "bootstrap_timeline_accounts", "Always recommend these accounts to new users")) + ` <input name="form_admin_settings[bootstrap_timeline_accounts]" value="` + html.EscapeString(settings.BootstrapTimelineAccounts) + `"></label></div>
  <h2>` + html.EscapeString(adminT(loc, "admin.settings.discovery.profile_directory", "Profile directory")) + `</h2>
  <div class="fields-group">` + checkbox("profile_directory", adminSettingsLabel(loc, "profile_directory", "Enable profile directory"), settings.ProfileDirectory) + `</div>
  <div class="actions"><button type="submit">` + html.EscapeString(settingsT(loc, "generic.save_changes", "Save changes")) + `</button></div>
</form>`
	return adminSettingsPageHTML(title, "discovery", notice, errorText, body, loc, theme)
}

func adminAuthorizedFetchCheckboxHTML(settings adminDiscoverySettings, locale string) string {
	label := adminT(locale, "admin.settings.security.authorized_fetch", "Require authentication from federated servers")
	checked := ""
	if settings.AuthorizedFetch {
		checked = " checked"
	}
	if !settings.AuthorizedFetchOverridden {
		return `<input type="hidden" name="form_admin_settings[authorized_fetch]" value="0">
  <label><input type="checkbox" name="form_admin_settings[authorized_fetch]" value="1"` + checked + `> ` + html.EscapeString(label) + `</label>`
	}
	hint := adminT(locale, "admin.settings.security.authorized_fetch_overridden_hint", "This setting is currently overridden by the server configuration.")
	return `<label><input type="checkbox" name="form_admin_settings[authorized_fetch]" value="1"` + checked + ` disabled data-recommended="overridden"> ` + html.EscapeString(label) + `</label>
  <p class="hint">` + html.EscapeString(hint) + `</p>`
}
