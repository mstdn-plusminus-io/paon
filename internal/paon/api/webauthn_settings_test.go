package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestWebauthnCreateOptionsIncludesExcludedCredentials(t *testing.T) {
	options := webauthnCreateOptions("example.com", "alice", "user-id", "challenge", []models.WebauthnCredential{{ExternalID: "credential-id"}})
	if options["challenge"] != "challenge" {
		t.Fatalf("challenge = %#v", options["challenge"])
	}
	rp := options["rp"].(map[string]any)
	if rp["name"] != railsWebauthnRPName || rp["id"] != "example.com" {
		t.Fatalf("rp = %#v", rp)
	}
	if options["timeout"] != railsWebauthnTimeout {
		t.Fatalf("timeout = %#v, want %#v", options["timeout"], railsWebauthnTimeout)
	}
	user := options["user"].(map[string]any)
	if user["name"] != "alice" || user["id"] != "user-id" {
		t.Fatalf("user = %#v", user)
	}
	exclude := options["excludeCredentials"].([]map[string]any)
	if len(exclude) != 1 || exclude[0]["id"] != "credential-id" {
		t.Fatalf("excludeCredentials = %#v", exclude)
	}
}

func TestSecurityKeysHTMLRendersRows(t *testing.T) {
	html := securityKeysHTML([]models.WebauthnCredential{{
		ID:        7,
		Nickname:  "Work key",
		CreatedAt: time.Date(2026, 6, 19, 0, 0, 0, 0, time.UTC),
	}}, "", "", "en")
	for _, want := range []string{"Work key", "/settings/security_keys/7", "2026-06-19", `data-method="delete"`, `class="table-action-link"`, `class="table-wrapper"`, `class="table"`, `class="spacer"`, `class="simple_form"`, `class="block-button"`} {
		if !strings.Contains(html, want) {
			t.Fatalf("html missing %q: %s", want, html)
		}
	}
	for _, forbidden := range []string{`<thead>`, `webauthn_credentials.not_enabled`, `Not enabled`, `account-security__tabs`, `name="_method"`} {
		if strings.Contains(html, forbidden) {
			t.Fatalf("html contains non-Rails index markup %q: %s", forbidden, html)
		}
	}
}

func TestNewSecurityKeyHTMLMatchesRailsWebAuthnPackContract(t *testing.T) {
	html := newSecurityKeyHTML("/packs/js/two_factor_authentication-hash.js", "en")
	for _, want := range []string{
		`id="new_webauthn_credential"`,
		`name="new_webauthn_credential[nickname]"`,
		`id="unsupported-browser-message"`,
		`id="security-key-error-message"`,
		`class="flash-message alert hidden" id="security-key-error-message"`,
		`Invalid security key`,
		`class="fields_group"`,
		`class="input with_block_label string required new_webauthn_credential_nickname field_with_hint"`,
		`<abbr title="required">*</abbr>`,
		`class="btn js-webauthn"`,
		`src="/packs/js/two_factor_authentication-hash.js"`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("new security key html missing %q: %s", want, html)
		}
	}
	if strings.Contains(html, `account-security__tabs`) {
		t.Fatalf("new security key html must not add non-Rails content tabs: %s", html)
	}
}

func TestSecurityKeySettingsUseLocalizedNoticesAndErrors(t *testing.T) {
	src, err := os.ReadFile("webauthn_settings.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`webT(locale, "webauthn_credentials.otp_required")`,
		`webT(locale, "webauthn_credentials.create.error")`,
		`webT(locale, "webauthn_credentials.invalid_credential")`,
		`webT(locale, "webauthn_credentials.destroy.success")`,
		`settingsT(locale, "webauthn_credentials.already_exists", "Security key already exists")`,
	} {
		if !strings.Contains(string(src), want) {
			t.Fatalf("webauthn settings missing localized string %q", want)
		}
	}
}

func TestSecurityKeyGuardRedirectHelpers(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/settings/security_keys/new", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())
	if err := settingsSecurityKeysOTPEnabledGuard(c, &models.User{}, "en"); err != nil {
		t.Fatal(err)
	}
	want := "/settings/two_factor_authentication_methods?error=" + url.QueryEscape(webT("en", "webauthn_credentials.otp_required"))
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != want {
		t.Fatalf("OTP guard status=%d location=%q want %q", rec.Code, rec.Header().Get("Location"), want)
	}

	req = httptest.NewRequest(http.MethodGet, "/settings/security_keys", nil)
	rec = httptest.NewRecorder()
	c = echo.NewContext(req, rec, echo.New())
	if err := settingsSecurityKeysWebauthnEnabledGuard(c, nil, "en"); err != nil {
		t.Fatal(err)
	}
	want = "/settings/two_factor_authentication_methods?error=" + url.QueryEscape(webT("en", "webauthn_credentials.not_enabled"))
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != want {
		t.Fatalf("WebAuthn guard status=%d location=%q want %q", rec.Code, rec.Header().Get("Location"), want)
	}

	req = httptest.NewRequest(http.MethodGet, "/settings/security_keys", nil)
	rec = httptest.NewRecorder()
	c = echo.NewContext(req, rec, echo.New())
	if err := settingsSecurityKeysOTPEnabledGuard(c, &models.User{OTPRequiredForLogin: true}, "en"); err != nil {
		t.Fatal(err)
	}
	if err := settingsSecurityKeysWebauthnEnabledGuard(c, []models.WebauthnCredential{{ID: 1}}, "en"); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("passing guards should not write redirect, status=%d", rec.Code)
	}
}

func TestWebauthnCredentialModelRoundTripUsesRailsColumns(t *testing.T) {
	credential := &webauthn.Credential{ID: []byte("credential-id"), PublicKey: []byte("public-key")}
	row := models.WebauthnCredential{
		ExternalID: webauthnCredentialExternalID(credential),
		PublicKey:  webauthnCredentialPublicKey(credential),
		SignCount:  42,
	}
	got, ok := webauthnCredentialFromModel(row)
	if !ok {
		t.Fatal("credential did not decode")
	}
	if string(got.ID) != "credential-id" || string(got.PublicKey) != "public-key" || got.Authenticator.SignCount != 42 {
		t.Fatalf("credential = %#v", got)
	}
}

func TestWebauthnRPIDStripsSchemeAndPort(t *testing.T) {
	for raw, want := range map[string]string{
		"https://social.example:443": "social.example",
		"localhost:3100":             "localhost",
		"":                           "localhost",
	} {
		if got := webauthnRPID(raw); got != want {
			t.Fatalf("webauthnRPID(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestWebauthnCredentialRequestUnwrapsRailsEnvelope(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/settings/security_keys", strings.NewReader(`{"credential":{"id":"abc","type":"public-key","response":{}},"nickname":"USB Key"}`))
	got, envelope, err := webauthnCredentialRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	if envelope.Nickname != "USB Key" {
		t.Fatalf("nickname = %q", envelope.Nickname)
	}
	body, err := io.ReadAll(got.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"id":"abc"`) || strings.Contains(string(body), "nickname") {
		t.Fatalf("credential body was not unwrapped: %s", string(body))
	}
}
