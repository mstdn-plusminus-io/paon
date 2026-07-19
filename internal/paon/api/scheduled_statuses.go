package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"github.com/mstdn-plusminus-io/paon/internal/paon/serializer"
	"gorm.io/gorm"
)

type scheduledStatusUpdatePayload struct {
	ScheduledAt      string `json:"scheduled_at"`
	HasScheduledAt   bool
	ClearScheduledAt bool
}

const (
	minScheduledStatusOffset  = 5 * time.Minute
	scheduledStatusTotalLimit = 300
	scheduledStatusDailyLimit = 25
)

const (
	railsScheduledStatusTooSoonMessage    = "Validation failed: Scheduled at The scheduled date must be in the future"
	railsScheduledStatusTotalLimitMessage = "Validation failed: You have exceeded the limit of 300 scheduled posts"
	railsScheduledStatusDailyLimitMessage = "Validation failed: You have exceeded the limit of 25 scheduled posts for today"
)

func (s *Server) scheduledStatuses(c *echo.Context) error {
	account, _, err := s.requireAccountScope(c, "read", "read:statuses")
	if err != nil {
		return err
	}
	c.Response().Header().Set("Vary", "Authorization")
	query := s.db.Preload("MediaAttachments").
		Where("account_id = ?", account.ID)
	if minID := c.QueryParam("min_id"); queryParamValuePresent(c, "min_id") {
		query = query.Where("id > ?", minID).Order("id ASC")
		if maxID := c.QueryParam("max_id"); queryParamValuePresent(c, "max_id") {
			query = query.Where("id < ?", maxID)
		}
	} else {
		if maxID := c.QueryParam("max_id"); queryParamValuePresent(c, "max_id") {
			query = query.Where("id < ?", maxID)
		}
		if sinceID := c.QueryParam("since_id"); queryParamValuePresent(c, "since_id") {
			query = query.Where("id > ?", sinceID)
		}
		query = query.Order("id DESC")
	}
	limitValue := limit(c, 20, 40)
	query = query.Limit(limitValue)

	var statuses []models.ScheduledStatus
	if err := query.Find(&statuses).Error; err != nil {
		return err
	}
	if queryParamValuePresent(c, "min_id") {
		reverseScheduledStatuses(statuses)
	}
	if len(statuses) > 0 {
		c.Response().Header().Set("Link", limitOnlyPaginationLink(c, statuses[0].ID, statuses[len(statuses)-1].ID, "min_id", len(statuses) == limitValue))
	}
	out := make([]serializer.ScheduledStatus, 0, len(statuses))
	for _, status := range statuses {
		out = append(out, serializer.ScheduledStatusFromModel(s.cfg, status))
	}
	return c.JSON(http.StatusOK, out)
}

func reverseScheduledStatuses(rows []models.ScheduledStatus) {
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
}

func (s *Server) showScheduledStatus(c *echo.Context) error {
	account, _, err := s.requireAccountScope(c, "read", "read:statuses")
	if err != nil {
		return err
	}
	c.Response().Header().Set("Vary", "Authorization")
	status, err := s.findScheduledStatus(c.Param("id"), account.ID)
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	return c.JSON(http.StatusOK, serializer.ScheduledStatusFromModel(s.cfg, status))
}

func (s *Server) updateScheduledStatus(c *echo.Context) error {
	account, _, err := s.requireAccountScope(c, "write", "write:statuses")
	if err != nil {
		return err
	}
	status, err := s.findScheduledStatus(c.Param("id"), account.ID)
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	payload, err := parseScheduledStatusUpdatePayload(c)
	if err != nil {
		return apiError(c, http.StatusBadRequest, "Malformed request")
	}
	if !payload.HasScheduledAt {
		return c.JSON(http.StatusOK, serializer.ScheduledStatusFromModel(s.cfg, status))
	}
	scheduledAt := sql.NullTime{}
	if !payload.ClearScheduledAt {
		parsed, err := parseScheduledAt(payload.ScheduledAt)
		if err != nil {
			return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Scheduled at is invalid")
		}
		scheduledAt = sql.NullTime{Time: parsed, Valid: true}
	}
	if err := s.validateScheduledStatusNullable(c.Request().Context(), account.ID, scheduledAt, time.Now().UTC()); err != nil {
		return err
	}
	status.ScheduledAt = scheduledAt
	if err := s.db.Model(&models.ScheduledStatus{}).
		Where("id = ? AND account_id = ?", status.ID, account.ID).
		Update("scheduled_at", scheduledAt).Error; err != nil {
		return err
	}
	return c.JSON(http.StatusOK, serializer.ScheduledStatusFromModel(s.cfg, status))
}

func (s *Server) deleteScheduledStatus(c *echo.Context) error {
	account, _, err := s.requireAccountScope(c, "write", "write:statuses")
	if err != nil {
		return err
	}
	status, err := s.findScheduledStatus(c.Param("id"), account.ID)
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.MediaAttachment{}).
			Where("scheduled_status_id = ?", status.ID).
			Updates(map[string]any{"scheduled_status_id": nil, "updated_at": time.Now().UTC()}).Error; err != nil {
			return err
		}
		return tx.Delete(&models.ScheduledStatus{}, status.ID).Error
	}); err != nil {
		return err
	}
	return renderEmpty(c)
}

func (s *Server) findScheduledStatus(id string, accountID int64) (models.ScheduledStatus, error) {
	var status models.ScheduledStatus
	err := s.db.Preload("MediaAttachments").
		Where("id = ? AND account_id = ?", id, accountID).
		First(&status).Error
	return status, err
}

func parseScheduledStatusUpdatePayload(c *echo.Context) (scheduledStatusUpdatePayload, error) {
	var payload scheduledStatusUpdatePayload
	if strings.Contains(c.Request().Header.Get("Content-Type"), "application/json") {
		raw := map[string]json.RawMessage{}
		if err := json.NewDecoder(c.Request().Body).Decode(&raw); err != nil {
			return payload, err
		}
		if value, ok := raw["scheduled_at"]; ok {
			payload.HasScheduledAt = true
			if string(value) != "null" {
				_ = json.Unmarshal(value, &payload.ScheduledAt)
			} else {
				payload.ClearScheduledAt = true
			}
		}
		return payload, nil
	}
	if value, ok := formField(c, "scheduled_at"); ok {
		payload.ScheduledAt = value
		payload.ClearScheduledAt = strings.TrimSpace(value) == ""
		payload.HasScheduledAt = true
	}
	return payload, nil
}

func parseScheduledAt(raw string) (time.Time, error) {
	value := strings.TrimSpace(raw)
	formats := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05.000Z",
		"2006-01-02T15:04:05Z",
		"2006-01-02T15:04Z07:00",
		"2006-01-02T15:04",
		"2006-01-02 15:04:05",
		"2006-01-02 15:04",
	}
	var lastErr error
	for _, format := range formats {
		parsed, err := time.Parse(format, value)
		if err == nil {
			return parsed.UTC(), nil
		}
		lastErr = err
	}
	return time.Time{}, lastErr
}

func scheduledStatusTooSoon(scheduledAt time.Time, now time.Time) bool {
	return !scheduledAt.IsZero() && !scheduledAt.After(now.UTC().Add(minScheduledStatusOffset))
}

func (s *Server) validateScheduledStatus(ctx context.Context, accountID int64, scheduledAt time.Time, now time.Time) error {
	return s.validateScheduledStatusNullable(ctx, accountID, sql.NullTime{Time: scheduledAt, Valid: true}, now)
}

func (s *Server) validateScheduledStatusNullable(ctx context.Context, accountID int64, scheduledAt sql.NullTime, now time.Time) error {
	if scheduledAt.Valid && scheduledStatusTooSoon(scheduledAt.Time, now) {
		return apiHTTPError{status: http.StatusUnprocessableEntity, message: railsScheduledStatusTooSoonMessage}
	}
	if s == nil || s.db == nil || accountID == 0 {
		return nil
	}
	var total int64
	if err := s.db.WithContext(ctx).
		Model(&models.ScheduledStatus{}).
		Where("account_id = ?", accountID).
		Count(&total).Error; err != nil {
		return err
	}
	if total >= scheduledStatusTotalLimit {
		return apiHTTPError{status: http.StatusUnprocessableEntity, message: railsScheduledStatusTotalLimitMessage}
	}
	if !scheduledAt.Valid {
		return nil
	}
	var daily int64
	if err := s.db.WithContext(ctx).
		Model(&models.ScheduledStatus{}).
		Where("account_id = ?", accountID).
		Where("scheduled_at::date = ?::date", scheduledAt.Time.UTC()).
		Count(&daily).Error; err != nil {
		return err
	}
	if daily >= scheduledStatusDailyLimit {
		return apiHTTPError{status: http.StatusUnprocessableEntity, message: railsScheduledStatusDailyLimitMessage}
	}
	return nil
}
