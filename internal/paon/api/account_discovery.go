package api

import (
	"net/http"
	"strconv"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"github.com/mstdn-plusminus-io/paon/internal/paon/serializer"
)

func (s *Server) endorsements(c *echo.Context) error {
	account, _, err := s.requireAccountScope(c, "read", "read:accounts")
	if err != nil {
		return err
	}
	c.Response().Header().Set("Vary", "Authorization")
	query := s.db.Model(&models.Account{}).
		Joins("JOIN account_pins ON account_pins.target_account_id = accounts.id").
		Preload("AccountStat").
		Preload("User.Role").
		Where("account_pins.account_id = ?", account.ID).
		Where("accounts.suspended_at IS NULL")
	unlimited := c.QueryParam("limit") == "0"
	if !unlimited {
		if maxID := c.QueryParam("max_id"); queryParamValuePresent(c, "max_id") {
			query = query.Where("accounts.id < ?", maxID)
		}
		if sinceID := c.QueryParam("since_id"); queryParamValuePresent(c, "since_id") {
			query = query.Where("accounts.id > ?", sinceID)
		}
		query = query.Order("accounts.id DESC")
	}
	limitValue := limit(c, 40, 80)
	if !unlimited {
		query = query.Limit(limitValue)
	}
	var accounts []models.Account
	if err := query.Find(&accounts).Error; err != nil {
		return err
	}
	if len(accounts) > 0 && !unlimited {
		c.Response().Header().Set("Link", limitOnlyPaginationLink(c, accounts[0].ID, accounts[len(accounts)-1].ID, "since_id", len(accounts) == limitValue))
	}
	return c.JSON(http.StatusOK, serializeAccounts(s.cfg, accounts))
}

func (s *Server) accountEndorsements(c *echo.Context) error {
	c.Response().Header().Set("Vary", "Authorization")
	publicRESTCacheIfUnauthenticated(c, 60)
	account, err := s.findAccountByID(c.Param("id"))
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	if account.SuspendedAt.Valid {
		return c.JSON(http.StatusOK, []serializer.Account{})
	}
	query := s.db.Model(&models.Account{}).
		Joins("JOIN account_pins ON account_pins.target_account_id = accounts.id").
		Preload("AccountStat").
		Preload("User.Role").
		Where("account_pins.account_id = ?", account.ID).
		Where("accounts.suspended_at IS NULL")
	if maxID := c.QueryParam("max_id"); queryParamValuePresent(c, "max_id") {
		query = query.Where("accounts.id < ?", maxID)
	}
	if sinceID := c.QueryParam("since_id"); queryParamValuePresent(c, "since_id") {
		query = query.Where("accounts.id > ?", sinceID)
	}
	limitValue := limit(c, 40, 80)
	var accounts []models.Account
	if err := query.Order("accounts.id DESC").Limit(limitValue).Find(&accounts).Error; err != nil {
		return err
	}
	if len(accounts) > 0 {
		c.Response().Header().Set("Link", limitOnlyPaginationLink(c, accounts[0].ID, accounts[len(accounts)-1].ID, "since_id", len(accounts) == limitValue))
	}
	return c.JSON(http.StatusOK, serializeAccounts(s.cfg, accounts))
}

func (s *Server) identityProofs(c *echo.Context) error {
	c.Response().Header().Set("Vary", "Authorization")
	if _, _, err := s.requireUser(c); err != nil {
		return err
	}
	if _, err := s.findAccountByID(c.Param("id")); err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	return c.JSON(http.StatusOK, []any{})
}

func (s *Server) familiarFollowers(c *echo.Context) error {
	c.Response().Header().Set("Vary", "Authorization")
	account, _, err := s.requireAccountScope(c, "read", "read:follows")
	if err != nil {
		return err
	}
	ids := relationshipIDs(c)
	targets, err := s.familiarFollowerTargets(ids)
	if err != nil {
		return err
	}
	out := make([]serializer.FamiliarFollowers, 0, len(targets))
	for _, target := range targets {
		followers := []models.Account{}
		if !target.HideCollections.Valid || !target.HideCollections.Bool {
			followers, err = s.familiarFollowersForAccount(account.ID, target.ID)
			if err != nil {
				return err
			}
		}
		out = append(out, serializer.FamiliarFollowers{
			ID:       strconv.FormatInt(target.ID, 10),
			Accounts: serializeAccounts(s.cfg, followers),
		})
	}
	return c.JSON(http.StatusOK, out)
}

func (s *Server) familiarFollowerTargets(ids []int64) ([]models.Account, error) {
	if len(ids) == 0 {
		return []models.Account{}, nil
	}
	var rows []models.Account
	if err := s.db.Select("id, hide_collections").
		Where("id IN ? AND suspended_at IS NULL", ids).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	byID := make(map[int64]models.Account, len(rows))
	for _, row := range rows {
		byID[row.ID] = row
	}
	out := make([]models.Account, 0, len(rows))
	for _, id := range ids {
		if row, ok := byID[id]; ok {
			out = append(out, row)
		}
	}
	return out, nil
}

func (s *Server) familiarFollowersForAccount(currentAccountID int64, targetAccountID int64) ([]models.Account, error) {
	var accounts []models.Account
	err := s.db.Model(&models.Account{}).
		Joins("JOIN follows common ON common.account_id = accounts.id AND common.target_account_id = ?", targetAccountID).
		Joins("JOIN follows mine ON mine.target_account_id = accounts.id AND mine.account_id = ?", currentAccountID).
		Joins("JOIN accounts followed_accounts ON followed_accounts.id = mine.target_account_id").
		Preload("AccountStat").
		Preload("User.Role").
		Where("COALESCE(followed_accounts.hide_collections, false) = false").
		Find(&accounts).Error
	return accounts, err
}
