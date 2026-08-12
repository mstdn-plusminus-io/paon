package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"github.com/mstdn-plusminus-io/paon/internal/paon/serializer"
	"gorm.io/gorm"
)

const featuredTagLimit = 10

type featuredTagPayload struct {
	Name string `json:"name" form:"name"`
}

type featuredTagStats struct {
	StatusesCount int64
	LastStatusAt  sql.NullTime
}

func (s *Server) accountFeaturedTags(c *echo.Context) error {
	c.Response().Header().Set("Vary", "Authorization")
	account, err := s.findAccountByID(c.Param("id"))
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	if account.SuspendedAt.Valid {
		return c.JSON(http.StatusOK, []serializer.FeaturedTag{})
	}
	tags, err := s.findAccountFeaturedTags(account.ID)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, serializeFeaturedTags(s.cfg, tags))
}

func (s *Server) featuredTags(c *echo.Context) error {
	account, _, err := s.requireAccountScope(c, "read", "read:accounts")
	if err != nil {
		return err
	}
	tags, err := s.findFeaturedTags(account.ID)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, serializeFeaturedTags(s.cfg, tags))
}

func (s *Server) createFeaturedTag(c *echo.Context) error {
	account, _, err := s.requireAccountScope(c, "write", "write:accounts")
	if err != nil {
		return err
	}
	payload, err := parseFeaturedTagPayload(c)
	if err != nil {
		return apiError(c, http.StatusBadRequest, "Malformed request")
	}
	if payload.Name == "" {
		return apiError(c, http.StatusBadRequest, "param is missing or the value is empty: name")
	}
	featured, created, err := s.createFeaturedTagForAccount(account, payload.Name, true)
	if err != nil {
		switch err {
		case errFeaturedTagInvalidName:
			return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Name is invalid")
		case errFeaturedTagLimit:
			return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Featured tag limit reached")
		case errFeaturedTagDuplicate:
			return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Tag has already been taken")
		}
		return err
	}
	if created {
		_ = s.deliverActivityPubAccountRawDistribution(featured.Account, activityPubAddFeaturedTag(s, *featured))
	}
	return c.JSON(http.StatusOK, serializer.FeaturedTagFromModel(s.cfg, *featured))
}

func (s *Server) deleteFeaturedTag(c *echo.Context) error {
	account, _, err := s.requireAccountScope(c, "write", "write:accounts")
	if err != nil {
		return err
	}
	var featured models.FeaturedTag
	err = s.db.Preload("Account.AccountStat").Preload("Account.User.Role").Preload("Tag").Where("id = ? AND account_id = ?", c.Param("id"), account.ID).First(&featured).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	if err != nil {
		return err
	}
	if !s.enqueueRemoveFeaturedTagTask(account.ID, featured.ID) {
		if err := s.removeFeaturedTagForAccount(c.Request().Context(), account.ID, featured.ID); err != nil {
			return err
		}
	}
	return renderEmpty(c)
}

func (s *Server) removeFeaturedTagForAccount(ctx context.Context, accountID int64, featuredTagID int64) error {
	if s == nil || s.db == nil || accountID == 0 || featuredTagID == 0 {
		return nil
	}
	var featured models.FeaturedTag
	err := s.db.WithContext(ctx).Preload("Account.AccountStat").Preload("Account.User.Role").Preload("Tag").Where("id = ? AND account_id = ?", featuredTagID, accountID).First(&featured).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := s.db.WithContext(ctx).Delete(&featured).Error; err != nil {
		return err
	}
	_ = s.deliverActivityPubAccountRawDistribution(featured.Account, activityPubRemoveFeaturedTag(s, featured))
	return nil
}

func (s *Server) featuredTagSuggestions(c *echo.Context) error {
	account, _, err := s.requireAccountScope(c, "read", "read:accounts")
	if err != nil {
		return err
	}

	var tags []models.Tag
	query := s.featuredTagSuggestionQuery(account.ID).
		Where("NOT EXISTS (SELECT 1 FROM featured_tags WHERE featured_tags.account_id = ? AND featured_tags.tag_id = tags.id)", account.ID)
	if err := query.Find(&tags).Error; err != nil {
		return err
	}

	out := make([]serializer.TagDetail, 0, len(tags))
	for _, tag := range tags {
		following := s.tagFollowing(c, tag.ID)
		out = append(out, serializer.TagDetailFromModelWithRelationships(s.cfg, tag, following, s.tagFeaturing(c, tag.ID), nil))
	}
	return c.JSON(http.StatusOK, out)
}

func (s *Server) findFeaturedTags(accountID int64) ([]models.FeaturedTag, error) {
	var tags []models.FeaturedTag
	err := s.db.Preload("Account.AccountStat").
		Preload("Account.User.Role").
		Preload("Tag").
		Where("account_id = ?", accountID).
		Order("statuses_count DESC").
		Order("id ASC").
		Find(&tags).Error
	return tags, err
}

func (s *Server) findAccountFeaturedTags(accountID int64) ([]models.FeaturedTag, error) {
	var tags []models.FeaturedTag
	err := s.db.Preload("Account.AccountStat").
		Preload("Account.User.Role").
		Preload("Tag").
		Where("account_id = ?", accountID).
		Find(&tags).Error
	return tags, err
}

func (s *Server) featuredTagSuggestionQuery(accountID int64) *gorm.DB {
	recentStatuses := s.db.Model(&models.Status{}).
		Select("id").
		Where("account_id = ? AND deleted_at IS NULL", accountID).
		Order("id DESC").
		Limit(1000)
	return s.db.Model(&models.Tag{}).
		Select("tags.*").
		Joins("JOIN statuses_tags ON statuses_tags.tag_id = tags.id").
		Joins("JOIN statuses ON statuses.id = statuses_tags.status_id").
		Where("statuses.id IN (?)", recentStatuses).
		Group("tags.id").
		Order("COUNT(*) DESC").
		Order("tags.id ASC").
		Limit(10)
}

func parseFeaturedTagPayload(c *echo.Context) (featuredTagPayload, error) {
	var payload featuredTagPayload
	if err := c.Bind(&payload); err != nil {
		return payload, err
	}
	if strings.TrimSpace(payload.Name) == "" {
		payload.Name = c.FormValue("name")
	}
	payload.Name = strings.TrimSpace(payload.Name)
	return payload, nil
}

func featuredStats(db *gorm.DB, accountID int64, tagID int64) (featuredTagStats, error) {
	var stats featuredTagStats
	query := db.Model(&models.Status{}).
		Joins("JOIN statuses_tags ON statuses_tags.status_id = statuses.id").
		Where("statuses.account_id = ?", accountID).
		Where("statuses.deleted_at IS NULL").
		Where("statuses.visibility IN ?", []int{0, 1}).
		Where("statuses_tags.tag_id = ?", tagID)
	if err := query.Count(&stats.StatusesCount).Error; err != nil {
		return stats, err
	}
	err := query.Select("statuses.created_at").
		Order("statuses.id DESC").
		Limit(1).
		Scan(&stats.LastStatusAt).Error
	return stats, err
}

func serializeFeaturedTags(cfg config.Config, tags []models.FeaturedTag) []serializer.FeaturedTag {
	out := make([]serializer.FeaturedTag, 0, len(tags))
	for _, tag := range tags {
		out = append(out, serializer.FeaturedTagFromModel(cfg, tag))
	}
	return out
}
