package web

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/i18n"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"github.com/mstdn-plusminus-io/paon/internal/paon/serializer"
)

type Renderer struct {
	cfg      config.Config
	manifest map[string]string
}

type AppOptions struct {
	DocumentTitle          string
	HeadMeta               []HeadMeta
	HeadLinks              []HeadLink
	HeadJSONLD             []string
	SiteTitle              string
	SiteTitleSet           bool
	RegistrationsOpen      bool
	MascotURL              string
	AdminAccount           *models.Account
	OwnerAccount           *models.Account
	Role                   *models.UserRole
	EveryoneRole           *models.UserRole
	ServerSettings         *serializer.InitialStateServerSettings
	Settings               map[string]any
	User                   *models.User
	DisabledAccount        *models.Account
	MovedToAccount         *models.Account
	PushSubscription       *models.WebPushSubscription
	CriticalUpdatesPending *bool
	TermsOfServiceEnabled  bool
	ComposeText            string
	ComposeVisibility      string
	IncludeCSRFMeta        bool
	CustomCSSPath          string
}

type HeadMeta struct {
	Name     string
	Property string
	Content  string
}

type HeadLink struct {
	Rel  string
	Type string
	Href string
}

func firstNonEmptyString(values []string, fallback string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return fallback
}

func NewRenderer(cfg config.Config) (*Renderer, error) {
	if strings.TrimSpace(cfg.PublicDir) == "" {
		return &Renderer{cfg: cfg, manifest: map[string]string{}}, nil
	}
	raw, err := os.ReadFile(filepath.Join(cfg.PublicDir, "packs", "manifest.json"))
	if err != nil {
		return nil, fmt.Errorf("read public packs manifest: %w", err)
	}
	return &Renderer{cfg: cfg, manifest: parsePackManifest(raw)}, nil
}

func ValidatePublicAssets(cfg config.Config) error {
	if strings.TrimSpace(cfg.PublicDir) == "" {
		return fmt.Errorf("PAON_PUBLIC_DIR is required")
	}
	for _, name := range requiredPublicFiles() {
		if err := validateLocalPublicFile(cfg.PublicDir, name); err != nil {
			return err
		}
	}
	raw, err := os.ReadFile(filepath.Join(cfg.PublicDir, "packs", "manifest.json"))
	if err != nil {
		return fmt.Errorf("read public packs manifest: %w", err)
	}
	manifest := parsePackManifest(raw)
	for _, name := range requiredPackAssets(cfg) {
		assetPath := strings.TrimSpace(manifest[name])
		if assetPath == "" {
			return fmt.Errorf("public packs manifest is missing %s", name)
		}
		if err := validatePackManifestLocalFile(cfg.PublicDir, name, assetPath); err != nil {
			return err
		}
	}
	for name, assetPath := range manifest {
		if !strings.HasPrefix(name, "locale/") || !strings.HasSuffix(name, "-json.js") {
			continue
		}
		if err := validatePackManifestLocalFile(cfg.PublicDir, name, assetPath); err != nil {
			return err
		}
	}
	return nil
}

func validatePackManifestLocalFile(publicDir, name, assetPath string) error {
	assetPath = strings.TrimSpace(assetPath)
	if strings.HasPrefix(assetPath, "http://") || strings.HasPrefix(assetPath, "https://") {
		return nil
	}
	relative := strings.TrimPrefix(assetPath, "/")
	if err := validateLocalPublicFile(publicDir, relative); err != nil {
		return fmt.Errorf("public packs asset %s points to missing file %s: %w", name, filepath.Join(publicDir, relative), err)
	}
	return nil
}

func ValidateServerRenderedLocales(cfg config.Config) error {
	if strings.TrimSpace(cfg.PublicDir) == "" {
		return fmt.Errorf("PAON_PUBLIC_DIR is required")
	}
	dir := rendererLocalesDir(cfg.PublicDir)
	if dir == "" {
		return fmt.Errorf("config/locales directory is required")
	}
	store := i18n.NewStore(dir)
	for _, locale := range []string{"en", i18n.NormalizeLocale(cfg.Locale())} {
		if locale == "" {
			continue
		}
		dict := store.Dict(locale)
		if len(dict) == 0 {
			return fmt.Errorf("config/locales is missing server-rendered translations for %s under %s", locale, dir)
		}
		for _, key := range requiredServerRenderedLocaleKeys() {
			if strings.TrimSpace(dict[key]) == "" {
				return fmt.Errorf("config/locales is missing server-rendered translation %s for %s under %s", key, locale, dir)
			}
		}
	}
	return nil
}

func requiredServerRenderedLocaleKeys() []string {
	return []string{
		"auth.login",
		"auth.register",
		"settings.title",
		"admin.dashboard.title",
		"simple_form.labels.defaults.email",
		"doorkeeper.authorizations.buttons.authorize",
		"devise.passwords.send_instructions",
	}
}

func requiredPackAssets(cfg config.Config) []string {
	required := []string{
		"application.js",
		"embed.js",
		"admin.js",
		"public.js",
		"error.js",
		"mailer.js",
		"share.js",
		"sign_up.js",
		"two_factor_authentication.js",
		"common.js",
		"common.css",
		"mailer.css",
		"base_polyfills.js",
		"extra_polyfills.js",
		"i18n-pluralrules-polyfill.js",
		"arrow-key-navigation.js",
		"media/images/logo-symbol-icon.svg",
		"media/images/mailer/icon_cached.png",
		"media/images/mailer/icon_done.png",
		"media/images/mailer/icon_email.png",
		"media/images/mailer/icon_file_download.png",
		"media/images/mailer/icon_flag.png",
		"media/images/mailer/icon_grade.png",
		"media/images/mailer/icon_lock_open.png",
		"media/images/mailer/icon_person_add.png",
		"media/images/mailer/icon_reply.png",
		"media/images/mailer/logo.png",
		"media/images/mailer/wordmark.png",
		"media/images/mailer-new/common/header-bg-start.png",
		"media/images/mailer-new/common/header-bg-end.png",
		"media/images/mailer-new/common/logo-header.png",
		"media/images/mailer-new/common/logo-footer.png",
		"media/icons/favicon-16x16.png",
		"media/icons/favicon-32x32.png",
		"media/icons/favicon-48x48.png",
		"media/icons/android-chrome-36x36.png",
		"media/icons/android-chrome-48x48.png",
		"media/icons/android-chrome-72x72.png",
		"media/icons/android-chrome-96x96.png",
		"media/icons/android-chrome-144x144.png",
		"media/icons/android-chrome-192x192.png",
		"media/icons/android-chrome-256x256.png",
		"media/icons/android-chrome-384x384.png",
		"media/icons/android-chrome-512x512.png",
		"media/icons/apple-touch-icon-57x57.png",
		"media/icons/apple-touch-icon-60x60.png",
		"media/icons/apple-touch-icon-72x72.png",
		"media/icons/apple-touch-icon-76x76.png",
		"media/icons/apple-touch-icon-114x114.png",
		"media/icons/apple-touch-icon-120x120.png",
		"media/icons/apple-touch-icon-144x144.png",
		"media/icons/apple-touch-icon-152x152.png",
		"media/icons/apple-touch-icon-167x167.png",
		"media/icons/apple-touch-icon-180x180.png",
		"media/icons/apple-touch-icon-1024x1024.png",
		"emoji_picker.js",
		"containers/media_container.js",
		"features/compose.js",
		"features/home_timeline.js",
		"features/notifications.js",
		"features/notifications/requests.js",
		"features/notifications/request.js",
		"features/public_timeline.js",
		"features/community_timeline.js",
		"features/firehose.js",
		"features/hashtag_timeline.js",
		"features/direct_timeline.js",
		"features/list_timeline.js",
		"features/lists.js",
		"features/status.js",
		"features/getting_started.js",
		"features/keyboard_shortcuts.js",
		"features/pinned_statuses.js",
		"features/account_timeline.js",
		"features/account_featured.js",
		"features/account_gallery.js",
		"features/followers.js",
		"features/following.js",
		"features/reblogs.js",
		"features/favourites.js",
		"features/quotes.js",
		"features/follow_requests.js",
		"features/favourited_statuses.js",
		"features/followed_tags.js",
		"features/bookmarked_statuses.js",
		"features/blocks.js",
		"features/domain_blocks.js",
		"features/mutes.js",
		"modals/mute_modal.js",
		"modals/block_modal.js",
		"modals/domain_block_modal.js",
		"modals/report_modal.js",
		"modals/embed_modal.js",
		"features/list_editor.js",
		"features/list_adder.js",
		"tesseract.js",
		"features/directory.js",
		"features/onboarding.js",
		"modals/compare_history_modal.js",
		"features/explore.js",
		"features/link_timeline.js",
		"modals/filter_modal.js",
		"modals/interaction_modal.js",
		"modals/subscribed_languages_modal.js",
		"modals/closed_registrations_modal.js",
		"modals/annual_report_modal.js",
		"features/instance_stats.js",
		"features/about.js",
		"features/privacy_policy.js",
		"features/terms_of_service.js",
		"remote_interaction_helper.js",
	}
	required = appendRailsLocalePackAssets(required, cfg.Locale())
	for _, theme := range supportedThemes() {
		required = append(required, theme+".css")
	}
	return required
}

func appendRailsLocalePackAssets(required []string, configuredLocale string) []string {
	seen := make(map[string]struct{}, len(required)+len(config.RailsI18nAvailableLocales())+1)
	for _, name := range required {
		seen[name] = struct{}{}
	}
	for _, locale := range append(config.RailsI18nAvailableLocales(), configuredLocale) {
		locale = strings.TrimSpace(locale)
		if locale == "" {
			continue
		}
		name := "locale/" + locale + "-json.js"
		if _, ok := seen[name]; ok {
			continue
		}
		required = append(required, name)
		seen[name] = struct{}{}
	}
	return required
}

func requiredPublicFiles() []string {
	return []string{
		"badge.png",
		"favicon.ico",
		"inert.css",
		"oops.gif",
		"oops.png",
		"robots.txt",
		"embed.js",
		filepath.Join("avatars", "original", "missing.png"),
		filepath.Join("headers", "original", "missing.png"),
		filepath.Join("packs", "sw.js"),
		filepath.Join("sounds", "boop.mp3"),
		filepath.Join("sounds", "boop.ogg"),
		filepath.Join("ocr", "lang-data", "eng.traineddata.gz"),
		"web-push-icon_expand.png",
		"web-push-icon_favourite.png",
		"web-push-icon_reblog.png",
	}
}

func validateLocalPublicFile(publicDir string, name string) error {
	localPath := filepath.Join(publicDir, filepath.Clean(strings.TrimPrefix(name, "/")))
	info, err := os.Stat(localPath)
	if err != nil {
		return fmt.Errorf("public file %s is missing: %w", localPath, err)
	}
	if info.IsDir() {
		return fmt.Errorf("public file %s is a directory", localPath)
	}
	return nil
}

func parsePackManifest(raw []byte) map[string]string {
	manifest := map[string]string{}
	var entries map[string]json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		return manifest
	}
	for name, entry := range entries {
		var path string
		if err := json.Unmarshal(entry, &path); err == nil {
			manifest[name] = path
			continue
		}
		var object struct {
			Src string `json:"src"`
		}
		if err := json.Unmarshal(entry, &object); err == nil && object.Src != "" {
			manifest[name] = object.Src
		}
	}
	return manifest
}

func (r *Renderer) AppHTML(path string, current *models.Account, token string, options ...AppOptions) (string, error) {
	opts := firstAppOptions(options)
	initial := serializer.InitialStateFromConfigWithOptions(r.cfg, current, token, serializer.InitialStateOptions{
		SiteTitle:              opts.SiteTitle,
		SiteTitleSet:           opts.SiteTitleSet,
		RegistrationsOpen:      opts.RegistrationsOpen,
		MascotURL:              opts.MascotURL,
		AdminAccount:           opts.AdminAccount,
		OwnerAccount:           opts.OwnerAccount,
		Role:                   opts.Role,
		EveryoneRole:           opts.EveryoneRole,
		ServerSettings:         opts.ServerSettings,
		Settings:               opts.Settings,
		User:                   opts.User,
		DisabledAccount:        opts.DisabledAccount,
		MovedToAccount:         opts.MovedToAccount,
		PushSubscription:       opts.PushSubscription,
		CriticalUpdatesPending: opts.CriticalUpdatesPending,
		TermsOfServiceEnabled:  opts.TermsOfServiceEnabled,
		ComposeText:            opts.ComposeText,
		ComposeVisibility:      opts.ComposeVisibility,
	})
	return r.appHTML(path, initial, "mastodon", r.asset("application.js"), []string{
		r.asset("locale/" + r.cfg.Locale() + "-json.js"),
		r.asset("features/compose.js"),
		r.asset("features/home_timeline.js"),
		r.asset("features/notifications.js"),
	}, CSRFTokenForSession(token), opts.User, "app-body", appHTMLIncludesCSRFMeta(current, opts), opts.DocumentTitle, opts.HeadMeta, opts.HeadLinks, opts.HeadJSONLD, opts.CustomCSSPath)
}

func (r *Renderer) ShareHTML(current *models.Account, token string, text string, options ...AppOptions) (string, error) {
	opts := firstAppOptions(options)
	initial := serializer.InitialStateFromConfigWithOptions(r.cfg, current, token, serializer.InitialStateOptions{
		SiteTitle:              opts.SiteTitle,
		SiteTitleSet:           opts.SiteTitleSet,
		ComposeText:            text,
		RegistrationsOpen:      opts.RegistrationsOpen,
		MascotURL:              opts.MascotURL,
		AdminAccount:           opts.AdminAccount,
		OwnerAccount:           opts.OwnerAccount,
		Role:                   opts.Role,
		EveryoneRole:           opts.EveryoneRole,
		ServerSettings:         opts.ServerSettings,
		Settings:               opts.Settings,
		User:                   opts.User,
		DisabledAccount:        opts.DisabledAccount,
		MovedToAccount:         opts.MovedToAccount,
		PushSubscription:       opts.PushSubscription,
		CriticalUpdatesPending: opts.CriticalUpdatesPending,
		TermsOfServiceEnabled:  opts.TermsOfServiceEnabled,
		ComposeVisibility:      opts.ComposeVisibility,
	})
	return r.appHTML("/share", initial, "mastodon-compose", r.asset("share.js"), []string{
		r.asset("locale/" + r.cfg.Locale() + "-json.js"),
		r.asset("features/compose.js"),
	}, CSRFTokenForSession(token), opts.User, "modal-layout compose-standalone", true, opts.DocumentTitle, opts.HeadMeta, opts.HeadLinks, opts.HeadJSONLD, opts.CustomCSSPath)
}

// EmbedHTML renders the isolated React status mount used by Mastodon oEmbed.
// The status itself is fetched through the public REST API so this boot payload
// intentionally contains only anonymous instance state and the status ID prop.
func (r *Renderer) EmbedHTML(statusID string, siteTitle ...string) (string, error) {
	initial := serializer.InitialStateFromConfig(r.cfg, nil, "")
	initialJSON, err := json.Marshal(initial)
	if err != nil {
		return "", err
	}
	propsJSON, err := json.Marshal(map[string]string{
		"id":     statusID,
		"locale": r.cfg.Locale(),
	})
	if err != nil {
		return "", err
	}
	title := r.cfg.Title
	if len(siteTitle) > 0 && strings.TrimSpace(siteTitle[0]) != "" {
		title = strings.TrimSpace(siteTitle[0])
	}
	data := struct {
		Title       string
		Locale      string
		CDNHost     string
		StorageHost string
		InitialJSON template.JS
		Props       string
		CommonCSS   string
		ThemeCSS    string
		CommonJS    string
		LocaleJS    string
		EmbedJS     string
	}{
		Title:       title,
		Locale:      r.cfg.Locale(),
		CDNHost:     r.cfg.CDNHost,
		StorageHost: r.cfg.StorageHost,
		InitialJSON: template.JS(initialJSON),
		Props:       string(propsJSON),
		CommonCSS:   r.asset("common.css"),
		ThemeCSS:    r.asset("mastodon-light.css"),
		CommonJS:    r.asset("common.js"),
		LocaleJS:    r.asset("locale/" + r.cfg.Locale() + "-json.js"),
		EmbedJS:     r.asset("embed.js"),
	}

	var out string
	err = embedTemplate.Execute(&stringWriter{target: &out}, data)
	return out, err
}

func firstAppOptions(options []AppOptions) AppOptions {
	if len(options) == 0 {
		return AppOptions{}
	}
	return options[0]
}

func appHTMLIncludesCSRFMeta(current *models.Account, opts AppOptions) bool {
	return opts.IncludeCSRFMeta || opts.User != nil || current != nil
}

func (r *Renderer) RemoteInteractionHelperHTML() (string, error) {
	data := struct {
		CommonJS string
		HelperJS string
	}{
		CommonJS: r.asset("common.js"),
		HelperJS: r.asset("remote_interaction_helper.js"),
	}

	var out string
	err := helperTemplate.Execute(&stringWriter{target: &out}, data)
	return out, err
}

func (r *Renderer) SettingsHTML(path string, current *models.Account, siteTitle ...string) (string, error) {
	displayName := current.DisplayName
	if displayName == "" {
		displayName = current.Username
	}
	title := r.cfg.Title
	if len(siteTitle) > 0 && strings.TrimSpace(siteTitle[0]) != "" {
		title = strings.TrimSpace(siteTitle[0])
	}
	locale := r.cfg.Locale()
	data := struct {
		Title       string
		Path        string
		DisplayName string
		Acct        string
		Locale      string
		Heading     string
		ServedBy    string
		BackToApp   string
		Nav         []settingsFallbackNavItem
	}{
		Title:       title,
		Path:        path,
		DisplayName: displayName,
		Acct:        current.Acct(),
		Locale:      locale,
		Heading:     r.settingsFallbackT(locale, "settings.title", "Settings"),
		ServedBy:    r.settingsFallbackT(locale, "settings.paon_go_served_by", "is served by Paon using the existing Mastodon database tables and Rails-compatible form field names."),
		BackToApp:   r.settingsFallbackT(locale, "settings.back_to_app", "Back to app"),
		Nav:         r.settingsFallbackNav(locale),
	}

	var out string
	err := settingsTemplate.Execute(&stringWriter{target: &out}, data)
	return out, err
}

type settingsFallbackNavItem struct {
	Path  string
	Label string
}

func (r *Renderer) settingsFallbackNav(locale string) []settingsFallbackNavItem {
	items := []struct {
		path     string
		key      string
		fallback string
	}{
		{"/settings/profile", "settings.profile", "Profile"},
		{"/settings/preferences/appearance", "settings.appearance", "Appearance"},
		{"/settings/preferences/notifications", "settings.notifications", "Notifications"},
		{"/settings/preferences/other", "settings.other", "Other"},
		{"/settings/privacy", "settings.privacy", "Privacy"},
		{"/settings/export", "settings.export", "Export"},
		{"/settings/imports", "settings.import", "Import"},
		{"/settings/applications", "settings.development", "Applications"},
		{"/settings/login_activities", "settings.login_activities", "Login activity"},
		{"/settings/sessions", "settings.sessions", "Sessions"},
		{"/settings/two_factor_authentication_methods", "settings.account_security", "Two-factor auth"},
		{"/settings/security_keys", "settings.webauthn_authentication", "Security keys"},
		{"/settings/aliases", "settings.account_aliases", "Aliases"},
		{"/settings/migration", "settings.migrate", "Migration"},
		{"/settings/featured_tags", "settings.featured_tags", "Featured tags"},
		{"/settings/verification", "settings.verification", "Verification"},
		{"/settings/delete", "settings.delete", "Delete account"},
	}
	out := make([]settingsFallbackNavItem, 0, len(items))
	for _, item := range items {
		out = append(out, settingsFallbackNavItem{
			Path:  item.path,
			Label: r.settingsFallbackT(locale, item.key, item.fallback),
		})
	}
	return out
}

func (r *Renderer) settingsFallbackT(locale string, key string, fallback string) string {
	dir := rendererLocalesDir(r.cfg.PublicDir)
	if dir == "" {
		return fallback
	}
	value := i18n.NewStore(dir).T(locale, key, nil)
	if value == "" || value == key {
		return fallback
	}
	return value
}

func rendererLocalesDir(publicDir string) string {
	if strings.TrimSpace(publicDir) == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(publicDir), "config", "locales")
}

func (r *Renderer) appHTML(path string, initial serializer.InitialState, mountID string, appJS string, preloads []string, csrfToken string, user *models.User, baseBodyClasses string, includeCSRFMeta bool, documentTitle string, headMeta []HeadMeta, headLinks []HeadLink, headJSONLD []string, customCSSPath ...string) (string, error) {
	initialJSON, err := json.Marshal(initial)
	if err != nil {
		return "", err
	}
	locale := r.cfg.Locale()
	propsJSON, err := json.Marshal(map[string]any{"locale": locale})
	if err != nil {
		return "", err
	}

	title := r.cfg.Title
	if metaTitle, ok := initial.Meta["title"].(string); ok && strings.TrimSpace(metaTitle) != "" {
		title = metaTitle
	}
	if strings.TrimSpace(documentTitle) != "" {
		title = documentTitle
	}

	jsonLD := make([]template.JS, 0, len(headJSONLD))
	for _, raw := range headJSONLD {
		var value any
		if err := json.Unmarshal([]byte(raw), &value); err != nil {
			continue
		}
		safe, err := json.Marshal(value)
		if err == nil {
			jsonLD = append(jsonLD, template.JS(safe))
		}
	}
	data := struct {
		Title       string
		InitialPath string
		CDNHost     string
		StorageHost string
		ServerKey   string
		InitialJSON template.JS
		Props       string
		MountID     string
		CommonJS    string
		AppJS       string
		Preloads    []string
		CommonCSS   string
		ThemeStyles []headStylesheet
		Theme       string
		Favicons    []headIcon
		AppleIcons  []headIcon
		MaskIcon    string
		BodyClasses string
		RemoteCache string
		CSRFMeta    bool
		CSRFParam   string
		CSRFToken   string
		Locale      string
		HeadMeta    []HeadMeta
		HeadLinks   []HeadLink
		HeadJSONLD  []template.JS
		CustomCSS   string
	}{
		Title:       title,
		InitialPath: path,
		CDNHost:     r.cfg.CDNHost,
		StorageHost: r.cfg.StorageHost,
		ServerKey:   r.cfg.VapidPublicKey,
		InitialJSON: template.JS(initialJSON),
		Props:       string(propsJSON),
		MountID:     mountID,
		CommonJS:    r.asset("common.js"),
		AppJS:       appJS,
		Preloads:    preloads,
		CommonCSS:   r.asset("common.css"),
		ThemeStyles: r.themeStylesheets(user),
		Theme:       selectedTheme(user),
		Favicons:    r.faviconLinks(),
		AppleIcons:  r.appleTouchIconLinks(),
		MaskIcon:    r.asset("media/images/logo-symbol-icon.svg"),
		BodyClasses: bodyClasses(baseBodyClasses, user, locale),
		RemoteCache: strconv.FormatBool(r.cfg.DisableRemoteMediaCache || r.cfg.DisableRemoteMediaCacheSet),
		CSRFMeta:    includeCSRFMeta,
		CSRFParam:   "authenticity_token",
		CSRFToken:   csrfToken,
		Locale:      locale,
		HeadMeta:    headMeta,
		HeadLinks:   headLinks,
		HeadJSONLD:  jsonLD,
		CustomCSS:   firstNonEmptyString(customCSSPath, "/custom.css"),
	}

	var out string
	err = appTemplate.Execute(&stringWriter{target: &out}, data)
	return out, err
}

type headIcon struct {
	Rel   string
	Sizes string
	Href  string
	Type  string
}

type headStylesheet struct {
	Href  string
	Media string
}

func (r *Renderer) faviconLinks() []headIcon {
	sizes := []string{"16", "32", "48"}
	links := make([]headIcon, 0, len(sizes))
	for _, size := range sizes {
		dimensions := size + "x" + size
		links = append(links, headIcon{Rel: "icon", Sizes: dimensions, Href: "/favicon-" + dimensions + ".png", Type: "image/png"})
	}
	return links
}

func (r *Renderer) appleTouchIconLinks() []headIcon {
	sizes := []string{"57", "60", "72", "76", "114", "120", "144", "152", "167", "180", "1024"}
	links := make([]headIcon, 0, len(sizes))
	for _, size := range sizes {
		dimensions := size + "x" + size
		links = append(links, headIcon{Rel: "apple-touch-icon", Sizes: dimensions, Href: "/apple-touch-icon-" + dimensions + ".png"})
	}
	return links
}

func themeCSSAssetName(user *models.User) string {
	theme := selectedTheme(user)
	if theme == "system" {
		theme = "default"
	}
	return theme + ".css"
}

func selectedTheme(user *models.User) string {
	theme := "system"
	settings := userSettings(user)
	if value, ok := settings["theme"].(string); ok && supportedTheme(value) {
		theme = value
	}
	return theme
}

func (r *Renderer) themeStylesheets(user *models.User) []headStylesheet {
	theme := selectedTheme(user)
	if theme == "system" {
		return []headStylesheet{
			{Href: r.asset("mastodon-light.css"), Media: "not all and (prefers-color-scheme: dark)"},
			{Href: r.asset("default.css"), Media: "(prefers-color-scheme: dark)"},
		}
	}
	return []headStylesheet{{Href: r.asset(theme + ".css"), Media: "all"}}
}

func bodyClasses(base string, user *models.User, locale string) string {
	settings := userSettings(user)
	classes := strings.Fields(base)
	theme := selectedTheme(user)
	classes = append(classes, "theme-"+theme)
	if boolSetting(settings, "web.use_system_font") {
		classes = append(classes, "system-font")
	}
	if !boolSetting(settings, "web.use_system_scrollbars") {
		classes = append(classes, "custom-scrollbars")
	}
	if boolSetting(settings, "web.reduce_motion") {
		classes = append(classes, "reduce-motion")
	} else {
		classes = append(classes, "no-reduce-motion")
	}
	if rtlLocale(locale) {
		classes = append(classes, "rtl")
	}
	return strings.Join(classes, " ")
}

func userSettings(user *models.User) map[string]any {
	settings := map[string]any{}
	if user == nil || !user.Settings.Valid || strings.TrimSpace(user.Settings.String) == "" {
		return settings
	}
	_ = json.Unmarshal([]byte(user.Settings.String), &settings)
	return settings
}

func boolSetting(settings map[string]any, key string) bool {
	switch value := settings[key].(type) {
	case bool:
		return value
	case string:
		return value == "true" || value == "1"
	case float64:
		return value != 0
	default:
		return false
	}
}

func rtlLocale(locale string) bool {
	base, _, _ := strings.Cut(strings.ToLower(locale), "-")
	switch base {
	case "ar", "ckb", "fa", "he", "ku", "ur":
		return true
	default:
		return false
	}
}

func supportedTheme(theme string) bool {
	if theme == "system" {
		return true
	}
	for _, supported := range supportedThemes() {
		if theme == supported {
			return true
		}
	}
	return false
}

func supportedThemes() []string {
	return []string{"default", "contrast", "mastodon-light", "single-column-chat-dark"}
}

func CSRFTokenForSession(token string) string {
	seed := token
	if seed == "" {
		seed = "anonymous"
	}
	sum := sha256.Sum256([]byte("paon-go-csrf:" + seed))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func (r *Renderer) asset(name string) string {
	if value := r.manifest[name]; value != "" {
		return value
	}
	return FallbackPackAssetPath(name)
}

func FallbackPackAssetPath(name string) string {
	name = strings.TrimPrefix(strings.TrimSpace(name), "/")
	if name == "" {
		return ""
	}
	if strings.HasPrefix(name, "media/") {
		return "/packs/" + name
	}
	if strings.HasSuffix(name, ".css") {
		return "/packs/css/" + name
	}
	if strings.HasSuffix(name, ".js") {
		return "/packs/js/" + name
	}
	return ""
}

// ResolvePackAssetPath returns the production fingerprinted path when a
// manifest is available and otherwise uses the deterministic development
// layout. Mail delivery uses this without constructing a full HTML renderer.
func ResolvePackAssetPath(cfg config.Config, name string) string {
	if strings.TrimSpace(cfg.PublicDir) != "" {
		raw, err := os.ReadFile(filepath.Join(cfg.PublicDir, "packs", "manifest.json"))
		if err == nil {
			if value := strings.TrimSpace(parsePackManifest(raw)[name]); value != "" {
				return value
			}
		}
	}
	return FallbackPackAssetPath(name)
}

func (r *Renderer) Asset(name string) string {
	return r.asset(name)
}

func (r *Renderer) RemoteMediaCacheMetaValue() string {
	if r == nil {
		return "false"
	}
	return strconv.FormatBool(r.cfg.DisableRemoteMediaCache || r.cfg.DisableRemoteMediaCacheSet)
}

type stringWriter struct {
	target *string
}

func (w *stringWriter) Write(p []byte) (int, error) {
	*w.target += string(p)
	return len(p), nil
}

var appTemplate = template.Must(template.New("app").Parse(`<!DOCTYPE html>
<html lang="{{ .Locale }}" class="app-ready">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  {{- if .CDNHost }}
  <link rel="dns-prefetch" href="{{ .CDNHost }}">
  <meta name="cdn-host" content="{{ .CDNHost }}">
  {{- end }}
  {{- if .StorageHost }}
  <link rel="dns-prefetch" href="{{ .StorageHost }}">
  {{- end }}
  <link rel="icon" href="/favicon.ico" type="image/x-icon">
  {{- range .Favicons }}
  <link rel="{{ .Rel }}" sizes="{{ .Sizes }}" href="{{ .Href }}" type="{{ .Type }}">
  {{- end }}
  {{- range .AppleIcons }}
  <link rel="{{ .Rel }}" sizes="{{ .Sizes }}" href="{{ .Href }}">
  {{- end }}
  {{- if .MaskIcon }}
  <link rel="mask-icon" href="{{ .MaskIcon }}" color="#6364FF">
  {{- end }}
  <link rel="manifest" href="/manifest.json">
  {{- if eq .Theme "system" }}
  <meta name="theme-color" content="#181820" media="(prefers-color-scheme: dark)">
  <meta name="theme-color" content="#ffffff" media="(prefers-color-scheme: light)">
  {{- else if eq .Theme "mastodon-light" }}
  <meta name="theme-color" content="#ffffff">
  {{- else }}
  <meta name="theme-color" content="#181820">
  {{- end }}
  <meta name="apple-mobile-web-app-capable" content="yes">
  <meta name="plusminus-disable-remote-media-cache" content="{{ .RemoteCache }}">
  <meta name="initialPath" content="{{ .InitialPath }}">
  <meta name="applicationServerKey" content="{{ .ServerKey }}">
  {{- if .CSRFMeta }}
  <meta name="csrf-param" content="{{ .CSRFParam }}">
  <meta name="csrf-token" content="{{ .CSRFToken }}">
  {{- end }}
  {{- range .HeadMeta }}
  {{- if .Name }}
  <meta name="{{ .Name }}" content="{{ .Content }}">
  {{- else if .Property }}
  <meta property="{{ .Property }}" content="{{ .Content }}">
  {{- end }}
  {{- end }}
  {{- range .HeadLinks }}
  <link rel="{{ .Rel }}"{{ if .Type }} type="{{ .Type }}"{{ end }} href="{{ .Href }}">
  {{- end }}
  {{- range .HeadJSONLD }}
  <script type="application/ld+json">{{ . }}</script>
  {{- end }}
  <title>{{ .Title }}</title>
  {{- if .CommonCSS }}
  <link rel="stylesheet" media="all" href="{{ .CommonCSS }}" crossorigin="anonymous">
  {{- end }}
  {{- range .ThemeStyles }}
  <link rel="stylesheet" media="{{ .Media }}" href="{{ .Href }}" crossorigin="anonymous">
  {{- end }}
  <link rel="stylesheet" media="all" id="inert-style" href="/inert.css">
  <link rel="stylesheet" media="all" href="{{ .CustomCSS }}">
  {{- range .Preloads }}
  {{- if . }}
  <link rel="preload" as="script" href="{{ . }}" crossorigin="anonymous">
  {{- end }}
  {{- end }}
  <script id="initial-state" type="application/json">{{ .InitialJSON }}</script>
  {{- if .CommonJS }}
  <script src="{{ .CommonJS }}" crossorigin="anonymous" defer></script>
  {{- end }}
  {{- if .AppJS }}
  <script src="{{ .AppJS }}" crossorigin="anonymous" defer></script>
  {{- end }}
</head>
<body class="{{ .BodyClasses }}">
  <div class="notranslate app-holder" id="{{ .MountID }}" data-props="{{ .Props }}">
    <noscript>
      <div>Mastodon requires JavaScript.</div>
    </noscript>
  </div>
</body>
</html>`))

var helperTemplate = template.Must(template.New("helper").Parse(`<!DOCTYPE html>
<html>
<head>
  <meta charset="utf-8">
  <meta name="robots" content="noindex">
  {{- if .CommonJS }}
  <script src="{{ .CommonJS }}" crossorigin="anonymous" defer></script>
  {{- end }}
  {{- if .HelperJS }}
  <script src="{{ .HelperJS }}" crossorigin="anonymous" defer></script>
  {{- end }}
</head>
</html>`))

var embedTemplate = template.Must(template.New("embed").Parse(`<!DOCTYPE html>
<html lang="{{ .Locale }}">
<head>
  <meta charset="utf-8">
  <meta name="robots" content="noindex">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  {{- if .CDNHost }}
  <link rel="dns-prefetch" href="{{ .CDNHost }}">
  <meta name="cdn-host" content="{{ .CDNHost }}">
  {{- end }}
  {{- if .StorageHost }}
  <link rel="dns-prefetch" href="{{ .StorageHost }}">
  {{- end }}
  <title>{{ .Title }}</title>
  {{- if .CommonCSS }}
  <link rel="stylesheet" media="all" href="{{ .CommonCSS }}" crossorigin="anonymous">
  {{- end }}
  {{- if .ThemeCSS }}
  <link rel="stylesheet" media="all" href="{{ .ThemeCSS }}" crossorigin="anonymous">
  {{- end }}
  {{- if .LocaleJS }}
  <link rel="preload" as="script" href="{{ .LocaleJS }}" crossorigin="anonymous">
  {{- end }}
  <script id="initial-state" type="application/json">{{ .InitialJSON }}</script>
  {{- if .CommonJS }}
  <script src="{{ .CommonJS }}" crossorigin="anonymous" defer></script>
  {{- end }}
  {{- if .EmbedJS }}
  <script src="{{ .EmbedJS }}" crossorigin="anonymous" defer></script>
  {{- end }}
</head>
<body class="embed theme-mastodon-light no-reduce-motion">
  <div id="mastodon-status" data-props="{{ .Props }}"></div>
</body>
</html>`))

var settingsTemplate = template.Must(template.New("settings").Parse(`<!DOCTYPE html>
<html lang="{{ .Locale }}">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <meta name="robots" content="noindex">
  <title>{{ .Title }}</title>
  <style>
    body{margin:0;background:#f6f8fa;color:#191b22;font:14px -apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif}
    main{max-width:760px;margin:32px auto;padding:24px;background:#fff;border:1px solid #d9e1e8;border-radius:6px}
    h1{font-size:22px;margin:0 0 8px}
    p{line-height:1.5}
    nav{display:flex;flex-wrap:wrap;gap:8px;margin:18px 0}
    a{color:#2b90d9;text-decoration:none}
    nav a{padding:8px 10px;border:1px solid #d9e1e8;border-radius:4px}
    code{background:#eef2f5;padding:2px 4px;border-radius:3px}
  </style>
</head>
<body>
  <main>
    <h1>{{ .Heading }}</h1>
    <p>{{ .DisplayName }} <span>@{{ .Acct }}</span></p>
    <nav>
      {{- range .Nav }}
      <a href="{{ .Path }}">{{ .Label }}</a>
      {{- end }}
    </nav>
    <p><code>{{ .Path }}</code> {{ .ServedBy }}</p>
    <p><a href="/home">{{ .BackToApp }}</a></p>
  </main>
</body>
</html>`))
