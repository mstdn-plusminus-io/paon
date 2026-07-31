package api

import (
	"net/http"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/web"
)

const railsCSRFErrorMessage = "Can't verify CSRF token authenticity."

func (s *Server) apiWebCSRF(next func(*echo.Context) error) func(*echo.Context) error {
	return func(c *echo.Context) error {
		c.Response().Header().Set("Vary", "Authorization")
		_, token, err := s.currentUserIncludingDisabled(c)
		if err != nil {
			return c.JSON(http.StatusUnauthorized, map[string]string{"error": "The access token is invalid"})
		}
		if !s.apiWebCSRFTokenValid(c, token) {
			return c.JSON(http.StatusUnprocessableEntity, map[string]string{"error": railsCSRFErrorMessage})
		}
		return next(c)
	}
}

func (s *Server) apiWebCSRFTokenValid(c *echo.Context, token string) bool {
	if state, err := s.browserSession(c, false); err == nil && browserCSRFTokenValid(c, state.CSRFToken) {
		return true
	}
	return browserCSRFTokenValid(c, web.CSRFTokenForSession(token))
}
