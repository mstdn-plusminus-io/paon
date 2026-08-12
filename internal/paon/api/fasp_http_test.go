package api

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestFASPHTTPMessageSignatureRequestRoundTrip(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_750_000_000, 0).UTC()
	body := []byte(`{"category":"content"}`)
	req := httptest.NewRequest(http.MethodPost, "https://paon.example/api/fasp/data_sharing/v0/backfill_requests", bytes.NewReader(body))
	if err := faspSignHTTPRequest(req, body, privateKey, "42", now); err != nil {
		t.Fatal(err)
	}
	input, err := faspVerifyHTTPRequest(req, body, publicKey, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("signed request did not verify: %v", err)
	}
	if input.KeyID != "42" {
		t.Fatalf("keyid = %q, want 42", input.KeyID)
	}
	if strings.Contains(req.Header.Get("Signature-Input"), "alg=") {
		t.Fatalf("signature input diverges from Linzer 0.7.3 defaults: %s", req.Header.Get("Signature-Input"))
	}

	tampered := req.Clone(req.Context())
	tampered.Header = req.Header.Clone()
	if _, err := faspVerifyHTTPRequest(tampered, []byte(`{"category":"account"}`), publicKey, now); err == nil {
		t.Fatal("tampered body passed FASP digest/signature verification")
	}
	if _, err := faspVerifyHTTPRequest(req, body, publicKey, now.Add(faspSignatureMaxAge+time.Second)); err == nil {
		t.Fatal("expired FASP signature was accepted")
	}
}

func TestFASPHTTPMessageSignatureResponseRoundTrip(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_750_000_000, 0).UTC()
	body := []byte(`{"subscription":{"id":7}}`)
	header := make(http.Header)
	if err := faspSignHTTPResponse(header, http.StatusCreated, body, privateKey, now); err != nil {
		t.Fatal(err)
	}
	response := &http.Response{StatusCode: http.StatusCreated, Header: header, Body: io.NopCloser(bytes.NewReader(body))}
	if err := faspVerifyHTTPResponse(response, body, publicKey, now); err != nil {
		t.Fatalf("signed response did not verify: %v", err)
	}
	if err := faspVerifyHTTPResponse(response, append(body, 'x'), publicKey, now); err == nil {
		t.Fatal("tampered response body passed verification")
	}
}

func TestFASPKeyEncodingAndFingerprint(t *testing.T) {
	privatePEM, publicBase64, err := faspGenerateServerKey()
	if err != nil {
		t.Fatal(err)
	}
	if got, err := faspServerPublicKeyBase64(privatePEM); err != nil || got != publicBase64 {
		t.Fatalf("server public key = %q, %v; want %q", got, err, publicBase64)
	}
	publicPEM, err := faspPublicKeyPEMFromBase64(publicBase64)
	if err != nil {
		t.Fatal(err)
	}
	provider := models.FaspProvider{ProviderPublicKeyPEM: publicPEM}
	if fingerprint, err := faspProviderPublicKeyFingerprint(provider); err != nil || strings.TrimSpace(fingerprint) == "" {
		t.Fatalf("fingerprint = %q, %v", fingerprint, err)
	}
	if _, err := faspPublicKeyPEMFromBase64("not-a-key"); err == nil {
		t.Fatal("invalid provider key was accepted")
	}
	// This is the raw Ed25519 key from Mastodon's fasp_provider fabricator.
	// Its fingerprint is asserted by the upstream provider model spec.
	fixturePEM, err := faspPublicKeyPEMFromBase64("h2ldXsaej2MXj0DHdCx7XibSo66uKlrLfJ5J6hte1Gk=")
	if err != nil {
		t.Fatal(err)
	}
	fixtureFingerprint, err := faspProviderPublicKeyFingerprint(models.FaspProvider{ProviderPublicKeyPEM: fixturePEM})
	if err != nil || fixtureFingerprint != "/AmW9EMlVq4o+Qcu9lNfTE8Ss/v9+evMPtyj2R437qE=" {
		t.Fatalf("upstream FASP fingerprint fixture = %q, %v", fixtureFingerprint, err)
	}
}

func TestFASPSignatureStringEscaping(t *testing.T) {
	got, err := faspSignatureString(`provider\"id`)
	if err != nil || got != `provider\\\"id` {
		t.Fatalf("escaped signature string = %q, %v", got, err)
	}
	if _, err := faspSignatureString("provider\nid"); err == nil {
		t.Fatal("control character was accepted in a signature parameter")
	}
	if _, err := faspSignatureString("provider-猫"); err == nil {
		t.Fatal("non-ASCII character was accepted in an RFC 9421 string parameter")
	}
}

func TestFASPProviderURLRequiresSafeHTTPS(t *testing.T) {
	for _, baseURL := range []string{
		"http://provider.example/fasp",
		"https://user:secret@provider.example/fasp",
		"https://127.0.0.1/fasp",
		"https://provider.example/fasp?secret=value",
	} {
		if _, err := faspProviderURL(models.FaspProvider{BaseURL: baseURL}, "/provider_info"); err == nil {
			t.Errorf("unsafe provider URL accepted: %s", baseURL)
		}
	}
}

func TestFASPRoutesAreFeatureGated(t *testing.T) {
	e := echo.New()
	s := &Server{echo: e, cfg: config.Config{}}
	e.HTTPErrorHandler = s.handleHTTPError
	s.registerFASPRoutes()
	req := httptest.NewRequest(http.MethodPost, "/api/fasp/registration", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	e.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("disabled FASP registration status = %d, want 404", recorder.Code)
	}

	e = echo.New()
	s = &Server{echo: e, cfg: config.Config{ExperimentalFeatures: []string{"fasp"}}}
	e.HTTPErrorHandler = s.handleHTTPError
	s.registerFASPRoutes()
	req = httptest.NewRequest(http.MethodPost, "/api/fasp/registration", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	recorder = httptest.NewRecorder()
	e.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("enabled FASP registration without DB status = %d, want 503", recorder.Code)
	}
}

func TestFASPBackfillAndSubscriptionValidation(t *testing.T) {
	if !faspValidCategory("account") || !faspValidCategory("content") || faspValidCategory("unknown") {
		t.Fatal("FASP data category validation diverges from Mastodon 4.4")
	}
	if !faspValidSubscriptionType("lifecycle") || !faspValidSubscriptionType("trends") || faspValidSubscriptionType("unknown") {
		t.Fatal("FASP subscription type validation diverges from Mastodon 4.4")
	}
	if got, err := faspURIList([]byte(`["https://remote.example/users/1","https://remote.example/users/1","http://remote.example/users/2","https://127.0.0.1/users/3"]`), 10); err != nil || len(got) != 1 || got[0] != "https://remote.example/users/1" {
		t.Fatalf("safe FASP URI list = %#v", got)
	}
	if _, err := faspURIList([]byte(`{"not":"a URI list"}`), 10); err == nil {
		t.Fatal("malformed FASP URI response was accepted")
	}
}

func TestFASPDecodeJSONAllowsProtocolExtensionsButRejectsTrailingValues(t *testing.T) {
	var input faspBackfillInput
	if err := faspDecodeJSON([]byte(`{"category":"content","maxCount":10,"extension":{"version":1}}`), &input); err != nil {
		t.Fatalf("FASP protocol extension was rejected: %v", err)
	}
	if input.Category != "content" || input.MaxCount != 10 {
		t.Fatalf("decoded FASP input = %#v", input)
	}
	if err := faspDecodeJSON([]byte(`{"category":"content"} {"category":"account"}`), &input); err == nil {
		t.Fatal("multiple JSON values were accepted")
	}
}

func TestFASPRegistrationAcceptsRailsFormEncoding(t *testing.T) {
	values := url.Values{
		"name":      {"Test Provider"},
		"baseUrl":   {"https://provider.example/fasp"},
		"serverId":  {"server-123"},
		"publicKey": {"9qgjOfWRhozWc9dwx5JmbshizZ7TyPBhYk9+b5tE3e4="},
	}
	body := []byte(values.Encode())
	req := httptest.NewRequest(http.MethodPost, "https://paon.example/api/fasp/registration", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
	var input faspRegistrationInput
	if err := faspDecodeRegistrationInput(req, body, &input); err != nil {
		t.Fatal(err)
	}
	if input.Name != "Test Provider" || input.BaseURL != "https://provider.example/fasp" || input.ServerID != "server-123" || input.PublicKey != values.Get("publicKey") {
		t.Fatalf("form registration decoded as %#v", input)
	}
}

func TestFASPAccountLifecycleUpdateEligibility(t *testing.T) {
	tests := []struct {
		name     string
		previous sql.NullBool
		current  sql.NullBool
		want     bool
	}{
		{name: "remains discoverable", previous: sql.NullBool{Bool: true, Valid: true}, current: sql.NullBool{Bool: true, Valid: true}, want: true},
		{name: "becomes discoverable", previous: sql.NullBool{Bool: false, Valid: true}, current: sql.NullBool{Bool: true, Valid: true}, want: true},
		{name: "becomes hidden", previous: sql.NullBool{Bool: true, Valid: true}, current: sql.NullBool{Bool: false, Valid: true}, want: true},
		{name: "unrelated hidden update", previous: sql.NullBool{Bool: false, Valid: true}, current: sql.NullBool{Bool: false, Valid: true}, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			previous := models.Account{Discoverable: test.previous}
			current := models.Account{Discoverable: test.current}
			if got := faspAccountLifecycleUpdateEligible(previous, current); got != test.want {
				t.Fatalf("eligible = %v, want %v", got, test.want)
			}
		})
	}
}

func TestAdminFASPProviderHTMLGatesDebugCallAndEscapesProviderLinks(t *testing.T) {
	provider := models.FaspProvider{
		ID:        7,
		Confirmed: true,
		Name:      `<script>alert(1)</script>`,
		BaseURL:   "https://provider.example/fasp",
		Capabilities: models.JSONValue(`[
			{"id":"callback","version":"0.1","enabled":false}
		]`),
		SignInURL: sql.NullString{String: `https://provider.example/sign-in?next=%22%3E%3Cscript%3E`, Valid: true},
	}
	page := adminFASPProviderHTML(provider, true, "", "en")
	if strings.Contains(page, "/debug_calls") {
		t.Fatal("debug call action was rendered for a disabled callback capability")
	}
	list := adminFASPProvidersHTML([]models.FaspProvider{provider}, "", "", "en")
	if strings.Contains(list, `<script>alert(1)</script>`) || !strings.Contains(list, `rel="noopener noreferrer"`) {
		t.Fatalf("provider list did not safely render remote metadata: %s", list)
	}

	provider.Capabilities = models.JSONValue(`[{"id":"callback","version":"0.1","enabled":true}]`)
	page = adminFASPProviderHTML(provider, true, "", "en")
	if !strings.Contains(page, "/admin/fasp/providers/7/debug_calls") {
		t.Fatal("debug call action was not rendered for an enabled callback capability")
	}
}
