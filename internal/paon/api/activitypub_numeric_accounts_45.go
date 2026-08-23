package api

import (
	"net/http"
	"strings"

	"github.com/labstack/echo/v5"
)

// numericActivityPubAccountRoute gives the existing username-aware handlers
// the account name needed for HTML redirects while keeping account_id as the
// authoritative lookup key. Mastodon 4.5 assigns /ap/users/:id actor IDs to
// newly-created accounts without changing the retained web profile URLs.
func (s *Server) numericActivityPubAccountRoute(handler echo.HandlerFunc) echo.HandlerFunc {
	return func(c *echo.Context) error {
		accountID := strings.TrimSpace(activityPubFormatParam(c, "account_id"))
		account, err := s.findAccountByID(accountID)
		if err != nil || account == nil || !account.Local() {
			return apiError(c, http.StatusNotFound, "Record not found")
		}
		return withPathParam(c, "username", account.Username, func() error { return handler(c) })
	}
}
