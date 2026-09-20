package api

import (
	"html"
	"strings"
	"sync/atomic"

	"github.com/mstdn-plusminus-io/paon/internal/paon/web"
)

// appAssetPaths holds the webpack-manifest-resolved asset URLs used by every server-rendered
// HTML page. They are populated once at server startup from the shared *web.Renderer (manifest)
// and read by package-level HTML helpers that do not have a *Server receiver. Asset paths are
// identical for every Server instance (they come from the single built manifest), so a package
// global is safe.
type appAssetPaths struct {
	commonCSS       string
	themeCSS        string
	themes          map[string]string
	commonJS        string
	errorJS         string
	localeJS        string
	publicJS        string
	adminJS         string
	favicon16       string
	favicon32       string
	favicon48       string
	apple           map[string]string
	logoSVG         string
	logoDesktopSVG  string
	logoWordmarkSVG string
	remoteCacheMeta string
}

var appAssets atomic.Value

var appAppleTouchIconSizes = []string{"57", "60", "72", "76", "114", "120", "144", "152", "167", "180", "1024"}

func setAppAssets(r *web.Renderer) {
	if r == nil {
		return
	}
	appAssets.Store(appAssetPaths{
		commonCSS: r.Asset("common.css"),
		themeCSS:  r.Asset("default.css"),
		themes: map[string]string{
			"default":                 r.Asset("default.css"),
			"contrast":                r.Asset("contrast.css"),
			"mastodon-light":          r.Asset("mastodon-light.css"),
			"single-column-chat-dark": r.Asset("single-column-chat-dark.css"),
		},
		commonJS:        r.Asset("common.js"),
		errorJS:         r.Asset("error.js"),
		localeJS:        r.Asset("locale/en-json.js"),
		publicJS:        r.Asset("public.js"),
		adminJS:         r.Asset("admin.js"),
		favicon16:       r.Asset("media/icons/favicon-16x16.png"),
		favicon32:       r.Asset("media/icons/favicon-32x32.png"),
		favicon48:       r.Asset("media/icons/favicon-48x48.png"),
		apple:           appAppleTouchIcons(r),
		logoSVG:         r.Asset("media/images/logo-symbol-icon.svg"),
		logoDesktopSVG:  r.Asset("media/images/logo.svg"),
		logoWordmarkSVG: r.Asset("media/images/logo-symbol-wordmark.svg"),
		remoteCacheMeta: r.RemoteMediaCacheMetaValue(),
	})
}

func currentAppAssets() appAssetPaths {
	if v, ok := appAssets.Load().(appAssetPaths); ok && v.commonCSS != "" {
		if strings.TrimSpace(v.adminJS) == "" {
			v.adminJS = "/packs/js/admin.js"
		}
		if strings.TrimSpace(v.errorJS) == "" {
			v.errorJS = "/packs/js/error.js"
		}
		if strings.TrimSpace(v.favicon32) == "" {
			v.favicon32 = "/packs/media/icons/favicon-32x32.png"
		}
		if strings.TrimSpace(v.favicon48) == "" {
			v.favicon48 = "/packs/media/icons/favicon-48x48.png"
		}
		if strings.TrimSpace(v.logoWordmarkSVG) == "" {
			v.logoWordmarkSVG = "/packs/media/images/logo-symbol-wordmark.svg"
		}
		if strings.TrimSpace(v.logoDesktopSVG) == "" {
			v.logoDesktopSVG = "/packs/media/images/logo.svg"
		}
		if len(v.apple) == 0 {
			v.apple = fallbackAppleTouchIcons()
		} else {
			for _, size := range appAppleTouchIconSizes {
				if strings.TrimSpace(v.apple[size]) == "" {
					v.apple[size] = fallbackAppleTouchIcon(size)
				}
			}
		}
		if strings.TrimSpace(v.remoteCacheMeta) == "" {
			v.remoteCacheMeta = "false"
		}
		if v.themes == nil {
			v.themes = map[string]string{
				"default":                 firstNonEmpty(v.themeCSS, "/packs/css/default.css"),
				"contrast":                "/packs/css/contrast.css",
				"mastodon-light":          "/packs/css/mastodon-light.css",
				"single-column-chat-dark": "/packs/css/single-column-chat-dark.css",
			}
		}
		return v
	}
	// Fallback used in tests / before startup: un-hashed logical paths. The real server always
	// calls setAppAssets at startup, so production serves the hashed manifest paths.
	return appAssetPaths{
		commonCSS: "/packs/css/common.css",
		themeCSS:  "/packs/css/default.css",
		themes: map[string]string{
			"default":                 "/packs/css/default.css",
			"contrast":                "/packs/css/contrast.css",
			"mastodon-light":          "/packs/css/mastodon-light.css",
			"single-column-chat-dark": "/packs/css/single-column-chat-dark.css",
		},
		commonJS:        "/packs/js/common.js",
		errorJS:         "/packs/js/error.js",
		localeJS:        "/packs/js/locale/en-json.js",
		publicJS:        "/packs/js/public.js",
		adminJS:         "/packs/js/admin.js",
		favicon16:       "/packs/media/icons/favicon-16x16.png",
		favicon32:       "/packs/media/icons/favicon-32x32.png",
		favicon48:       "/packs/media/icons/favicon-48x48.png",
		apple:           fallbackAppleTouchIcons(),
		logoSVG:         "/packs/media/images/logo-symbol-icon.svg",
		logoDesktopSVG:  "/packs/media/images/logo.svg",
		logoWordmarkSVG: "/packs/media/images/logo-symbol-wordmark.svg",
		remoteCacheMeta: "false",
	}
}

func appAppleTouchIcons(r *web.Renderer) map[string]string {
	icons := make(map[string]string, len(appAppleTouchIconSizes))
	if r == nil {
		return fallbackAppleTouchIcons()
	}
	for _, size := range appAppleTouchIconSizes {
		dimensions := size + "x" + size
		icons[size] = r.Asset("media/icons/apple-touch-icon-" + dimensions + ".png")
	}
	return icons
}

func fallbackAppleTouchIcons() map[string]string {
	icons := make(map[string]string, len(appAppleTouchIconSizes))
	for _, size := range appAppleTouchIconSizes {
		icons[size] = fallbackAppleTouchIcon(size)
	}
	return icons
}

func fallbackAppleTouchIcon(size string) string {
	dimensions := size + "x" + size
	return "/packs/media/icons/apple-touch-icon-" + dimensions + ".png"
}

func buildAppleTouchIconLinks(_ appAssetPaths) string {
	var b strings.Builder
	for _, size := range appAppleTouchIconSizes {
		dimensions := size + "x" + size
		href := "/apple-touch-icon-" + dimensions + ".png"
		b.WriteString(`    <link rel="apple-touch-icon" sizes="`)
		b.WriteString(html.EscapeString(dimensions))
		b.WriteString(`" href="`)
		b.WriteString(html.EscapeString(href))
		b.WriteString(`">` + "\n")
	}
	return b.String()
}

// buildAppHead renders the <head> contents mirroring Rails layouts/application.html.haml:
// Mastodon branding (favicon, mask-icon, theme-color), common.css + current theme CSS,
// custom CSS, and the common.js + locale packs. The browser security middleware injects the
// session-bound CSRF meta tag; the CSP nonce meta is omitted because Go's CSP is header-based.
func buildAppHead(title string, theme ...string) string {
	a := currentAppAssets()
	themeName := "system"
	if len(theme) > 0 {
		themeName = normalizedWebTheme(theme[0])
	}
	themeColorTags := `    <meta name="theme-color" content="#181820" media="(prefers-color-scheme: dark)">
    <meta name="theme-color" content="#ffffff" media="(prefers-color-scheme: light)">`
	themeStyleTags := `    <link rel="stylesheet" href="` + html.EscapeString(a.themes["mastodon-light"]) + `" media="not all and (prefers-color-scheme: dark)" crossorigin="anonymous">
    <link rel="stylesheet" href="` + html.EscapeString(a.themes["default"]) + `" media="(prefers-color-scheme: dark)" crossorigin="anonymous">`
	if themeName != "system" {
		themeColor := "#181820"
		if themeName == "mastodon-light" {
			themeColor = "#ffffff"
		}
		themeColorTags = `    <meta name="theme-color" content="` + themeColor + `">`
		themeStyleTags = `    <link rel="stylesheet" href="` + html.EscapeString(a.themes[themeName]) + `" media="all" crossorigin="anonymous">`
	}
	return `<meta charset="utf-8">
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <meta name="robots" content="noindex,nofollow">
    <link rel="icon" href="/favicon.ico" type="image/x-icon">
    <link rel="icon" sizes="16x16" href="/favicon-16x16.png" type="image/png">
    <link rel="icon" sizes="32x32" href="/favicon-32x32.png" type="image/png">
    <link rel="icon" sizes="48x48" href="/favicon-48x48.png" type="image/png">
` + buildAppleTouchIconLinks(a) + `    <link rel="mask-icon" href="` + html.EscapeString(a.logoSVG) + `" color="#6364FF">
    <link rel="manifest" href="/manifest.json">
` + themeColorTags + `
    <meta name="apple-mobile-web-app-capable" content="yes">
    <meta name="plusminus-disable-remote-media-cache" content="` + html.EscapeString(a.remoteCacheMeta) + `">
    <title>` + html.EscapeString(title) + `</title>
    <link rel="stylesheet" href="` + html.EscapeString(a.commonCSS) + `" media="all" crossorigin="anonymous">
` + themeStyleTags + `
    <link rel="stylesheet" href="/inert.css" media="all" id="inert-style">
    <link rel="stylesheet" href="/custom.css" media="all">
    <script src="` + html.EscapeString(a.commonJS) + `" crossorigin="anonymous" defer></script>
    <script src="` + html.EscapeString(a.localeJS) + `" crossorigin="anonymous" defer></script>`
}

func normalizedWebTheme(theme string) string {
	switch strings.TrimSpace(theme) {
	case "system", "default", "contrast", "mastodon-light", "single-column-chat-dark":
		return strings.TrimSpace(theme)
	default:
		return "system"
	}
}

// authShellHTML mirrors Rails layouts/auth.html.haml: a .container-alt with a .logo-container
// (wordmark link) and a .form-container (flashes + content), built on buildAppHead plus the
// public.js pack. This is the shell for all auth pages (sign in/up/reset/2FA/...).
func authShellHTML(title string, notice string, errorText string, body string, locale ...string) string {
	loc := ""
	theme := "system"
	if len(locale) > 0 {
		loc = locale[0]
	}
	if len(locale) > 1 {
		theme = normalizedWebTheme(locale[1])
	}
	if loc == "" {
		loc = webDefaultLocaleValue()
	}
	flashes := settingsFlashHTML(notice, errorText)
	a := currentAppAssets()
	return `<!DOCTYPE html>
<html lang="` + html.EscapeString(loc) + `">
  <head>
    ` + buildAppHead(title, theme) + `
    <script src="` + html.EscapeString(a.publicJS) + `" crossorigin="anonymous" defer></script>
  </head>
  <body class="app-body lighter theme-` + html.EscapeString(theme) + ` no-reduce-motion">
    <div class="container-alt">
      <div class="logo-container">
        <h1><a href="/"><img class="logo logo--wordmark" alt="` + html.EscapeString(title) + `" src="` + html.EscapeString(a.logoWordmarkSVG) + `"></a></h1>
      </div>
      <div class="form-container">
		` + flashes + body + `
      </div>
    </div>
    <div class="logo-resources" tabindex="-1" aria-hidden="true"></div>
  </body>
</html>`
}

// simpleFormOpen opens a Mastodon simple_form form. method is the logical HTTP verb: "post" renders
// a plain POST; put/patch/delete are tunneled through the Rails _method hidden field.
func simpleFormOpen(action string, method string) string {
	verb := strings.ToLower(strings.TrimSpace(method))
	if verb == "" || verb == "post" {
		return `<form class="simple_form" method="post" action="` + html.EscapeString(action) + `" novalidate="novalidate">`
	}
	return `<form class="simple_form" method="post" action="` + html.EscapeString(action) + `" novalidate="novalidate"><input type="hidden" name="_method" value="` + html.EscapeString(verb) + `">`
}

// simpleFormClose closes a simple_form form.
func simpleFormClose() string {
	return `</form>`
}

// simpleTextInput renders a Mastodon .fields-group > .input.with_label > .label_input field,
// matching the structure styled by app/javascript/styles/mastodon/forms.scss.
func simpleTextInput(label string, name string, value string, inputType string, attrs string) string {
	if inputType == "" {
		inputType = "text"
	}
	id := strings.NewReplacer("[", "_", "]", "", ".", "_").Replace(name)
	required := strings.Contains(" "+attrs+" ", " required ") || strings.Contains(attrs, "required=")
	requiredClass := ""
	requiredMarker := ""
	ariaRequired := ""
	if required {
		requiredClass = " required"
		requiredMarker = ` <abbr title="required">*</abbr>`
		ariaRequired = ` aria-required="true"`
	}
	return `<div class="fields-group"><div class="input with_label` + requiredClass + `"><div class="label_input"><label class="` + html.EscapeString(inputType) + requiredClass + `" for="` + html.EscapeString(id) + `">` + html.EscapeString(label) + requiredMarker + `</label><div class="label_input__wrapper"><input class="` + html.EscapeString(inputType) + requiredClass + `" type="` + html.EscapeString(inputType) + `" id="` + html.EscapeString(id) + `" aria-label="` + html.EscapeString(label) + `"` + ariaRequired + ` name="` + html.EscapeString(name) + `" value="` + html.EscapeString(value) + `" ` + attrs + `></div></div></div></div>`
}

// simpleCheckbox renders a Mastodon boolean toggle field (.input.boolean.with_label).
func simpleCheckbox(label string, name string, checked bool) string {
	checkedAttr := ""
	if checked {
		checkedAttr = " checked"
	}
	escapedName := html.EscapeString(name)
	return `<div class="fields-group"><div class="input boolean with_label"><label class="boolean"><input type="hidden" name="` + escapedName + `" value="0"><input type="checkbox" name="` + escapedName + `" value="1"` + checkedAttr + `> ` + html.EscapeString(label) + `</label></div></div>`
}

// simpleSubmit renders the Mastodon .actions submit button.
func simpleSubmit(label string) string {
	return `<div class="actions"><button type="submit" class="button">` + html.EscapeString(label) + `</button></div>`
}

// authFormFooter renders the Mastodon .form-footer (Rails auth/shared/_links) around the given
// link HTML.
func authFormFooter(links string) string {
	if strings.TrimSpace(links) == "" {
		return ""
	}
	return `<div class="form-footer">` + links + `</div>`
}

func authSharedLinksHTML(controller string, signUpPath string, locale string) string {
	type authLink struct {
		href  string
		label string
	}
	var links []authLink
	switch controller {
	case "sessions":
		links = append(links,
			authLink{href: signUpPath, label: webT(locale, "auth.register")},
			authLink{href: "/auth/password/new", label: webT(locale, "auth.forgot_password")},
			authLink{href: "/auth/confirmation/new", label: webT(locale, "auth.didnt_get_confirmation")},
		)
	case "registrations":
		links = append(links,
			authLink{href: "/auth/sign_in", label: webT(locale, "auth.login")},
			authLink{href: "/auth/confirmation/new", label: webT(locale, "auth.didnt_get_confirmation")},
		)
	case "passwords":
		links = append(links,
			authLink{href: "/auth/sign_in", label: webT(locale, "auth.login")},
			authLink{href: signUpPath, label: webT(locale, "auth.register")},
			authLink{href: "/auth/confirmation/new", label: webT(locale, "auth.didnt_get_confirmation")},
		)
	case "confirmations":
		links = append(links,
			authLink{href: "/auth/sign_in", label: webT(locale, "auth.login")},
			authLink{href: signUpPath, label: webT(locale, "auth.register")},
			authLink{href: "/auth/password/new", label: webT(locale, "auth.forgot_password")},
		)
	default:
		links = append(links,
			authLink{href: "/auth/sign_in", label: webT(locale, "auth.login")},
			authLink{href: signUpPath, label: webT(locale, "auth.register")},
			authLink{href: "/auth/password/new", label: webT(locale, "auth.forgot_password")},
			authLink{href: "/auth/confirmation/new", label: webT(locale, "auth.didnt_get_confirmation")},
		)
	}

	var b strings.Builder
	b.WriteString(`<ul class="no-list">`)
	for _, link := range links {
		b.WriteString(`<li><a href="`)
		b.WriteString(html.EscapeString(link.href))
		b.WriteString(`">`)
		b.WriteString(html.EscapeString(link.label))
		b.WriteString(`</a></li>`)
	}
	b.WriteString(`</ul>`)
	return b.String()
}

func authSharedFooterHTML(controller string, signUpPath string, locale string) string {
	return authFormFooter(authSharedLinksHTML(controller, signUpPath, locale))
}

func (s *Server) availableSignUpPath() string {
	if s == nil {
		return "/auth/sign_up"
	}
	if s.registrationMode() == "none" || s.cfg.OmniAuthOnly {
		return "https://joinmastodon.org/#getting-started"
	}
	if s.cfg.SSOAccountSignUpURLSet {
		return s.cfg.SSOAccountSignUpURL
	}
	return "/auth/sign_up"
}

func (s *Server) authSharedFooterHTML(controller string, locale string) string {
	return authSharedFooterHTML(controller, s.availableSignUpPath(), locale)
}
