package api

import (
	"net/http"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"github.com/mstdn-plusminus-io/paon/internal/paon/serializer"
	"gorm.io/gorm"
)

type accountCollectionItem struct {
	ID      int64
	Account models.Account
}

func (s *Server) blocks(c *echo.Context) error {
	account, _, err := s.requireAccountScope(c, "follow", "read", "read:blocks")
	if err != nil {
		return err
	}
	c.Response().Header().Set("Vary", "Authorization")

	rows := []models.Block{}
	limitValue := accountCollectionLimit(c)
	query := accountCollectionQuery(c, s.db.Model(&models.Block{}), "blocks", account.ID, limitValue)
	if err := query.Find(&rows).Error; err != nil {
		return err
	}

	out := make([]accountCollectionItem, 0, len(rows))
	for _, row := range rows {
		out = append(out, accountCollectionItem{ID: row.ID, Account: row.TargetAccount})
	}
	return s.accountCollectionResponse(c, out, limitValue)
}

func (s *Server) mutes(c *echo.Context) error {
	account, _, err := s.requireAccountScope(c, "follow", "read", "read:mutes")
	if err != nil {
		return err
	}
	c.Response().Header().Set("Vary", "Authorization")

	rows := []models.Mute{}
	limitValue := accountCollectionLimit(c)
	query := accountCollectionQuery(c, s.db.Model(&models.Mute{}), "mutes", account.ID, limitValue)
	if err := query.Find(&rows).Error; err != nil {
		return err
	}

	out := make([]serializer.MutedAccount, 0, len(rows))
	for _, row := range rows {
		out = append(out, serializer.MutedAccountFromModel(s.cfg, row.TargetAccount, row.ExpiresAt))
	}
	if len(rows) > 0 {
		c.Response().Header().Set("Link", limitOnlyPaginationLink(c, rows[0].ID, rows[len(rows)-1].ID, "since_id", len(rows) == limitValue))
	}
	return c.JSON(http.StatusOK, out)
}

func accountCollectionLimit(c *echo.Context) int {
	return limit(c, 40, 80)
}

func accountCollectionQuery(c *echo.Context, query *gorm.DB, table string, accountID int64, limitValue int) *gorm.DB {
	query = query.
		Preload("TargetAccount.AccountStat").
		Preload("TargetAccount.User.Role").
		Joins("JOIN accounts ON accounts.id = "+table+".target_account_id").
		Where(table+".account_id = ? AND accounts.suspended_at IS NULL", accountID)
	if maxID := c.QueryParam("max_id"); queryParamValuePresent(c, "max_id") {
		query = query.Where(table+".id < ?", maxID)
	}
	if sinceID := c.QueryParam("since_id"); queryParamValuePresent(c, "since_id") {
		query = query.Where(table+".id > ?", sinceID)
	}
	return query.Order(table + ".id DESC").Limit(limitValue)
}

func (s *Server) accountCollectionResponse(c *echo.Context, rows []accountCollectionItem, limitValue int) error {
	accounts := make([]models.Account, 0, len(rows))
	for _, row := range rows {
		accounts = append(accounts, row.Account)
	}
	if len(rows) > 0 {
		c.Response().Header().Set("Link", limitOnlyPaginationLink(c, rows[0].ID, rows[len(rows)-1].ID, "since_id", len(rows) == limitValue))
	}
	return c.JSON(http.StatusOK, serializeAccounts(s.cfg, accounts))
}
