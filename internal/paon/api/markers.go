package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"github.com/mstdn-plusminus-io/paon/internal/paon/serializer"
	"gorm.io/gorm"
)

var markerTimelines = []string{"home", "notifications"}
var errMarkerStaleObject = errors.New("marker stale object")

type markerTimelinePayload struct {
	LastReadID *string `json:"last_read_id" form:"last_read_id"`
}

func (s *Server) markers(c *echo.Context) error {
	user, _, err := s.requireUserScope(c, "read", "read:statuses")
	if err != nil {
		return err
	}
	c.Response().Header().Set("Vary", "Authorization")

	timelines := markerTimelineParams(c)
	if len(timelines) == 0 {
		return c.JSON(http.StatusOK, map[string]serializer.Marker{})
	}

	var markers []models.Marker
	if err := s.db.Where("user_id = ? AND timeline IN ?", user.ID, timelines).Find(&markers).Error; err != nil {
		return err
	}
	return c.JSON(http.StatusOK, serializeMarkerMap(markers))
}

func (s *Server) updateMarkers(c *echo.Context) error {
	user, _, err := s.requireUserScope(c, "write", "write:statuses")
	if err != nil {
		return err
	}
	payload, err := parseMarkerPayload(c)
	if err != nil {
		return apiError(c, http.StatusBadRequest, "Malformed request")
	}

	updated := map[string]models.Marker{}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		for _, timeline := range markerTimelines {
			timelinePayload, ok := payload[timeline]
			if !ok {
				continue
			}
			var lastReadID int64
			lastReadIDPresent := timelinePayload.LastReadID != nil
			if lastReadIDPresent {
				var parseErr error
				lastReadID, parseErr = parseMarkerLastReadID(*timelinePayload.LastReadID)
				if parseErr != nil {
					return apiHTTPError{status: http.StatusUnprocessableEntity, message: "Validation failed: Last read is invalid"}
				}
			}

			now := time.Now().UTC()
			var marker models.Marker
			err = tx.Where("user_id = ? AND timeline = ?", user.ID, timeline).First(&marker).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				marker = models.Marker{
					UserID:      models.MarkerUserID(user.ID),
					Timeline:    timeline,
					LastReadID:  lastReadID,
					LockVersion: 0,
					CreatedAt:   now,
					UpdatedAt:   now,
				}
				if err := tx.Create(&marker).Error; err != nil {
					return err
				}
			} else if err != nil {
				return err
			} else if !lastReadIDPresent {
				// Rails update!({}) validates and returns the existing marker without changing last_read_id.
			} else {
				marker.LastReadID = lastReadID
				previousLockVersion := marker.LockVersion
				marker.LockVersion++
				marker.UpdatedAt = now
				result := tx.Model(&models.Marker{}).
					Where("id = ? AND lock_version = ?", marker.ID, previousLockVersion).
					Updates(map[string]any{
						"last_read_id": lastReadID,
						"lock_version": marker.LockVersion,
						"updated_at":   now,
					})
				if result.Error != nil {
					return result.Error
				}
				if result.RowsAffected == 0 {
					return errMarkerStaleObject
				}
			}
			updated[timeline] = marker
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, errMarkerStaleObject) {
			return apiError(c, http.StatusConflict, "Conflict during update, please try again")
		}
		return err
	}
	return c.JSON(http.StatusOK, serializeMarkerMapFromMap(updated))
}

func markerTimelineParams(c *echo.Context) []string {
	values := c.QueryParams()
	out := []string{}
	seen := map[string]struct{}{}
	for _, key := range []string{"timeline[]", "timeline"} {
		for _, value := range values[key] {
			if !validMarkerTimeline(value) {
				continue
			}
			if _, ok := seen[value]; ok {
				continue
			}
			seen[value] = struct{}{}
			out = append(out, value)
		}
	}
	return out
}

func parseMarkerPayload(c *echo.Context) (map[string]markerTimelinePayload, error) {
	payload := map[string]markerTimelinePayload{}
	contentType := c.Request().Header.Get("Content-Type")
	if strings.Contains(contentType, "application/json") {
		if err := json.NewDecoder(c.Request().Body).Decode(&payload); err != nil {
			if errors.Is(err, io.EOF) {
				return payload, nil
			}
			return payload, err
		}
		return filterMarkerPayload(payload), nil
	}

	for _, timeline := range markerTimelines {
		if value, ok := formField(c, timeline+"[last_read_id]"); ok {
			payload[timeline] = markerTimelinePayload{LastReadID: &value}
		}
	}
	return payload, nil
}

func parseMarkerLastReadID(value string) (int64, error) {
	return strconv.ParseInt(strings.TrimSpace(value), 10, 64)
}

func filterMarkerPayload(payload map[string]markerTimelinePayload) map[string]markerTimelinePayload {
	out := map[string]markerTimelinePayload{}
	for timeline, marker := range payload {
		if validMarkerTimeline(timeline) {
			out[timeline] = marker
		}
	}
	return out
}

func validMarkerTimeline(timeline string) bool {
	for _, valid := range markerTimelines {
		if timeline == valid {
			return true
		}
	}
	return false
}

func serializeMarkerMap(markers []models.Marker) map[string]serializer.Marker {
	out := map[string]serializer.Marker{}
	for _, marker := range markers {
		out[marker.Timeline] = serializer.MarkerFromModel(marker)
	}
	return out
}

func serializeMarkerMapFromMap(markers map[string]models.Marker) map[string]serializer.Marker {
	out := map[string]serializer.Marker{}
	for _, timeline := range markerTimelines {
		if marker, ok := markers[timeline]; ok {
			out[timeline] = serializer.MarkerFromModel(marker)
		}
	}
	return out
}
