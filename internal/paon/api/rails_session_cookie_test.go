package api

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"hash"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"golang.org/x/crypto/pbkdf2"
)

func TestVerifyRailsSignedCookieReadsSessionIDMetadataEnvelope(t *testing.T) {
	now := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)
	cookie := railsSignedCookieFixture(t, "session-123", "secret-base", railsSessionCookiePurpose, now.Add(time.Hour), sha1.New)

	got, ok := verifyRailsSignedCookie(cookie, "secret-base", railsSessionCookiePurpose, now)
	if !ok || got != "session-123" {
		t.Fatalf("verified session = %q ok=%v", got, ok)
	}
}

func TestVerifyRailsSignedCookieRejectsWrongPurposeExpiryAndSignature(t *testing.T) {
	now := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)
	cookie := railsSignedCookieFixture(t, "session-123", "secret-base", railsSessionCookiePurpose, now.Add(time.Hour), sha1.New)
	if _, ok := verifyRailsSignedCookie(cookie, "secret-base", "cookie.other", now); ok {
		t.Fatal("wrong purpose was accepted")
	}
	expired := railsSignedCookieFixture(t, "session-123", "secret-base", railsSessionCookiePurpose, now.Add(-time.Hour), sha1.New)
	if _, ok := verifyRailsSignedCookie(expired, "secret-base", railsSessionCookiePurpose, now); ok {
		t.Fatal("expired cookie was accepted")
	}
	if _, ok := verifyRailsSignedCookie(cookie+"bad", "secret-base", railsSessionCookiePurpose, now); ok {
		t.Fatal("tampered cookie was accepted")
	}
}

func TestVerifyRailsSignedCookieAcceptsRotatedSHA256DerivedSecret(t *testing.T) {
	now := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)
	cookie := railsSignedCookieFixture(t, "session-rotated", "secret-base", railsSessionCookiePurpose, now.Add(time.Hour), sha256.New)

	got, ok := verifyRailsSignedCookie(cookie, "secret-base", railsSessionCookiePurpose, now)
	if !ok || got != "session-rotated" {
		t.Fatalf("verified rotated session = %q ok=%v", got, ok)
	}
}

func TestRailsSessionIDFromCookieRequiresSecretKeyBase(t *testing.T) {
	now := time.Now().UTC()
	cookie := railsSignedCookieFixture(t, "session-123", "secret-base", railsSessionCookiePurpose, now.Add(time.Hour), sha1.New)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: railsSessionIDCookieName, Value: cookie})
	c := echo.NewContext(req, httptest.NewRecorder(), echo.New())

	if got, ok := (&Server{}).railsSessionIDFromCookie(c); ok || got != "" {
		t.Fatalf("missing secret key accepted session = %q ok=%v", got, ok)
	}
	got, ok := (&Server{cfg: config.Config{SecretKeyBase: "secret-base"}}).railsSessionIDFromCookie(c)
	if !ok || got != "session-123" {
		t.Fatalf("session = %q ok=%v", got, ok)
	}
}

func TestDecryptRailsEncryptedSessionCookieReadsAuthID(t *testing.T) {
	now := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)
	cookie := railsEncryptedSessionCookieFixture(t, "encrypted-session-123", "secret-base", now.Add(time.Hour), sha1.New)

	got, ok := decryptRailsEncryptedSessionCookie(cookie, "secret-base", now)
	if !ok || got != "encrypted-session-123" {
		t.Fatalf("encrypted session = %q ok=%v", got, ok)
	}
}

func TestDecryptRailsEncryptedSessionCookieRejectsWrongPurposeExpiryAndTampering(t *testing.T) {
	now := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)
	wrongPurpose := railsEncryptedSessionCookieFixtureWithPurpose(t, "encrypted-session-123", "secret-base", "cookie.other", now.Add(time.Hour), sha1.New)
	if _, ok := decryptRailsEncryptedSessionCookie(wrongPurpose, "secret-base", now); ok {
		t.Fatal("wrong purpose encrypted session was accepted")
	}
	expired := railsEncryptedSessionCookieFixture(t, "encrypted-session-123", "secret-base", now.Add(-time.Hour), sha1.New)
	if _, ok := decryptRailsEncryptedSessionCookie(expired, "secret-base", now); ok {
		t.Fatal("expired encrypted session was accepted")
	}
	valid := railsEncryptedSessionCookieFixture(t, "encrypted-session-123", "secret-base", now.Add(time.Hour), sha1.New)
	if _, ok := decryptRailsEncryptedSessionCookie(valid+"bad", "secret-base", now); ok {
		t.Fatal("tampered encrypted session was accepted")
	}
}

func TestDecryptRailsEncryptedSessionCookieAcceptsRotatedSHA256DerivedSecret(t *testing.T) {
	now := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)
	cookie := railsEncryptedSessionCookieFixture(t, "encrypted-session-rotated", "secret-base", now.Add(time.Hour), sha256.New)

	got, ok := decryptRailsEncryptedSessionCookie(cookie, "secret-base", now)
	if !ok || got != "encrypted-session-rotated" {
		t.Fatalf("rotated encrypted session = %q ok=%v", got, ok)
	}
}

func TestRailsSessionIDFromEncryptedSessionRequiresSecretKeyBase(t *testing.T) {
	now := time.Now().UTC()
	cookie := railsEncryptedSessionCookieFixture(t, "encrypted-session-123", "secret-base", now.Add(time.Hour), sha1.New)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: railsSessionCookieName, Value: cookie})
	c := echo.NewContext(req, httptest.NewRecorder(), echo.New())

	if got, ok := (&Server{}).railsSessionIDFromEncryptedSession(c); ok || got != "" {
		t.Fatalf("missing secret key accepted encrypted session = %q ok=%v", got, ok)
	}
	got, ok := (&Server{cfg: config.Config{SecretKeyBase: "secret-base"}}).railsSessionIDFromEncryptedSession(c)
	if !ok || got != "encrypted-session-123" {
		t.Fatalf("encrypted session = %q ok=%v", got, ok)
	}
}

func TestRailsSessionActivationEnsuresUsableAccessToken(t *testing.T) {
	src, err := os.ReadFile("rails_session_cookie.go")
	if err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		functionName string
		want         string
	}{
		{"currentUserByRailsSession", `s.accessTokenForRailsSessionActivation(&user, &activation, c)`},
		{"accessTokenForRailsSessionActivation", `Where("id = ? AND revoked_at IS NULL", activation.AccessTokenID.Int64)`},
		{"accessTokenForRailsSessionActivation", `strings.TrimSpace(accessToken.Token) != ""`},
		{"accessTokenForRailsSessionActivation", `s.issueAccessTokenForRailsSessionActivation(user, activation, c)`},
		{"issueAccessTokenForRailsSessionActivation", `s.db.Transaction(func(tx *gorm.DB) error`},
		{"issueAccessTokenForRailsSessionActivation", `Where("superapp = ?", true)`},
		{"issueAccessTokenForRailsSessionActivation", `Scopes:          "read write follow"`},
		{"issueAccessTokenForRailsSessionActivation", `ApplicationID:   applicationID`},
		{"issueAccessTokenForRailsSessionActivation", `tx.Create(&token)`},
		{"issueAccessTokenForRailsSessionActivation", `Update("access_token_id", token.ID)`},
	}
	for _, check := range checks {
		if !functionBodyContains(t, src, check.functionName, check.want) {
			t.Fatalf("%s missing %q", check.functionName, check.want)
		}
	}
	if strings.Contains(string(src), "return &user, \"\", nil") {
		t.Fatal("Rails session fallback can still return an authenticated user without an API token")
	}
}

func railsSignedCookieFixture(t *testing.T, sessionID string, secretKeyBase string, purpose string, expiresAt time.Time, keyDigest func() hash.Hash) string {
	t.Helper()
	value, err := json.Marshal(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	message := base64.StdEncoding.EncodeToString(value)
	envelope, err := json.Marshal(map[string]any{"_rails": map[string]any{"message": message, "exp": expiresAt.UTC().Format("2006-01-02T15:04:05.000Z"), "pur": purpose}})
	if err != nil {
		t.Fatal(err)
	}
	encoded := base64.StdEncoding.EncodeToString(envelope)
	key := pbkdf2.Key([]byte(secretKeyBase), []byte(railsSignedCookieSalt), 1000, 64, keyDigest)
	mac := hmac.New(sha1.New, key)
	_, _ = mac.Write([]byte(encoded))
	return encoded + "--" + hex.EncodeToString(mac.Sum(nil))
}

func railsEncryptedSessionCookieFixture(t *testing.T, sessionID string, secretKeyBase string, expiresAt time.Time, keyDigest func() hash.Hash) string {
	t.Helper()
	return railsEncryptedSessionCookieFixtureWithPurpose(t, sessionID, secretKeyBase, railsEncryptedSessionCookiePurpose, expiresAt, keyDigest)
}

func railsEncryptedSessionCookieFixtureWithPurpose(t *testing.T, sessionID string, secretKeyBase string, purpose string, expiresAt time.Time, keyDigest func() hash.Hash) string {
	t.Helper()
	sessionPayload, err := json.Marshal(map[string]any{"auth_id": sessionID, "session_id": "rack-session-id"})
	if err != nil {
		t.Fatal(err)
	}
	message := base64.StdEncoding.EncodeToString(sessionPayload)
	envelope, err := json.Marshal(map[string]any{"_rails": map[string]any{"message": message, "exp": expiresAt.UTC().Format("2006-01-02T15:04:05.000Z"), "pur": purpose}})
	if err != nil {
		t.Fatal(err)
	}
	key := pbkdf2.Key([]byte(secretKeyBase), []byte(railsAuthenticatedEncryptedSalt), 1000, 32, keyDigest)
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	iv := []byte("123456789012")
	sealed := aead.Seal(nil, iv, envelope, nil)
	encryptedData := sealed[:len(sealed)-16]
	authTag := sealed[len(sealed)-16:]
	return base64.StdEncoding.EncodeToString(encryptedData) + "--" + base64.StdEncoding.EncodeToString(iv) + "--" + base64.StdEncoding.EncodeToString(authTag)
}
