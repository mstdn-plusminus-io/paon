package api

import (
	"net/http"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

func (s *Server) accountFollowers(c *echo.Context) error {
	c.Response().Header().Set("Vary", "Authorization")
	publicRESTCacheIfUnauthenticated(c, 15)
	if err := s.authorizeTokenScopeIfPresent(c, "read", "read:accounts"); err != nil {
		return err
	}
	target, err := s.findAccountByID(c.Param("id"))
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	current, _, _ := s.currentAccount(c)
	hidden, err := s.hideFollowCollection(target, current)
	if err != nil {
		return err
	}
	if hidden {
		return c.JSON(http.StatusOK, []any{})
	}

	query := s.db.Model(&models.Follow{}).
		Preload("Account.AccountStat").
		Preload("Account.User.Role").
		Joins("JOIN accounts ON accounts.id = follows.account_id").
		Where("follows.target_account_id = ?", target.ID)
	query = applyFollowPagination(c, query, "follows")
	if current != nil && current.ID != target.ID {
		query = applyFollowCollectionExclusions(query, current, "accounts")
	}

	limitValue := limit(c, 40, 80)
	var follows []models.Follow
	if err := query.Limit(limitValue).Find(&follows).Error; err != nil {
		return err
	}
	accounts := make([]models.Account, 0, len(follows))
	for _, follow := range follows {
		accounts = append(accounts, follow.Account)
	}
	if len(follows) > 0 {
		c.Response().Header().Set("Link", limitOnlyPaginationLink(c, follows[0].ID, follows[len(follows)-1].ID, "since_id", len(follows) == limitValue))
	}
	return c.JSON(http.StatusOK, serializeAccounts(s.cfg, accounts))
}

func (s *Server) accountFollowing(c *echo.Context) error {
	c.Response().Header().Set("Vary", "Authorization")
	publicRESTCacheIfUnauthenticated(c, 15)
	if err := s.authorizeTokenScopeIfPresent(c, "read", "read:accounts"); err != nil {
		return err
	}
	target, err := s.findAccountByID(c.Param("id"))
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	current, _, _ := s.currentAccount(c)
	hidden, err := s.hideFollowCollection(target, current)
	if err != nil {
		return err
	}
	if hidden {
		return c.JSON(http.StatusOK, []any{})
	}

	query := s.db.Model(&models.Follow{}).
		Preload("TargetAccount.AccountStat").
		Preload("TargetAccount.User.Role").
		Joins("JOIN accounts ON accounts.id = follows.target_account_id").
		Where("follows.account_id = ?", target.ID)
	query = applyFollowPagination(c, query, "follows")
	if current != nil && current.ID != target.ID {
		query = applyFollowCollectionExclusions(query, current, "accounts")
	}

	limitValue := limit(c, 40, 80)
	var follows []models.Follow
	if err := query.Limit(limitValue).Find(&follows).Error; err != nil {
		return err
	}
	accounts := make([]models.Account, 0, len(follows))
	for _, follow := range follows {
		accounts = append(accounts, follow.TargetAccount)
	}
	if len(follows) > 0 {
		c.Response().Header().Set("Link", limitOnlyPaginationLink(c, follows[0].ID, follows[len(follows)-1].ID, "since_id", len(follows) == limitValue))
	}
	return c.JSON(http.StatusOK, serializeAccounts(s.cfg, accounts))
}

func (s *Server) hideFollowCollection(target *models.Account, current *models.Account) (bool, error) {
	if target.SuspendedAt.Valid {
		return true, nil
	}
	if target.HideCollections.Valid && target.HideCollections.Bool && (current == nil || current.ID != target.ID) {
		return true, nil
	}
	if current == nil || current.ID == target.ID {
		return false, nil
	}
	var count int64
	err := s.db.Model(&models.Block{}).
		Where("account_id = ? AND target_account_id = ?", target.ID, current.ID).
		Count(&count).Error
	return count > 0, err
}

func applyFollowPagination(c *echo.Context, db *gorm.DB, table string) *gorm.DB {
	if maxID := c.QueryParam("max_id"); queryParamValuePresent(c, "max_id") {
		db = db.Where(table+".id < ?", maxID)
	}
	if sinceID := c.QueryParam("since_id"); queryParamValuePresent(c, "since_id") {
		db = db.Where(table+".id > ?", sinceID)
	}
	return db.Order(table + ".id DESC")
}

func applyFollowCollectionExclusions(query *gorm.DB, current *models.Account, accountTable string) *gorm.DB {
	return applyFollowCollectionExclusionsByIDExpression(query, current, accountTable+".id")
}

func applyFollowCollectionExclusionsByIDExpression(query *gorm.DB, current *models.Account, accountIDExpression string) *gorm.DB {
	if current == nil {
		return query
	}
	query = query.
		Where(`NOT EXISTS (
			SELECT 1 FROM blocks
			WHERE (blocks.account_id = ? AND blocks.target_account_id = `+accountIDExpression+`)
			   OR (blocks.account_id = `+accountIDExpression+` AND blocks.target_account_id = ?)
		)`, current.ID, current.ID).
		Where(`NOT EXISTS (
			SELECT 1 FROM mutes
			WHERE mutes.account_id = ? AND mutes.target_account_id = `+accountIDExpression+`
		)`, current.ID)
	return query
}
