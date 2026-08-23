package api

import (
	"bytes"
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdsa"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/beevik/etree"
	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	dsig "github.com/russellhaering/goxmldsig"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

func TestValidBCryptPasswordAcceptsDevise2yPrefix(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("correct horse battery staple"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	deviseHash := "$2y$" + string(hash[4:])

	if !validBCryptPassword(deviseHash, "correct horse battery staple") {
		t.Fatal("expected $2y$ bcrypt hash to validate")
	}
	if validBCryptPassword(deviseHash, "wrong") {
		t.Fatal("wrong password validated")
	}
}

func TestPurgeOldSessionActivationsNegativeOneDisablesLimit(t *testing.T) {
	// A zero-value DB would be unusable if purgeOldSessionActivations attempted
	// a query, so this also verifies that the unlimited setting returns early.
	if err := purgeOldSessionActivations(&gorm.DB{}, 1, -1); err != nil {
		t.Fatalf("purgeOldSessionActivations with unlimited setting: %v", err)
	}
}

func TestUserCanUseAuthenticatedAPIRequiresFunctionalConfirmedUser(t *testing.T) {
	confirmed := models.User{Approved: true, ConfirmedAt: sql.NullTime{Time: time.Now(), Valid: true}}
	if !userCanUseAuthenticatedAPI(confirmed) {
		t.Fatal("confirmed approved user was rejected")
	}
	for name, user := range map[string]models.User{
		"disabled":    {Disabled: true, Approved: true, ConfirmedAt: sql.NullTime{Time: time.Now(), Valid: true}},
		"pending":     {Approved: false, ConfirmedAt: sql.NullTime{Time: time.Now(), Valid: true}},
		"unconfirmed": {Approved: true},
	} {
		if userCanUseAuthenticatedAPI(user) {
			t.Fatalf("%s user was accepted", name)
		}
	}
}

func TestAuthRedirectFlashesUseDeviseLocaleKeys(t *testing.T) {
	src, err := os.ReadFile("auth.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		fn   string
		want string
	}{
		{"signIn", `s.authInvalidSignInMessage(c, nil)`},
		{"signInWithTwoFactorOTP", `s.authInvalidSignInMessage(c, nil)`},
		{"omniauthLogout", `s.authSignedOutMessage(c, nil)`},
	} {
		if !functionBodyContains(t, src, check.fn, check.want) {
			t.Fatalf("%s must use Devise locale helper %q", check.fn, check.want)
		}
	}
	for _, check := range []struct {
		fn    string
		stale string
	}{
		{"signIn", `QueryEscape("Invalid email or password")`},
		{"signInWithTwoFactorOTP", `QueryEscape("Invalid email or password")`},
		{"omniauthLogout", `QueryEscape("Signed out")`},
	} {
		if functionBodyContains(t, src, check.fn, check.stale) {
			t.Fatalf("%s must not use fixed Go-only auth flash %q", check.fn, check.stale)
		}
	}

	s := &Server{cfg: config.Config{DefaultLocale: "ja"}}
	req := httptest.NewRequest(http.MethodGet, "/auth/sign_in", nil)
	c := echo.NewContext(req, httptest.NewRecorder(), echo.New())
	if got := s.authInvalidSignInMessage(c, nil); got == "Invalid Email or password." || !strings.Contains(got, "パスワード") {
		t.Fatalf("Japanese invalid sign-in flash did not resolve Devise key: %q", got)
	}
	if got := s.authSignedOutMessage(c, nil); got == "Signed out successfully." || !strings.Contains(got, "ログアウト") {
		t.Fatalf("Japanese signed-out flash did not resolve Devise key: %q", got)
	}
}

func renderSignInFormForTest(t *testing.T, cfg config.Config) string {
	t.Helper()
	s := &Server{cfg: cfg}
	req := httptest.NewRequest(http.MethodGet, "/auth/sign_in", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())
	if err := s.signInForm(c); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

func TestSignInFormDoesNotLinkAwayFromOAuthAuthorizationFlow(t *testing.T) {
	s := &Server{cfg: config.Config{DefaultLocale: "en"}}
	req := httptest.NewRequest(http.MethodGet, "/auth/sign_in?redirect_to=%2Foauth%2Fauthorize%3Fclient_id%3Dtest", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())
	if err := s.signInForm(c); err != nil {
		t.Fatal(err)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `class="logo logo--wordmark"`) || strings.Contains(body, `<h1><a href="/">`) {
		t.Fatalf("OAuth authorization sign-in must show a non-linking wordmark: %s", body)
	}
	if strings.Contains(body, `class="form-footer"`) {
		t.Fatalf("OAuth authorization sign-in must not link to unrelated auth pages: %s", body)
	}
	if !strings.Contains(body, `name="redirect_to" value="/oauth/authorize?client_id=test"`) {
		t.Fatalf("OAuth authorization redirect was not preserved: %s", body)
	}

	for _, unsafe := range []string{"https://evil.example/oauth/authorize", "//evil.example/oauth/authorize"} {
		if signInWithinOAuthAuthorizationFlow(unsafe) {
			t.Fatalf("external redirect %q must not be treated as an authorization flow", unsafe)
		}
	}
}

func TestTwoFactorSignInHTMLMatchesRailsWebAuthnPackContract(t *testing.T) {
	html := twoFactorSignInHTML("/packs/js/two_factor_authentication-hash.js", "en", true, true)
	for _, want := range []string{
		`id="webauthn-form"`,
		`id="otp-authentication-form" class="hidden"`,
		`name="user[otp_attempt]"`,
		`id="unsupported-browser-message"`,
		`id="security-key-error-message"`,
		`id="link-to-otp"`,
		`id="link-to-webauthn"`,
		`class="btn js-webauthn"`,
		`src="/packs/js/two_factor_authentication-hash.js"`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("two-factor html missing %q: %s", want, html)
		}
	}

	otpOnly := twoFactorSignInHTML("/packs/js/two_factor_authentication-hash.js", "en", false, false)
	if strings.Contains(otpOnly, `id="webauthn-form"`) || strings.Contains(otpOnly, `id="link-to-webauthn"`) {
		t.Fatalf("OTP-only two-factor page must not render WebAuthn fragments: %s", otpOnly)
	}
	if strings.Contains(otpOnly, `id="otp-authentication-form" class="hidden"`) || !strings.Contains(otpOnly, `id="otp-authentication-form"`) {
		t.Fatalf("OTP-only two-factor page must show the OTP form: %s", otpOnly)
	}
}

func TestOmniAuthFallbackRoutesDoNot404WhenProviderMiddlewareIsUnavailable(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name     string
		method   string
		target   string
		location string
	}{
		{
			name:     "entry",
			method:   http.MethodGet,
			target:   "/auth/auth/openid_connect",
			location: "/auth/sign_in?error=External+authentication+provider+openid_connect+is+not+available",
		},
		{
			name:     "callback",
			method:   http.MethodPost,
			target:   "/auth/auth/openid_connect/callback",
			location: "/auth/sign_in?error=External+authentication+provider+openid_connect+is+not+available",
		},
		{
			name:     "logout",
			method:   http.MethodGet,
			target:   "/auth/auth/openid_connect/logout",
			location: "/auth/sign_in?notice=Signed+out+successfully.",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(test.method, test.target, nil)
			rec := httptest.NewRecorder()
			s.echo.ServeHTTP(rec, req)
			if rec.Code != http.StatusFound || rec.Header().Get("Location") != test.location {
				t.Fatalf("redirect = %d %q", rec.Code, rec.Header().Get("Location"))
			}
		})
	}
}

func TestOpenIDConnectAuthorizationURLResolvesRailsClientOptions(t *testing.T) {
	cfg := config.Config{
		OIDCEnabled:      true,
		OIDCClientID:     "client",
		OIDCRedirectURI:  "https://example.test/callback",
		OIDCHTTPScheme:   "http",
		OIDCHost:         "idp.example.test",
		OIDCPort:         "8080",
		OIDCAuthEndpoint: "/oauth/authorize?existing=1",
		OIDCScope:        "openid,email",
	}
	got, err := openIDConnectAuthorizationURL(cfg, "state", "")
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme != "http" || u.Host != "idp.example.test:8080" || u.Path != "/oauth/authorize" {
		t.Fatalf("relative endpoint resolved to %q", got)
	}
	if u.Query().Get("existing") != "1" || u.Query().Get("client_id") != "client" || u.Query().Get("scope") != "openid email" {
		t.Fatalf("authorization query not preserved: %q", got)
	}

	cfg.OIDCHTTPScheme = ""
	got, err = openIDConnectAuthorizationURL(cfg, "state", "")
	if err != nil {
		t.Fatal(err)
	}
	u, err = url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme != "" || u.Host != "idp.example.test:8080" || u.Path != "/oauth/authorize" {
		t.Fatalf("blank OIDC scheme should be preserved for relative endpoint, got %q", got)
	}
}

func TestOpenIDConnectAuthorizationURLRequiresConfiguredEndpoint(t *testing.T) {
	cfg := config.Config{OIDCEnabled: true, OIDCClientID: "client", OIDCRedirectURI: "https://example.test/callback", OIDCScope: "openid,email"}
	if _, err := openIDConnectAuthorizationURL(cfg, "state", "nonce"); err == nil {
		t.Fatal("expected missing authorization endpoint to fail")
	}
	cfg.OIDCAuthEndpoint = "https://idp.example.test/auth"
	got, err := openIDConnectAuthorizationURL(cfg, "state", "")
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	if u.Query().Get("scope") != "openid email" || u.Query().Get("nonce") != "" || u.Query().Get("state") != "state" {
		t.Fatalf("unexpected authorization URL %q", got)
	}
}

func TestOpenIDConnectOmniAuthInfoHelpersMatchRailsEmailAndNameSemantics(t *testing.T) {
	claims := map[string]any{
		"email":          "alice@example.test",
		"verified_email": "verified@example.test",
		"email_verified": "false",
	}
	if got := openIDConnectEmailFromClaims(claims); got != "verified@example.test" {
		t.Fatalf("email = %q", got)
	}
	if !openIDConnectEmailVerified(claims, config.Config{}) {
		t.Fatal("verified_email should mark OIDC email verified like Rails email_from_auth")
	}
	if !openIDConnectEmailVerified(map[string]any{"email_verified": "true"}, config.Config{}) {
		t.Fatal("truthy email_verified should mark email verified")
	}
	if !openIDConnectEmailVerified(map[string]any{}, config.Config{OIDCSecurityAssumeEmailVerified: true}) {
		t.Fatal("OIDC security assume_email_is_verified should mark email verified")
	}
	name := omniauthDisplayName(omniauthAuthInfo{FirstName: "Alice", LastName: "Example"})
	if name != "Alice Example" {
		t.Fatalf("display name = %q", name)
	}
	multibyteName := strings.Repeat("象", 29) + "🐘" + "末"
	if got, want := omniauthDisplayName(omniauthAuthInfo{Name: multibyteName}), strings.Repeat("象", 29)+"🐘"; got != want || !utf8.ValidString(got) {
		t.Fatalf("multibyte display name = %q, want valid UTF-8 %q", got, want)
	}
	if supportedOmniAuthImageURL("javascript:alert(1)") || supportedOmniAuthImageURL("https:avatar") || !supportedOmniAuthImageURL("https://cdn.example.test/avatar.png") {
		t.Fatal("OmniAuth avatar URL validation should accept only host-qualified HTTP(S) URLs")
	}
	eoleUID := openIDConnectClaimString(map[string]any{"sub": []any{map[string]any{"user": "eole-userinfo-user"}}}, "sub")
	if eoleUID != "eole-userinfo-user" {
		t.Fatalf("OIDC uid Hashie::Array compatibility value = %q", eoleUID)
	}
}

func TestOpenIDConnectCodeCallbackCanUseIDTokenUIDWhenUserInfoUnavailable(t *testing.T) {
	oldClient := oidcHTTPClient
	t.Cleanup(func() { oidcHTTPClient = oldClient })
	oidcHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != "https://idp.example.test/token" {
			t.Fatalf("unexpected request %s", req.URL.String())
		}
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatal(err)
		}
		form, err := url.ParseQuery(string(body))
		if err != nil {
			t.Fatal(err)
		}
		if form.Get("client_secret") != "client-secret" {
			t.Fatalf("client_secret_post did not include secret: %q", string(body))
		}
		token := testUnsignedJWT(map[string]any{"sub": "id-token-sub"})
		return jsonResponse(http.StatusOK, `{"id_token":"`+token+`"}`), nil
	})}
	cfg := config.Config{
		OIDCEnabled:          true,
		OIDCClientID:         "client-id",
		OIDCClientSecret:     "client-secret",
		OIDCRedirectURI:      "https://example.com/auth/auth/openid_connect/callback",
		OIDCTokenEndpoint:    "https://idp.example.test/token",
		OIDCUIDField:         "sub",
		OIDCClientAuthMethod: "client_secret_post",
	}
	req := httptest.NewRequest(http.MethodPost, "/auth/auth/openid_connect/callback", strings.NewReader("code=callback-code"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())
	uid, err := (&Server{cfg: cfg}).openIDConnectUIDFromCallback(c)
	if err != nil {
		t.Fatal(err)
	}
	if uid != "id-token-sub" {
		t.Fatalf("uid = %q", uid)
	}
}

func TestOpenIDConnectCodeCallbackRejectsNonceMismatch(t *testing.T) {
	oldClient := oidcHTTPClient
	t.Cleanup(func() { oidcHTTPClient = oldClient })
	oidcHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		token := testUnsignedJWT(map[string]any{"sub": "id-token-sub", "nonce": "wrong-nonce"})
		return jsonResponse(http.StatusOK, `{"id_token":"`+token+`"}`), nil
	})}
	cfg := config.Config{
		OIDCEnabled:          true,
		OIDCClientID:         "client-id",
		OIDCClientSecret:     "client-secret",
		OIDCRedirectURI:      "https://example.com/auth/auth/openid_connect/callback",
		OIDCTokenEndpoint:    "https://idp.example.test/token",
		OIDCUIDField:         "sub",
		OIDCClientAuthMethod: "basic",
	}
	server := &Server{cfg: cfg}
	setupRecorder := httptest.NewRecorder()
	setupContext := echo.NewContext(httptest.NewRequest(http.MethodGet, "/auth/auth/openid_connect", nil), setupRecorder, echo.New())
	if err := server.setBrowserOIDCState(setupContext, "expected-state", "expected-nonce"); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/auth/auth/openid_connect/callback?code=callback-code&state=expected-state", nil)
	req.AddCookie(cookiesByName(setupRecorder.Result().Cookies())[browserSessionCookieName])
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())
	if _, err := server.openIDConnectUIDFromCallback(c); err == nil || !strings.Contains(err.Error(), "nonce") {
		t.Fatalf("expected nonce mismatch error, got %v", err)
	}
}

func TestOpenIDConnectCodeCallbackVerifiesRS256IDTokenWithJWKS(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	token := testSignedRS256JWT(t, privateKey, "kid-1", map[string]any{
		"iss":   "https://idp.example.test",
		"aud":   []string{"client-id"},
		"exp":   time.Now().Add(time.Hour).Unix(),
		"sub":   "signed-sub",
		"nonce": "expected-nonce",
	})
	oldClient := oidcHTTPClient
	t.Cleanup(func() { oidcHTTPClient = oldClient })
	oidcHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.String() {
		case "https://idp.example.test/token":
			return jsonResponse(http.StatusOK, `{"id_token":"`+token+`"}`), nil
		case "https://idp.example.test/jwks":
			jwks, err := json.Marshal(map[string]any{"keys": []map[string]string{testRSAJWK(privateKey.PublicKey, "kid-1")}})
			if err != nil {
				t.Fatal(err)
			}
			return jsonResponse(http.StatusOK, string(jwks)), nil
		default:
			t.Fatalf("unexpected request %s", req.URL.String())
			return nil, nil
		}
	})}
	cfg := config.Config{
		OIDCEnabled:          true,
		OIDCIssuer:           "https://idp.example.test",
		OIDCClientID:         "client-id",
		OIDCClientSecret:     "client-secret",
		OIDCRedirectURI:      "https://example.com/auth/auth/openid_connect/callback",
		OIDCTokenEndpoint:    "https://idp.example.test/token",
		OIDCJWKSURI:          "https://idp.example.test/jwks",
		OIDCUIDField:         "sub",
		OIDCClientAuthMethod: "basic",
	}
	req := httptest.NewRequest(http.MethodGet, "/auth/auth/openid_connect/callback?code=callback-code", nil)
	req.AddCookie(&http.Cookie{Name: omniauthNonceCookie, Value: "expected-nonce"})
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())
	uid, err := (&Server{cfg: cfg}).openIDConnectUIDFromCallback(c)
	if err != nil {
		t.Fatal(err)
	}
	if uid != "signed-sub" {
		t.Fatalf("uid = %q", uid)
	}
}

func TestOpenIDConnectCodeCallbackRejectsInvalidSignedIDTokenClaims(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	token := testSignedRS256JWT(t, privateKey, "kid-1", map[string]any{
		"iss": "https://idp.example.test",
		"aud": "other-client",
		"exp": time.Now().Add(time.Hour).Unix(),
		"sub": "signed-sub",
	})
	oldClient := oidcHTTPClient
	t.Cleanup(func() { oidcHTTPClient = oldClient })
	oidcHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.String() {
		case "https://idp.example.test/token":
			return jsonResponse(http.StatusOK, `{"id_token":"`+token+`"}`), nil
		case "https://idp.example.test/jwks":
			jwks, err := json.Marshal(map[string]any{"keys": []map[string]string{testRSAJWK(privateKey.PublicKey, "kid-1")}})
			if err != nil {
				t.Fatal(err)
			}
			return jsonResponse(http.StatusOK, string(jwks)), nil
		default:
			t.Fatalf("unexpected request %s", req.URL.String())
			return nil, nil
		}
	})}
	cfg := config.Config{
		OIDCEnabled:          true,
		OIDCIssuer:           "https://idp.example.test",
		OIDCClientID:         "client-id",
		OIDCClientSecret:     "client-secret",
		OIDCRedirectURI:      "https://example.com/auth/auth/openid_connect/callback",
		OIDCTokenEndpoint:    "https://idp.example.test/token",
		OIDCJWKSURI:          "https://idp.example.test/jwks",
		OIDCUIDField:         "sub",
		OIDCClientAuthMethod: "basic",
	}
	req := httptest.NewRequest(http.MethodGet, "/auth/auth/openid_connect/callback?code=callback-code", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())
	if _, err := (&Server{cfg: cfg}).openIDConnectUIDFromCallback(c); err == nil || !strings.Contains(err.Error(), "audience") {
		t.Fatalf("expected audience error, got %v", err)
	}
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func testUnsignedJWT(claims map[string]any) string {
	header, _ := json.Marshal(map[string]any{"alg": "none", "typ": "JWT"})
	payload, _ := json.Marshal(claims)
	return base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload) + "."
}

func testSignedRS256JWT(t *testing.T, key *rsa.PrivateKey, kid string, claims map[string]any) string {
	t.Helper()
	header, err := json.Marshal(map[string]any{"alg": "RS256", "kid": kid, "typ": "JWT"})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	signingInput := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	digest := sha256.Sum256([]byte(signingInput))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func testSignedES256JWT(t *testing.T, key *ecdsa.PrivateKey, kid string, claims map[string]any) string {
	t.Helper()
	header, err := json.Marshal(map[string]any{"alg": "ES256", "kid": kid, "typ": "JWT"})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	signingInput := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	digest := sha256.Sum256([]byte(signingInput))
	r, s, err := ecdsa.Sign(rand.Reader, key, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	signature := append(leftPadBytesForTest(r.Bytes(), 32), leftPadBytesForTest(s.Bytes(), 32)...)
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func testSignedHS256JWT(t *testing.T, secret string, claims map[string]any) string {
	t.Helper()
	header, err := json.Marshal(map[string]any{"alg": "HS256", "typ": "JWT"})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	signingInput := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(signingInput))
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func testRSAJWK(key rsa.PublicKey, kid string) map[string]string {
	return map[string]string{
		"kty": "RSA",
		"use": "sig",
		"kid": kid,
		"alg": "RS256",
		"n":   base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
		"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
	}
}

func testECDSAJWK(key ecdsa.PublicKey, kid string, alg string) map[string]string {
	return map[string]string{
		"kty": "EC",
		"use": "sig",
		"kid": kid,
		"alg": alg,
		"crv": "P-256",
		"x":   base64.RawURLEncoding.EncodeToString(leftPadBytesForTest(key.X.Bytes(), 32)),
		"y":   base64.RawURLEncoding.EncodeToString(leftPadBytesForTest(key.Y.Bytes(), 32)),
	}
}

func leftPadBytesForTest(value []byte, size int) []byte {
	if len(value) >= size {
		return value
	}
	out := make([]byte, size)
	copy(out[size-len(value):], value)
	return out
}

func TestCASCallbackValidatesTicketAndBuildsOmniAuthInfo(t *testing.T) {
	oldClient := casHTTPClient
	t.Cleanup(func() { casHTTPClient = oldClient })
	var sawValidate bool
	casHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		sawValidate = true
		if req.URL.String() != "https://cas.example.test/serviceValidate?service=https%3A%2F%2Fexample.com%2Fauth%2Fauth%2Fcas%2Fcallback&ticket=+ST-123+" {
			t.Fatalf("CAS validate URL = %q", req.URL.String())
		}
		body := `<cas:serviceResponse xmlns:cas="http://www.yale.edu/tp/cas">
  <cas:authenticationSuccess>
    <cas:user>alice</cas:user>
    <cas:attributes>
      <cas:uid>alice-id</cas:uid>
      <cas:mail>alice@example.test</cas:mail>
      <cas:displayName>Alice Example</cas:displayName>
      <cas:givenName>Alice</cas:givenName>
      <cas:sn>Example</cas:sn>
      <cas:nickname>alice</cas:nickname>
      <cas:office>Tokyo</cas:office>
      <cas:avatar>https://cdn.example.test/alice.png</cas:avatar>
      <cas:telephoneNumber>+81-3-1234-5678</cas:telephoneNumber>
    </cas:attributes>
  </cas:authenticationSuccess>
</cas:serviceResponse>`
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/xml"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	cfg := config.Config{
		CASEnabled:                     true,
		CASValidateURL:                 "https://cas.example.test/serviceValidate",
		CASCallbackURL:                 "https://example.com/auth/auth/cas/callback",
		CASUIDKey:                      "uid",
		CASEmailKey:                    "mail",
		CASNameKey:                     "displayName",
		CASFirstNameKey:                "givenName",
		CASLastNameKey:                 "sn",
		CASNicknameKey:                 "nickname",
		CASLocationKey:                 "office",
		CASImageKey:                    "avatar",
		CASPhoneKey:                    "telephoneNumber",
		CASSecurityAssumeEmailVerified: true,
	}
	req := httptest.NewRequest(http.MethodGet, "/auth/auth/cas/callback?ticket=+ST-123+", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())
	authInfo, err := (&Server{cfg: cfg}).casAuthFromCallback(c)
	if err != nil {
		t.Fatal(err)
	}
	if !sawValidate || authInfo.Provider != "cas" || authInfo.UID != "alice-id" || authInfo.Email != "alice@example.test" || !authInfo.EmailVerified || authInfo.Name != "Alice Example" || authInfo.FirstName != "Alice" || authInfo.LastName != "Example" || authInfo.Nickname != "alice" || authInfo.Location != "Tokyo" || authInfo.Image != "https://cdn.example.test/alice.png" || authInfo.Phone != "+81-3-1234-5678" {
		t.Fatalf("authInfo=%#v sawValidate=%v", authInfo, sawValidate)
	}
}

func TestCASHTTPClientForConfigUsesRailsTLSOptions(t *testing.T) {
	certPEM := testCertificatePEM(t)
	caFile := t.TempDir() + "/cas-ca.pem"
	if err := os.WriteFile(caFile, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}

	client, err := casHTTPClientForConfig(config.Config{CASEnabled: true, CASCAPath: caFile})
	if err != nil {
		t.Fatal(err)
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok || transport.TLSClientConfig == nil || transport.TLSClientConfig.RootCAs == nil {
		t.Fatalf("CAS client transport did not load custom CA: %#v", client.Transport)
	}
	if transport.TLSClientConfig.InsecureSkipVerify {
		t.Fatal("CAS_CA_PATH should not disable certificate verification")
	}

	client, err = casHTTPClientForConfig(config.Config{CASEnabled: true, CASDisableSSLVerification: true})
	if err != nil {
		t.Fatal(err)
	}
	transport, ok = client.Transport.(*http.Transport)
	if !ok || transport.TLSClientConfig == nil || !transport.TLSClientConfig.InsecureSkipVerify {
		t.Fatalf("CAS_DISABLE_SSL_VERIFICATION did not configure TLS skip verify: %#v", client.Transport)
	}
}

func TestLDAPTLSConfigForConfigKeepsPerConnectionVerificationState(t *testing.T) {
	insecure := ldapTLSConfigForConfig(config.Config{LDAPHost: "ldap-insecure.example", LDAPTLSNoVerify: true})
	secure := ldapTLSConfigForConfig(config.Config{LDAPHost: "ldap-secure.example"})

	if insecure == secure {
		t.Fatal("LDAP connections unexpectedly share mutable TLS configuration")
	}
	if !insecure.InsecureSkipVerify {
		t.Fatal("explicit LDAP_TLS_NO_VERIFY=true was ignored")
	}
	if secure.InsecureSkipVerify {
		t.Fatal("certificate verification leaked from another LDAP connection")
	}
	insecure.ServerName = "mutated.example"
	if secure.ServerName != "ldap-secure.example" {
		t.Fatalf("LDAP TLS state leaked between connections: %#v", secure)
	}
}

func testCertificatePEM(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "CAS Test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func TestCASAuthInfoFromServiceResponseFallsBackToUserAsUID(t *testing.T) {
	cfg := config.Config{CASUIDKey: "uid", CASUIDField: "principal", CASEmailKey: "email"}
	body := `<serviceResponse><authenticationSuccess><user>alice</user><attributes><email>alice@example.test</email></attributes></authenticationSuccess></serviceResponse>`
	authInfo, err := casAuthInfoFromServiceResponse(strings.NewReader(body), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if authInfo.UID != "alice" || authInfo.Email != "alice@example.test" {
		t.Fatalf("authInfo=%#v", authInfo)
	}
}

func TestGenericOmniAuthCallbackInfoParsesRailsLikeInfoFields(t *testing.T) {
	cfg := config.Config{CASSecurityAssumeEmailVerified: true, CASEmailKey: "mail", CASNameKey: "displayName", CASUIDKey: "uid", CASLocationKey: "office", CASPhoneKey: "telephoneNumber"}
	req := httptest.NewRequest(http.MethodPost, "/auth/auth/cas/callback", strings.NewReader("omniauth%5Binfo%5D%5Bmail%5D=alice%40example.test&omniauth%5Binfo%5D%5BdisplayName%5D=Alice+Example&omniauth%5Binfo%5D%5Buid%5D=alice-id&omniauth%5Binfo%5D%5Boffice%5D=Tokyo&omniauth%5Binfo%5D%5BtelephoneNumber%5D=%2B81-3-1234-5678"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())
	authInfo := (&Server{cfg: cfg}).omniauthAuthInfoFromCallback(c, "cas")
	if authInfo.UID != "alice-id" || authInfo.Email != "alice@example.test" || authInfo.Name != "Alice Example" || authInfo.Location != "Tokyo" || authInfo.Phone != "+81-3-1234-5678" || !authInfo.EmailVerified {
		t.Fatalf("authInfo=%#v", authInfo)
	}
}

func TestSAMLCallbackBuildsOmniAuthInfoFromUnsignedResponseWhenAllowed(t *testing.T) {
	responseXML := `<samlp:Response xmlns:samlp="urn:oasis:names:tc:SAML:2.0:protocol" xmlns:saml="urn:oasis:names:tc:SAML:2.0:assertion">
  <samlp:Status><samlp:StatusCode Value="urn:oasis:names:tc:SAML:2.0:status:Success"></samlp:StatusCode></samlp:Status>
  <saml:Assertion>
    <saml:Subject><saml:NameID>nameid@example.test</saml:NameID></saml:Subject>
    <saml:AttributeStatement>
      <saml:Attribute Name="uid"><saml:AttributeValue>alice-id</saml:AttributeValue></saml:Attribute>
      <saml:Attribute Name="mail"><saml:AttributeValue>alice@example.test</saml:AttributeValue></saml:Attribute>
      <saml:Attribute Name="displayName"><saml:AttributeValue>Alice Example</saml:AttributeValue></saml:Attribute>
      <saml:Attribute Name="givenName"><saml:AttributeValue>Alice</saml:AttributeValue></saml:Attribute>
      <saml:Attribute Name="sn"><saml:AttributeValue>Example</saml:AttributeValue></saml:Attribute>
      <saml:Attribute Name="verified"><saml:AttributeValue>true</saml:AttributeValue></saml:Attribute>
    </saml:AttributeStatement>
  </saml:Assertion>
</samlp:Response>`
	cfg := config.Config{
		SAMLEnabled:                true,
		SAMLAttributeUID:           "uid",
		SAMLAttributeEmail:         "mail",
		SAMLAttributeFullName:      "displayName",
		SAMLAttributeFirstName:     "givenName",
		SAMLAttributeLastName:      "sn",
		SAMLAttributeVerified:      "verified",
		SAMLAttributeVerifiedEmail: "verified_email",
	}
	req := httptest.NewRequest(http.MethodPost, "/auth/auth/saml/callback", strings.NewReader("SAMLResponse="+url.QueryEscape(base64.StdEncoding.EncodeToString([]byte(responseXML)))))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())
	authInfo, err := (&Server{cfg: cfg}).samlAuthFromCallback(c)
	if err != nil {
		t.Fatal(err)
	}
	if authInfo.Provider != "saml" || authInfo.UID != "alice-id" || authInfo.Email != "alice@example.test" || !authInfo.EmailVerified || authInfo.Name != "Alice Example" || authInfo.FirstName != "Alice" || authInfo.LastName != "Example" {
		t.Fatalf("authInfo=%#v", authInfo)
	}
}

func TestSAMLCallbackRejectsExpiredUnsignedAssertion(t *testing.T) {
	responseXML := `<samlp:Response xmlns:samlp="urn:oasis:names:tc:SAML:2.0:protocol" xmlns:saml="urn:oasis:names:tc:SAML:2.0:assertion">
  <samlp:Status><samlp:StatusCode Value="urn:oasis:names:tc:SAML:2.0:status:Success"></samlp:StatusCode></samlp:Status>
  <saml:Assertion>
    <saml:Subject><saml:NameID>nameid@example.test</saml:NameID></saml:Subject>
    <saml:Conditions NotOnOrAfter="2000-01-01T00:00:00Z"></saml:Conditions>
  </saml:Assertion>
</samlp:Response>`
	req := httptest.NewRequest(http.MethodPost, "/auth/auth/saml/callback", strings.NewReader("SAMLResponse="+url.QueryEscape(base64.StdEncoding.EncodeToString([]byte(responseXML)))))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c := echo.NewContext(req, httptest.NewRecorder(), echo.New())
	_, err := (&Server{cfg: config.Config{SAMLEnabled: true, SAMLAllowedClockDrift: "30"}}).samlAuthFromCallback(c)
	if err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expected expired assertion rejection, got %v", err)
	}
}

func TestSAMLCallbackVerifiesSignedAssertionWithIDPCert(t *testing.T) {
	key, cert, certPEM := testSAMLSigningCertificate(t)
	responseXML := `<samlp:Response xmlns:samlp="urn:oasis:names:tc:SAML:2.0:protocol" xmlns:saml="urn:oasis:names:tc:SAML:2.0:assertion" Version="2.0">
  <samlp:Status><samlp:StatusCode Value="urn:oasis:names:tc:SAML:2.0:status:Success"></samlp:StatusCode></samlp:Status>
  <saml:Assertion ID="assertion-id">
    <saml:Subject><saml:NameID>nameid@example.test</saml:NameID></saml:Subject>
    <saml:AttributeStatement>
      <saml:Attribute Name="uid"><saml:AttributeValue>alice-id</saml:AttributeValue></saml:Attribute>
      <saml:Attribute Name="mail"><saml:AttributeValue>alice@example.test</saml:AttributeValue></saml:Attribute>
    </saml:AttributeStatement>
  </saml:Assertion>
</samlp:Response>`
	signedXML := signSAMLAssertionForTest(t, responseXML, key, cert)
	cfg := config.Config{SAMLEnabled: true, SAMLIDPCert: string(certPEM), SAMLSecurityWantAssertionsSigned: true, SAMLAttributeUID: "uid", SAMLAttributeEmail: "mail"}
	authInfo, err := samlAuthInfoFromResponse(signedXML, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if authInfo.UID != "alice-id" || authInfo.Email != "alice@example.test" {
		t.Fatalf("authInfo=%#v", authInfo)
	}

	tampered := bytes.Replace(signedXML, []byte("alice@example.test"), []byte("mallory@example.test"), 1)
	if _, err := samlAuthInfoFromResponse(tampered, cfg); err == nil || !strings.Contains(err.Error(), "signature") {
		t.Fatalf("expected tampered signed assertion rejection, got %v", err)
	}
}

func TestSAMLCallbackDecryptsEncryptedAssertionWithPrivateKey(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	assertionXML := `<saml:Assertion xmlns:saml="urn:oasis:names:tc:SAML:2.0:assertion">
  <saml:Subject><saml:NameID>nameid@example.test</saml:NameID></saml:Subject>
  <saml:AttributeStatement>
    <saml:Attribute Name="uid"><saml:AttributeValue>alice-id</saml:AttributeValue></saml:Attribute>
    <saml:Attribute Name="mail"><saml:AttributeValue>alice@example.test</saml:AttributeValue></saml:Attribute>
  </saml:AttributeStatement>
</saml:Assertion>`
	responseXML := encryptedSAMLResponseForTest(t, assertionXML, &privateKey.PublicKey)
	privateKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey)})
	cfg := config.Config{SAMLEnabled: true, SAMLPrivateKey: string(privateKeyPEM), SAMLSecurityWantAssertionsEncrypted: true, SAMLAttributeUID: "uid", SAMLAttributeEmail: "mail"}
	authInfo, err := samlAuthInfoFromResponse(responseXML, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if authInfo.UID != "alice-id" || authInfo.Email != "alice@example.test" {
		t.Fatalf("authInfo=%#v", authInfo)
	}
	referencedKeyResponseXML := encryptedSAMLResponseWithRetrievalMethodForTest(t, assertionXML, &privateKey.PublicKey)
	authInfo, err = samlAuthInfoFromResponse(referencedKeyResponseXML, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if authInfo.UID != "alice-id" || authInfo.Email != "alice@example.test" {
		t.Fatalf("authInfo=%#v", authInfo)
	}

	if _, err := samlAuthInfoFromResponse(responseXML, config.Config{SAMLEnabled: true, SAMLSecurityWantAssertionsEncrypted: true}); err == nil || !strings.Contains(err.Error(), "SAML_PRIVATE_KEY") {
		t.Fatalf("expected missing private key rejection, got %v", err)
	}
	unsignedResponse := []byte(`<Response><Assertion><Subject><NameID>alice</NameID></Subject></Assertion></Response>`)
	if _, err := samlAuthInfoFromResponse(unsignedResponse, cfg); err == nil || !strings.Contains(err.Error(), "did not include an EncryptedAssertion") {
		t.Fatalf("expected plaintext assertion rejection for encrypted-required config, got %v", err)
	}
}

func TestSAMLCallbackRejectsUnsignedOrUnverifiableSignedResponses(t *testing.T) {
	signedResponse := base64.StdEncoding.EncodeToString([]byte(`<Response><Signature></Signature><Assertion><Subject><NameID>alice</NameID></Subject></Assertion></Response>`))
	req := httptest.NewRequest(http.MethodPost, "/auth/auth/saml/callback", strings.NewReader("SAMLResponse="+url.QueryEscape(signedResponse)))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())
	if _, err := (&Server{cfg: config.Config{SAMLEnabled: true}}).samlAuthFromCallback(c); err == nil || !strings.Contains(err.Error(), "SAML_IDP_CERT") {
		t.Fatalf("expected signed response rejection, got %v", err)
	}

	unsignedResponse := base64.StdEncoding.EncodeToString([]byte(`<Response><Assertion><Subject><NameID>alice</NameID></Subject></Assertion></Response>`))
	req = httptest.NewRequest(http.MethodPost, "/auth/auth/saml/callback", strings.NewReader("SAMLResponse="+url.QueryEscape(unsignedResponse)))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c = echo.NewContext(req, httptest.NewRecorder(), echo.New())
	if _, err := (&Server{cfg: config.Config{SAMLEnabled: true, SAMLSecurityWantAssertionsSigned: true}}).samlAuthFromCallback(c); err == nil || !strings.Contains(err.Error(), "unsigned") {
		t.Fatalf("expected signed-required config rejection, got %v", err)
	}

	req = httptest.NewRequest(http.MethodPost, "/auth/auth/saml/callback", strings.NewReader("SAMLResponse="+url.QueryEscape(unsignedResponse)))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c = echo.NewContext(req, httptest.NewRecorder(), echo.New())
	if _, err := (&Server{cfg: config.Config{SAMLEnabled: true, SAMLIDPCertFingerprintValidator: "lambda"}}).samlAuthFromCallback(c); err == nil || !strings.Contains(err.Error(), "unsigned") {
		t.Fatalf("expected unsigned response rejection for fingerprint-validator config, got %v", err)
	}
}

func testSAMLSigningCertificate(t *testing.T) (*rsa.PrivateKey, *x509.Certificate, []byte) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(2),
		Subject:               pkix.Name{CommonName: "SAML Test IdP"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return key, cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func signSAMLResponseForTest(t *testing.T, responseXML string, key *rsa.PrivateKey, cert *x509.Certificate) []byte {
	t.Helper()
	doc := etree.NewDocument()
	if err := doc.ReadFromString(responseXML); err != nil {
		t.Fatal(err)
	}
	ctx, err := dsig.NewSigningContext(key, [][]byte{cert.Raw})
	if err != nil {
		t.Fatal(err)
	}
	ctx.IdAttribute = "ID"
	signedRoot, err := ctx.SignEnveloped(doc.Root())
	if err != nil {
		t.Fatal(err)
	}
	doc.SetRoot(signedRoot)
	out, err := doc.WriteToBytes()
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func signSAMLAssertionForTest(t *testing.T, responseXML string, key *rsa.PrivateKey, cert *x509.Certificate) []byte {
	t.Helper()
	doc := etree.NewDocument()
	if err := doc.ReadFromString(responseXML); err != nil {
		t.Fatal(err)
	}
	assertion := firstElementByTagForTest(doc.Root(), "Assertion")
	if assertion == nil || assertion.Parent() == nil {
		t.Fatal("test SAML response did not include an Assertion element with a parent")
	}
	ctx, err := dsig.NewSigningContext(key, [][]byte{cert.Raw})
	if err != nil {
		t.Fatal(err)
	}
	ctx.IdAttribute = "ID"
	signedAssertion, err := ctx.SignEnveloped(samlElementWithInheritedNamespaces(assertion))
	if err != nil {
		t.Fatal(err)
	}
	parent := assertion.Parent()
	parent.RemoveChild(assertion)
	parent.AddChild(signedAssertion)
	out, err := doc.WriteToBytes()
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func encryptedSAMLResponseForTest(t *testing.T, assertionXML string, publicKey *rsa.PublicKey) []byte {
	t.Helper()
	return encryptedSAMLResponseXMLForTest(t, assertionXML, publicKey, false)
}

func encryptedSAMLResponseWithRetrievalMethodForTest(t *testing.T, assertionXML string, publicKey *rsa.PublicKey) []byte {
	t.Helper()
	return encryptedSAMLResponseXMLForTest(t, assertionXML, publicKey, true)
}

func encryptedSAMLResponseXMLForTest(t *testing.T, assertionXML string, publicKey *rsa.PublicKey, referencedKey bool) []byte {
	t.Helper()
	contentKey := make([]byte, 32)
	if _, err := rand.Read(contentKey); err != nil {
		t.Fatal(err)
	}
	block, err := aes.NewCipher(contentKey)
	if err != nil {
		t.Fatal(err)
	}
	iv := make([]byte, block.BlockSize())
	if _, err := rand.Read(iv); err != nil {
		t.Fatal(err)
	}
	plaintext := pkcs7PadForTest([]byte(assertionXML), block.BlockSize())
	ciphertext := make([]byte, len(plaintext))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(ciphertext, plaintext)
	encryptedData := append(append([]byte(nil), iv...), ciphertext...)
	encryptedKey, err := rsa.EncryptOAEP(sha1.New(), rand.Reader, publicKey, contentKey, nil)
	if err != nil {
		t.Fatal(err)
	}
	keyInfo := `<ds:KeyInfo>
        <xenc:EncryptedKey>
          <xenc:EncryptionMethod Algorithm="` + xmlencRSAOAEP + `"></xenc:EncryptionMethod>
          <xenc:CipherData><xenc:CipherValue>` + base64.StdEncoding.EncodeToString(encryptedKey) + `</xenc:CipherValue></xenc:CipherData>
        </xenc:EncryptedKey>
      </ds:KeyInfo>`
	topLevelKey := ""
	if referencedKey {
		keyInfo = `<ds:KeyInfo>
        <ds:RetrievalMethod URI="#paon-test-key" Type="http://www.w3.org/2001/04/xmlenc#EncryptedKey"></ds:RetrievalMethod>
      </ds:KeyInfo>`
		topLevelKey = `
  <xenc:EncryptedKey Id="paon-test-key">
    <xenc:EncryptionMethod Algorithm="` + xmlencRSAOAEP + `"></xenc:EncryptionMethod>
    <xenc:CipherData><xenc:CipherValue>` + base64.StdEncoding.EncodeToString(encryptedKey) + `</xenc:CipherValue></xenc:CipherData>
  </xenc:EncryptedKey>`
	}
	responseXML := `<samlp:Response xmlns:samlp="urn:oasis:names:tc:SAML:2.0:protocol" xmlns:saml="urn:oasis:names:tc:SAML:2.0:assertion" xmlns:xenc="http://www.w3.org/2001/04/xmlenc#" xmlns:ds="http://www.w3.org/2000/09/xmldsig#">
  <samlp:Status><samlp:StatusCode Value="urn:oasis:names:tc:SAML:2.0:status:Success"></samlp:StatusCode></samlp:Status>
  <saml:EncryptedAssertion>
    <xenc:EncryptedData Type="http://www.w3.org/2001/04/xmlenc#Element">
      <xenc:EncryptionMethod Algorithm="` + xmlencAES256CBC + `"></xenc:EncryptionMethod>
      ` + keyInfo + `
      <xenc:CipherData><xenc:CipherValue>` + base64.StdEncoding.EncodeToString(encryptedData) + `</xenc:CipherValue></xenc:CipherData>
    </xenc:EncryptedData>
  </saml:EncryptedAssertion>` + topLevelKey + `
</samlp:Response>`
	return []byte(responseXML)
}

func pkcs7PadForTest(data []byte, blockSize int) []byte {
	padding := blockSize - len(data)%blockSize
	out := append([]byte(nil), data...)
	return append(out, bytes.Repeat([]byte{byte(padding)}, padding)...)
}

func firstElementByTagForTest(el *etree.Element, tag string) *etree.Element {
	if el == nil {
		return nil
	}
	if el.Tag == tag {
		return el
	}
	for _, child := range el.ChildElements() {
		if found := firstElementByTagForTest(child, tag); found != nil {
			return found
		}
	}
	return nil
}

func TestOmniAuthSignedInResourceOverridesIdentityUserLikeRails(t *testing.T) {
	now := time.Now().UTC()
	signedIn := &models.User{ID: 10, Approved: true, ConfirmedAt: sql.NullTime{Time: now, Valid: true}}
	linkedIdentity := models.Identity{
		UserID: sql.NullInt64{Int64: 20, Valid: true},
		User:   models.User{ID: 20, Approved: true, ConfirmedAt: sql.NullTime{Time: now, Valid: true}},
	}
	user, attach, ok := omniauthSignedInOrIdentityUser(signedIn, linkedIdentity)
	if !ok || user.ID != signedIn.ID || attach {
		t.Fatalf("signed-in resource should be returned without reattaching linked identity, user=%#v attach=%v ok=%v", user, attach, ok)
	}
	unlinkedIdentity := models.Identity{}
	user, attach, ok = omniauthSignedInOrIdentityUser(signedIn, unlinkedIdentity)
	if !ok || user.ID != signedIn.ID || !attach {
		t.Fatalf("signed-in resource should attach unlinked identity, user=%#v attach=%v ok=%v", user, attach, ok)
	}
	user, attach, ok = omniauthSignedInOrIdentityUser(nil, linkedIdentity)
	if ok || user != nil || attach {
		t.Fatalf("missing signed-in resource should fall through to identity/reattach/create flow, user=%#v attach=%v ok=%v", user, attach, ok)
	}
}

func TestOmniAuthCallbackUIDAcceptsRailsLikeCallbackForms(t *testing.T) {
	for _, test := range []struct {
		name   string
		target string
		body   string
		want   string
	}{
		{name: "query", target: "/auth/auth/openid_connect/callback?uid=alice", want: "alice"},
		{name: "top-level form", target: "/auth/auth/openid_connect/callback", body: "uid=bob", want: "bob"},
		{name: "omniauth form", target: "/auth/auth/openid_connect/callback", body: "omniauth%5Buid%5D=carol", want: "carol"},
		{name: "user form", target: "/auth/auth/openid_connect/callback", body: "user%5Buid%5D=dave", want: "dave"},
		{name: "EOLE SSO top-level uid array", target: "/auth/auth/openid_connect/callback", body: "uid%5B0%5D%5Buid%5D=eole-uid", want: "eole-uid"},
		{name: "EOLE SSO top-level user array fallback", target: "/auth/auth/openid_connect/callback", body: "uid%5B0%5D%5Buser%5D=eole-user", want: "eole-user"},
		{name: "EOLE SSO omniauth uid array", target: "/auth/auth/openid_connect/callback", body: "omniauth%5Buid%5D%5B0%5D%5Buid%5D=eole-omniauth-uid", want: "eole-omniauth-uid"},
		{name: "EOLE SSO user uid array fallback", target: "/auth/auth/openid_connect/callback", body: "user%5Buid%5D%5B0%5D%5Buser%5D=eole-user-fallback", want: "eole-user-fallback"},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := strings.NewReader(test.body)
			req := httptest.NewRequest(http.MethodPost, test.target, body)
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			rec := httptest.NewRecorder()
			c := echo.NewContext(req, rec, echo.New())
			if got := omniauthCallbackUID(c); got != test.want {
				t.Fatalf("omniauthCallbackUID() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestOmniAuthCallbackStateRequiresMatchingCookieWhenPresent(t *testing.T) {
	for _, test := range []struct {
		name      string
		target    string
		form      string
		cookie    string
		wantValid bool
	}{
		{name: "missing encrypted state rejects", target: "/auth/auth/openid_connect/callback?state=client", wantValid: false},
		{name: "query matches cookie", target: "/auth/auth/openid_connect/callback?state=server", cookie: "server", wantValid: true},
		{name: "form matches cookie", target: "/auth/auth/openid_connect/callback", form: "state=server", cookie: "server", wantValid: true},
		{name: "mismatch rejects", target: "/auth/auth/openid_connect/callback?state=attacker", cookie: "server", wantValid: false},
		{name: "missing request state rejects", target: "/auth/auth/openid_connect/callback", cookie: "server", wantValid: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := &Server{cfg: config.Config{SecretKeyBase: "test-secret"}}
			req := httptest.NewRequest(http.MethodPost, test.target, strings.NewReader(test.form))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			if test.cookie != "" {
				setupRecorder := httptest.NewRecorder()
				setupContext := echo.NewContext(httptest.NewRequest(http.MethodGet, "/auth/auth/openid_connect", nil), setupRecorder, echo.New())
				if err := server.setBrowserOIDCState(setupContext, test.cookie, ""); err != nil {
					t.Fatal(err)
				}
				req.AddCookie(cookiesByName(setupRecorder.Result().Cookies())[browserSessionCookieName])
			}
			rec := httptest.NewRecorder()
			c := echo.NewContext(req, rec, echo.New())
			if got := server.omniauthCallbackStateValid(c); got != test.wantValid {
				t.Fatalf("omniauthCallbackStateValid() = %v, want %v", got, test.wantValid)
			}
		})
	}
}

func TestSignInSupportsOTPAfterPasswordWebAuthnChallenge(t *testing.T) {
	src, err := os.ReadFile("auth.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`return s.signInWithTwoFactorOTP(c, otpAttempt)`,
		`twoFactorSignInHTML(s.packAssetPath("two_factor_authentication.js"), s.webLocale(c, user), hasWebAuthn, preferWebAuthn)`,
		`s.clearBrowserTwoFactorAttempt(c)`,
		`expireCookie(c, webauthnAttemptRedirectCookie, s.cfg.ForceSSL)`,
	} {
		if !strings.Contains(string(src), want) {
			t.Fatalf("auth.go missing two-factor continuation fragment %q", want)
		}
	}
}

func TestRequireFunctionalUserMatchesRailsRequireUserErrors(t *testing.T) {
	now := time.Now()
	server := &Server{}
	checks := []struct {
		name    string
		user    models.User
		status  int
		message string
	}{
		{
			name:    "unconfirmed",
			user:    models.User{Approved: true},
			status:  http.StatusForbidden,
			message: "Your login is missing a confirmed e-mail address",
		},
		{
			name:    "pending",
			user:    models.User{Approved: false, ConfirmedAt: sql.NullTime{Time: now, Valid: true}},
			status:  http.StatusForbidden,
			message: "Your login is currently pending approval",
		},
		{
			name:    "disabled",
			user:    models.User{Disabled: true, Approved: true, ConfirmedAt: sql.NullTime{Time: now, Valid: true}},
			status:  http.StatusForbidden,
			message: "Your login is currently disabled",
		},
		{
			name: "suspended",
			user: models.User{
				Approved:    true,
				ConfirmedAt: sql.NullTime{Time: now, Valid: true},
				Account:     &models.Account{ID: 1, SuspendedAt: sql.NullTime{Time: now, Valid: true}},
			},
			status:  http.StatusForbidden,
			message: "Your login is currently disabled",
		},
		{
			name: "memorial",
			user: models.User{
				Approved:    true,
				ConfirmedAt: sql.NullTime{Time: now, Valid: true},
				Account:     &models.Account{ID: 1, Memorial: true},
			},
			status:  http.StatusForbidden,
			message: "Your login is currently disabled",
		},
		{
			name: "moved",
			user: models.User{
				Approved:    true,
				ConfirmedAt: sql.NullTime{Time: now, Valid: true},
				Account:     &models.Account{ID: 1, MovedToAccountID: sql.NullInt64{Int64: 2, Valid: true}},
			},
			status:  http.StatusForbidden,
			message: "Your login is currently disabled",
		},
	}
	for _, tt := range checks {
		err := server.requireFunctionalUser(nil, tt.user)
		apiErr, ok := err.(apiHTTPError)
		if !ok || apiErr.status != tt.status || apiErr.message != tt.message {
			t.Fatalf("%s error = %#v", tt.name, err)
		}
	}

	healthy := models.User{Approved: true, ConfirmedAt: sql.NullTime{Time: now, Valid: true}, Account: &models.Account{ID: 1}}
	if err := server.requireFunctionalUser(nil, healthy); err != nil {
		t.Fatalf("healthy user rejected: %v", err)
	}
}

func TestRequireUserLoadsDisabledUsersBeforeRailsFunctionalCheck(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		functionName string
		want         string
	}{
		{"requireUser", `user, token, err := s.currentUserIncludingDisabled(c)`},
		{"requireUser", `if err := s.requireFunctionalUser(c, *user); err != nil`},
		{"requireAccount", `user, token, err := s.currentUserIncludingDisabled(c)`},
		{"requireAccount", `if err := s.requireFunctionalUser(c, *user); err != nil`},
	} {
		if !functionBodyContains(t, src, check.functionName, check.want) {
			t.Fatalf("%s missing %q", check.functionName, check.want)
		}
	}
}

func TestRequireUserUpdatesSignInAfterRailsFunctionalCheck(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, fn := range []string{"requireUser", "requireAccount"} {
		body := functionBody(t, src, fn)
		functionalIdx := strings.Index(body, `if err := s.requireFunctionalUser(c, *user); err != nil`)
		updateIdx := strings.Index(body, `if err := s.updateUserSignInIfNeeded(c, user); err != nil`)
		if functionalIdx < 0 || updateIdx < 0 || functionalIdx > updateIdx {
			t.Fatalf("%s must update sign-in tracking only after Rails functional-user checks", fn)
		}
	}
}

func TestTokenResponseMatchesDoorkeeperShape(t *testing.T) {
	created := time.Unix(1_781_789_600, 0).UTC()
	resp := tokenResponse(&models.OAuthAccessToken{
		Token:     "token",
		Scopes:    "read write",
		CreatedAt: created,
	})

	if resp["access_token"] != "token" {
		t.Fatalf("access_token = %#v", resp["access_token"])
	}
	if resp["token_type"] != "Bearer" {
		t.Fatalf("token_type = %#v", resp["token_type"])
	}
	if resp["scope"] != "read write" {
		t.Fatalf("scope = %#v", resp["scope"])
	}
	if resp["created_at"] != created.Unix() {
		t.Fatalf("created_at = %#v", resp["created_at"])
	}
}

func TestTokenResponseIncludesRefreshAndExpiryWhenPresent(t *testing.T) {
	resp := tokenResponse(&models.OAuthAccessToken{
		Token:        "token",
		RefreshToken: sql.NullString{String: "refresh", Valid: true},
		ExpiresIn:    sql.NullInt64{Int64: 3600, Valid: true},
		Scopes:       "read",
		CreatedAt:    time.Unix(1_781_789_600, 0).UTC(),
	})

	if resp["refresh_token"] != "refresh" || resp["expires_in"] != int64(3600) {
		t.Fatalf("response = %#v", resp)
	}
}

func TestOAuthTokenInfoResponseMatchesDoorkeeperShape(t *testing.T) {
	now := time.Unix(1_781_793_200, 0).UTC()
	created := now.Add(-600 * time.Second)
	resp := tokenInfoResponse(models.OAuthAccessToken{
		Scopes:          "read write",
		CreatedAt:       created,
		ExpiresIn:       sql.NullInt64{Int64: 3600, Valid: true},
		ResourceOwnerID: sql.NullInt64{Int64: 42, Valid: true},
	}, "client-uid", now)

	if resp["resource_owner_id"] != int64(42) {
		t.Fatalf("resource_owner_id = %#v", resp["resource_owner_id"])
	}
	if resp["scope"] != "read write" {
		t.Fatalf("scope = %#v", resp["scope"])
	}
	scopes, ok := resp["scopes"].([]string)
	if !ok || len(scopes) != 2 || scopes[0] != "read" || scopes[1] != "write" {
		t.Fatalf("scopes = %#v", resp["scopes"])
	}
	if resp["expires_in"] != int64(3000) {
		t.Fatalf("expires_in = %#v", resp["expires_in"])
	}
	if resp["created_at"] != created.Unix() {
		t.Fatalf("created_at = %#v", resp["created_at"])
	}
	app, ok := resp["application"].(map[string]string)
	if !ok || app["uid"] != "client-uid" {
		t.Fatalf("application = %#v", resp["application"])
	}
}

func TestOAuthTokenInfoInvalidTokenUsesBearerChallenge(t *testing.T) {
	e := echo.New()
	rec := httptest.NewRecorder()
	c := echo.NewContext(httptest.NewRequest(http.MethodGet, "/oauth/token/info", nil), rec, e)

	handleAPIError(c, invalidOAuthTokenError())

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("WWW-Authenticate"); got != `Bearer realm="Doorkeeper"` {
		t.Fatalf("WWW-Authenticate = %q", got)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["error"] != "invalid_token" || body["error_description"] != "The access token is invalid" {
		t.Fatalf("body = %#v", body)
	}
}

func TestOAuthTokenErrorRendersDoorkeeperShape(t *testing.T) {
	e := echo.New()
	rec := httptest.NewRecorder()
	c := echo.NewContext(httptest.NewRequest(http.MethodPost, "/oauth/token", nil), rec, e)

	handleAPIError(c, invalidOAuthClientError())

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("WWW-Authenticate"); got != `Basic realm="Doorkeeper"` {
		t.Fatalf("WWW-Authenticate = %q", got)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["error"] != "invalid_client" || body["error_description"] != "The client credentials were incorrect" {
		t.Fatalf("body = %#v", body)
	}
}

func TestOAuthTokenRequiresExplicitSupportedGrantType(t *testing.T) {
	tests := []struct {
		name string
		body string
		code string
	}{
		{name: "missing", body: "", code: "invalid_request"},
		{name: "unsupported", body: "grant_type=refresh_banana", code: "unsupported_grant_type"},
		{name: "refresh token disabled like rails", body: "grant_type=refresh_token&refresh_token=abc", code: "unsupported_grant_type"},
	}
	for _, tt := range tests {
		req := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(tt.body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		c := echo.NewContext(req, rec, echo.New())

		err := (&Server{db: &gorm.DB{}}).oauthToken(c)
		if err == nil {
			t.Fatalf("%s: expected OAuth error", tt.name)
		}
		handleAPIError(c, err)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, body = %s", tt.name, rec.Code, rec.Body.String())
		}
		var body map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body["error"] != tt.code || body["error_description"] == "" {
			t.Fatalf("%s: body = %#v", tt.name, body)
		}
	}
}

func TestOAuthAccessTokenLastUsedTrackingMatchesRailsFrequency(t *testing.T) {
	now := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)
	if !oauthAccessTokenNeedsLastUsedUpdate(models.OAuthAccessToken{}, now) {
		t.Fatal("never-used token should be tracked")
	}
	if oauthAccessTokenNeedsLastUsedUpdate(models.OAuthAccessToken{LastUsedAt: sql.NullTime{Time: now.Add(-23 * time.Hour), Valid: true}}, now) {
		t.Fatal("recently used token should not be tracked again")
	}
	if !oauthAccessTokenNeedsLastUsedUpdate(models.OAuthAccessToken{LastUsedAt: sql.NullTime{Time: now.Add(-25 * time.Hour), Valid: true}}, now) {
		t.Fatal("stale token should be tracked")
	}
}

func TestAuthenticatedTokenPathsUseSharedLastUsedTracking(t *testing.T) {
	files := map[string][]struct {
		functionName string
		want         string
	}{
		"server.go": {
			{functionName: "currentUserByToken", want: `s.trackAccessTokenUse(c, &accessToken)`},
		},
		"web_push_subscriptions.go": {
			{functionName: "accessTokenFromRequest", want: `s.trackAccessTokenUse(c, &accessToken)`},
		},
		"rails_session_cookie.go": {
			{functionName: "accessTokenForRailsSessionActivation", want: `s.trackAccessTokenUse(c, &accessToken)`},
		},
	}
	for file, checks := range files {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, check := range checks {
			if !functionBodyContains(t, src, check.functionName, check.want) {
				t.Fatalf("%s:%s missing %q", file, check.functionName, check.want)
			}
		}
	}
}

func TestOAuthRevokeRequiresDatabase(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest("POST", "/oauth/revoke", strings.NewReader("token=abc"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	s := &Server{}
	err := s.oauthRevoke(c)
	if err == nil {
		t.Fatal("expected database error")
	}
	handleAPIError(c, err)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestOAuthRevocationMatchesApplication(t *testing.T) {
	token := models.OAuthAccessToken{
		Token:         "access-token",
		RefreshToken:  sql.NullString{String: "refresh-token", Valid: true},
		ApplicationID: sql.NullInt64{Int64: 42, Valid: true},
	}

	if !oauthRevocationMatchesApplication(token, "access-token", 42) {
		t.Fatal("access token did not match owning application")
	}
	if oauthRevocationMatchesApplication(token, " refresh-token ", 42) {
		t.Fatal("padded refresh token matched even though Doorkeeper tokens are opaque")
	}
	if !oauthRevocationMatchesApplication(token, "refresh-token", 42) {
		t.Fatal("refresh token did not match owning application")
	}
	if oauthRevocationMatchesApplication(token, "access-token", 7) {
		t.Fatal("token matched another application")
	}
	token.ApplicationID.Valid = false
	if oauthRevocationMatchesApplication(token, "access-token", 42) {
		t.Fatal("applicationless token matched")
	}
}

func TestOAuthApplicationFromOptionalTokenRequestAllowsMissingClient(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest("POST", "/oauth/token", strings.NewReader("grant_type=password"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c := echo.NewContext(req, httptest.NewRecorder(), e)

	app, ok, err := (&Server{}).oauthApplicationFromOptionalTokenRequest(c)
	if err != nil {
		t.Fatal(err)
	}
	if ok || app != nil {
		t.Fatalf("app = %#v, ok = %v", app, ok)
	}
}

func TestOAuthTokenNoLongerImplementsPasswordGrant(t *testing.T) {
	src, err := os.ReadFile("auth.go")
	if err != nil {
		t.Fatal(err)
	}
	if functionBodyContains(t, src, "oauthToken", `case "password":`) ||
		functionBodyContains(t, src, "oauthToken", `authenticateUserPassword`) {
		t.Fatal("Mastodon 4.4 removed the OAuth resource-owner password grant")
	}
}

func TestOAuthParamReadsJSONBodyOnceLikeRailsParams(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest("POST", "/oauth/token", strings.NewReader(`{
		"grant_type": "client_credentials",
		"client_id": "client-id",
		"client_secret": "client-secret",
		"scope": ["read", "write"]
	}`))
	req.Header.Set("Content-Type", "application/json")
	c := echo.NewContext(req, httptest.NewRecorder(), e)

	if _, err := oauthRequestJSONPayload(c); err != nil {
		t.Fatal(err)
	}
	if got := oauthParam(c, "grant_type"); got != "client_credentials" {
		t.Fatalf("grant_type = %q", got)
	}
	if got := oauthParam(c, "client_id"); got != "client-id" {
		t.Fatalf("client_id = %q", got)
	}
	if got := oauthParam(c, "client_secret"); got != "client-secret" {
		t.Fatalf("client_secret = %q", got)
	}
	if got := oauthParam(c, "scope", "scopes"); got != "read write" {
		t.Fatalf("scope = %q", got)
	}
}

func TestOAuthParamKeepsFormFallback(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest("POST", "/oauth/token", strings.NewReader("grant_type=password&username=alice&scope=read+write"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c := echo.NewContext(req, httptest.NewRecorder(), e)

	if got := oauthParam(c, "grant_type"); got != "password" {
		t.Fatalf("grant_type = %q", got)
	}
	if got := oauthParam(c, "username"); got != "alice" {
		t.Fatalf("username = %q", got)
	}
	if got := oauthParam(c, "scope"); got != "read write" {
		t.Fatalf("scope = %q", got)
	}
}

func TestOAuthScopeParamAcceptsScopeAliasesAcrossRequestShapes(t *testing.T) {
	e := echo.New()
	tests := []struct {
		name        string
		method      string
		target      string
		body        string
		contentType string
		want        string
	}{
		{name: "json scopes", method: http.MethodPost, target: "/oauth/token", body: `{"scopes":["read","write"]}`, contentType: "application/json", want: "read write"},
		{name: "form scopes", method: http.MethodPost, target: "/oauth/token", body: "scopes=read+follow", contentType: "application/x-www-form-urlencoded", want: "read follow"},
		{name: "query scopes", method: http.MethodGet, target: "/oauth/authorize?scopes=read+push", want: "read push"},
		{name: "fallback", method: http.MethodGet, target: "/oauth/authorize", want: "read"},
	}
	for _, tt := range tests {
		req := httptest.NewRequest(tt.method, tt.target, strings.NewReader(tt.body))
		if tt.contentType != "" {
			req.Header.Set("Content-Type", tt.contentType)
		}
		c := echo.NewContext(req, httptest.NewRecorder(), e)
		if got := oauthScopeParam(c, "read"); got != tt.want {
			t.Fatalf("%s: oauthScopeParam = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestAuthorizationRequestAcceptsScopesAliasLikeTokenRequests(t *testing.T) {
	src, err := os.ReadFile("auth.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, src, "authorizationRequest", `normalizeRequestedScopes(oauthScopeParam(c, "read"), app.Scopes)`) {
		t.Fatal("authorizationRequest should accept the scopes alias as a Rails-compatible client tolerance")
	}
}

func TestSafeRedirectAllowsOnlyLocalPaths(t *testing.T) {
	if safeRedirect("/home") != "/home" {
		t.Fatal("local redirect was not preserved")
	}
	if safeRedirect("https://evil.example") != "/home" {
		t.Fatal("absolute redirect was allowed")
	}
	if safeRedirect("//evil.example") != "/home" {
		t.Fatal("scheme-relative redirect was allowed")
	}
}

func TestSetSessionCookieExpiresRailsSessionCookie(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	if err := (&Server{}).setSessionCookie(c, "access-token"); err != nil {
		t.Fatal(err)
	}

	cookies := cookiesByName(rec.Result().Cookies())
	session := cookies[sessionCookieName]
	if session == nil {
		t.Fatal("paon session cookie was not set")
	}
	if session.Value != "access-token" || session.Path != "/" || session.MaxAge <= 0 || !session.HttpOnly || session.SameSite != http.SameSiteLaxMode {
		t.Fatalf("paon session cookie = %#v", session)
	}
	if session.MaxAge < int((300 * 24 * time.Hour).Seconds()) {
		t.Fatalf("paon session cookie should use the Rails one-year lifetime: %#v", session)
	}
	if cookies[browserSessionCookieName] == nil {
		t.Fatal("encrypted browser session cookie was not set")
	}
	rails := cookies[railsSessionCookieName]
	if rails == nil {
		t.Fatal("Rails session cookie was not expired")
	}
	if rails.Value != "" || rails.Path != "/" || rails.MaxAge != -1 || !rails.HttpOnly || rails.SameSite != http.SameSiteLaxMode {
		t.Fatalf("Rails session cookie = %#v", rails)
	}
	railsSessionID := cookies[railsSessionIDCookieName]
	if railsSessionID == nil {
		t.Fatal("Rails signed session id cookie was not expired")
	}
	if railsSessionID.Value != "" || railsSessionID.Path != "/" || railsSessionID.MaxAge != -1 || !railsSessionID.HttpOnly || railsSessionID.SameSite != http.SameSiteLaxMode {
		t.Fatalf("Rails signed session id cookie = %#v", railsSessionID)
	}
}

func TestClearSessionCookieExpiresGoAndRailsSessionCookies(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	clearSessionCookie(c, true)

	cookies := cookiesByName(rec.Result().Cookies())
	for _, name := range []string{sessionCookieName, railsSessionCookieName, railsSessionIDCookieName, browserSessionCookieName} {
		cookie := cookies[name]
		if cookie == nil {
			t.Fatalf("%s was not expired", name)
		}
		if cookie.Value != "" || cookie.Path != "/" || cookie.MaxAge != -1 || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || !cookie.Secure {
			t.Fatalf("%s cookie = %#v", name, cookie)
		}
	}
}

func cookiesByName(cookies []*http.Cookie) map[string]*http.Cookie {
	out := make(map[string]*http.Cookie, len(cookies))
	for _, cookie := range cookies {
		out[cookie.Name] = cookie
	}
	return out
}

func TestTokenHasAnyScopeMatchesAdminScopes(t *testing.T) {
	if !tokenHasAnyScope("read admin:read:accounts", "admin:read:accounts", "admin:read") {
		t.Fatal("specific admin read scope was not accepted")
	}
	if !tokenHasAnyScope("read admin:read", "admin:read:reports", "admin:read") {
		t.Fatal("global admin read scope was not accepted")
	}
	if tokenHasAnyScope("read write", "admin:read:accounts", "admin:read") {
		t.Fatal("non-admin scopes were accepted")
	}
}

func TestOAuthScopeFallbackMatrixMatchesRailsDoorkeeperUsage(t *testing.T) {
	tests := []struct {
		name     string
		token    string
		required []string
		want     bool
	}{
		{name: "read umbrella covers statuses", token: "read", required: []string{"read", "read:statuses"}, want: true},
		{name: "specific read status covers statuses", token: "read:statuses", required: []string{"read", "read:statuses"}, want: true},
		{name: "unrelated read scope rejected", token: "read:notifications", required: []string{"read", "read:statuses"}, want: false},
		{name: "write umbrella covers accounts", token: "write", required: []string{"write", "write:accounts"}, want: true},
		{name: "specific write account covers accounts", token: "write:accounts", required: []string{"write", "write:accounts"}, want: true},
		{name: "specific write status covers posting", token: "read write:media write:statuses", required: []string{"write", "write:statuses"}, want: true},
		{name: "media write does not cover posting", token: "read write:media", required: []string{"write", "write:statuses"}, want: false},
		{name: "follow umbrella covers follows", token: "follow", required: []string{"follow", "read", "read:follows"}, want: true},
		{name: "specific read follows covers follows", token: "read:follows", required: []string{"follow", "read", "read:follows"}, want: true},
		{name: "push stays separate from write", token: "write", required: []string{"push"}, want: false},
		{name: "push accepted for webpush", token: "push", required: []string{"push"}, want: true},
		{name: "admin read fallback accepted", token: "admin:read", required: []string{"admin:read:reports", "admin:read"}, want: true},
		{name: "admin write fallback accepted", token: "admin:write", required: []string{"admin:write:accounts", "admin:write"}, want: true},
		{name: "admin specific does not imply global write", token: "admin:write:accounts", required: []string{"admin:write:reports", "admin:write"}, want: false},
	}
	for _, tt := range tests {
		if got := tokenHasAnyScope(tt.token, tt.required...); got != tt.want {
			t.Fatalf("%s: tokenHasAnyScope(%q, %#v) = %v, want %v", tt.name, tt.token, tt.required, got, tt.want)
		}
	}
}

func TestOAuthRedirectAndScopeHelpers(t *testing.T) {
	redirects := "https://app.example/callback\nurn:ietf:wg:oauth:2.0:oob"
	if firstRedirectURI(redirects) != "https://app.example/callback" {
		t.Fatalf("first redirect = %q", firstRedirectURI(redirects))
	}
	if !redirectURIMatches(redirects, "urn:ietf:wg:oauth:2.0:oob") {
		t.Fatal("redirect URI did not match")
	}
	if redirectURIMatches(redirects, "https://evil.example/callback") {
		t.Fatal("unregistered redirect URI matched")
	}
	if scopes := normalizeRequestedScopes("read write admin:read", "read write"); scopes != "read write" {
		t.Fatalf("scopes = %q", scopes)
	}
}

func TestOAuthAccessTokenReusableRequiresSameUnexpiredScopeSet(t *testing.T) {
	now := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)
	token := models.OAuthAccessToken{
		Scopes:    "read write",
		CreatedAt: now.Add(-time.Minute),
	}

	if !oauthAccessTokenReusable(token, "write read", now) {
		t.Fatal("same scope set in a different order was not reusable")
	}
	if oauthAccessTokenReusable(token, "read", now) {
		t.Fatal("broader existing token was reused for a narrower request")
	}
	if oauthAccessTokenReusable(token, "read write follow", now) {
		t.Fatal("narrower existing token was reused for a broader request")
	}

	token.ExpiresIn = sql.NullInt64{Int64: 30, Valid: true}
	if oauthAccessTokenReusable(token, "read write", now) {
		t.Fatal("expired access token was reusable")
	}
	token.ExpiresIn = sql.NullInt64{}
	token.RevokedAt = sql.NullTime{Time: now, Valid: true}
	if oauthAccessTokenReusable(token, "read write", now) {
		t.Fatal("revoked access token was reusable")
	}
}

func TestOAuthAuthorizationDeniedMatchesDoorkeeperDelete(t *testing.T) {
	tests := []struct {
		method string
		body   string
		denied bool
	}{
		{method: http.MethodDelete, denied: true},
		{method: http.MethodPost, body: "_method=delete", denied: true},
		{method: http.MethodPost, body: "deny=1", denied: false},
		{method: http.MethodPost, body: "deny=", denied: false},
		{method: http.MethodPost, body: "allow=1", denied: false},
	}
	for _, test := range tests {
		req := httptest.NewRequest(test.method, "/oauth/authorize", strings.NewReader(test.body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		c := echo.NewContext(req, httptest.NewRecorder(), echo.New())
		if got := oauthAuthorizationDenied(c); got != test.denied {
			t.Fatalf("%s %q denied = %v, want %v", test.method, test.body, got, test.denied)
		}
	}
}

func TestOAuthAuthorizeUsesRailsPrivateNoStoreCacheHeaders(t *testing.T) {
	src, err := os.ReadFile("auth.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		functionName string
		want         string
	}{
		{"oauthAuthorize", `setOAuthAuthorizeCacheHeaders(c)`},
		{"oauthAuthorizeDecision", `setOAuthAuthorizeCacheHeaders(c)`},
		{"setOAuthAuthorizeCacheHeaders", `setPrivateNoStoreCacheHeaders(c)`},
		{"setPrivateNoStoreCacheHeaders", `"Cache-Control", "private, no-store"`},
	} {
		if !functionBodyContains(t, src, check.functionName, check.want) {
			t.Fatalf("%s missing %q", check.functionName, check.want)
		}
	}
}

func TestRedirectOAuthErrorIncludesDescriptionAndState(t *testing.T) {
	e := echo.New()
	rec := httptest.NewRecorder()
	c := echo.NewContext(httptest.NewRequest(http.MethodPost, "/oauth/authorize", nil), rec, e)

	err := redirectOAuthError(c, "https://app.example/callback?existing=1", "access_denied", "The resource owner or authorization server denied the request.", "client-state")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d", rec.Code)
	}
	location, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	query := location.Query()
	if query.Get("error") != "access_denied" ||
		query.Get("error_description") != "The resource owner or authorization server denied the request." ||
		query.Get("state") != "client-state" ||
		query.Get("existing") != "1" {
		t.Fatalf("redirect query = %s", location.RawQuery)
	}
}

func TestOAuthGrantExpiry(t *testing.T) {
	created := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	grant := models.OAuthAccessGrant{CreatedAt: created, ExpiresIn: 600}
	if grantExpired(grant, created.Add(599*time.Second)) {
		t.Fatal("grant expired too early")
	}
	if !grantExpired(grant, created.Add(601*time.Second)) {
		t.Fatal("grant did not expire")
	}
}

func TestPKCEAuthorizationCodeVerification(t *testing.T) {
	verifier := "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ1234567890-._~"
	plainCode := authorizationCodeToken("plain", verifier)
	if !verifyPKCECode(plainCode, verifier) {
		t.Fatal("plain PKCE verifier was rejected")
	}
	if verifyPKCECode(plainCode, verifier+"x") {
		t.Fatal("wrong plain PKCE verifier was accepted")
	}

	digest := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(digest[:])
	s256Code := authorizationCodeToken("S256", challenge)
	if !verifyPKCECode(s256Code, verifier) {
		t.Fatal("S256 PKCE verifier was rejected")
	}
	if verifyPKCECode(s256Code, "short") {
		t.Fatal("invalid S256 verifier was accepted")
	}
}

func TestPKCEChallengeValidation(t *testing.T) {
	valid := "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ1234567890-._~"
	if !validPKCEChallenge("S256", valid) || !validPKCEChallenge("plain", valid) {
		t.Fatal("valid PKCE challenge rejected")
	}
	if validPKCEChallenge("bad", valid) {
		t.Fatal("invalid PKCE method accepted")
	}
	if validPKCEChallenge("S256", "short") {
		t.Fatal("short PKCE challenge accepted")
	}
}

func TestSuspiciousSignInUsesRailsIPTolerance(t *testing.T) {
	user := models.User{CurrentSignInAt: sql.NullTime{Time: time.Now().UTC(), Valid: true}}

	if suspiciousSignInFromSeenIPs(user, "192.0.2.44", []string{"192.0.99.10"}) {
		t.Fatal("same IPv4 /16 should not be suspicious")
	}
	if !suspiciousSignInFromSeenIPs(user, "192.0.2.44", []string{"198.51.100.10"}) {
		t.Fatal("new IPv4 /16 should be suspicious")
	}
	if suspiciousSignInFromSeenIPs(user, "2001:db8:abcd:12::44", []string{"2001:db8:abcd:12::99"}) {
		t.Fatal("same IPv6 /64 should not be suspicious")
	}
	if !suspiciousSignInFromSeenIPs(user, "2001:db8:abcd:13::44", []string{"2001:db8:abcd:12::99"}) {
		t.Fatal("new IPv6 /64 should be suspicious")
	}
}

func TestSuspiciousSignInSkipsFreshOrProtectedAccounts(t *testing.T) {
	fresh := models.User{}
	if suspiciousSignInFromSeenIPs(fresh, "203.0.113.10", nil) {
		t.Fatal("first sign-in should not be suspicious")
	}

	protected := models.User{
		CurrentSignInAt:     sql.NullTime{Time: time.Now().UTC(), Valid: true},
		OTPRequiredForLogin: true,
	}
	if suspiciousSignInFromSeenIPs(protected, "203.0.113.10", nil) {
		t.Fatal("OTP-protected user should not be suspicious")
	}
	if suspiciousSignInFromSeenIPs(models.User{CurrentSignInAt: protected.CurrentSignInAt}, "not an ip", nil) {
		t.Fatal("invalid remote IP should not be suspicious")
	}
}
