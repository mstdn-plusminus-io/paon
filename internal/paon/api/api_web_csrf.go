package api

import (
	"net/http"

	"github.com/labstack/echo/v5"
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
	return s.browserCSRFTokenValidForAuthentication(c, token)
}
