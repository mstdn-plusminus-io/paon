package api

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"github.com/mstdn-plusminus-io/paon/internal/paon/serializer"
	"gorm.io/gorm"
)

type cryptoDevicePayload struct {
	AccountID      string `json:"account_id"`
	DeviceID       string `json:"device_id"`
	Name           string `json:"name"`
	FingerprintKey string `json:"fingerprint_key"`
	IdentityKey    string `json:"identity_key"`
	ClaimURL       string `json:"claim"`
	Type           int    `json:"type"`
	Body           string `json:"body"`
	HMAC           string `json:"hmac"`
}

type cryptoOneTimeKeyPayload struct {
	KeyID     string `json:"key_id"`
	Key       string `json:"key"`
	Signature string `json:"signature"`
}

type cryptoUploadPayload struct {
	Device      cryptoDevicePayload       `json:"device"`
	OneTimeKeys []cryptoOneTimeKeyPayload `json:"one_time_keys"`
}

type cryptoQueryPayload struct {
	ID []string `json:"id"`
}

type cryptoDevicesPayload struct {
	Device []cryptoDevicePayload `json:"device"`
}

type cryptoRawDevicesPayload struct {
	Device json.RawMessage `json:"device"`
}

type cryptoClaimResult struct {
	AccountID int64
	DeviceID  string
	KeyID     string
	Key       string
	Signature string
}

func (s *Server) cryptoUploadKey(c *echo.Context) error {
	account, accessToken, err := s.requireCryptoAccount(c)
	if err != nil {
		return err
	}
	payload, err := parseCryptoUploadPayload(c)
	if err != nil {
		return apiError(c, http.StatusBadRequest, err.Error())
	}
	if err := validateCryptoUploadPayload(payload); err != nil {
		return err
	}
	now := time.Now().UTC()
	var device models.Device
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		err := tx.Where("access_token_id = ?", accessToken.ID).First(&device).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			device = models.Device{AccessTokenID: sql.NullInt64{Int64: accessToken.ID, Valid: true}, CreatedAt: now}
		} else if err != nil {
			return err
		}
		nextDeviceID := payload.Device.DeviceID
		identityChanged := cryptoDeviceIdentityChanged(device, nextDeviceID, payload.Device.FingerprintKey, payload.Device.IdentityKey)
		if device.ID != 0 && identityChanged {
			if err := deleteCryptoDeviceAssociations(tx, device.ID); err != nil {
				return err
			}
		}
		device.AccountID = models.DeviceAccountID(account.ID)
		device.DeviceID = nextDeviceID
		device.Name = payload.Device.Name
		device.FingerprintKey = payload.Device.FingerprintKey
		device.IdentityKey = payload.Device.IdentityKey
		device.UpdatedAt = now
		if device.ID == 0 {
			if err := tx.Create(&device).Error; err != nil {
				return err
			}
		} else if err := tx.Save(&device).Error; err != nil {
			return err
		}
		for _, raw := range payload.OneTimeKeys {
			key := models.OneTimeKey{DeviceID: models.OneTimeKeyDeviceID(device.ID), KeyID: raw.KeyID, Key: raw.Key, Signature: raw.Signature, CreatedAt: now, UpdatedAt: now}
			if err := tx.Create(&key).Error; err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, cryptoDeviceResponse(device))
}

func cryptoDeviceIdentityChanged(device models.Device, nextDeviceID string, nextFingerprintKey string, nextIdentityKey string) bool {
	return device.DeviceID != nextDeviceID ||
		device.FingerprintKey != nextFingerprintKey ||
		device.IdentityKey != nextIdentityKey
}

func deleteCryptoDeviceAssociations(tx *gorm.DB, deviceID int64) error {
	if err := tx.Where("device_id = ?", deviceID).Delete(&models.OneTimeKey{}).Error; err != nil {
		return err
	}
	return tx.Where("device_id = ?", deviceID).Delete(&models.EncryptedMessage{}).Error
}

func validateCryptoUploadPayload(payload cryptoUploadPayload) error {
	if strings.TrimSpace(payload.Device.Name) == "" {
		return apiHTTPError{status: http.StatusUnprocessableEntity, message: "Validation failed: Name can't be blank"}
	}
	if strings.TrimSpace(payload.Device.FingerprintKey) == "" {
		return apiHTTPError{status: http.StatusUnprocessableEntity, message: "Validation failed: Fingerprint key can't be blank"}
	}
	if !validCryptoEd25519Key(payload.Device.FingerprintKey) {
		return apiHTTPError{status: http.StatusUnprocessableEntity, message: "Validation failed: Fingerprint key is not a valid Ed25519 or Curve25519 key"}
	}
	if strings.TrimSpace(payload.Device.IdentityKey) == "" {
		return apiHTTPError{status: http.StatusUnprocessableEntity, message: "Validation failed: Identity key can't be blank"}
	}
	if !validCryptoEd25519Key(payload.Device.IdentityKey) {
		return apiHTTPError{status: http.StatusUnprocessableEntity, message: "Validation failed: Identity key is not a valid Ed25519 or Curve25519 key"}
	}
	for _, key := range payload.OneTimeKeys {
		if strings.TrimSpace(key.KeyID) == "" {
			return apiHTTPError{status: http.StatusUnprocessableEntity, message: "Validation failed: Key can't be blank"}
		}
		if strings.TrimSpace(key.Key) == "" {
			return apiHTTPError{status: http.StatusUnprocessableEntity, message: "Validation failed: Key can't be blank"}
		}
		if !validCryptoEd25519Key(key.Key) {
			return apiHTTPError{status: http.StatusUnprocessableEntity, message: "Validation failed: Key is not a valid Ed25519 or Curve25519 key"}
		}
		if strings.TrimSpace(key.Signature) == "" {
			return apiHTTPError{status: http.StatusUnprocessableEntity, message: "Validation failed: Signature can't be blank"}
		}
		if !validCryptoEd25519Signature(payload.Device.FingerprintKey, key.Signature, key.Key) {
			return apiHTTPError{status: http.StatusUnprocessableEntity, message: "Validation failed: Signature is not a valid Ed25519 signature"}
		}
	}
	return nil
}

func validCryptoEd25519Key(raw string) bool {
	key, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return false
	}
	return len(key) == ed25519.PublicKeySize
}

func validCryptoEd25519Signature(rawVerifyKey string, rawSignature string, message string) bool {
	verifyKey, err := base64.StdEncoding.DecodeString(rawVerifyKey)
	if err != nil || len(verifyKey) != ed25519.PublicKeySize {
		return false
	}
	signature, err := base64.StdEncoding.DecodeString(rawSignature)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return false
	}
	return ed25519.Verify(ed25519.PublicKey(verifyKey), []byte(message), signature)
}

func (s *Server) cryptoKeyCount(c *echo.Context) error {
	device, err := s.currentCryptoDevice(c)
	if err != nil {
		return err
	}
	var count int64
	if err := s.db.Model(&models.OneTimeKey{}).Where("device_id = ?", device.ID).Count(&count).Error; err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]int64{"one_time_keys": count})
}

func (s *Server) cryptoQueryKeys(c *echo.Context) error {
	if _, _, err := s.requireCryptoAccount(c); err != nil {
		return err
	}
	ids, err := parseCryptoQueryIDs(c)
	if err != nil {
		return apiError(c, http.StatusBadRequest, err.Error())
	}
	var accounts []models.Account
	if len(ids) > 0 {
		if err := s.db.Where("id IN ?", ids).Find(&accounts).Error; err != nil {
			return err
		}
	}
	out := make([]serializer.KeyQueryResult, 0, len(accounts))
	for _, account := range accounts {
		if account.Local() {
			var devices []models.Device
			if err := s.db.Where("account_id = ?", account.ID).Find(&devices).Error; err != nil {
				return err
			}
			out = append(out, cryptoQueryResultResponse(account, devices))
			continue
		}
		devices, err := cryptoRemoteDevices(account)
		if err != nil || len(devices) == 0 {
			continue
		}
		out = append(out, cryptoRemoteQueryResultResponse(account, devices))
	}
	return c.JSON(http.StatusOK, out)
}

func (s *Server) cryptoClaimKeys(c *echo.Context) error {
	source, _, err := s.requireCryptoAccount(c)
	if err != nil {
		return err
	}
	devices, err := parseCryptoDevicesPayload(c)
	if err != nil {
		return apiError(c, http.StatusBadRequest, err.Error())
	}
	out := make([]serializer.KeyClaimResult, 0, len(devices))
	for _, request := range devices {
		targetID, parseErr := strconv.ParseInt(strings.TrimSpace(request.AccountID), 10, 64)
		if parseErr != nil {
			continue
		}
		claim, err := s.claimCryptoOneTimeKey(*source, targetID, request.DeviceID)
		if err != nil {
			return err
		}
		if claim != nil {
			out = append(out, cryptoClaimResponse(*claim))
		}
	}
	return c.JSON(http.StatusOK, out)
}

func (s *Server) cryptoEncryptedMessages(c *echo.Context) error {
	device, err := s.currentCryptoDevice(c)
	if err != nil {
		return err
	}
	limitValue := limitParam(c, 80, 160)
	query := s.db.Model(&models.EncryptedMessage{}).Where("device_id = ?", device.ID)
	if minID := c.QueryParam("min_id"); queryParamValuePresent(c, "min_id") {
		query = query.Where("encrypted_messages.id > ?", minID).Order("encrypted_messages.id ASC")
		if maxID := c.QueryParam("max_id"); queryParamValuePresent(c, "max_id") {
			query = query.Where("encrypted_messages.id < ?", maxID)
		}
	} else {
		if maxID := c.QueryParam("max_id"); queryParamValuePresent(c, "max_id") {
			query = query.Where("encrypted_messages.id < ?", maxID)
		}
		if sinceID := c.QueryParam("since_id"); queryParamValuePresent(c, "since_id") {
			query = query.Where("encrypted_messages.id > ?", sinceID)
		}
		query = query.Order("encrypted_messages.id DESC")
	}
	query = query.Limit(limitValue)
	var messages []models.EncryptedMessage
	if err := query.Find(&messages).Error; err != nil {
		return err
	}
	if queryParamValuePresent(c, "min_id") {
		reverseEncryptedMessages(messages)
	}
	if len(messages) > 0 {
		c.Response().Header().Set("Link", limitOnlyPaginationLink(c, messages[0].ID, messages[len(messages)-1].ID, "min_id", len(messages) == limitValue))
	}
	out := make([]serializer.EncryptedMessage, 0, len(messages))
	for _, message := range messages {
		out = append(out, cryptoEncryptedMessageResponse(message))
	}
	return c.JSON(http.StatusOK, out)
}

func reverseEncryptedMessages(rows []models.EncryptedMessage) {
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
}

func (s *Server) cryptoClearEncryptedMessages(c *echo.Context) error {
	device, err := s.currentCryptoDevice(c)
	if err != nil {
		return err
	}
	upTo, hasUpTo, err := cryptoClearUpToID(c)
	if err != nil {
		return err
	}
	if !hasUpTo {
		return renderEmpty(c)
	}
	query := s.db.Where("device_id = ?", device.ID)
	query = query.Where("id <= ?", upTo)
	if err := query.Delete(&models.EncryptedMessage{}).Error; err != nil {
		return err
	}
	return renderEmpty(c)
}

func (s *Server) cryptoDeliveries(c *echo.Context) error {
	account, _, err := s.requireCryptoAccount(c)
	if err != nil {
		return err
	}
	sourceDevice, err := s.currentCryptoDevice(c)
	if err != nil {
		return err
	}
	devices, err := parseCryptoDevicesPayload(c)
	if err != nil {
		return apiError(c, http.StatusBadRequest, err.Error())
	}
	if len(devices) == 0 {
		return apiError(c, http.StatusBadRequest, "param is missing or the value is empty: device")
	}
	now := time.Now().UTC()
	type createdEncryptedMessage struct {
		message models.EncryptedMessage
		device  models.Device
	}
	created := make([]createdEncryptedMessage, 0, len(devices))
	for _, request := range devices {
		targetID, parseErr := strconv.ParseInt(strings.TrimSpace(request.AccountID), 10, 64)
		if parseErr != nil {
			continue
		}
		var target models.Account
		if err := s.db.Where("id = ?", targetID).First(&target).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apiError(c, http.StatusNotFound, "Record not found")
			}
			return err
		}
		if !target.Local() {
			if err := s.deliverRemoteEncryptedMessage(*account, *sourceDevice, target, request, now); err != nil {
				return err
			}
			continue
		}
		var targetDevice models.Device
		if err := s.db.Where("account_id = ? AND device_id = ?", target.ID, request.DeviceID).First(&targetDevice).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apiError(c, http.StatusNotFound, "Record not found")
			}
			return err
		}
		messageFranking, err := s.cryptoMessageFranking(account.ID, target.ID, request.HMAC, now)
		if err != nil {
			return err
		}
		message := models.EncryptedMessage{
			DeviceID:        models.EncryptedMessageDeviceID(targetDevice.ID),
			FromAccountID:   models.EncryptedMessageFromAccountID(account.ID),
			FromDeviceID:    sourceDevice.DeviceID,
			MessageType:     request.Type,
			Body:            request.Body,
			Digest:          request.HMAC,
			MessageFranking: messageFranking,
			CreatedAt:       now,
			UpdatedAt:       now,
		}
		if err := s.db.Create(&message).Error; err != nil {
			return err
		}
		created = append(created, createdEncryptedMessage{message: message, device: targetDevice})
	}
	for _, item := range created {
		s.publishEncryptedMessage(item.message, item.device)
	}
	return renderEmpty(c)
}

func (s *Server) deliverRemoteEncryptedMessage(source models.Account, sourceDevice models.Device, target models.Account, request cryptoDevicePayload, now time.Time) error {
	inboxURL := strings.TrimSpace(target.InboxURL)
	if inboxURL == "" {
		return nil
	}
	messageFranking, err := s.cryptoMessageFranking(source.ID, target.ID, request.HMAC, now)
	if err != nil {
		return err
	}
	body, err := json.Marshal(activityPubEncryptedMessagePayload(s, source, sourceDevice, target, request, messageFranking, now))
	if err != nil {
		return err
	}
	return s.deliverActivityPub(source, inboxURL, body)
}

func (s *Server) requireCryptoAccount(c *echo.Context) (*models.Account, *models.OAuthAccessToken, error) {
	c.Response().Header().Set("Vary", "Authorization")
	user, _, err := s.requireUser(c)
	if err != nil {
		return nil, nil, err
	}
	accessToken, err := s.currentAccessToken(c)
	if err != nil || !accessToken.ResourceOwnerID.Valid || accessToken.ResourceOwnerID.Int64 != user.ID {
		return nil, nil, apiError(c, http.StatusUnauthorized, "The access token is invalid")
	}
	if !tokenHasAnyScope(accessToken.Scopes, "crypto") {
		return nil, nil, apiError(c, http.StatusForbidden, "This action is outside the authorized scopes")
	}
	var account models.Account
	if err := s.db.Where("id = ?", user.AccountID).First(&account).Error; err != nil {
		return nil, nil, err
	}
	return &account, accessToken, nil
}

func (s *Server) currentCryptoDevice(c *echo.Context) (*models.Device, error) {
	_, accessToken, err := s.requireCryptoAccount(c)
	if err != nil {
		return nil, err
	}
	var device models.Device
	if err := s.db.Where("access_token_id = ?", accessToken.ID).First(&device).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, apiError(c, http.StatusNotFound, "Record not found")
		}
		return nil, err
	}
	return &device, nil
}

func (s *Server) claimCryptoOneTimeKey(source models.Account, targetID int64, deviceID string) (*cryptoClaimResult, error) {
	var target models.Account
	if err := s.db.Where("id = ?", targetID).First(&target).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	if target.Local() {
		key, err := s.claimLocalOneTimeKey(target.ID, deviceID)
		if err != nil {
			return nil, err
		}
		if key == nil {
			return nil, nil
		}
		return &cryptoClaimResult{AccountID: target.ID, DeviceID: deviceID, KeyID: key.KeyID, Key: key.Key, Signature: key.Signature}, nil
	}
	return s.claimRemoteOneTimeKey(source, target, deviceID)
}

func (s *Server) claimRemoteOneTimeKey(source models.Account, target models.Account, deviceID string) (*cryptoClaimResult, error) {
	devices, err := cryptoRemoteDevices(target)
	if err != nil {
		return nil, nil
	}
	var claimURL string
	for _, device := range devices {
		if device.DeviceID == deviceID {
			claimURL = device.ClaimURL
			break
		}
	}
	if strings.TrimSpace(claimURL) == "" {
		return nil, nil
	}
	keyID, key, signature, err := s.cryptoRemoteClaim(source, claimURL)
	if err != nil || key == "" {
		return nil, nil
	}
	return &cryptoClaimResult{AccountID: target.ID, DeviceID: deviceID, KeyID: keyID, Key: key, Signature: signature}, nil
}

func cryptoDeviceResponse(device models.Device) serializer.KeyDevice {
	return serializer.KeyDeviceFromModel(device)
}

func cryptoQueryResultResponse(account models.Account, devices []models.Device) serializer.KeyQueryResult {
	return serializer.KeyQueryResultFromModel(account, devices)
}

func cryptoRemoteQueryResultResponse(account models.Account, devices []cryptoDevicePayload) serializer.KeyQueryResult {
	out := make([]serializer.KeyDevice, 0, len(devices))
	for _, device := range devices {
		out = append(out, serializer.KeyDevice{
			DeviceID:       device.DeviceID,
			Name:           device.Name,
			IdentityKey:    device.IdentityKey,
			FingerprintKey: device.FingerprintKey,
		})
	}
	return serializer.KeyQueryResultFromDevices(account, out)
}

func cryptoClaimResponse(result cryptoClaimResult) serializer.KeyClaimResult {
	return serializer.KeyClaimResultFromValues(result.AccountID, result.DeviceID, result.KeyID, result.Key, result.Signature)
}

func activityPubEncryptedMessagePayload(s *Server, source models.Account, sourceDevice models.Device, target models.Account, request cryptoDevicePayload, messageFranking string, published time.Time) map[string]any {
	sourceActor := activityPubActorID(s, source)
	targetActor := activityPubAccountTagManagerURI(s, target)
	return map[string]any{
		"@context":  activityPubEncryptedMessageContext(),
		"id":        activityPubGeneratedPayloadURI(s),
		"type":      "Create",
		"actor":     sourceActor,
		"published": published.UTC().Format(time.RFC3339),
		"to":        targetActor,
		"cc":        nil,
		"object": map[string]any{
			"type":            "EncryptedMessage",
			"attributedTo":    map[string]any{"type": "Device", "deviceId": sourceDevice.DeviceID},
			"to":              map[string]any{"type": "Device", "deviceId": request.DeviceID},
			"messageType":     request.Type,
			"cipherText":      request.Body,
			"messageFranking": messageFranking,
			"digest": map[string]any{
				"type":            "Digest",
				"digestAlgorithm": "http://www.w3.org/2000/09/xmldsig#hmac-sha256",
				"digestValue":     request.HMAC,
			},
		},
	}
}

func activityPubGeneratedPayloadURI(s *Server) string {
	return strings.TrimRight(s.cfg.BaseURL(), "/") + "/payloads/" + uuid.NewString()
}

func cryptoEncryptedMessageResponse(message models.EncryptedMessage) serializer.EncryptedMessage {
	accountID := ""
	if message.FromAccountID.Valid {
		accountID = strconv.FormatInt(message.FromAccountID.Int64, 10)
	}
	return serializer.EncryptedMessage{
		ID:              strconv.FormatInt(message.ID, 10),
		AccountID:       accountID,
		DeviceID:        message.FromDeviceID,
		Type:            message.MessageType,
		Body:            message.Body,
		Digest:          message.Digest,
		MessageFranking: message.MessageFranking,
		CreatedAt:       message.CreatedAt.UTC().Format(time.RFC3339),
	}
}

func cryptoRemoteDevices(account models.Account) ([]cryptoDevicePayload, error) {
	rawURL := strings.TrimSpace(string(account.DevicesURL))
	if rawURL == "" {
		return nil, nil
	}
	body, err := fetchActivityResource(rawURL)
	if err != nil {
		return nil, err
	}
	var collection struct {
		Items []map[string]any `json:"items"`
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	items := activityJSONLDListItems(firstActivityValue(activityJSONLDValue(raw, "items"), activityJSONLDValue(raw, "orderedItems")))
	if len(items) == 0 {
		if err := json.Unmarshal(body, &collection); err != nil {
			return nil, err
		}
		for _, item := range collection.Items {
			items = append(items, item)
		}
	}
	out := make([]cryptoDevicePayload, 0, len(items))
	for _, item := range items {
		object, ok := activityJSONLDSingle(item).(map[string]any)
		if !ok {
			continue
		}
		device := cryptoDevicePayload{
			DeviceID:       cryptoRemoteDeviceID(object),
			Name:           activityJSONLDString(object, "name"),
			IdentityKey:    activityJSONLDStringValue(nestedActivityJSONLDValue(object, "identityKey", "publicKeyBase64")),
			FingerprintKey: activityJSONLDStringValue(nestedActivityJSONLDValue(object, "fingerprintKey", "publicKeyBase64")),
			ClaimURL:       firstActivityString(activityJSONLDValue(object, "claim")),
		}
		if device.DeviceID != "" {
			out = append(out, device)
		}
	}
	return out, nil
}

func cryptoRemoteDeviceID(object map[string]any) string {
	for _, value := range []any{object["id"], object["@id"], activityJSONLDValue(object, "deviceId")} {
		if raw, ok := activityJSONLDSingle(value).(string); ok && raw != "" {
			return raw
		}
	}
	return ""
}

func firstActivityValue(values ...any) any {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func (s *Server) cryptoRemoteClaim(source models.Account, claimURL string) (string, string, string, error) {
	if !source.PrivateKey.Valid || strings.TrimSpace(source.PrivateKey.String) == "" {
		return "", "", "", nil
	}
	parsed, err := url.Parse(claimURL)
	if err != nil || parsed.Host == "" || !activityFetchHostAllowed(parsed.Hostname()) || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", "", "", nil
	}
	req, err := http.NewRequest(http.MethodPost, claimURL, nil)
	if err != nil {
		return "", "", "", err
	}
	req.Header.Set("Content-Type", "application/activity+json")
	req.Header.Set("Date", time.Now().UTC().Format(http.TimeFormat))
	req.Header.Set("Host", req.Host)
	key, err := activityPrivateKey(source.PrivateKey.String)
	if err != nil {
		return "", "", "", err
	}
	headers := []string{"host", "date", "content-type", "(request-target)"}
	if err := s.signActivityPubRequest(req, source, key, headers); err != nil {
		return "", "", "", err
	}
	resp, err := activityHTTPClient.Do(req)
	if err != nil {
		return "", "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", "", nil
	}
	body, err := readActivityResponseBodyWithRailsLimit(resp, "crypto-claim")
	if err != nil {
		return "", "", "", nil
	}
	var payload struct {
		ID              string `json:"id"`
		KeyID           string `json:"keyId"`
		PublicKeyBase64 string `json:"publicKeyBase64"`
		Signature       struct {
			SignatureValue string `json:"signatureValue"`
		} `json:"signature"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", "", "", err
	}
	keyID := payload.ID
	if keyID == "" {
		keyID = payload.KeyID
	}
	return keyID, payload.PublicKeyBase64, payload.Signature.SignatureValue, nil
}

func parseCryptoUploadPayload(c *echo.Context) (cryptoUploadPayload, error) {
	var payload cryptoUploadPayload
	if requestIsJSON(c) {
		if err := json.NewDecoder(c.Request().Body).Decode(&payload); err != nil {
			return payload, err
		}
		return payload, nil
	}
	if err := c.Request().ParseForm(); err != nil {
		return payload, err
	}
	payload.Device.DeviceID = c.FormValue("device[device_id]")
	payload.Device.Name = c.FormValue("device[name]")
	payload.Device.FingerprintKey = c.FormValue("device[fingerprint_key]")
	payload.Device.IdentityKey = c.FormValue("device[identity_key]")
	payload.OneTimeKeys = parseCryptoFormOneTimeKeys(c.Request().Form)
	return payload, nil
}

func cryptoClearUpToID(c *echo.Context) (string, bool, error) {
	if values, ok := c.QueryParams()["up_to_id"]; ok && len(values) > 0 {
		return values[len(values)-1], true, nil
	}
	if requestIsJSON(c) {
		var raw map[string]json.RawMessage
		if err := json.NewDecoder(c.Request().Body).Decode(&raw); err != nil {
			if errors.Is(err, io.EOF) {
				return "", false, nil
			}
			return "", false, err
		}
		value, ok := raw["up_to_id"]
		return rawJSONString(value), ok, nil
	}
	if err := c.Request().ParseForm(); err != nil {
		return "", false, err
	}
	if values, ok := c.Request().Form["up_to_id"]; ok && len(values) > 0 {
		return values[len(values)-1], true, nil
	}
	return "", false, nil
}

func parseCryptoQueryIDs(c *echo.Context) ([]int64, error) {
	var ids []string
	if requestIsJSON(c) {
		var payload cryptoQueryPayload
		if err := json.NewDecoder(c.Request().Body).Decode(&payload); err != nil {
			return nil, err
		}
		ids = payload.ID
	} else {
		if err := c.Request().ParseForm(); err != nil {
			return nil, err
		}
		ids = append(ids, c.Request().Form["id"]...)
		ids = append(ids, c.Request().Form["id[]"]...)
	}
	out := make([]int64, 0, len(ids))
	for _, raw := range ids {
		if id := railsToInt64(raw); id > 0 {
			out = append(out, id)
		}
	}
	return out, nil
}

func parseCryptoDevicesPayload(c *echo.Context) ([]cryptoDevicePayload, error) {
	if requestIsJSON(c) {
		var payload cryptoRawDevicesPayload
		if err := json.NewDecoder(c.Request().Body).Decode(&payload); err != nil {
			return nil, err
		}
		return parseCryptoJSONDevices(payload.Device)
	}
	if err := c.Request().ParseForm(); err != nil {
		return nil, err
	}
	return parseCryptoFormDevices(c.Request().Form), nil
}

func parseCryptoJSONDevices(raw json.RawMessage) ([]cryptoDevicePayload, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil, nil
	}
	if strings.HasPrefix(trimmed, "[") {
		var devices []cryptoDevicePayload
		if err := json.Unmarshal(raw, &devices); err != nil {
			return nil, err
		}
		return devices, nil
	}
	var device cryptoDevicePayload
	if err := json.Unmarshal(raw, &device); err != nil {
		return nil, err
	}
	return []cryptoDevicePayload{device}, nil
}

func parseCryptoFormOneTimeKeys(values map[string][]string) []cryptoOneTimeKeyPayload {
	keyIDs := values["one_time_keys[][key_id]"]
	keys := values["one_time_keys[][key]"]
	signatures := values["one_time_keys[][signature]"]
	count := maxLen(keyIDs, keys, signatures)
	out := make([]cryptoOneTimeKeyPayload, 0, count)
	for i := 0; i < count; i++ {
		out = append(out, cryptoOneTimeKeyPayload{KeyID: valueAt(keyIDs, i), Key: valueAt(keys, i), Signature: valueAt(signatures, i)})
	}
	return out
}

func parseCryptoFormDevices(values map[string][]string) []cryptoDevicePayload {
	accountIDs := values["device[][account_id]"]
	deviceIDs := values["device[][device_id]"]
	types := values["device[][type]"]
	bodies := values["device[][body]"]
	hmacs := values["device[][hmac]"]
	if maxLen(accountIDs, deviceIDs, types, bodies, hmacs) == 0 {
		accountIDs = values["device[account_id]"]
		deviceIDs = values["device[device_id]"]
		types = values["device[type]"]
		bodies = values["device[body]"]
		hmacs = values["device[hmac]"]
	}
	count := maxLen(accountIDs, deviceIDs, types, bodies, hmacs)
	out := make([]cryptoDevicePayload, 0, count)
	for i := 0; i < count; i++ {
		messageType, _ := strconv.Atoi(valueAt(types, i))
		out = append(out, cryptoDevicePayload{
			AccountID: valueAt(accountIDs, i),
			DeviceID:  valueAt(deviceIDs, i),
			Type:      messageType,
			Body:      valueAt(bodies, i),
			HMAC:      valueAt(hmacs, i),
		})
	}
	return out
}

func requestIsJSON(c *echo.Context) bool {
	return strings.Contains(strings.ToLower(c.Request().Header.Get("Content-Type")), "application/json")
}

type cryptoMessageFrankingPayload struct {
	HMAC             string  `json:"hmac"`
	SourceAccountID  int64   `json:"source_account_id"`
	TargetAccountID  int64   `json:"target_account_id"`
	Timestamp        string  `json:"timestamp"`
	OriginalFranking *string `json:"original_franking"`
}

func (s *Server) cryptoMessageFranking(sourceID int64, targetID int64, hmac string, at time.Time) (string, error) {
	return s.cryptoMessageFrankingWithOriginal(sourceID, targetID, hmac, "", at)
}

func (s *Server) cryptoMessageFrankingWithOriginal(sourceID int64, targetID int64, hmac string, originalFranking string, at time.Time) (string, error) {
	key, err := s.currentSystemKey(at)
	if err != nil {
		return "", err
	}
	var original *string
	if strings.TrimSpace(originalFranking) != "" {
		original = &originalFranking
	}
	return cryptoMessageFrankingToken(key, sourceID, targetID, hmac, original, at, rand.Reader)
}

func (s *Server) currentSystemKey(now time.Time) ([]byte, error) {
	var systemKey models.SystemKey
	err := s.db.Order("id DESC").First(&systemKey).Error
	if err == nil && len(systemKey.Key) == 32 && !systemKey.CreatedAt.Before(now.UTC().Add(-7*24*time.Hour)) {
		return systemKey.Key, nil
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, err
	}
	systemKey = models.SystemKey{Key: key, CreatedAt: now.UTC(), UpdatedAt: now.UTC()}
	if err := s.db.Create(&systemKey).Error; err != nil {
		return nil, err
	}
	return key, nil
}

func cryptoMessageFrankingToken(key []byte, sourceID int64, targetID int64, hmacValue string, originalFranking *string, at time.Time, random io.Reader) (string, error) {
	payload := cryptoMessageFrankingPayload{
		HMAC:             hmacValue,
		SourceAccountID:  sourceID,
		TargetAccountID:  targetID,
		Timestamp:        at.UTC().Format(time.RFC3339Nano),
		OriginalFranking: originalFranking,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return railsEncryptAndSignAES256GCM(key, encoded, random)
}

func railsEncryptAndSignAES256GCM(key []byte, plaintext []byte, random io.Reader) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	iv := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(random, iv); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nil, iv, plaintext, []byte(""))
	authTagStart := len(sealed) - 16
	parts := []string{
		base64.StdEncoding.EncodeToString(sealed[:authTagStart]),
		base64.StdEncoding.EncodeToString(iv),
		base64.StdEncoding.EncodeToString(sealed[authTagStart:]),
	}
	return strings.Join(parts, "--"), nil
}

func maxLen(values ...[]string) int {
	max := 0
	for _, value := range values {
		if len(value) > max {
			max = len(value)
		}
	}
	return max
}

func valueAt(values []string, index int) string {
	if index < 0 || index >= len(values) {
		return ""
	}
	return values[index]
}

func limitParam(c *echo.Context, def int, max int) int {
	values, ok := c.Request().URL.Query()["limit"]
	if ok {
		raw := ""
		if len(values) > 0 {
			raw = values[0]
		}
		value := rubyToI(raw)
		if value < 0 {
			value = -value
		}
		if value > max {
			return max
		}
		return value
	}
	if def <= 0 {
		return max
	}
	if def > max {
		return max
	}
	return def
}
