package api

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

func (s *Server) publicStatus(c *echo.Context) error {
	if publicAccountPathIsRemote(publicShortAccountParam(c, "username")) {
		return s.webApp(c)
	}
	idParam := publicShortAccountParam(c, "id")
	if id, ok := publicPathWithoutFormat(idParam, "json"); ok {
		return withPathParam(c, "id", id, func() error {
			return s.activityPubStatus(c)
		})
	}
	if publicRequestHasFormat(c, "json") {
		return withPathParam(c, "id", idParam, func() error {
			return s.activityPubStatus(c)
		})
	}
	if format := publicRequestFormat(c); format != "" {
		switch format {
		case "html":
		case "json":
			return withPathParam(c, "id", idParam, func() error {
				return s.activityPubStatus(c)
			})
		default:
			return noContentError(http.StatusNotAcceptable)
		}
	}
	if idParam != "" && idParam != c.Param("id") {
		return withPathParam(c, "id", idParam, func() error {
			return s.publicStatusResolved(c)
		})
	}
	return s.publicStatusResolved(c)
}

func (s *Server) publicStatusResolved(c *echo.Context) error {
	switch publicHTMLActivityPubAcceptFormat(c.Request().Header.Get("Accept")) {
	case publicAcceptUnsupported:
		return noContentError(http.StatusNotAcceptable)
	case publicAcceptActivityPub:
		return s.activityPubStatus(c)
	}
	if err := s.requirePublicAccountAuthenticationIfLimited(c); err != nil {
		return err
	}
	status, err := s.publicStatusHTMLStatus(c)
	if err != nil {
		return err
	}
	s.setPublicStatusLinkHeaderForStatus(c, *status)
	if redirectURL := s.publicStatusOriginalRedirectURL(*status); redirectURL != "" {
		return c.Redirect(http.StatusFound, redirectURL)
	}
	publicHTMLCacheIfUnauthenticated(c, 10, 0)
	return s.webApp(c)
}

func (s *Server) setPublicStatusLinkHeader(c *echo.Context) {
	status, err := s.findPublicStatusForPath(c)
	if err != nil {
		return
	}
	s.setPublicStatusLinkHeaderForStatus(c, *status)
}

func (s *Server) setPublicStatusLinkHeaderForStatus(c *echo.Context, status models.Status) {
	c.Response().Header().Set("Link", publicStatusLinkHeader(s.cfg, status))
}

func (s *Server) findPublicStatusForPath(c *echo.Context) (*models.Status, error) {
	status, err := s.findStatus(c.Param("id"))
	if err != nil || !publicStatusPathStatusAllowed(*status, c.Param("username")) || status.ReblogOfID.Valid {
		if err != nil {
			return nil, err
		}
		return nil, gorm.ErrRecordNotFound
	}
	if !status.Account.Local() || status.Account.Username != c.Param("username") {
		return nil, errors.New("status does not match path account")
	}
	return status, nil
}

func (s *Server) publicStatusHTMLStatus(c *echo.Context) (*models.Status, error) {
	status, err := s.findStatus(c.Param("id"))
	if err != nil {
		return nil, apiError(c, http.StatusNotFound, "Record not found")
	}
	if !publicStatusPathStatusAllowed(*status, c.Param("username")) {
		return nil, apiError(c, http.StatusNotFound, "Record not found")
	}
	if err := s.activityPubAccountOwnedGuard(c, &status.Account, false); err != nil {
		return nil, err
	}
	return status, nil
}

func publicStatusPathStatusAllowed(status models.Status, username string) bool {
	return status.Visibility <= 1 && status.Account.Local() && status.Account.Username == username
}

func (s *Server) publicStatusOriginalRedirectURL(status models.Status) string {
	if !status.ReblogOfID.Valid {
		return ""
	}
	if status.Reblog != nil && status.Reblog.ID != 0 {
		return activityPubStatusPublicURL(s, *status.Reblog)
	}
	target, err := s.findStatus(strconv.FormatInt(status.ReblogOfID.Int64, 10))
	if err != nil || target == nil {
		return ""
	}
	return activityPubStatusPublicURL(s, *target)
}

func publicStatusLinkHeader(cfg config.Config, status models.Status) string {
	actorURL := cfg.BaseURL() + "/users/" + url.PathEscape(status.Account.Username)
	statusURL := actorURL + "/statuses/" + url.PathEscape(strconv.FormatInt(status.ID, 10))
	return "<" + statusURL + `>; rel="alternate"; type="application/activity+json"`
}
