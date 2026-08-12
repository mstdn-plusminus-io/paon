package otp

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

var activeRecordTestCredentials = Credentials{
	PrimaryKey:        "primary-key-0123456789abcdef0123456789abcdef",
	DeterministicKey:  "deterministic-key-0123456789abcdef0123456789abcdef",
	KeyDerivationSalt: "derivation-salt-0123456789abcdef0123456789abcdef",
}

// This ciphertext was produced by ActiveRecord::Encryption::Encryptor using
// the credentials above. Rails 7.1 (Mastodon 4.3) and Rails 8.1 share this
// non-deterministic message envelope.
func TestDecryptActiveRecordRailsFixture(t *testing.T) {
	const fixture = `{"p":"n9LL2jaC6oVT+L7UsIXEHw==","h":{"iv":"ydwTxnvuskZ6ZFd/","at":"xb45Aef5O8cCFrDfpI3D5w=="}}`
	got, err := DecryptActiveRecord(fixture, activeRecordTestCredentials)
	if err != nil {
		t.Fatal(err)
	}
	if got != "JBSWY3DPEHPK3PXP" {
		t.Fatalf("plaintext = %q", got)
	}
}

func TestDecryptActiveRecordAcceptsMastodonSHA1FallbackFixture(t *testing.T) {
	const fixture = `{"p":"WcKDRQd/MJ9XdnlkqXA2Iw==","h":{"iv":"07D8klREBXAIXPiX","at":"1Mvy+nSHCxZ7EKu1aZ2RYQ=="}}`
	got, err := DecryptActiveRecord(fixture, activeRecordTestCredentials)
	if err != nil {
		t.Fatal(err)
	}
	if got != "JBSWY3DPEHPK3PXP" {
		t.Fatalf("plaintext = %q", got)
	}
}

func TestEncryptActiveRecordProducesRailsEnvelope(t *testing.T) {
	value, err := encryptActiveRecord("JBSWY3DPEHPK3PXP", activeRecordTestCredentials, bytes.NewReader([]byte("123456789012")))
	if err != nil {
		t.Fatal(err)
	}
	var envelope map[string]any
	if err := json.Unmarshal([]byte(value), &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope) != 2 || envelope["p"] == nil || envelope["h"] == nil {
		t.Fatalf("unexpected Rails envelope: %s", value)
	}
	got, err := DecryptActiveRecord(value, activeRecordTestCredentials)
	if err != nil {
		t.Fatal(err)
	}
	if got != "JBSWY3DPEHPK3PXP" {
		t.Fatalf("round-trip plaintext = %q", got)
	}
}

func TestDecryptActiveRecordRejectsWrongKeyTamperingAndTrailingJSON(t *testing.T) {
	value, err := encryptActiveRecord("JBSWY3DPEHPK3PXP", activeRecordTestCredentials, bytes.NewReader([]byte("123456789012")))
	if err != nil {
		t.Fatal(err)
	}
	wrong := activeRecordTestCredentials
	wrong.PrimaryKey = strings.Repeat("x", MinimumCredentialLength)
	if _, err := DecryptActiveRecord(value, wrong); !errors.Is(err, ErrInvalidCiphertext) {
		t.Fatalf("wrong-key error = %v", err)
	}
	tampered := strings.Replace(value, `"p":"`, `"p":"A`, 1)
	if _, err := DecryptActiveRecord(tampered, activeRecordTestCredentials); !errors.Is(err, ErrInvalidCiphertext) {
		t.Fatalf("tamper error = %v", err)
	}
	if _, err := DecryptActiveRecord(value+` {}`, activeRecordTestCredentials); !errors.Is(err, ErrInvalidCiphertext) {
		t.Fatalf("trailing-data error = %v", err)
	}
}

func TestCredentialsValidationIsSecretSafe(t *testing.T) {
	credentials := activeRecordTestCredentials
	credentials.PrimaryKey = "very-secret-but-short"
	err := credentials.Validate()
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("error = %v", err)
	}
	if strings.Contains(err.Error(), credentials.PrimaryKey) {
		t.Fatalf("error leaked credential: %v", err)
	}
}
