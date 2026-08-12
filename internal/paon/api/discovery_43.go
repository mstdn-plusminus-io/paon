package api

import (
	"net/http"
	"strings"

	"github.com/labstack/echo/v5"
)

// oauthAuthorizationServerMetadata implements RFC 8414 and Mastodon's
// app_registration_endpoint extension. Keep this document independent from
// browser sessions: discovery requests must not roll or create cookies.
func (s *Server) oauthAuthorizationServerMetadata(c *echo.Context) error {
	baseURL := strings.TrimRight(s.cfg.BaseURL(), "/")
	c.Response().Header().Set("Cache-Control", "max-age=0, private, must-revalidate")
	return c.JSON(http.StatusOK, map[string]any{
		"issuer":                                baseURL + "/",
		"authorization_endpoint":                baseURL + "/oauth/authorize",
		"token_endpoint":                        baseURL + "/oauth/token",
		"revocation_endpoint":                   baseURL + "/oauth/revoke",
		"userinfo_endpoint":                     baseURL + "/oauth/userinfo",
		"scopes_supported":                      append([]string{}, oauthConfiguredScopeOrder...),
		"response_types_supported":              []string{"code"},
		"response_modes_supported":              []string{"query", "fragment", "form_post"},
		"grant_types_supported":                 []string{"authorization_code", "client_credentials"},
		"token_endpoint_auth_methods_supported": []string{"client_secret_basic", "client_secret_post"},
		"code_challenge_methods_supported":      []string{"S256"},
		"service_documentation":                 "https://docs.joinmastodon.org/",
		"app_registration_endpoint":             baseURL + "/api/v1/apps",
	})
}

func (s *Server) hostMetaJSON(c *echo.Context) error {
	appendVaryHeader(c, "Origin")
	c.Response().Header().Set("Cache-Control", "max-age=259200, public")
	return c.JSON(http.StatusOK, map[string]any{
		"links": []map[string]string{{
			"rel":      "lrdd",
			"template": s.cfg.BaseURL() + "/.well-known/webfinger?resource={uri}",
		}},
	})
}

func hostMetaWantsJSON(c *echo.Context) bool {
	if strings.EqualFold(strings.TrimSpace(c.Param("format")), "json") || strings.HasSuffix(strings.ToLower(c.Request().URL.Path), ".json") {
		return true
	}
	accept := strings.ToLower(c.Request().Header.Get("Accept"))
	return strings.Contains(accept, "application/json") && !strings.Contains(accept, "application/xrd+xml")
}
