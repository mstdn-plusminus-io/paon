package otp

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"strings"

	"golang.org/x/crypto/pbkdf2"
)

const (
	activeRecordKeyIterations = 1 << 16
	activeRecordKeyLength     = 32
	activeRecordIVLength      = 12
	activeRecordAuthTagLength = 16
	MinimumCredentialLength   = 32
)

var (
	ErrInvalidCredentials = errors.New("invalid Active Record encryption credentials")
	ErrInvalidCiphertext  = errors.New("invalid Active Record encrypted value")
)

type Credentials struct {
	PrimaryKey        string
	DeterministicKey  string
	KeyDerivationSalt string
}

func (credentials Credentials) Validate() error {
	for _, item := range []struct {
		name  string
		value string
	}{
		{name: "ACTIVE_RECORD_ENCRYPTION_PRIMARY_KEY", value: credentials.PrimaryKey},
		{name: "ACTIVE_RECORD_ENCRYPTION_DETERMINISTIC_KEY", value: credentials.DeterministicKey},
		{name: "ACTIVE_RECORD_ENCRYPTION_KEY_DERIVATION_SALT", value: credentials.KeyDerivationSalt},
	} {
		if len(item.value) < MinimumCredentialLength {
			return fmt.Errorf("%w: %s must contain at least %d bytes", ErrInvalidCredentials, item.name, MinimumCredentialLength)
		}
	}
	return nil
}

type activeRecordEnvelope struct {
	Payload string                      `json:"p"`
	Headers activeRecordEnvelopeHeaders `json:"h"`
}

type activeRecordEnvelopeHeaders struct {
	IV      string `json:"iv"`
	AuthTag string `json:"at"`
}

// EncryptActiveRecord encrypts a non-deterministic Rails 7.1 Active Record
// attribute. The JSON/Base64 envelope and AES-256-GCM layout match Rails 7.1.
func EncryptActiveRecord(clearText string, credentials Credentials) (string, error) {
	return encryptActiveRecord(clearText, credentials, rand.Reader)
}

func encryptActiveRecord(clearText string, credentials Credentials, random io.Reader) (string, error) {
	if err := credentials.Validate(); err != nil {
		return "", err
	}
	key := deriveActiveRecordKey(credentials.PrimaryKey, credentials.KeyDerivationSalt, sha256.New)
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("create Active Record cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("create Active Record GCM: %w", err)
	}
	iv := make([]byte, activeRecordIVLength)
	if _, err := io.ReadFull(random, iv); err != nil {
		return "", fmt.Errorf("generate Active Record IV: %w", err)
	}
	sealed := gcm.Seal(nil, iv, []byte(clearText), []byte(""))
	cipherText := sealed[:len(sealed)-activeRecordAuthTagLength]
	authTag := sealed[len(sealed)-activeRecordAuthTagLength:]
	envelope := activeRecordEnvelope{
		Payload: base64.StdEncoding.EncodeToString(cipherText),
		Headers: activeRecordEnvelopeHeaders{
			IV:      base64.StdEncoding.EncodeToString(iv),
			AuthTag: base64.StdEncoding.EncodeToString(authTag),
		},
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return "", fmt.Errorf("serialize Active Record encrypted value: %w", err)
	}
	return string(encoded), nil
}

// DecryptActiveRecord accepts Rails 7.1 SHA-256 derived ciphertext and the
// SHA-1 non-deterministic compatibility format enabled by Mastodon 4.3.
func DecryptActiveRecord(encryptedValue string, credentials Credentials) (string, error) {
	if err := credentials.Validate(); err != nil {
		return "", err
	}
	envelope, err := parseActiveRecordEnvelope(encryptedValue)
	if err != nil {
		return "", err
	}
	var lastErr error
	for _, digest := range []func() hash.Hash{sha256.New, sha1.New} {
		plainText, err := decryptActiveRecordEnvelope(envelope, credentials, digest)
		if err == nil {
			return plainText, nil
		}
		lastErr = err
	}
	return "", fmt.Errorf("%w: authentication failed: %v", ErrInvalidCiphertext, lastErr)
}

func parseActiveRecordEnvelope(value string) (activeRecordEnvelope, error) {
	var envelope activeRecordEnvelope
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return envelope, fmt.Errorf("%w: malformed envelope", ErrInvalidCiphertext)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return envelope, fmt.Errorf("%w: trailing envelope data", ErrInvalidCiphertext)
	}
	if strings.TrimSpace(value) == "" || envelope.Payload == "" || envelope.Headers.IV == "" || envelope.Headers.AuthTag == "" {
		return envelope, fmt.Errorf("%w: incomplete envelope", ErrInvalidCiphertext)
	}
	return envelope, nil
}

func decryptActiveRecordEnvelope(envelope activeRecordEnvelope, credentials Credentials, digest func() hash.Hash) (string, error) {
	decode := base64.StdEncoding.Strict().DecodeString
	cipherText, err := decode(envelope.Payload)
	if err != nil {
		return "", err
	}
	iv, err := decode(envelope.Headers.IV)
	if err != nil || len(iv) != activeRecordIVLength {
		return "", ErrInvalidCiphertext
	}
	authTag, err := decode(envelope.Headers.AuthTag)
	if err != nil || len(authTag) != activeRecordAuthTagLength {
		return "", ErrInvalidCiphertext
	}
	key := deriveActiveRecordKey(credentials.PrimaryKey, credentials.KeyDerivationSalt, digest)
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	sealed := make([]byte, 0, len(cipherText)+len(authTag))
	sealed = append(sealed, cipherText...)
	sealed = append(sealed, authTag...)
	plainText, err := gcm.Open(nil, iv, sealed, []byte(""))
	if err != nil {
		return "", err
	}
	return string(plainText), nil
}

func deriveActiveRecordKey(password string, salt string, digest func() hash.Hash) []byte {
	return pbkdf2.Key([]byte(password), []byte(salt), activeRecordKeyIterations, activeRecordKeyLength, digest)
}
