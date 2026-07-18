package api

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/pbkdf2"
)

const railsSignedGlobalIDPurposeUnsubscribe = "unsubscribe"

type railsMessageEnvelope struct {
	Rails struct {
		Data    string `json:"data"`
		Message string `json:"message"`
		Purpose string `json:"pur"`
		Expiry  string `json:"exp"`
	} `json:"_rails"`
	GID       string `json:"gid"`
	Purpose   string `json:"purpose"`
	ExpiresAt string `json:"expires_at"`
}

func railsSignedGlobalIDUserID(token string, secretKeyBase string, now func() time.Time) (int64, bool) {
	gid, ok := railsSignedGlobalIDMessage(token, secretKeyBase, railsSignedGlobalIDPurposeUnsubscribe, now)
	if !ok {
		return 0, false
	}
	userID, ok := railsGlobalIDUserID(gid)
	return userID, ok
}

func railsSignedGlobalIDForUser(userID int64, secretKeyBase string) string {
	if userID <= 0 || strings.TrimSpace(secretKeyBase) == "" {
		return ""
	}
	envelope := railsMessageEnvelope{}
	envelope.Rails.Data = "gid://mastodon/User/" + strconv.FormatInt(userID, 10)
	envelope.Rails.Purpose = railsSignedGlobalIDPurposeUnsubscribe
	message, err := json.Marshal(envelope)
	if err != nil {
		return ""
	}
	encoded := base64.RawURLEncoding.EncodeToString(message)
	key := pbkdf2.Key([]byte(secretKeyBase), []byte("signed_global_ids"), 1000, 64, sha1.New)
	mac := hmac.New(sha1.New, key)
	_, _ = mac.Write([]byte(encoded))
	return encoded + "--" + hex.EncodeToString(mac.Sum(nil))
}

func railsSignedGlobalIDMessage(token string, secretKeyBase string, purpose string, now func() time.Time) (string, bool) {
	encoded, ok := railsVerifySignedMessage(token, secretKeyBase)
	if !ok {
		return "", false
	}
	message, err := railsBase64URLDecode(encoded)
	if err != nil {
		return "", false
	}
	return railsMessageData(message, purpose, now)
}

func railsVerifySignedMessage(token string, secretKeyBase string) (string, bool) {
	if strings.TrimSpace(token) == "" || strings.TrimSpace(secretKeyBase) == "" {
		return "", false
	}
	separator := strings.LastIndex(token, "--")
	if separator <= 0 {
		return "", false
	}
	encoded := token[:separator]
	digest := token[separator+2:]
	if len(digest) != sha1.Size*2 {
		return "", false
	}
	decodedDigest, err := hex.DecodeString(digest)
	if err != nil {
		return "", false
	}
	// Rails.application.key_generator wraps ActiveSupport::KeyGenerator with 1000 iterations.
	key := pbkdf2.Key([]byte(secretKeyBase), []byte("signed_global_ids"), 1000, 64, sha1.New)
	mac := hmac.New(sha1.New, key)
	_, _ = mac.Write([]byte(encoded))
	if !hmac.Equal(decodedDigest, mac.Sum(nil)) {
		return "", false
	}
	return encoded, true
}

func railsMessageData(message []byte, purpose string, now func() time.Time) (string, bool) {
	var envelope railsMessageEnvelope
	if err := json.Unmarshal(message, &envelope); err != nil {
		var raw string
		if json.Unmarshal(message, &raw) == nil && purpose == "" {
			return raw, true
		}
		return "", false
	}
	if envelope.Rails.Data != "" {
		if !railsMessagePurposeMatches(envelope.Rails.Purpose, purpose) || railsMessageExpired(envelope.Rails.Expiry, now) {
			return "", false
		}
		return envelope.Rails.Data, true
	}
	if envelope.Rails.Message != "" {
		if !railsMessagePurposeMatches(envelope.Rails.Purpose, purpose) || railsMessageExpired(envelope.Rails.Expiry, now) {
			return "", false
		}
		inner, err := railsBase64URLDecode(envelope.Rails.Message)
		if err != nil {
			return "", false
		}
		var data string
		if err := json.Unmarshal(inner, &data); err != nil {
			return "", false
		}
		return data, true
	}
	if envelope.GID != "" {
		if !railsMessagePurposeMatches(envelope.Purpose, purpose) || railsMessageExpired(envelope.ExpiresAt, now) {
			return "", false
		}
		return envelope.GID, true
	}
	return "", false
}

func railsMessagePurposeMatches(actual string, expected string) bool {
	return actual == expected
}

func railsMessageExpired(value string, now func() time.Time) bool {
	if strings.TrimSpace(value) == "" {
		return false
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return true
	}
	return !now().UTC().Before(expiresAt.UTC())
}

func railsBase64URLDecode(encoded string) ([]byte, error) {
	if mod := len(encoded) % 4; mod != 0 {
		encoded += strings.Repeat("=", 4-mod)
	}
	return base64.URLEncoding.DecodeString(encoded)
}

func railsGlobalIDUserID(gid string) (int64, bool) {
	parsed, err := url.Parse(gid)
	if err != nil || parsed.Scheme != "gid" {
		return 0, false
	}
	segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(segments) != 2 || segments[0] != "User" {
		return 0, false
	}
	userID, err := strconv.ParseInt(segments[1], 10, 64)
	if err != nil || userID <= 0 {
		return 0, false
	}
	return userID, true
}
