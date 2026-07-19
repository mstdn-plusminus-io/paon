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
		if !apiWebCSRFTokenValid(c, web.CSRFTokenForSession(token)) {
			return c.JSON(http.StatusUnprocessableEntity, map[string]string{"error": railsCSRFErrorMessage})
		}
		return next(c)
	}
}

func apiWebCSRFTokenValid(c *echo.Context, expected string) bool {
	if expected == "" {
		return false
	}
	for _, value := range []string{
		c.Request().Header.Get("X-CSRF-Token"),
		c.Request().Header.Get("X-XSRF-Token"),
		c.FormValue("authenticity_token"),
	} {
		if value == expected {
			return true
		}
	}
	return false
}
