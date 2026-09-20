package api

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

const (
	oauthIntrospectionInvalidRequestDescription = "The request is missing a required parameter, includes an unsupported parameter value, or is otherwise malformed."
	oauthIntrospectionInvalidClientDescription  = "Client authentication failed due to unknown client, no client authentication included, or unsupported authentication method."
)

func (s *Server) oauthNativeAuthorizationCode(c *echo.Context) error {
	setOAuthAuthorizeCacheHeaders(c)
	setOAuthAuthorizeCSPHeaders(c, s.cfg)
	user, _, handled, err := s.requireFunctionalWebUser(c)
	if handled || err != nil {
		return err
	}
	return c.HTML(http.StatusOK, authorizationCodePage(oauthRawParamValue(c, "code"), s.webLocale(c, user)))
}

func (s *Server) oauthIntrospect(c *echo.Context) error {
	if s.db == nil {
		return apiError(c, http.StatusServiceUnavailable, "DATABASE_URL is not set")
	}
	if _, err := oauthRequestJSONPayload(c); err != nil {
		return writeOAuthIntrospectionError(c, http.StatusBadRequest, "invalid_request", oauthIntrospectionInvalidRequestDescription, "")
	}

	tokenValue := oauthRawParamValue(c, "token")
	target, found, err := s.oauthIntrospectionTarget(tokenValue)
	if err != nil {
		return err
	}

	_, hasClientCredentials, clientErr := s.oauthApplicationFromOptionalTokenRequest(c)
	if hasClientCredentials {
		if clientErr != nil {
			return writeOAuthIntrospectionError(c, http.StatusUnauthorized, "invalid_client", oauthIntrospectionInvalidClientDescription, "")
		}
	} else {
		authorizationValue := requestToken(c)
		if authorizationValue == "" {
			return writeOAuthIntrospectionError(c, http.StatusBadRequest, "invalid_request", oauthIntrospectionInvalidRequestDescription, "")
		}
		var authorization models.OAuthAccessToken
		if err := s.db.Where("token = ?", authorizationValue).First(&authorization).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return writeOAuthIntrospectionInvalidToken(c, nil, time.Now().UTC())
			}
			return err
		}
		if found && authorization.ID == target.ID {
			return writeOAuthIntrospectionInvalidToken(c, nil, time.Now().UTC())
		}
		if !oauthIntrospectionTokenActive(authorization, time.Now().UTC()) {
			return writeOAuthIntrospectionInvalidToken(c, &authorization, time.Now().UTC())
		}
	}

	header := c.Response().Header()
	header.Set(echo.HeaderContentType, echo.MIMEApplicationJSONCharsetUTF8)
	header.Set("Cache-Control", "max-age=0, private, must-revalidate")
	if !found || !oauthIntrospectionTokenActive(target, time.Now().UTC()) {
		return c.JSON(http.StatusOK, map[string]any{"active": false})
	}

	var clientID any
	if target.ApplicationID.Valid {
		var application models.OAuthApplication
		if err := s.db.Select("uid").Where("id = ?", target.ApplicationID.Int64).First(&application).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		} else if err == nil {
			clientID = application.UID
		}
	}
	expiresAt := int64(0)
	if target.ExpiresIn.Valid {
		expiresAt = target.CreatedAt.Add(time.Duration(target.ExpiresIn.Int64) * time.Second).Unix()
	}
	return c.JSON(http.StatusOK, map[string]any{
		"active":     true,
		"scope":      string(target.Scopes),
		"client_id":  clientID,
		"token_type": "Bearer",
		"exp":        expiresAt,
		"iat":        target.CreatedAt.Unix(),
	})
}

func (s *Server) oauthIntrospectionTarget(tokenValue string) (models.OAuthAccessToken, bool, error) {
	if tokenValue == "" {
		return models.OAuthAccessToken{}, false, nil
	}
	var token models.OAuthAccessToken
	if err := s.db.Where("token = ? OR refresh_token = ?", tokenValue, tokenValue).First(&token).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return models.OAuthAccessToken{}, false, nil
		}
		return models.OAuthAccessToken{}, false, err
	}
	return token, true, nil
}

func oauthIntrospectionTokenActive(token models.OAuthAccessToken, now time.Time) bool {
	return !token.RevokedAt.Valid && !oauthAccessTokenExpired(token, now)
}

func writeOAuthIntrospectionInvalidToken(c *echo.Context, token *models.OAuthAccessToken, now time.Time) error {
	description := "The access token is invalid"
	if token != nil {
		switch {
		case token.RevokedAt.Valid:
			description = "The access token was revoked"
		case oauthAccessTokenExpired(*token, now):
			description = "The access token expired"
		}
	}
	return writeOAuthIntrospectionError(c, http.StatusUnauthorized, "invalid_token", description, "unauthorized")
}

func writeOAuthIntrospectionError(c *echo.Context, status int, code string, description string, state string) error {
	header := c.Response().Header()
	header.Set(echo.HeaderContentType, echo.MIMEApplicationJSONCharsetUTF8)
	header.Set("Cache-Control", "no-store")
	header.Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm="Doorkeeper", error=%q, error_description=%q`, code, description))
	body := map[string]string{
		"error":             code,
		"error_description": description,
	}
	if state != "" {
		body["state"] = state
	}
	return c.JSON(status, body)
}
