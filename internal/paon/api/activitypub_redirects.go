package api

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func (s *Server) activityPubActorOrWebRedirect(c *echo.Context) error {
	if format := strings.ToLower(c.Param("format")); format != "" {
		username := c.Param("username")
		switch format {
		case "json":
			return s.activityPubActor(c)
		case "rss":
			return s.publicAccount(c)
		case "html":
			return s.activityPubAccountHTMLRedirect(c, username)
		default:
			return noContentError(http.StatusNotAcceptable)
		}
	}
	if username, format, ok := publicPathFormat(c.Param("username")); ok {
		switch format {
		case "json":
			return withPathParam(c, "username", username, func() error {
				return s.activityPubActor(c)
			})
		case "rss":
			return withPathParam(c, "username", username, func() error {
				return s.publicAccount(c)
			})
		case "html":
			return s.activityPubAccountHTMLRedirect(c, username)
		default:
			return noContentError(http.StatusNotAcceptable)
		}
	}
	if activityPubRequestWantsJSON(c) {
		return withActivityPubFormatParam(c, "username", func() error {
			return s.activityPubActor(c)
		})
	}
	if publicRequestHasFormat(c, "rss") || acceptsRSS(c.Request().Header.Get("Accept")) {
		return s.publicAccount(c)
	}
	return s.activityPubAccountHTMLRedirect(c, c.Param("username"))
}

func (s *Server) activityPubAccountHTMLRedirect(c *echo.Context, username string) error {
	if s == nil || s.db == nil {
		return activityPubHTMLRedirect(c, "/@"+url.PathEscape(username))
	}
	var account *models.Account
	var err error
	if accountID := strings.TrimSpace(activityPubFormatParam(c, "account_id")); accountID != "" {
		account, err = s.findAccountByID(accountID)
	} else {
		account, err = s.findAccountByUsernameDomainTx(s.db, username, "")
	}
	if err == nil && account != nil && account.Local() {
		c.Response().Header().Set("Link", publicAccountLinkHeader(s.cfg, *account))
	}
	return activityPubHTMLRedirect(c, "/@"+url.PathEscape(username))
}

func (s *Server) activityPubFollowersFormat(c *echo.Context) error {
	return s.activityPubFollowCollectionFormat(c, true)
}

func (s *Server) activityPubFollowingFormat(c *echo.Context) error {
	return s.activityPubFollowCollectionFormat(c, false)
}

func (s *Server) activityPubFollowCollectionFormat(c *echo.Context, followers bool) error {
	username := c.Param("username")
	switch strings.ToLower(c.Param("format")) {
	case "json":
		if followers {
			return s.activityPubFollowers(c)
		}
		return s.activityPubFollowing(c)
	case "html":
		path := "/@" + url.PathEscape(username)
		if followers {
			path += "/followers"
		} else {
			path += "/following"
		}
		return activityPubHTMLRedirect(c, path)
	default:
		return noContentError(http.StatusNotAcceptable)
	}
}

func (s *Server) activityPubFollowersOrWebRedirect(c *echo.Context) error {
	if activityPubRequestWantsJSON(c) {
		return s.activityPubFollowers(c)
	}
	return activityPubHTMLRedirect(c, "/@"+url.PathEscape(c.Param("username"))+"/followers")
}

func (s *Server) activityPubFollowingOrWebRedirect(c *echo.Context) error {
	if activityPubRequestWantsJSON(c) {
		return s.activityPubFollowing(c)
	}
	return activityPubHTMLRedirect(c, "/@"+url.PathEscape(c.Param("username"))+"/following")
}

func (s *Server) activityPubStatusOrWebRedirect(c *echo.Context) error {
	if statusID := c.Param("id"); strings.Contains(statusID, ".") && !strings.HasSuffix(strings.ToLower(statusID), ".json") {
		return s.activityPubStatusUnsupportedFormat(c)
	}
	if activityPubRequestWantsJSON(c) {
		return withActivityPubFormatParam(c, "id", func() error {
			return s.activityPubStatus(c)
		})
	}
	return activityPubHTMLRedirect(c, "/@"+url.PathEscape(c.Param("username"))+"/"+url.PathEscape(c.Param("id")))
}

func (s *Server) activityPubStatusUnsupportedFormat(c *echo.Context) error {
	return noContentError(http.StatusNotAcceptable)
}

func activityPubRequestWantsJSON(c *echo.Context) bool {
	return publicRequestHasFormat(c, "json") || acceptsActivityPub(c.Request().Header.Get("Accept"))
}

func activityPubFormatParam(c *echo.Context, name string) string {
	value, ok := publicPathWithoutFormat(c.Param(name), "json")
	if !ok {
		return c.Param(name)
	}
	return value
}

func withActivityPubFormatParam(c *echo.Context, name string, fn func() error) error {
	value, ok := publicPathWithoutFormat(c.Param(name), "json")
	if !ok {
		return fn()
	}
	return withPathParam(c, name, value, fn)
}

func activityPubHTMLRedirect(c *echo.Context, path string) error {
	c.Response().Header().Set("Vary", "Origin, Accept")
	return c.Redirect(http.StatusMovedPermanently, withRawQuery(path, c.Request().URL.RawQuery))
}
