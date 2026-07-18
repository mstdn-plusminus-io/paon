package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"github.com/mstdn-plusminus-io/paon/internal/paon/serializer"
	"gorm.io/gorm"
)

type webPushPayload struct {
	Subscription webPushSubscriptionPayload `json:"subscription"`
	Data         json.RawMessage            `json:"data"`
}

type webPushSubscriptionPayload struct {
	Endpoint string             `json:"endpoint"`
	Keys     webPushKeysPayload `json:"keys"`
}

type webPushKeysPayload struct {
	Auth   string `json:"auth"`
	P256dh string `json:"p256dh"`
}

var webPushAlertTypeList = []string{
	"mention",
	"status",
	"reblog",
	"follow",
	"follow_request",
	"favourite",
	"poll",
	"update",
	"admin.sign_up",
	"admin.report",
}

var webPushAlertTypes = func() map[string]struct{} {
	types := make(map[string]struct{}, len(webPushAlertTypeList))
	for _, key := range webPushAlertTypeList {
		types[key] = struct{}{}
	}
	return types
}()

func (s *Server) createWebPushSubscription(c *echo.Context) error {
	user, accessToken, err := s.requireUserAccessToken(c, "push")
	if err != nil {
		return err
	}
	c.Response().Header().Set("Vary", "Authorization")
	payload, err := parseWebPushPayload(c)
	if err != nil {
		return apiError(c, http.StatusBadRequest, "Malformed request")
	}
	if webPushSubscriptionPayloadMissing(payload) {
		return apiError(c, http.StatusBadRequest, "param is missing or the value is empty: subscription")
	}
	if payload.Subscription.Endpoint == "" || payload.Subscription.Keys.Auth == "" || payload.Subscription.Keys.P256dh == "" {
		return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Subscription is invalid")
	}

	activation, hasActivation, err := s.currentWebPushSessionActivation(c, user.ID, accessToken.ID)
	if err != nil {
		return err
	}
	alertsEnabled := s.webPushAlertsEnabledForAccessToken(accessToken.ID)
	if hasActivation {
		alertsEnabled = webPushAlertsEnabledForUserAgent(activation.UserAgent)
	}
	data := webPushDataWithDefaultAlerts(payload.Data, alertsEnabled)
	now := time.Now().UTC()
	subscription := models.WebPushSubscription{
		Endpoint:      payload.Subscription.Endpoint,
		KeyP256dh:     payload.Subscription.Keys.P256dh,
		KeyAuth:       payload.Subscription.Keys.Auth,
		Data:          data,
		UserID:        sql.NullInt64{Int64: user.ID, Valid: true},
		AccessTokenID: sql.NullInt64{Int64: accessToken.ID, Valid: true},
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if hasActivation {
			if activation.WebPushSubscriptionID.Valid {
				if err := tx.Delete(&models.WebPushSubscription{}, activation.WebPushSubscriptionID.Int64).Error; err != nil {
					return err
				}
				if err := tx.Model(&models.SessionActivation{}).
					Where("id = ?", activation.ID).
					Updates(map[string]any{"web_push_subscription_id": nil, "updated_at": time.Now().UTC()}).Error; err != nil {
					return err
				}
			}
		} else {
			if err := tx.Where("access_token_id = ?", accessToken.ID).Delete(&models.WebPushSubscription{}).Error; err != nil {
				return err
			}
		}
		if err := tx.Create(&subscription).Error; err != nil {
			return err
		}
		if hasActivation {
			return s.linkWebPushSubscriptionToSessionActivation(tx, activation.ID, subscription.ID)
		}
		return s.linkWebPushSubscriptionToSession(tx, accessToken.ID, subscription.ID)
	}); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, serializer.WebPushSubscriptionFromModel(s.cfg, subscription))
}

func (s *Server) showPushSubscription(c *echo.Context) error {
	_, accessToken, err := s.requireUserAccessToken(c, "push")
	if err != nil {
		return err
	}
	c.Response().Header().Set("Vary", "Authorization")
	subscription, err := s.pushSubscriptionForAccessToken(accessToken.ID)
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	return c.JSON(http.StatusOK, serializer.WebPushSubscriptionFromModel(s.cfg, subscription))
}

func (s *Server) createPushSubscription(c *echo.Context) error {
	user, accessToken, err := s.requireUserAccessToken(c, "push")
	if err != nil {
		return err
	}
	c.Response().Header().Set("Vary", "Authorization")
	payload, err := parseWebPushPayload(c)
	if err != nil {
		return apiError(c, http.StatusBadRequest, "Malformed request")
	}
	if webPushSubscriptionPayloadMissing(payload) {
		return apiError(c, http.StatusBadRequest, "param is missing or the value is empty: subscription")
	}
	if payload.Subscription.Endpoint == "" || payload.Subscription.Keys.Auth == "" || payload.Subscription.Keys.P256dh == "" {
		return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Subscription is invalid")
	}

	now := time.Now().UTC()
	subscription := models.WebPushSubscription{
		Endpoint:      payload.Subscription.Endpoint,
		KeyP256dh:     payload.Subscription.Keys.P256dh,
		KeyAuth:       payload.Subscription.Keys.Auth,
		Data:          normalizeWebPushData(payload.Data),
		UserID:        sql.NullInt64{Int64: user.ID, Valid: true},
		AccessTokenID: sql.NullInt64{Int64: accessToken.ID, Valid: true},
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("access_token_id = ?", accessToken.ID).Delete(&models.WebPushSubscription{}).Error; err != nil {
			return err
		}
		if err := tx.Create(&subscription).Error; err != nil {
			return err
		}
		return s.linkWebPushSubscriptionToSession(tx, accessToken.ID, subscription.ID)
	}); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, serializer.WebPushSubscriptionFromModel(s.cfg, subscription))
}

func (s *Server) updatePushSubscription(c *echo.Context) error {
	_, accessToken, err := s.requireUserAccessToken(c, "push")
	if err != nil {
		return err
	}
	c.Response().Header().Set("Vary", "Authorization")
	payload, err := parseWebPushPayload(c)
	if err != nil {
		return apiError(c, http.StatusBadRequest, "Malformed request")
	}
	subscription, err := s.pushSubscriptionForAccessToken(accessToken.ID)
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}

	data := normalizeWebPushData(payload.Data)
	updatedAt := time.Now().UTC()
	if err := s.db.Model(&models.WebPushSubscription{}).
		Where("id = ? AND access_token_id = ?", subscription.ID, accessToken.ID).
		Updates(map[string]any{"data": data, "updated_at": updatedAt}).Error; err != nil {
		return err
	}
	subscription.Data = data
	subscription.UpdatedAt = updatedAt
	return c.JSON(http.StatusOK, serializer.WebPushSubscriptionFromModel(s.cfg, subscription))
}

func (s *Server) deletePushSubscription(c *echo.Context) error {
	_, accessToken, err := s.requireUserAccessToken(c, "push")
	if err != nil {
		return err
	}
	c.Response().Header().Set("Vary", "Authorization")
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("access_token_id = ?", accessToken.ID).Delete(&models.WebPushSubscription{}).Error; err != nil {
			return err
		}
		return s.unlinkWebPushSubscriptionFromSession(tx, accessToken.ID)
	}); err != nil {
		return err
	}
	return renderEmpty(c)
}

func (s *Server) updateWebPushSubscription(c *echo.Context) error {
	_, _, err := s.requireUser(c)
	if err != nil {
		return err
	}
	c.Response().Header().Set("Vary", "Authorization")
	payload, err := parseWebPushPayload(c)
	if err != nil {
		return apiError(c, http.StatusBadRequest, "Malformed request")
	}
	if webPushDataPayloadMissing(payload) {
		return apiError(c, http.StatusBadRequest, "param is missing or the value is empty: data")
	}
	data := normalizeWebPushData(payload.Data)

	var subscription models.WebPushSubscription
	err = s.db.Where("id = ?", c.Param("id")).First(&subscription).Error
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	subscription.Data = data
	subscription.UpdatedAt = time.Now().UTC()
	if err := s.db.Model(&models.WebPushSubscription{}).
		Where("id = ?", subscription.ID).
		Updates(map[string]any{"data": data, "updated_at": subscription.UpdatedAt}).Error; err != nil {
		return err
	}
	return c.JSON(http.StatusOK, serializer.WebPushSubscriptionFromModel(s.cfg, subscription))
}

func (s *Server) requireUserAccessToken(c *echo.Context, scopes ...string) (*models.User, *models.OAuthAccessToken, error) {
	c.Response().Header().Set("Vary", "Authorization")
	user, _, err := s.requireUser(c)
	if err != nil {
		return nil, nil, err
	}
	accessToken, err := s.currentAccessToken(c)
	if err != nil || !accessToken.ResourceOwnerID.Valid || accessToken.ResourceOwnerID.Int64 != user.ID {
		return nil, nil, apiError(c, http.StatusUnauthorized, "The access token is invalid")
	}
	if len(scopes) > 0 && !tokenHasAnyScope(accessToken.Scopes, scopes...) {
		return nil, nil, apiError(c, http.StatusForbidden, "This action is outside the authorized scopes")
	}
	return user, accessToken, nil
}

func (s *Server) pushSubscriptionForAccessToken(accessTokenID int64) (models.WebPushSubscription, error) {
	var subscription models.WebPushSubscription
	err := s.db.Where("access_token_id = ?", accessTokenID).First(&subscription).Error
	return subscription, err
}

func (s *Server) webPushSubscriptionForToken(token string, userID int64) *models.WebPushSubscription {
	if s.db == nil || token == "" || userID == 0 {
		return nil
	}
	var subscription models.WebPushSubscription
	err := s.db.
		Joins("JOIN oauth_access_tokens ON oauth_access_tokens.id = web_push_subscriptions.access_token_id").
		Where("oauth_access_tokens.token = ? AND oauth_access_tokens.revoked_at IS NULL AND web_push_subscriptions.user_id = ?", token, userID).
		First(&subscription).Error
	if err != nil {
		return nil
	}
	return &subscription
}

func (s *Server) currentWebPushSessionActivation(c *echo.Context, userID int64, accessTokenID int64) (models.SessionActivation, bool, error) {
	var activation models.SessionActivation
	if s == nil || s.db == nil || userID == 0 {
		return activation, false, nil
	}
	if sessionID, ok := s.railsSessionIDFromCookie(c); ok {
		err := s.db.Where("session_id = ? AND user_id = ?", sessionID, userID).First(&activation).Error
		if err == nil {
			return activation, true, nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return activation, false, err
		}
	}
	if sessionID, ok := s.railsSessionIDFromEncryptedSession(c); ok {
		err := s.db.Where("session_id = ? AND user_id = ?", sessionID, userID).First(&activation).Error
		if err == nil {
			return activation, true, nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return activation, false, err
		}
	}
	if accessTokenID == 0 {
		return activation, false, nil
	}
	err := s.db.Where("access_token_id = ? AND user_id = ?", accessTokenID, userID).Order("updated_at DESC").First(&activation).Error
	if err == nil {
		return activation, true, nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return activation, false, nil
	}
	return activation, false, err
}

func (s *Server) linkWebPushSubscriptionToSession(tx *gorm.DB, accessTokenID int64, subscriptionID int64) error {
	if tx == nil || accessTokenID == 0 || subscriptionID == 0 {
		return nil
	}
	return tx.Model(&models.SessionActivation{}).
		Where("access_token_id = ?", accessTokenID).
		Updates(map[string]any{"web_push_subscription_id": subscriptionID, "updated_at": time.Now().UTC()}).Error
}

func (s *Server) linkWebPushSubscriptionToSessionActivation(tx *gorm.DB, activationID int64, subscriptionID int64) error {
	if tx == nil || activationID == 0 || subscriptionID == 0 {
		return nil
	}
	return tx.Model(&models.SessionActivation{}).
		Where("id = ?", activationID).
		Updates(map[string]any{"web_push_subscription_id": subscriptionID, "updated_at": time.Now().UTC()}).Error
}

func (s *Server) unlinkWebPushSubscriptionFromSession(tx *gorm.DB, accessTokenID int64) error {
	if tx == nil || accessTokenID == 0 {
		return nil
	}
	return tx.Model(&models.SessionActivation{}).
		Where("access_token_id = ?", accessTokenID).
		Updates(map[string]any{"web_push_subscription_id": nil, "updated_at": time.Now().UTC()}).Error
}

func parseWebPushPayload(c *echo.Context) (webPushPayload, error) {
	var payload webPushPayload
	if !requestIsJSON(c) {
		return parseWebPushFormPayload(c)
	}
	if err := json.NewDecoder(c.Request().Body).Decode(&payload); err != nil {
		return payload, err
	}
	return normalizeWebPushPayload(payload)
}

func parseWebPushFormPayload(c *echo.Context) (webPushPayload, error) {
	var payload webPushPayload
	req := c.Request()
	if err := req.ParseForm(); err != nil {
		return payload, err
	}
	values := req.Form
	payload.Subscription.Endpoint = strings.TrimSpace(lastFormValue(values, "subscription[endpoint]"))
	payload.Subscription.Keys.Auth = strings.TrimSpace(lastFormValue(values, "subscription[keys][auth]"))
	payload.Subscription.Keys.P256dh = strings.TrimSpace(lastFormValue(values, "subscription[keys][p256dh]"))

	data, err := webPushDataFromForm(values)
	if err != nil {
		return payload, err
	}
	payload.Data = data
	return normalizeWebPushPayload(payload)
}

func normalizeWebPushPayload(payload webPushPayload) (webPushPayload, error) {
	if len(payload.Data) > 0 && string(payload.Data) != "null" && !json.Valid(payload.Data) {
		return payload, errors.New("invalid data")
	}
	return payload, nil
}

func webPushSubscriptionPayloadMissing(payload webPushPayload) bool {
	return payload.Subscription.Endpoint == "" &&
		payload.Subscription.Keys.Auth == "" &&
		payload.Subscription.Keys.P256dh == ""
}

func webPushDataPayloadMissing(payload webPushPayload) bool {
	raw := strings.TrimSpace(string(payload.Data))
	return raw == "" || raw == "null"
}

func webPushDataFromForm(values map[string][]string) (json.RawMessage, error) {
	data := map[string]any{}
	if _, ok := values["data[policy]"]; ok {
		data["policy"] = lastFormValue(values, "data[policy]")
	}
	alerts := map[string]any{}
	for key := range values {
		alertKey, ok := webPushAlertKeyFromForm(key)
		if !ok {
			continue
		}
		alerts[alertKey] = formBoolValue(lastFormValue(values, key))
	}
	if len(alerts) > 0 {
		data["alerts"] = alerts
	}
	if len(data) == 0 {
		return nil, nil
	}
	encoded, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	return encoded, nil
}

func webPushAlertKeyFromForm(key string) (string, bool) {
	const prefix = "data[alerts]["
	if !strings.HasPrefix(key, prefix) || !strings.HasSuffix(key, "]") {
		return "", false
	}
	alertKey := strings.TrimSuffix(strings.TrimPrefix(key, prefix), "]")
	return alertKey, alertKey != ""
}

func defaultWebPushData(raw json.RawMessage) models.JSONValue {
	return webPushDataWithDefaultAlerts(raw, false)
}

func (s *Server) defaultWebPushDataForAccessToken(accessTokenID int64, raw json.RawMessage) models.JSONValue {
	return webPushDataWithDefaultAlerts(raw, s.webPushAlertsEnabledForAccessToken(accessTokenID))
}

func webPushDataWithDefaultAlerts(raw json.RawMessage, alertsEnabled bool) models.JSONValue {
	base := map[string]any{"policy": "all", "alerts": defaultWebPushAlerts(alertsEnabled)}
	mergeWebPushData(base, raw)
	encoded, _ := json.Marshal(base)
	return models.JSONValue(encoded)
}

func defaultWebPushAlerts(enabled bool) map[string]bool {
	alerts := make(map[string]bool, len(webPushAlertTypeList))
	for _, key := range webPushAlertTypeList {
		alerts[key] = enabled
	}
	return alerts
}

func (s *Server) webPushAlertsEnabledForAccessToken(accessTokenID int64) bool {
	if s == nil || s.db == nil || accessTokenID == 0 {
		return false
	}
	var activation models.SessionActivation
	err := s.db.Select("user_agent").
		Where("access_token_id = ?", accessTokenID).
		Order("updated_at DESC").
		First(&activation).Error
	if err != nil {
		return false
	}
	return webPushAlertsEnabledForUserAgent(activation.UserAgent)
}

func webPushAlertsEnabledForUserAgent(userAgent string) bool {
	ua := strings.ToLower(strings.TrimSpace(userAgent))
	if ua == "" {
		return false
	}
	for _, token := range []string{
		"iphone",
		"ipod",
		"ipad",
		"windows phone",
		"blackberry",
		"opera mini",
		"mobile",
		"tablet",
		"kindle",
		"silk/",
		"playbook",
	} {
		if strings.Contains(ua, token) {
			return true
		}
	}
	if strings.Contains(ua, "android") {
		return true
	}
	return strings.Contains(ua, "macintosh") && strings.Contains(ua, "mobile/")
}

func normalizeWebPushData(raw json.RawMessage) models.JSONValue {
	if len(raw) == 0 || string(raw) == "null" || !json.Valid(raw) {
		return models.JSONValue(`{}`)
	}
	base := map[string]any{}
	mergeWebPushData(base, raw)
	encoded, _ := json.Marshal(base)
	return models.JSONValue(encoded)
}

func mergeWebPushData(base map[string]any, raw json.RawMessage) {
	if len(raw) == 0 || string(raw) == "null" || !json.Valid(raw) {
		return
	}
	var incoming map[string]any
	if json.Unmarshal(raw, &incoming) != nil {
		return
	}
	if policy, ok := incoming["policy"].(string); ok {
		base["policy"] = policy
	}
	alerts, ok := incoming["alerts"].(map[string]any)
	if !ok {
		return
	}
	cleanAlerts := map[string]any{}
	for key, value := range alerts {
		if _, allowed := webPushAlertTypes[key]; allowed {
			cleanAlerts[key] = value
		}
	}
	if existing, ok := base["alerts"].(map[string]bool); ok {
		for key, value := range cleanAlerts {
			if boolValue, ok := value.(bool); ok {
				existing[key] = boolValue
			}
		}
		return
	}
	base["alerts"] = cleanAlerts
}

func (s *Server) currentAccessToken(c *echo.Context) (*models.OAuthAccessToken, error) {
	accessToken, err := s.accessTokenFromRequest(c)
	if err != nil || !accessToken.ResourceOwnerID.Valid {
		return nil, errors.New("invalid token")
	}
	return accessToken, nil
}

func (s *Server) accessTokenFromRequest(c *echo.Context) (*models.OAuthAccessToken, error) {
	token := requestToken(c)
	if token == "" || s.db == nil {
		return nil, errors.New("missing token")
	}
	var accessToken models.OAuthAccessToken
	err := s.db.Where("token = ? AND revoked_at IS NULL", token).First(&accessToken).Error
	if err != nil {
		return nil, errors.New("invalid token")
	}
	_ = s.trackAccessTokenUse(c, &accessToken)
	return &accessToken, nil
}
