package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/serializer"
)

func (s *Server) verifyAppCredentials(c *echo.Context) error {
	c.Response().Header().Set("Vary", "Authorization")
	accessToken, err := s.accessTokenFromRequest(c)
	if err != nil || !accessToken.ApplicationID.Valid {
		return apiError(c, http.StatusUnauthorized, "The access token is invalid")
	}
	var app oauthApplication
	if err := s.db.Where("id = ?", accessToken.ApplicationID.Int64).First(&app).Error; err != nil {
		return apiError(c, http.StatusUnauthorized, "The access token is invalid")
	}
	return c.JSON(http.StatusOK, publicRESTApplicationResponse(app, s.cfg.VapidPublicKey))
}

func appCredentialsWebsite(website string) *string {
	if strings.TrimSpace(website) == "" {
		return nil
	}
	return &website
}

func restApplicationResponse(app oauthApplication, vapidKey string) serializer.Application {
	out := publicRESTApplicationResponse(app, vapidKey)
	neverExpires := int64(0)
	out.ClientID = app.UID
	out.ClientSecret = app.Secret
	out.ClientSecretExpiresAt = &neverExpires
	return out
}

func publicRESTApplicationResponse(app oauthApplication, vapidKey string) serializer.Application {
	var website *string
	if strings.TrimSpace(string(app.Website)) != "" {
		value := string(app.Website)
		website = &value
	}
	return serializer.Application{
		ID:           strconv.FormatInt(app.ID, 10),
		Name:         app.Name,
		Website:      website,
		Scopes:       strings.Fields(app.Scopes),
		RedirectURIs: strings.Fields(app.RedirectURI),
		RedirectURI:  app.RedirectURI,
		VapidKey:     vapidKey,
	}
}
