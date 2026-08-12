package api

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"golang.org/x/crypto/hkdf"
)

type capturingWebPushClient struct {
	request *http.Request
	body    []byte
}

func (client *capturingWebPushClient) Do(request *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	client.request = request.Clone(request.Context())
	client.request.Header = request.Header.Clone()
	client.body = body
	return &http.Response{StatusCode: http.StatusCreated, Body: http.NoBody}, nil
}

func TestParseWebPushPayloadAcceptsMastodon44StandardFlag(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		body        string
		want        bool
	}{
		{"json boolean", "application/json", `{"subscription":{"endpoint":"https://push.example/1","standard":true,"keys":{"auth":"auth","p256dh":"key"}}}`, true},
		{"json rails true string", "application/json", `{"subscription":{"endpoint":"https://push.example/1","standard":"1","keys":{"auth":"auth","p256dh":"key"}}}`, true},
		{"json rails false string", "application/json", `{"subscription":{"endpoint":"https://push.example/1","standard":"0","keys":{"auth":"auth","p256dh":"key"}}}`, false},
		{"form true", "application/x-www-form-urlencoded", "subscription%5Bendpoint%5D=https%3A%2F%2Fpush.example%2F1&subscription%5Bkeys%5D%5Bauth%5D=auth&subscription%5Bkeys%5D%5Bp256dh%5D=key&subscription%5Bstandard%5D=1", true},
		{"form false", "application/x-www-form-urlencoded", "subscription%5Bendpoint%5D=https%3A%2F%2Fpush.example%2F1&subscription%5Bkeys%5D%5Bauth%5D=auth&subscription%5Bkeys%5D%5Bp256dh%5D=key&subscription%5Bstandard%5D=0", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			e := echo.New()
			request := httptest.NewRequest(http.MethodPost, "/api/v1/push/subscription", strings.NewReader(test.body))
			request.Header.Set("Content-Type", test.contentType)
			payload, err := parseWebPushPayload(echo.NewContext(request, httptest.NewRecorder(), e))
			if err != nil {
				t.Fatal(err)
			}
			if got := bool(payload.Subscription.Standard); got != test.want {
				t.Fatalf("standard = %v, want %v", got, test.want)
			}
		})
	}
}

func TestWebPush44CreatePersistsStandardAndTokenScopedCRUD(t *testing.T) {
	source := []byte(mustReadTestFile(t, "web_push_subscriptions.go"))
	for _, function := range []string{"createWebPushSubscription", "createPushSubscription"} {
		if !functionBodyContains(t, source, function, `Standard:      bool(payload.Subscription.Standard)`) {
			t.Fatalf("%s does not persist the Mastodon 4.4 standard flag", function)
		}
	}
	for _, function := range []string{"showPushSubscription", "updatePushSubscription", "deletePushSubscription"} {
		if !functionBodyContains(t, source, function, "accessToken") {
			t.Fatalf("%s is not scoped through the authenticated owner token", function)
		}
	}
}

func TestDeliverWebPushWithClientUsesLegacyAesgcmForNonStandardSubscription(t *testing.T) {
	clientPrivate, clientX, clientY, err := elliptic.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	clientPublic := elliptic.Marshal(elliptic.P256(), clientX, clientY)
	authSecret := []byte("0123456789abcdef")
	cfg := testWebPush44Config(t)
	now := time.Date(2026, time.August, 12, 12, 0, 0, 0, time.UTC)
	subscription := models.WebPushSubscription{
		ID:        44,
		Endpoint:  "https://push.example.test/send/legacy",
		KeyP256dh: base64.RawURLEncoding.EncodeToString(clientPublic),
		KeyAuth:   base64.RawURLEncoding.EncodeToString(authSecret),
		Standard:  false,
	}
	payload := []byte(`{"title":"legacy"}`)
	capture := &capturingWebPushClient{}

	response, err := deliverWebPushWithClient(t.Context(), cfg, subscription, payload, capture, now, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d", response.StatusCode)
	}
	request := capture.request
	assertWebPushCommonHeaders(t, request, now, subscription.ID)
	if got := request.Header.Get("Content-Encoding"); got != "aesgcm" {
		t.Fatalf("Content-Encoding = %q", got)
	}
	if !strings.HasPrefix(request.Header.Get("Authorization"), "WebPush ") {
		t.Fatalf("Authorization = %q", request.Header.Get("Authorization"))
	}
	assertVAPIDClaims(t, strings.TrimPrefix(request.Header.Get("Authorization"), "WebPush "), cfg.VapidPublicKey, now)

	saltValue := strings.TrimPrefix(request.Header.Get("Encryption"), "salt=")
	salt, err := base64.RawURLEncoding.DecodeString(saltValue)
	if err != nil || len(salt) != 16 {
		t.Fatalf("Encryption = %q: %v", request.Header.Get("Encryption"), err)
	}
	cryptoKey := request.Header.Get("Crypto-Key")
	parts := strings.Split(cryptoKey, ";")
	if len(parts) != 2 || !strings.HasPrefix(parts[0], "dh=") || parts[1] != "p256ecdsa="+strings.TrimRight(cfg.VapidPublicKey, "=") {
		t.Fatalf("Crypto-Key = %q", cryptoKey)
	}
	serverPublic, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(parts[0], "dh="))
	if err != nil {
		t.Fatal(err)
	}
	decrypted := decryptLegacyWebPushForTest(t, capture.body, salt, serverPublic, clientPublic, clientPrivate, authSecret)
	wantPlaintext := append([]byte{0, 0}, payload...)
	if !bytes.Equal(decrypted, wantPlaintext) {
		t.Fatalf("decrypted payload = %q, want %q", decrypted, wantPlaintext)
	}
}

func TestDeliverWebPushWithClientUsesRFC8291ForStandardSubscription(t *testing.T) {
	clientPrivateKey, clientPublicKey, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		t.Fatal(err)
	}
	authSecret := []byte("0123456789abcdef")
	cfg := testWebPush44Config(t)
	now := time.Date(2026, time.August, 12, 12, 0, 0, 0, time.UTC)
	subscription := models.WebPushSubscription{
		ID:        45,
		Endpoint:  "https://push.example.test/send/standard",
		KeyP256dh: clientPublicKey,
		KeyAuth:   base64.RawURLEncoding.EncodeToString(authSecret),
		Standard:  true,
	}
	capture := &capturingWebPushClient{}

	payload := []byte(`{"title":"standard"}`)
	if _, err := deliverWebPushWithClient(t.Context(), cfg, subscription, payload, capture, now, rand.Reader); err != nil {
		t.Fatal(err)
	}
	request := capture.request
	assertWebPushCommonHeaders(t, request, now, subscription.ID)
	if got := request.Header.Get("Content-Encoding"); got != "aes128gcm" {
		t.Fatalf("Content-Encoding = %q", got)
	}
	if request.Header.Get("Encryption") != "" || request.Header.Get("Crypto-Key") != "" {
		t.Fatalf("standard request contains legacy headers: %#v", request.Header)
	}
	authorization := request.Header.Get("Authorization")
	if !strings.HasPrefix(authorization, "vapid t=") || strings.Contains(authorization, ", ") || !strings.Contains(authorization, ",k=") {
		t.Fatalf("Authorization = %q", authorization)
	}
	token := strings.TrimPrefix(strings.SplitN(authorization, ",k=", 2)[0], "vapid t=")
	assertVAPIDClaims(t, token, cfg.VapidPublicKey, now)
	if got := request.Header.Get("Content-Length"); got != strconv.Itoa(len(capture.body)) {
		t.Fatalf("Content-Length = %q, body length = %d", got, len(capture.body))
	}
	if len(capture.body) == 0 {
		t.Fatal("standard encrypted payload is empty")
	}
	decrypted := decryptStandardWebPushForTest(t, capture.body, clientPrivateKey, clientPublicKey, authSecret)
	if !bytes.HasPrefix(decrypted, payload) || len(decrypted) <= len(payload) || decrypted[len(payload)] != 0x02 {
		t.Fatalf("standard decrypted record has invalid content/delimiter: %x", decrypted[:min(len(decrypted), len(payload)+8)])
	}
	for _, padding := range decrypted[len(payload)+1:] {
		if padding != 0 {
			t.Fatalf("standard decrypted record has non-zero padding: %x", padding)
		}
	}
}

func testWebPush44Config(t *testing.T) config.Config {
	t.Helper()
	privateKey, publicKey, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		t.Fatal(err)
	}
	return config.Config{
		Scheme:          "https",
		LocalDomain:     "social.example.test",
		WebDomain:       "social.example.test",
		SecretKeyBase:   "web-push-unsubscribe-test-secret",
		VapidSubject:    "mailto:sender@example.test",
		VapidPublicKey:  publicKey,
		VapidPrivateKey: privateKey,
	}
}

func assertWebPushCommonHeaders(t *testing.T, request *http.Request, now time.Time, subscriptionID int64) {
	t.Helper()
	if request == nil {
		t.Fatal("web push request was not captured")
	}
	for key, want := range map[string]string{
		"Content-Type": "application/octet-stream",
		"TTL":          "172800",
		"Urgency":      "normal",
	} {
		if got := request.Header.Get(key); got != want {
			t.Fatalf("%s = %q, want %q", key, got, want)
		}
	}
	unsubscribeURL := request.Header.Get("Unsubscribe-URL")
	prefix := "https://social.example.test/api/web/push_subscriptions/"
	if !strings.HasPrefix(unsubscribeURL, prefix) {
		t.Fatalf("Unsubscribe-URL = %q", unsubscribeURL)
	}
	token, err := url.PathUnescape(strings.TrimPrefix(unsubscribeURL, prefix))
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := verifyWebPushUnsubscribeToken(token, "web-push-unsubscribe-test-secret", now); !ok || got != subscriptionID {
		t.Fatalf("unsubscribe token = (%d, %v), want (%d, true)", got, ok, subscriptionID)
	}
}

func assertVAPIDClaims(t *testing.T, token string, encodedPublicKey string, now time.Time) {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("VAPID token has %d parts", len(parts))
	}
	claimsJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims struct {
		Audience  string `json:"aud"`
		ExpiresAt int64  `json:"exp"`
		Subject   string `json:"sub"`
	}
	if err := json.Unmarshal(claimsJSON, &claims); err != nil {
		t.Fatal(err)
	}
	if claims.Audience != "https://push.example.test" || claims.ExpiresAt != now.Add(24*time.Hour).Unix() || claims.Subject != "mailto:sender@example.test" {
		t.Fatalf("VAPID claims = %#v", claims)
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(signature) != 64 {
		t.Fatalf("VAPID signature length = %d: %v", len(signature), err)
	}
	publicKey, err := decodeWebPushKey(encodedPublicKey)
	if err != nil {
		t.Fatal(err)
	}
	x, y := elliptic.Unmarshal(elliptic.P256(), publicKey)
	if x == nil || y == nil {
		t.Fatal("VAPID public key is invalid")
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	r := new(big.Int).SetBytes(signature[:32])
	s := new(big.Int).SetBytes(signature[32:])
	if !ecdsa.Verify(&ecdsa.PublicKey{Curve: elliptic.P256(), X: x, Y: y}, digest[:], r, s) {
		t.Fatal("VAPID ES256 signature does not verify")
	}
}

func decryptLegacyWebPushForTest(t *testing.T, ciphertext []byte, salt []byte, serverPublic []byte, clientPublic []byte, clientPrivate []byte, authSecret []byte) []byte {
	t.Helper()
	curve := elliptic.P256()
	serverX, serverY := elliptic.Unmarshal(curve, serverPublic)
	if serverX == nil || serverY == nil {
		t.Fatal("server public key is invalid")
	}
	sharedX, _ := curve.ScalarMult(serverX, serverY, clientPrivate)
	sharedSecret := make([]byte, 32)
	sharedX.FillBytes(sharedSecret)
	prk := readHKDFForTest(t, sharedSecret, authSecret, []byte("Content-Encoding: auth\x00"), 32)

	context := bytes.NewBuffer(nil)
	context.WriteByte(0)
	_ = binary.Write(context, binary.BigEndian, uint16(len(clientPublic)))
	context.Write(clientPublic)
	_ = binary.Write(context, binary.BigEndian, uint16(len(serverPublic)))
	context.Write(serverPublic)
	cekInfo := append([]byte("Content-Encoding: aesgcm\x00P-256"), context.Bytes()...)
	nonceInfo := append([]byte("Content-Encoding: nonce\x00P-256"), context.Bytes()...)
	cek := readHKDFForTest(t, prk, salt, cekInfo, 16)
	nonce := readHKDFForTest(t, prk, salt, nonceInfo, 12)
	block, err := aes.NewCipher(cek)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		t.Fatal(err)
	}
	return plaintext
}

func decryptStandardWebPushForTest(t *testing.T, record []byte, encodedClientPrivate string, encodedClientPublic string, authSecret []byte) []byte {
	t.Helper()
	if len(record) < 21 {
		t.Fatalf("standard encrypted record is too short: %d", len(record))
	}
	salt := record[:16]
	recordSize := binary.BigEndian.Uint32(record[16:20])
	keyLength := int(record[20])
	if recordSize != webpush.MaxRecordSize || keyLength == 0 || len(record) < 21+keyLength+16 {
		t.Fatalf("standard record header = rs:%d key:%d total:%d", recordSize, keyLength, len(record))
	}
	serverPublic := record[21 : 21+keyLength]
	ciphertext := record[21+keyLength:]
	clientPrivate, err := decodeWebPushKey(encodedClientPrivate)
	if err != nil {
		t.Fatal(err)
	}
	clientPublic, err := decodeWebPushKey(encodedClientPublic)
	if err != nil {
		t.Fatal(err)
	}
	curve := elliptic.P256()
	serverX, serverY := elliptic.Unmarshal(curve, serverPublic)
	if serverX == nil || serverY == nil {
		t.Fatal("standard server public key is invalid")
	}
	sharedX, _ := curve.ScalarMult(serverX, serverY, clientPrivate)
	sharedSecret := make([]byte, 32)
	sharedX.FillBytes(sharedSecret)
	info := append([]byte("WebPush: info\x00"), clientPublic...)
	info = append(info, serverPublic...)
	prk := readHKDFForTest(t, sharedSecret, authSecret, info, 32)
	cek := readHKDFForTest(t, prk, salt, []byte("Content-Encoding: aes128gcm\x00"), 16)
	nonce := readHKDFForTest(t, prk, salt, []byte("Content-Encoding: nonce\x00"), 12)
	block, err := aes.NewCipher(cek)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		t.Fatal(err)
	}
	return plaintext
}

func readHKDFForTest(t *testing.T, secret []byte, salt []byte, info []byte, size int) []byte {
	t.Helper()
	out := make([]byte, size)
	if _, err := io.ReadFull(hkdf.New(sha256.New, secret, salt, info), out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestWebPushUnsubscribeTokenIsScopedTamperProofAndExpiresWithTTL(t *testing.T) {
	now := time.Date(2026, time.August, 12, 12, 0, 0, 0, time.UTC)
	token, err := webPushUnsubscribeToken(99, "secret", now)
	if err != nil {
		t.Fatal(err)
	}
	const railsToken = "eyJfcmFpbHMiOnsiZGF0YSI6Wzk5XSwiZXhwIjoiMjAyNi0wOC0xNFQxMjowMDowMC4wMDBaIiwicHVyIjoiV2ViOjpQdXNoU3Vic2NyaXB0aW9uXG51bnN1YnNjcmliZVxuMTcyODAwIn19--7529faed53e0f898628b0a584e8f3d71695df13c"
	if token != railsToken {
		t.Fatalf("token does not match Rails 8 MessageVerifier fixture:\n got %s\nwant %s", token, railsToken)
	}
	if got, ok := verifyWebPushUnsubscribeToken(token, "secret", now.Add(48*time.Hour-time.Nanosecond)); !ok || got != 99 {
		t.Fatalf("token immediately before TTL boundary = (%d, %v)", got, ok)
	}
	if _, ok := verifyWebPushUnsubscribeToken(token, "secret", now.Add(48*time.Hour)); ok {
		t.Fatal("expired token was accepted")
	}
	if _, ok := verifyWebPushUnsubscribeToken(token+"x", "secret", now); ok {
		t.Fatal("tampered token was accepted")
	}
	if _, ok := verifyWebPushUnsubscribeToken(token, "other-secret", now); ok {
		t.Fatal("token signed by another instance was accepted")
	}
}

func TestDestroyWebPushSubscriptionByInvalidTokenIsIdempotent(t *testing.T) {
	e := echo.New()
	request := httptest.NewRequest(http.MethodDelete, "https://social.example.test/api/web/push_subscriptions/invalid", nil)
	recorder := httptest.NewRecorder()
	context := echo.NewContext(request, recorder, e)
	context.SetPathValues(echo.PathValues{{Name: "id", Value: "invalid"}})
	server := &Server{cfg: config.Config{SecretKeyBase: "secret"}}
	if err := server.destroyWebPushSubscriptionByToken(context); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
}

func TestWebPushUnsubscribeRouteIsPublicAndIdempotent(t *testing.T) {
	server, err := NewServer(config.Config{
		Title:         "Paon",
		Scheme:        "https",
		LocalDomain:   "social.example.test",
		SecretKeyBase: "secret",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodDelete, "https://social.example.test/api/web/push_subscriptions/invalid", nil)
	recorder := httptest.NewRecorder()
	server.echo.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestWebPushNotificationOutsideTTLMatchesMastodon44(t *testing.T) {
	now := time.Date(2026, time.August, 12, 12, 0, 0, 0, time.UTC)
	if webPushNotificationOutsideTTL(models.Notification{UpdatedAt: now.Add(-48 * time.Hour)}, now) {
		t.Fatal("notification at the 48 hour boundary must remain deliverable")
	}
	if !webPushNotificationOutsideTTL(models.Notification{UpdatedAt: now.Add(-48*time.Hour - time.Nanosecond)}, now) {
		t.Fatal("notification older than 48 hours must not be delivered")
	}
	if webPushNotificationOutsideTTL(models.Notification{}, now) {
		t.Fatal("zero timestamp fixture should remain deliverable")
	}
}
