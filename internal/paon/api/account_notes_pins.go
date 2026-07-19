package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const railsAccountPinFollowValidationMessage = "Validation failed: You must be already following the person you want to endorse"

func (s *Server) noteAccount(c *echo.Context) error {
	account, target, err := s.relationshipAccounts(c, "write", "write:accounts")
	if err != nil {
		return err
	}
	comment := accountNoteComment(c)

	if strings.TrimSpace(comment) == "" {
		err = s.db.Where("account_id = ? AND target_account_id = ?", account.ID, target.ID).Delete(&models.AccountNote{}).Error
	} else {
		if len([]rune(comment)) > 2000 {
			return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Comment is too long")
		}
		now := time.Now().UTC()
		err = s.db.Exec(`
			INSERT INTO account_notes (account_id, target_account_id, comment, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT (account_id, target_account_id)
			DO UPDATE SET comment = excluded.comment, updated_at = excluded.updated_at
		`, account.ID, target.ID, comment, now, now).Error
	}
	if err != nil {
		return err
	}
	s.invalidateRelationshipCaches(c.Request().Context(), account.ID, target.ID)
	return s.relationshipResponse(c, account.ID, target)
}

func (s *Server) pinAccount(c *echo.Context) error {
	account, target, err := s.relationshipAccounts(c, "write", "write:accounts")
	if err != nil {
		return err
	}

	var follow models.Follow
	if err := s.db.Where("account_id = ? AND target_account_id = ?", account.ID, target.ID).First(&follow).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apiError(c, http.StatusUnprocessableEntity, railsAccountPinFollowValidationMessage)
		}
		return err
	}

	now := time.Now().UTC()
	pin := models.AccountPin{AccountID: models.AccountPinAccountID(account.ID), TargetAccountID: models.AccountPinTargetAccountID(target.ID), CreatedAt: now, UpdatedAt: now}
	if err := s.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&pin).Error; err != nil {
		return err
	}
	s.invalidateRelationshipCaches(c.Request().Context(), account.ID, target.ID)
	return s.relationshipResponse(c, account.ID, target)
}

func (s *Server) unpinAccount(c *echo.Context) error {
	account, target, err := s.relationshipAccounts(c, "write", "write:accounts")
	if err != nil {
		return err
	}
	if err := s.db.Where("account_id = ? AND target_account_id = ?", account.ID, target.ID).Delete(&models.AccountPin{}).Error; err != nil {
		return err
	}
	s.invalidateRelationshipCaches(c.Request().Context(), account.ID, target.ID)
	return s.relationshipResponse(c, account.ID, target)
}

func accountNoteComment(c *echo.Context) string {
	if value := c.FormValue("comment"); value != "" {
		return value
	}
	if strings.Contains(strings.ToLower(c.Request().Header.Get("Content-Type")), "application/json") {
		var body map[string]json.RawMessage
		if err := json.NewDecoder(c.Request().Body).Decode(&body); err == nil {
			if raw, ok := body["comment"]; ok {
				return accountNoteJSONComment(raw)
			}
		}
	}
	return ""
}

func accountNoteJSONComment(raw json.RawMessage) string {
	if string(raw) == "null" {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	var boolean bool
	if err := json.Unmarshal(raw, &boolean); err == nil {
		if boolean {
			return "t"
		}
		return ""
	}
	var number json.Number
	if err := json.Unmarshal(raw, &number); err == nil {
		return number.String()
	}
	var array []any
	if err := json.Unmarshal(raw, &array); err == nil && len(array) == 0 {
		return ""
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err == nil && len(object) == 0 {
		return ""
	}
	return string(raw)
}
