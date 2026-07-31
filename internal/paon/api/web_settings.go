package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm/clause"
)

type webSettingsPayload struct {
	Data json.RawMessage `json:"data"`
}

func (s *Server) updateWebSettings(c *echo.Context) error {
	c.Response().Header().Set("Vary", "Authorization")
	user, _, err := s.requireUser(c)
	if err != nil {
		return err
	}
	payload, err := parseWebSettingsPayload(c)
	if err != nil {
		return apiError(c, http.StatusBadRequest, "Malformed request")
	}

	now := time.Now().UTC()
	setting := models.WebSetting{
		UserID:    user.ID,
		Data:      models.JSONValue(payload.Data),
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "user_id"}},
		DoUpdates: clause.Assignments(map[string]any{
			"data":       models.JSONValue(payload.Data),
			"updated_at": now,
		}),
	}).Create(&setting).Error; err != nil {
		return err
	}
	return renderEmpty(c)
}

func parseWebSettingsPayload(c *echo.Context) (webSettingsPayload, error) {
	var payload webSettingsPayload
	if !requestIsJSON(c) {
		return parseWebSettingsFormPayload(c)
	}
	if err := json.NewDecoder(c.Request().Body).Decode(&payload); err != nil {
		return payload, err
	}
	return normalizeWebSettingsPayload(payload)
}

func parseWebSettingsFormPayload(c *echo.Context) (webSettingsPayload, error) {
	var payload webSettingsPayload
	req := c.Request()
	if err := req.ParseForm(); err != nil {
		return payload, err
	}
	if raw := strings.TrimSpace(req.Form.Get("data")); raw != "" {
		payload.Data = json.RawMessage(raw)
		return normalizeWebSettingsPayload(payload)
	}
	data := map[string]any{}
	for key, values := range req.Form {
		path, ok := webSettingsDataPath(key)
		if !ok || len(path) == 0 {
			continue
		}
		assignWebSettingsFormValue(data, path, values)
	}
	if len(data) == 0 {
		payload.Data = json.RawMessage(`{}`)
		return payload, nil
	}
	encoded, err := json.Marshal(data)
	if err != nil {
		return payload, err
	}
	payload.Data = encoded
	return payload, nil
}

func normalizeWebSettingsPayload(payload webSettingsPayload) (webSettingsPayload, error) {
	if len(payload.Data) == 0 || string(payload.Data) == "null" {
		payload.Data = json.RawMessage(`{}`)
	}
	if !json.Valid(payload.Data) {
		return payload, errors.New("invalid data")
	}
	var data any
	if err := json.Unmarshal(payload.Data, &data); err != nil {
		return payload, err
	}
	encoded, err := json.Marshal(data)
	if err != nil {
		return payload, err
	}
	payload.Data = encoded
	return payload, nil
}

func webSettingsDataPath(key string) ([]string, bool) {
	if key == "data" {
		return nil, true
	}
	if !strings.HasPrefix(key, "data[") {
		return nil, false
	}
	rest := strings.TrimPrefix(key, "data")
	path := []string{}
	for rest != "" {
		if !strings.HasPrefix(rest, "[") {
			return nil, false
		}
		end := strings.Index(rest, "]")
		if end < 0 {
			return nil, false
		}
		path = append(path, rest[1:end])
		rest = rest[end+1:]
	}
	return path, true
}

func assignWebSettingsFormValue(root map[string]any, path []string, values []string) {
	if len(path) == 0 {
		return
	}
	current := root
	for i, key := range path {
		last := i == len(path)-1
		if last {
			value := webSettingsFormValue(values)
			if key == "" {
				return
			}
			current[key] = value
			return
		}
		if key == "" {
			return
		}
		next, _ := current[key].(map[string]any)
		if next == nil {
			next = map[string]any{}
			current[key] = next
		}
		current = next
	}
}

func webSettingsFormValue(values []string) any {
	if len(values) == 0 {
		return ""
	}
	if len(values) > 1 {
		out := make([]any, 0, len(values))
		for _, value := range values {
			out = append(out, webSettingsScalarValue(value))
		}
		return out
	}
	return webSettingsScalarValue(values[0])
}

func webSettingsScalarValue(value string) any {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "{") || strings.HasPrefix(value, "[") || strings.HasPrefix(value, `"`) {
		var decoded any
		if err := json.Unmarshal([]byte(value), &decoded); err == nil {
			return decoded
		}
	}
	return value
}

func decodeWebSettings(raw []byte) map[string]any {
	settings := map[string]any{}
	if len(raw) == 0 {
		return settings
	}
	if err := json.Unmarshal(raw, &settings); err != nil || settings == nil {
		return map[string]any{}
	}
	return settings
}
