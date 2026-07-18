package api

import (
	"net/http"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"github.com/mstdn-plusminus-io/paon/internal/paon/serializer"
	"gorm.io/gorm"
)

func (s *Server) directory(c *echo.Context) error {
	c.Response().Header().Set("Vary", "Authorization")
	if !s.profileDirectoryEnabled() {
		return c.NoContent(http.StatusNotFound)
	}
	publicRESTCacheIfUnauthenticated(c, 15)
	if s.db == nil {
		return c.JSON(http.StatusOK, []any{})
	}

	query := s.directoryQuery(c)
	var accounts []models.Account
	if err := query.Offset(offset(c)).Limit(limit(c, 40, 80)).Find(&accounts).Error; err != nil {
		return err
	}

	out := make([]serializer.Account, 0, len(accounts))
	for _, account := range accounts {
		out = append(out, serializer.AccountFromModel(s.cfg, account))
	}
	return c.JSON(http.StatusOK, out)
}

func (s *Server) profileDirectoryEnabled() bool {
	return s.settingBoolValue("profile_directory", true)
}

func (s *Server) directoryQuery(c *echo.Context) *gorm.DB {
	query := accountSerializerPreloads(s.db.Model(&models.Account{})).
		Joins("JOIN account_stats ON account_stats.account_id = accounts.id").
		Joins("LEFT JOIN users ON users.account_id = accounts.id").
		Where("accounts.suspended_at IS NULL").
		Where("accounts.silenced_at IS NULL").
		Where("accounts.moved_to_account_id IS NULL").
		Where("accounts.discoverable = ?", true).
		Where("accounts.id <> ?", -99).
		Where("(accounts.domain IS NOT NULL OR (users.approved = ? AND users.confirmed_at IS NOT NULL))", true)

	if truthy(c.QueryParam("local")) {
		query = query.Where("accounts.domain IS NULL")
	}

	if current, _, err := s.currentAccount(c); err == nil && current != nil {
		query = applyDirectoryExclusions(query, current.ID, !truthy(c.QueryParam("local")))
	}

	switch c.QueryParam("order") {
	case "new":
		return query.Order("accounts.id DESC")
	case "", "active":
		return query.Order("account_stats.last_status_at DESC NULLS LAST")
	default:
		return query
	}
}

func applyDirectoryExclusions(query *gorm.DB, accountID int64, includeDomainBlocks bool) *gorm.DB {
	query = query.
		Where(`NOT EXISTS (
			SELECT 1 FROM blocks
			WHERE (blocks.account_id = ? AND blocks.target_account_id = accounts.id)
			   OR (blocks.account_id = accounts.id AND blocks.target_account_id = ?)
		)`, accountID, accountID).
		Where(`NOT EXISTS (
			SELECT 1 FROM mutes
			WHERE mutes.account_id = ? AND mutes.target_account_id = accounts.id
		)`, accountID)
	if includeDomainBlocks {
		query = query.Where(`NOT EXISTS (
			SELECT 1 FROM account_domain_blocks
			WHERE account_domain_blocks.account_id = ?
			  AND lower(account_domain_blocks.domain) = lower(accounts.domain)
		)`, accountID)
	}
	return query
}

func offset(c *echo.Context) int {
	value := rubyToI(c.QueryParam("offset"))
	if value < 0 {
		return 0
	}
	return value
}
