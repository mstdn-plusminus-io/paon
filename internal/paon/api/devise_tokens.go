package api

import (
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"

	"golang.org/x/crypto/pbkdf2"
)

const (
	deviseResetPasswordTokenColumn = "reset_password_token"
	deviseConfirmationTokenColumn  = "confirmation_token"
)

func deviseTokenForStorage(raw string, column string, secretKeyBase string) string {
	digested, ok := deviseTokenDigest(raw, column, secretKeyBase)
	if ok {
		return digested
	}
	return raw
}

func deviseTokenLookupValues(raw string, column string, secretKeyBase string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	values := []string{raw}
	for _, candidate := range deviseTokenDigestCandidates(raw, column, secretKeyBase) {
		if !slices.Contains(values, candidate) {
			values = append(values, candidate)
		}
	}
	return values
}

func deviseTokenDigest(raw string, column string, secretKeyBase string) (string, bool) {
	candidates := deviseTokenDigestCandidates(raw, column, secretKeyBase)
	if len(candidates) == 0 {
		return "", false
	}
	return candidates[0], true
}

func deviseTokenDigestCandidates(raw string, column string, secretKeyBase string) []string {
	raw = strings.TrimSpace(raw)
	column = strings.TrimSpace(column)
	secretKeyBase = strings.TrimSpace(secretKeyBase)
	if raw == "" || column == "" || secretKeyBase == "" {
		return nil
	}
	salt := []byte("Devise " + column)
	secret := []byte(secretKeyBase)
	keys := [][]byte{
		pbkdf2.Key(secret, salt, 1000, 64, sha1.New),
		pbkdf2.Key(secret, salt, 1000, 64, sha256.New),
	}
	values := make([]string, 0, len(keys))
	for _, key := range keys {
		mac := hmac.New(sha256.New, key)
		_, _ = mac.Write([]byte(raw))
		values = append(values, hex.EncodeToString(mac.Sum(nil)))
	}
	return values
}
