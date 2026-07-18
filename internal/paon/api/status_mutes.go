package api

import (
	"net/http"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm/clause"
)

func (s *Server) muteStatus(c *echo.Context) error {
	return s.toggleStatusMute(c, true)
}

func (s *Server) unmuteStatus(c *echo.Context) error {
	return s.toggleStatusMute(c, false)
}

func (s *Server) toggleStatusMute(c *echo.Context, mute bool) error {
	account, _, err := s.requireAccountScope(c, "write", "write:mutes")
	if err != nil {
		return err
	}
	status, err := s.findVisibleStatusForAccount(account, c.Param("id"))
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	if !status.ConversationID.Valid {
		return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Conversation is missing")
	}

	if mute {
		row := models.ConversationMute{AccountID: account.ID, ConversationID: status.ConversationID.Int64}
		if err := s.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
			return err
		}
	} else if err := s.db.Where("account_id = ? AND conversation_id = ?", account.ID, status.ConversationID.Int64).Delete(&models.ConversationMute{}).Error; err != nil {
		return err
	}

	if err := s.hydrateStatusRelationship(status, account); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, statusWithFilterContext(s.cfg, *status, account, s.accountFilters(account), "public"))
}
