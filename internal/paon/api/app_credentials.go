package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/serializer"
)

type appCredentials struct {
	Name     string  `json:"name"`
	Website  *string `json:"website"`
	VapidKey string  `json:"vapid_key"`
}

func (s *Server) verifyAppCredentials(c *echo.Context) error {
	c.Response().Header().Set("Vary", "Authorization")
	accessToken, err := s.accessTokenFromRequest(c)
	if err != nil || !accessToken.ApplicationID.Valid {
		return apiError(c, http.StatusUnauthorized, "The access token is invalid")
	}
	if !tokenHasAnyScope(accessToken.Scopes, "read") {
		return apiError(c, http.StatusForbidden, "This action is outside the authorized scopes")
	}

	var app oauthApplication
	if err := s.db.Where("id = ?", accessToken.ApplicationID.Int64).First(&app).Error; err != nil {
		return apiError(c, http.StatusUnauthorized, "The access token is invalid")
	}
	return c.JSON(http.StatusOK, appCredentials{
		Name:     app.Name,
		Website:  appCredentialsWebsite(string(app.Website)),
		VapidKey: s.cfg.VapidPublicKey,
	})
}

func appCredentialsWebsite(website string) *string {
	if strings.TrimSpace(website) == "" {
		return nil
	}
	return &website
}

func restApplicationResponse(app oauthApplication, vapidKey string) serializer.Application {
	var website *string
	if strings.TrimSpace(string(app.Website)) != "" {
		value := string(app.Website)
		website = &value
	}
	return serializer.Application{
		ID:           strconv.FormatInt(app.ID, 10),
		Name:         app.Name,
		Website:      website,
		RedirectURI:  app.RedirectURI,
		ClientID:     app.UID,
		ClientSecret: app.Secret,
		VapidKey:     vapidKey,
	}
}
