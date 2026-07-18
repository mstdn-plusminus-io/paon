package api

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestCryptoDeviceResponseShape(t *testing.T) {
	out := cryptoDeviceResponse(models.Device{
		DeviceID:       "phone",
		Name:           "Phone",
		IdentityKey:    "identity",
		FingerprintKey: "fingerprint",
	})
	if out.DeviceID != "phone" || out.Name != "Phone" || out.IdentityKey != "identity" || out.FingerprintKey != "fingerprint" {
		t.Fatalf("device response = %#v", out)
	}
}

func TestCryptoEncryptedMessageResponseShape(t *testing.T) {
	created := time.Date(2026, 6, 19, 0, 0, 0, 0, time.UTC)
	out := cryptoEncryptedMessageResponse(models.EncryptedMessage{
		ID:              7,
		FromAccountID:   models.EncryptedMessageFromAccountID(42),
		FromDeviceID:    "source",
		MessageType:     1,
		Body:            "cipher",
		Digest:          "hmac",
		MessageFranking: "frank",
		CreatedAt:       created,
	})
	if out.ID != "7" ||
		out.AccountID != "42" ||
		out.DeviceID != "source" ||
		out.Type != 1 ||
		out.Body != "cipher" ||
		out.Digest != "hmac" ||
		out.MessageFranking != "frank" {
		t.Fatalf("encrypted message response = %#v", out)
	}
}

func TestEncryptedMessageStreamPayloadMatchesMastodonShape(t *testing.T) {
	created := time.Date(2026, 6, 19, 0, 0, 0, 0, time.UTC)
	payload := encryptedMessageStreamPayload(models.EncryptedMessage{
		ID:              7,
		FromAccountID:   models.EncryptedMessageFromAccountID(42),
		FromDeviceID:    "source",
		MessageType:     1,
		Body:            "cipher",
		Digest:          "hmac",
		MessageFranking: "frank",
		CreatedAt:       created,
	})
	var decoded struct {
		Event   string         `json:"event"`
		Payload map[string]any `json:"payload"`
	}
	if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Event != "encrypted_message" || decoded.Payload["id"] != "7" || decoded.Payload["body"] != "cipher" {
		t.Fatalf("payload = %#v", decoded)
	}
}

func TestReverseEncryptedMessagesKeepsNewestFirstForMinIDPagination(t *testing.T) {
	rows := []models.EncryptedMessage{{ID: 101}, {ID: 102}, {ID: 103}}
	reverseEncryptedMessages(rows)
	if rows[0].ID != 103 || rows[1].ID != 102 || rows[2].ID != 101 {
		t.Fatalf("rows = %#v", rows)
	}
}

func TestCryptoEncryptedMessagePaginationLinkMatchesRailsLimitOnlyMinID(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/v1/crypto/encrypted_messages?limit=10&device=phone&max_id=99&since_id=50", nil)
	req.Host = "social.example"
	c := echo.NewContext(req, httptest.NewRecorder(), echo.New())

	got := limitOnlyPaginationLink(c, 110, 100, "min_id", true)
	if !strings.Contains(got, "limit=10") || !strings.Contains(got, "max_id=100") || !strings.Contains(got, "min_id=110") {
		t.Fatalf("Link missing Rails crypto params: %q", got)
	}
	for _, unwanted := range []string{"device=", "since_id="} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("Link should omit Rails-filtered param %q: %q", unwanted, got)
		}
	}
}

func TestCryptoClearUpToIDReadsRailsParamsSources(t *testing.T) {
	e := echo.New()
	cases := []struct {
		name    string
		req     *http.Request
		want    string
		wantSet bool
	}{
		{
			name:    "query last value",
			req:     httptest.NewRequest(http.MethodPost, "/api/v1/crypto/encrypted_messages/clear?up_to_id=1&up_to_id=2", nil),
			want:    "2",
			wantSet: true,
		},
		{
			name: "form",
			req: func() *http.Request {
				req := httptest.NewRequest(http.MethodPost, "/api/v1/crypto/encrypted_messages/clear", strings.NewReader("up_to_id=42abc"))
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				return req
			}(),
			want:    "42abc",
			wantSet: true,
		},
		{
			name: "json number",
			req: func() *http.Request {
				req := httptest.NewRequest(http.MethodPost, "/api/v1/crypto/encrypted_messages/clear", strings.NewReader(`{"up_to_id":99}`))
				req.Header.Set("Content-Type", "application/json")
				return req
			}(),
			want:    "99",
			wantSet: true,
		},
		{
			name:    "absent",
			req:     httptest.NewRequest(http.MethodPost, "/api/v1/crypto/encrypted_messages/clear", nil),
			wantSet: false,
		},
		{
			name: "empty json body",
			req: func() *http.Request {
				req := httptest.NewRequest(http.MethodPost, "/api/v1/crypto/encrypted_messages/clear", nil)
				req.Header.Set("Content-Type", "application/json")
				return req
			}(),
			wantSet: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok, err := cryptoClearUpToID(e.NewContext(tc.req, httptest.NewRecorder()))
			if err != nil {
				t.Fatal(err)
			}
			if ok != tc.wantSet || got != tc.want {
				t.Fatalf("cryptoClearUpToID = %q, %v; want %q, %v", got, ok, tc.want, tc.wantSet)
			}
		})
	}
}

func TestParseCryptoQueryIDsMatchesRailsToI(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest("POST", "/api/v1/crypto/keys/query", strings.NewReader("id%5B%5D=42abc&id%5B%5D=bad&id=7"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c := echo.NewContext(req, httptest.NewRecorder(), e)

	got, err := parseCryptoQueryIDs(c)
	if err != nil {
		t.Fatal(err)
	}
	want := []int64{7, 42}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ids = %#v, want %#v", got, want)
	}

	req = httptest.NewRequest("POST", "/api/v1/crypto/keys/query", strings.NewReader(`{"id":["99x","bad","0","-5"]}`))
	req.Header.Set("Content-Type", "application/json")
	c = echo.NewContext(req, httptest.NewRecorder(), e)

	got, err = parseCryptoQueryIDs(c)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []int64{99}) {
		t.Fatalf("json ids = %#v", got)
	}
}

func TestEncryptedMessageStreamingChannelUsesDeviceTimeline(t *testing.T) {
	got := encryptedMessageStreamingChannel("mastodon:", 42, "phone")
	if got != "mastodon:timeline:42:phone" {
		t.Fatalf("channel = %q", got)
	}
	if encryptedMessageStreamingChannel("mastodon:", 0, "phone") != "" {
		t.Fatal("zero account should not produce a channel")
	}
}

func TestStreamingUserChannelIncludesCryptoDeviceWhenScoped(t *testing.T) {
	account := &models.Account{ID: 42}
	base := []string{"timeline:42"}
	got := streamingChannelIDsForSession("user", append([]string{}, base...), streamingSession{Account: account, Scopes: "read crypto", DeviceID: "phone"})
	want := []string{"timeline:42", "timeline:42:notifications", "timeline:42:phone"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("channels = %#v, want %#v", got, want)
	}
	got = streamingChannelIDsForSession("user", append([]string{}, base...), streamingSession{Account: account, Scopes: "read", DeviceID: "phone"})
	if want := []string{"timeline:42", "timeline:42:notifications"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("channels without crypto = %#v", got)
	}
	got = streamingChannelIDsForSession("user", append([]string{}, base...), streamingSession{Account: account, Scopes: "read:statuses", DeviceID: "phone"})
	if !reflect.DeepEqual(got, base) {
		t.Fatalf("read:statuses user stream should not include notifications or crypto: %#v", got)
	}
	got = streamingChannelIDsForSession("user", append([]string{}, base...), streamingSession{Account: account, Scopes: "read:notifications"})
	if want := []string{"timeline:42", "timeline:42:notifications"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("read:notifications user stream channels = %#v, want %#v", got, want)
	}
}

func TestCryptoDeliveriesPublishesEncryptedMessages(t *testing.T) {
	src, err := os.ReadFile("crypto.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, src, "cryptoDeliveries", `s.publishEncryptedMessage(item.message, item.device)`) {
		t.Fatal("cryptoDeliveries does not publish encrypted messages after creation")
	}
}

func TestCryptoDeliveriesRequiresDeviceLikeRails(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/crypto/deliveries", strings.NewReader(`{"device":[]}`))
	req.Header.Set("Content-Type", "application/json")
	c := echo.NewContext(req, httptest.NewRecorder(), echo.New())

	devices, err := parseCryptoDevicesPayload(c)
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 0 {
		t.Fatalf("devices = %#v, want empty", devices)
	}

	src, err := os.ReadFile("crypto.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, src, "cryptoDeliveries", `return apiError(c, http.StatusBadRequest, "param is missing or the value is empty: device")`) {
		t.Fatal("cryptoDeliveries must match Rails params.require(:device) 400 response")
	}
}

func TestCryptoActivityPubPathsTolerateWrappedRecordNotFound(t *testing.T) {
	src, err := os.ReadFile("crypto.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		fn   string
		want string
	}{
		{fn: "cryptoUploadKey", want: `errors.Is(err, gorm.ErrRecordNotFound)`},
		{fn: "cryptoDeliveries", want: `errors.Is(err, gorm.ErrRecordNotFound)`},
		{fn: "currentCryptoDevice", want: `errors.Is(err, gorm.ErrRecordNotFound)`},
		{fn: "claimCryptoOneTimeKey", want: `errors.Is(err, gorm.ErrRecordNotFound)`},
		{fn: "currentSystemKey", want: `!errors.Is(err, gorm.ErrRecordNotFound)`},
	} {
		if !functionBodyContains(t, src, check.fn, check.want) {
			t.Fatalf("crypto.go:%s must tolerate wrapped gorm.ErrRecordNotFound", check.fn)
		}
		if functionBodyContains(t, src, check.fn, `err == gorm.ErrRecordNotFound`) ||
			functionBodyContains(t, src, check.fn, `err != gorm.ErrRecordNotFound`) {
			t.Fatalf("crypto.go:%s must not directly compare gorm.ErrRecordNotFound", check.fn)
		}
	}
}

func TestParseCryptoUploadPayloadAcceptsJSON(t *testing.T) {
	body := `{"device":{"device_id":"phone","name":"Phone","fingerprint_key":"fp","identity_key":"id"},"one_time_keys":[{"key_id":"k1","key":"pub","signature":"sig"}]}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/crypto/keys/upload", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	c := echo.NewContext(req, httptest.NewRecorder(), echo.New())

	payload, err := parseCryptoUploadPayload(c)
	if err != nil {
		t.Fatal(err)
	}
	if payload.Device.DeviceID != "phone" || payload.Device.FingerprintKey != "fp" || len(payload.OneTimeKeys) != 1 || payload.OneTimeKeys[0].KeyID != "k1" {
		t.Fatalf("payload = %#v", payload)
	}
}

func TestParseCryptoFormDevicesAcceptsRailsArrayFields(t *testing.T) {
	values := url.Values{}
	values.Add("device[][account_id]", "42")
	values.Add("device[][device_id]", "phone")
	values.Add("device[][type]", "1")
	values.Add("device[][body]", "cipher")
	values.Add("device[][hmac]", "digest")

	devices := parseCryptoFormDevices(values)
	if len(devices) != 1 || devices[0].AccountID != "42" || devices[0].DeviceID != "phone" || devices[0].Type != 1 || devices[0].Body != "cipher" || devices[0].HMAC != "digest" {
		t.Fatalf("devices = %#v", devices)
	}
}

func assertCryptoDevicePayload(t *testing.T, devices []cryptoDevicePayload, source string) {
	t.Helper()
	if len(devices) != 1 || devices[0].AccountID != "42" || devices[0].DeviceID != "phone" || devices[0].Type != 1 || devices[0].Body != "cipher" || devices[0].HMAC != "digest" {
		t.Fatalf("%s devices = %#v", source, devices)
	}
}

func TestCryptoMessageFrankingMatchesRailsMessageEncryptorShape(t *testing.T) {
	at := time.Date(2026, 6, 19, 0, 0, 0, 0, time.UTC)
	key := []byte("0123456789abcdef0123456789abcdef")
	token, err := cryptoMessageFrankingToken(key, 1, 2, "hmac", nil, at, strings.NewReader("123456789012"))
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(token, "--")
	if len(parts) != 3 {
		t.Fatalf("token parts = %#v", parts)
	}
	decrypted := decryptRailsAESGCMTestToken(t, key, token)
	var payload cryptoMessageFrankingPayload
	if err := json.Unmarshal(decrypted, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.HMAC != "hmac" || payload.SourceAccountID != 1 || payload.TargetAccountID != 2 || payload.OriginalFranking != nil {
		t.Fatalf("payload = %#v", payload)
	}
	if payload.Timestamp != "2026-06-19T00:00:00Z" {
		t.Fatalf("timestamp = %q", payload.Timestamp)
	}
}

func TestCryptoRemoteDevicesParsesActivityPubCollection(t *testing.T) {
	previous := activityHTTPClient
	t.Cleanup(func() { activityHTTPClient = previous })
	activityHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet || req.Header.Get("Accept") == "" {
			t.Fatalf("request = %s accept=%q", req.Method, req.Header.Get("Accept"))
		}
		body := `{"items":[{"id":" phone ","name":"Phone","claim":"https://remote.example/users/alice/claim/phone","identityKey":{"publicKeyBase64":"identity"},"fingerprintKey":{"publicKeyBase64":"fingerprint"}}]}`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
	})}
	devices, err := cryptoRemoteDevices(models.Account{DevicesURL: "https://remote.example/users/alice/devices"})
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 1 || devices[0].DeviceID != " phone " || devices[0].ClaimURL == "" || devices[0].IdentityKey != "identity" || devices[0].FingerprintKey != "fingerprint" {
		t.Fatalf("devices = %#v", devices)
	}
}

func TestCryptoRemoteClaimRejectsOversizedBodyLikeRails(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("GenerateKey error = %v", err)
	}
	privatePEM := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))

	previous := activityHTTPClient
	t.Cleanup(func() { activityHTTPClient = previous })
	activityHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusOK,
			Body:          io.NopCloser(strings.NewReader(`{"id":"ignored","publicKeyBase64":"ignored"}`)),
			Header:        http.Header{},
			ContentLength: maxActivityResourceBodySize + 1,
		}, nil
	})}

	server := &Server{cfg: config.Config{Scheme: "https", WebDomain: "example.com", LocalDomain: "example.com", Version: "6.0.2", MastodonVersion: "4.2.27"}}
	id, keyValue, signature, err := server.cryptoRemoteClaim(models.Account{Username: "bob", PrivateKey: sql.NullString{String: privatePEM, Valid: true}}, "https://remote.example/users/alice/claim?id=phone")
	if err != nil {
		t.Fatalf("cryptoRemoteClaim error = %v", err)
	}
	if id != "" || keyValue != "" || signature != "" {
		t.Fatalf("oversized claim body should be ignored like Rails rescue path, got id:%q key:%q signature:%q", id, keyValue, signature)
	}

	activityHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusOK,
			Body:          io.NopCloser(strings.NewReader(strings.Repeat("x", maxActivityResourceBodySize+1))),
			Header:        http.Header{},
			ContentLength: -1,
		}, nil
	})}
	id, keyValue, signature, err = server.cryptoRemoteClaim(models.Account{Username: "bob", PrivateKey: sql.NullString{String: privatePEM, Valid: true}}, "https://remote.example/users/alice/claim?id=phone")
	if err != nil {
		t.Fatalf("cryptoRemoteClaim streamed oversized error = %v", err)
	}
	if id != "" || keyValue != "" || signature != "" {
		t.Fatalf("streamed oversized claim body should be ignored like Rails rescue path, got id:%q key:%q signature:%q", id, keyValue, signature)
	}
}

func decryptRailsAESGCMTestToken(t *testing.T, key []byte, token string) []byte {
	t.Helper()
	parts := strings.Split(token, "--")
	if len(parts) != 3 {
		t.Fatalf("token parts = %#v", parts)
	}
	cipherText, err := base64.StdEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatal(err)
	}
	iv, err := base64.StdEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	authTag, err := base64.StdEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatal(err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := gcm.Open(nil, iv, append(cipherText, authTag...), []byte(""))
	if err != nil {
		t.Fatal(err)
	}
	return plaintext
}
