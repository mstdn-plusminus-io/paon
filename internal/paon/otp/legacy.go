package otp

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha1"
	"encoding/base64"
	"errors"
	"strings"

	"golang.org/x/crypto/pbkdf2"
)

const LegacyPaonPrefix = "paon-go-totp:"

func ParseLegacyPaon(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, LegacyPaonPrefix) {
		return "", false
	}
	secret := strings.TrimSpace(strings.TrimPrefix(value, LegacyPaonPrefix))
	return secret, secret != ""
}

func DecryptLegacyMastodon(encryptedValue string, encodedIV string, encodedSalt string, otpSecretKey string) (string, error) {
	if strings.TrimSpace(encryptedValue) == "" || strings.TrimSpace(encodedIV) == "" || strings.TrimSpace(encodedSalt) == "" || otpSecretKey == "" {
		return "", errors.New("missing legacy otp secret material")
	}
	ciphertextWithTag, err := decodeLegacyBase64(encryptedValue)
	if err != nil {
		return "", err
	}
	iv, err := decodeLegacyBase64(encodedIV)
	if err != nil {
		return "", err
	}
	salt, err := decodeLegacySalt(encodedSalt)
	if err != nil {
		return "", err
	}
	if len(ciphertextWithTag) <= 16 {
		return "", errors.New("legacy otp ciphertext is too short")
	}
	key := pbkdf2.Key([]byte(otpSecretKey), salt, 2000, 32, sha1.New)
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	plaintext, err := gcm.Open(nil, iv, ciphertextWithTag, []byte(""))
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

func decodeLegacySalt(value string) ([]byte, error) {
	return decodeLegacyBase64(value)
}

func decodeLegacyBase64(value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, errors.New("empty attr_encrypted value")
	}
	// Ruby's Base64.decode64, used by Mastodon 4.2, ignores characters outside
	// the standard alphabet (legacy salts commonly start with '_' or '$').
	var normalized strings.Builder
	for _, character := range value {
		if character >= 'A' && character <= 'Z' || character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || strings.ContainsRune("+/=", character) {
			normalized.WriteRune(character)
		}
	}
	if normalized.Len() == 0 {
		return nil, errors.New("empty attr_encrypted base64 value")
	}
	return base64.StdEncoding.DecodeString(normalized.String())
}
