package api

import (
	"net/http"
	"strings"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"github.com/mstdn-plusminus-io/paon/internal/paon/serializer"
)

type oauthUserInfoResponse struct {
	Issuer            string `json:"iss"`
	Subject           string `json:"sub"`
	Name              string `json:"name"`
	PreferredUsername string `json:"preferred_username"`
	Profile           string `json:"profile"`
	Picture           string `json:"picture"`
}

func (s *Server) oauthUserInfo(c *echo.Context) error {
	account, _, err := s.requireAccountScope(c, "profile")
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, s.oauthUserInfoFromAccount(*account))
}

func (s *Server) oauthUserInfoFromAccount(account models.Account) oauthUserInfoResponse {
	restAccount := serializer.AccountFromModel(s.cfg, account)
	return oauthUserInfoResponse{
		Issuer:            strings.TrimRight(s.cfg.BaseURL(), "/") + "/",
		Subject:           activityPubAccountTagManagerURI(s, account),
		Name:              account.DisplayName,
		PreferredUsername: account.Username,
		Profile:           restAccount.URL,
		Picture:           restAccount.Avatar,
	}
}
