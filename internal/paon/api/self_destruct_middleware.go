package api

import (
	"html"
	"net/http"
	"strings"

	"github.com/labstack/echo/v5"
)

func selfDestructAllowedRequest(method string, path string) bool {
	method = strings.ToUpper(strings.TrimSpace(method))
	path = strings.TrimSpace(path)
	if path == "" {
		path = "/"
	}
	// Mastodon's health controller does not inherit ApplicationController, so
	// operational health and metrics remain available while user traffic is gone.
	if path == "/health" || strings.HasPrefix(path, "/health.") || path == "/health/ready" || path == "/metrics" {
		return true
	}
	for _, prefix := range []string{"/packs/", "/assets/", "/emoji/", "/system/", "/avatars/", "/headers/", "/sounds/", "/ocr/"} {
		if method == http.MethodGet && strings.HasPrefix(path, prefix) {
			return true
		}
	}
	if method == http.MethodGet {
		if strings.HasPrefix(path, "/favicon-") || strings.HasPrefix(path, "/android-chrome-") || strings.HasPrefix(path, "/apple-touch-icon") {
			return true
		}
		for _, exact := range []string{
			"/manifest", "/manifest.json", "/favicon.ico", "/robots.txt", "/sw.js", "/sw.js.map",
			"/auth/edit", "/auth/sign_in", "/auth/confirmation", "/auth/confirmation/new",
			"/auth/password/new", "/auth/password/edit", "/auth/sessions/security_key_options",
			"/settings/export", "/settings/login_activities", "/settings/two_factor_authentication_methods",
			"/settings/otp_authentication", "/settings/two_factor_authentication/confirmation/new",
			"/settings/security_keys", "/settings/security_keys/new", "/settings/security_keys/options",
		} {
			if path == exact || strings.HasPrefix(path, exact+".") {
				return true
			}
		}
		if strings.HasPrefix(path, "/backups/") || strings.HasPrefix(path, "/settings/exports/") || selfDestructOmniAuthRequest(path) {
			return true
		}
	}
	if method == http.MethodPost {
		for _, exact := range []string{
			"/auth/sign_in", "/auth/confirmation", "/auth/password", "/auth/challenge", "/settings/export",
			"/settings/two_factor_authentication_methods/disable", "/settings/otp_authentication",
			"/settings/two_factor_authentication/confirmation", "/settings/two_factor_authentication/recovery_codes",
			"/settings/security_keys",
		} {
			if path == exact || strings.HasPrefix(path, exact+".") {
				return true
			}
		}
		return selfDestructOmniAuthRequest(path)
	}
	if method == http.MethodDelete {
		return path == "/auth/sign_out" || strings.HasPrefix(path, "/settings/security_keys/")
	}
	if method == http.MethodPut || method == http.MethodPatch {
		return path == "/auth" || path == "/auth/password" || path == "/auth/setup" || strings.HasPrefix(path, "/auth/setup.")
	}
	return false
}

func selfDestructOmniAuthRequest(path string) bool {
	rest, ok := strings.CutPrefix(path, "/auth/auth/")
	if !ok {
		return false
	}
	parts := strings.Split(rest, "/")
	return len(parts) == 1 && parts[0] != "" || len(parts) == 2 && parts[0] != "" && parts[1] == "callback"
}

func selfDestructResponseIsJSON(c *echo.Context) bool {
	if c == nil || c.Request() == nil {
		return false
	}
	path := strings.ToLower(c.Request().URL.Path)
	accept := strings.ToLower(c.Request().Header.Get("Accept"))
	contentType := strings.ToLower(c.Request().Header.Get("Content-Type"))
	return strings.HasPrefix(path, "/api/") || strings.HasSuffix(path, ".json") || strings.Contains(accept, "json") || strings.Contains(contentType, "json")
}

func (s *Server) selfDestructMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c *echo.Context) error {
		if !s.selfDestructEnabled() || selfDestructAllowedRequest(c.Request().Method, c.Request().URL.Path) {
			return next(c)
		}
		if selfDestructResponseIsJSON(c) {
			return c.JSON(http.StatusGone, map[string]string{"error": "Gone"})
		}
		locale := s.webLocale(c, nil)
		title := settingsT(locale, "self_destruct.title", "This server is closing down")
		lead := settingsTVars(locale, "self_destruct.lead_html", "Unfortunately, <strong>%{domain}</strong> is permanently closing down. If you had an account there, you will not be able to continue using it, but you can still request a backup of your data.", map[string]string{"domain": html.EscapeString(s.cfg.LocalDomain)})
		body := `<div class="flash-message warning"><p>` + lead + `</p><p><a class="button" href="/auth/sign_in">` + html.EscapeString(settingsT(locale, "auth.login", "Log in")) + `</a></p></div>`
		return c.HTML(http.StatusGone, authPageHTML(title, "", "", body, locale))
	}
}
