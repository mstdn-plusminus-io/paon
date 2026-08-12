package api

import (
	"crypto/hmac"
	"crypto/sha1" // Rails 7.1 MessageVerifier compatibility.
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"golang.org/x/crypto/pbkdf2"
)

const selfDestructVerifierPurpose = "self-destruct"

// GenerateSelfDestructToken produces the same signed JSON string shape as
// Rails.application.message_verifier("self-destruct").generate(domain) in
// Mastodon 4.3. The purpose is part of key derivation, so a token generated for
// any other purpose cannot enable this destructive mode.
func GenerateSelfDestructToken(secretKeyBase string, localDomain string) (string, error) {
	return generateSelfDestructTokenForPurpose(secretKeyBase, localDomain, selfDestructVerifierPurpose)
}

func generateSelfDestructTokenForPurpose(secretKeyBase string, localDomain string, purpose string) (string, error) {
	if strings.TrimSpace(secretKeyBase) == "" {
		return "", errors.New("SECRET_KEY_BASE is required to sign SELF_DESTRUCT")
	}
	if strings.TrimSpace(localDomain) == "" {
		return "", errors.New("LOCAL_DOMAIN is required to sign SELF_DESTRUCT")
	}
	if strings.TrimSpace(purpose) == "" {
		return "", errors.New("SELF_DESTRUCT signing purpose is required")
	}
	message, err := json.Marshal(localDomain)
	if err != nil {
		return "", err
	}
	encoded := base64.StdEncoding.EncodeToString(message)
	// Rails load_defaults 7.1 inherits the 7.0 KeyGenerator default (SHA-256),
	// while MessageVerifier still signs its encoded message with HMAC-SHA1.
	key := pbkdf2.Key([]byte(secretKeyBase), []byte(purpose), 1000, 64, sha256.New)
	mac := hmac.New(sha1.New, key)
	_, _ = mac.Write([]byte(encoded))
	return encoded + "--" + hex.EncodeToString(mac.Sum(nil)), nil
}

// VerifySelfDestructToken verifies both the signature purpose and the exact
// local domain. It deliberately rejects an unsigned domain name.
func VerifySelfDestructToken(token string, secretKeyBase string, localDomain string) bool {
	if strings.TrimSpace(token) == "" || strings.TrimSpace(secretKeyBase) == "" || strings.TrimSpace(localDomain) == "" {
		return false
	}
	separator := strings.LastIndex(token, "--")
	if separator <= 0 {
		return false
	}
	encoded := token[:separator]
	digest, err := hex.DecodeString(token[separator+2:])
	if err != nil || len(digest) != sha1.Size {
		return false
	}
	key := pbkdf2.Key([]byte(secretKeyBase), []byte(selfDestructVerifierPurpose), 1000, 64, sha256.New)
	mac := hmac.New(sha1.New, key)
	_, _ = mac.Write([]byte(encoded))
	if !hmac.Equal(digest, mac.Sum(nil)) {
		return false
	}
	message, err := decodeSelfDestructMessage(encoded)
	if err != nil {
		return false
	}
	var signedDomain string
	return json.Unmarshal(message, &signedDomain) == nil && signedDomain == localDomain
}

func decodeSelfDestructMessage(encoded string) ([]byte, error) {
	for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if decoded, err := encoding.DecodeString(encoded); err == nil {
			return decoded, nil
		}
	}
	return nil, errors.New("invalid SELF_DESTRUCT message encoding")
}

func (s *Server) selfDestructEnabled() bool {
	return s != nil && VerifySelfDestructToken(s.cfg.SelfDestruct, s.cfg.SecretKeyBase, s.cfg.LocalDomain)
}

func selfDestructDomain(s *Server) string {
	if s == nil || !s.selfDestructEnabled() {
		return ""
	}
	return strings.TrimSpace(s.cfg.LocalDomain)
}
