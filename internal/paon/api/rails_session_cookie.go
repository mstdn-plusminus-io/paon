package api

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"golang.org/x/crypto/pbkdf2"
	"gorm.io/gorm"
)

const (
	railsSessionIDCookieName           = "_session_id"
	railsSignedCookieSalt              = "signed cookie"
	railsAuthenticatedEncryptedSalt    = "authenticated encrypted cookie"
	railsSessionCookiePurpose          = "cookie._session_id"
	railsEncryptedSessionCookiePurpose = "cookie._mastodon_session"
)

type railsSignedCookieEnvelope struct {
	Rails struct {
		Message string `json:"message"`
		Data    string `json:"data"`
		Expiry  string `json:"exp"`
		Purpose string `json:"pur"`
	} `json:"_rails"`
}

func (s *Server) currentUserByRailsSession(c *echo.Context, includeDisabled bool) (*models.User, string, error) {
	sessionID, ok := s.railsSessionIDFromCookie(c)
	if !ok {
		sessionID, ok = s.railsSessionIDFromEncryptedSession(c)
	}
	if !ok || s.db == nil {
		return nil, "", gorm.ErrRecordNotFound
	}
	var activation models.SessionActivation
	if err := s.db.Where("session_id = ?", sessionID).First(&activation).Error; err != nil {
		return nil, "", err
	}
	query := s.db.Where("id = ?", activation.UserID)
	if !includeDisabled {
		query = query.Where("disabled = false")
	}
	var user models.User
	if err := query.First(&user).Error; err != nil {
		return nil, "", err
	}
	tokenValue, err := s.accessTokenForRailsSessionActivation(&user, &activation, c)
	if err != nil {
		return nil, "", err
	}
	_ = s.touchSessionActivationIfNeeded(&activation)
	return &user, tokenValue, nil
}

const sessionActivityUpdateFrequency = 24 * time.Hour

func (s *Server) touchSessionActivationIfNeeded(activation *models.SessionActivation) error {
	now := time.Now().UTC()
	if s == nil || s.db == nil || activation == nil || activation.ID == 0 || !sessionActivationNeedsActivityUpdate(*activation, now) {
		return nil
	}
	return s.db.Model(&models.SessionActivation{}).Where("id = ?", activation.ID).Update("updated_at", now).Error
}

func sessionActivationNeedsActivityUpdate(activation models.SessionActivation, now time.Time) bool {
	return activation.UpdatedAt.Before(now.Add(-sessionActivityUpdateFrequency))
}

func (s *Server) accessTokenForRailsSessionActivation(user *models.User, activation *models.SessionActivation, c *echo.Context) (string, error) {
	if activation.AccessTokenID.Valid {
		var accessToken models.OAuthAccessToken
		err := s.db.Where("id = ? AND revoked_at IS NULL", activation.AccessTokenID.Int64).First(&accessToken).Error
		if err == nil && strings.TrimSpace(accessToken.Token) != "" {
			_ = s.trackAccessTokenUse(c, &accessToken)
			return accessToken.Token, nil
		}
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return "", err
		}
	}
	token, err := s.issueAccessTokenForRailsSessionActivation(user, activation, c)
	if err != nil {
		return "", err
	}
	return token.Token, nil
}

func (s *Server) issueAccessTokenForRailsSessionActivation(user *models.User, activation *models.SessionActivation, c *echo.Context) (*models.OAuthAccessToken, error) {
	now := time.Now().UTC()
	var token models.OAuthAccessToken
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var superapp oauthApplication
		applicationID := sql.NullInt64{}
		if err := tx.Select("id").Where("superapp = ?", true).First(&superapp).Error; err == nil {
			applicationID = sql.NullInt64{Int64: superapp.ID, Valid: true}
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		token = models.OAuthAccessToken{
			Token:           randomHex(32),
			CreatedAt:       now,
			Scopes:          "read write follow",
			ApplicationID:   applicationID,
			ResourceOwnerID: sql.NullInt64{Int64: user.ID, Valid: true},
			LastUsedAt:      sql.NullTime{Time: now, Valid: true},
		}
		if err := tx.Create(&token).Error; err != nil {
			return err
		}
		if err := tx.Model(&models.SessionActivation{}).Where("id = ?", activation.ID).Update("access_token_id", token.ID).Error; err != nil {
			return err
		}
		trimmedIP := strings.TrimSpace(c.RealIP())
		if trimmedIP != "" {
			if err := tx.Model(&models.OAuthAccessToken{}).Where("id = ?", token.ID).Update("last_used_ip", trimmedIP).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	activation.AccessTokenID = sql.NullInt64{Int64: token.ID, Valid: true}
	return &token, nil
}

func (s *Server) railsSessionIDFromCookie(c *echo.Context) (string, bool) {
	if strings.TrimSpace(s.cfg.SecretKeyBase) == "" {
		return "", false
	}
	cookie, err := c.Cookie(railsSessionIDCookieName)
	if err != nil || strings.TrimSpace(cookie.Value) == "" {
		return "", false
	}
	return verifyRailsSignedCookie(cookie.Value, s.cfg.SecretKeyBase, railsSessionCookiePurpose, time.Now().UTC())
}

func (s *Server) railsSessionIDFromEncryptedSession(c *echo.Context) (string, bool) {
	if strings.TrimSpace(s.cfg.SecretKeyBase) == "" {
		return "", false
	}
	cookie, err := c.Cookie(railsSessionCookieName)
	if err != nil || strings.TrimSpace(cookie.Value) == "" {
		return "", false
	}
	return decryptRailsEncryptedSessionCookie(cookie.Value, s.cfg.SecretKeyBase, time.Now().UTC())
}

func verifyRailsSignedCookie(raw string, secretKeyBase string, purpose string, now time.Time) (string, bool) {
	raw = railsCookieUnescape(raw)
	encoded, signature, ok := strings.Cut(raw, "--")
	if !ok || encoded == "" || signature == "" {
		return "", false
	}
	for _, key := range railsSignedCookieKeys(secretKeyBase) {
		if railsSignedCookieSignatureMatches(encoded, signature, key) {
			return railsSignedCookiePayload(encoded, purpose, now)
		}
	}
	return "", false
}

func decryptRailsEncryptedSessionCookie(raw string, secretKeyBase string, now time.Time) (string, bool) {
	raw = railsCookieUnescape(raw)
	encryptedData, iv, authTag, ok := railsEncryptedCookieParts(raw)
	if !ok {
		return "", false
	}
	for _, key := range railsAuthenticatedEncryptedCookieKeys(secretKeyBase) {
		block, err := aes.NewCipher(key)
		if err != nil {
			continue
		}
		aead, err := cipher.NewGCM(block)
		if err != nil {
			continue
		}
		plaintext, err := aead.Open(nil, iv, append(encryptedData, authTag...), []byte(""))
		if err != nil {
			continue
		}
		sessionJSON, ok := railsMetadataPayload(plaintext, railsEncryptedSessionCookiePurpose, now)
		if !ok {
			continue
		}
		return railsSessionIDFromSessionJSON(sessionJSON)
	}
	return "", false
}

func railsCookieUnescape(raw string) string {
	raw = strings.TrimSpace(raw)
	if strings.Contains(raw, "%") {
		if decoded, err := url.PathUnescape(raw); err == nil {
			return decoded
		}
	}
	return raw
}

func railsEncryptedCookieParts(raw string) ([]byte, []byte, []byte, bool) {
	parts := strings.Split(raw, "--")
	if len(parts) != 3 {
		return nil, nil, nil, false
	}
	encryptedData, ok := railsBase64Decode(parts[0])
	if !ok {
		return nil, nil, nil, false
	}
	iv, ok := railsBase64Decode(parts[1])
	if !ok || len(iv) == 0 {
		return nil, nil, nil, false
	}
	authTag, ok := railsBase64Decode(parts[2])
	if !ok || len(authTag) != 16 {
		return nil, nil, nil, false
	}
	return encryptedData, iv, authTag, true
}

func railsAuthenticatedEncryptedCookieKeys(secretKeyBase string) [][]byte {
	secret := []byte(secretKeyBase)
	salt := []byte(railsAuthenticatedEncryptedSalt)
	return [][]byte{
		pbkdf2.Key(secret, salt, 1000, 32, sha1.New),
		pbkdf2.Key(secret, salt, 1000, 32, sha256.New),
	}
}

func railsSignedCookieKeys(secretKeyBase string) [][]byte {
	secret := []byte(secretKeyBase)
	salt := []byte(railsSignedCookieSalt)
	return [][]byte{
		pbkdf2.Key(secret, salt, 1000, 64, sha1.New),
		pbkdf2.Key(secret, salt, 1000, 64, sha256.New),
	}
}

func railsSignedCookieSignatureMatches(encoded string, signature string, key []byte) bool {
	expectedMAC := hmac.New(sha1.New, key)
	_, _ = expectedMAC.Write([]byte(encoded))
	expected := []byte(hex.EncodeToString(expectedMAC.Sum(nil)))
	return hmac.Equal([]byte(strings.ToLower(signature)), expected)
}

func railsSignedCookiePayload(encoded string, purpose string, now time.Time) (string, bool) {
	decoded, ok := railsBase64Decode(encoded)
	if !ok {
		return "", false
	}
	return railsMetadataPayload(decoded, purpose, now)
}

func railsMetadataPayload(decoded []byte, purpose string, now time.Time) (string, bool) {
	var envelope railsSignedCookieEnvelope
	if json.Unmarshal(decoded, &envelope) == nil && (envelope.Rails.Message != "" || envelope.Rails.Data != "") {
		if envelope.Rails.Purpose != "" && envelope.Rails.Purpose != purpose {
			return "", false
		}
		if envelope.Rails.Expiry != "" {
			expiry, err := time.Parse(time.RFC3339Nano, envelope.Rails.Expiry)
			if err != nil || !now.Before(expiry) {
				return "", false
			}
		}
		payload := envelope.Rails.Data
		if envelope.Rails.Message != "" {
			message, ok := railsBase64Decode(envelope.Rails.Message)
			if !ok {
				return "", false
			}
			payload = string(message)
		}
		return railsCookieJSONValue(payload)
	}
	return railsCookieJSONValue(string(decoded))
}

func railsSessionIDFromSessionJSON(payload string) (string, bool) {
	var session map[string]any
	if json.Unmarshal([]byte(payload), &session) != nil {
		return "", false
	}
	if value, ok := session["auth_id"].(string); ok && strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value), true
	}
	return "", false
}

func railsCookieJSONValue(payload string) (string, bool) {
	var value string
	if json.Unmarshal([]byte(payload), &value) == nil {
		value = strings.TrimSpace(value)
		return value, value != ""
	}
	payload = strings.TrimSpace(payload)
	return payload, payload != ""
}

func railsBase64Decode(value string) ([]byte, bool) {
	for _, encoding := range []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	} {
		if decoded, err := encoding.DecodeString(value); err == nil {
			return decoded, true
		}
	}
	return nil, false
}
