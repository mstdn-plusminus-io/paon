package api

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha1"
	"database/sql"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/crypto/pbkdf2"
)

func TestOTPQRCodeDataURIEncodesProvisioningURI(t *testing.T) {
	dataURI := otpQRCodeDataURI("abcd efgh ijkl mnop", "Paon Test", "alice@example.test")
	const prefix = "data:image/png;base64,"
	if !strings.HasPrefix(dataURI, prefix) {
		t.Fatalf("QR data URI = %q", dataURI)
	}
	png, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(dataURI, prefix))
	if err != nil {
		t.Fatal(err)
	}
	if len(png) < 8 || string(png[:8]) != "\x89PNG\r\n\x1a\n" {
		t.Fatalf("QR output is not PNG: %x", png)
	}
}

func TestTwoFactorConfirmationSettingsHTMLRendersQRCode(t *testing.T) {
	html := twoFactorConfirmationHTMLWithProvisioning("ABCDEFGHIJKLMNOP", "", "Paon", "alice@example.test", "en", "default", `<ul><li id="security"></li></ul>`)
	for _, want := range []string{
		`class="admin theme-default no-reduce-motion"`,
		`class="qr-wrapper"`,
		`data:image/png;base64,`,
		`ABCD EFGH IJKL MNOP`,
		`class="simple_form new_form_two_factor_confirmation"`,
		`id="new_form_two_factor_confirmation"`,
		`class="input with_label string required form_two_factor_confirmation_otp_attempt field_with_hint"`,
		`class="label_input__wrapper"`,
		`name="form_two_factor_confirmation[otp_attempt]"`,
		`class="btn"`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("two-factor confirmation HTML missing %q: %s", want, html)
		}
	}
}

func TestSettingsTwoFactorDisableRequiresWebAuthentication(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodPost, "/settings/two_factor_authentication_methods/disable", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())

	if err := s.disableSettingsTwoFactor(c); err != nil {
		t.Fatal(err)
	}
	want := "/auth/sign_in?redirect_to=" + url.QueryEscape("/settings/two_factor_authentication_methods/disable")
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != want {
		t.Fatalf("status = %d location = %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestSettingsRequireChallengeMatchesRailsSkipAndTimeout(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/settings/otp_authentication", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())
	server := &Server{cfg: config.Config{SecretKeyBase: "test-secret"}}
	user := &models.User{ID: 1, EncryptedPassword: ""}
	handled, err := server.settingsRequireChallenge(c, user, "/settings/otp_authentication", "en")
	if err != nil || handled {
		t.Fatalf("blank encrypted password should skip challenge like Rails, handled=%v err=%v", handled, err)
	}

	req = httptest.NewRequest(http.MethodPost, "/settings/otp_authentication", nil)
	rec = httptest.NewRecorder()
	c = echo.NewContext(req, rec, echo.New())
	user.EncryptedPassword = "hash"
	handled, err = server.settingsRequireChallenge(c, user, "/settings/otp_authentication", "en")
	if err != nil {
		t.Fatal(err)
	}
	if !handled || rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `name="form_challenge[return_to]"`) {
		t.Fatalf("missing challenge should render challenge form, handled=%v status=%d body=%s", handled, rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `action="/settings/otp_authentication"`) {
		t.Fatalf("POST challenge form must post back to the original protected action like Rails: %s", rec.Body.String())
	}

	setupRecorder := httptest.NewRecorder()
	setupContext := echo.NewContext(httptest.NewRequest(http.MethodPost, "/auth/challenge", nil), setupRecorder, echo.New())
	if err := server.setBrowserChallengePassed(setupContext, user.ID); err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest(http.MethodPost, "/settings/otp_authentication", nil)
	req.AddCookie(cookiesByName(setupRecorder.Result().Cookies())[browserSessionCookieName])
	rec = httptest.NewRecorder()
	c = echo.NewContext(req, rec, echo.New())
	handled, err = server.settingsRequireChallenge(c, user, "/settings/otp_authentication", "en")
	if err != nil || handled {
		t.Fatalf("challenge within Rails one-hour timeout should pass, handled=%v err=%v", handled, err)
	}
}

func TestSettingsRequireChallengeValidatesPostedPasswordLikeRailsConcern(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{cfg: config.Config{SecretKeyBase: "test-secret"}}
	user := &models.User{ID: 1, EncryptedPassword: string(hash)}

	for _, test := range []struct {
		name     string
		password string
		passed   bool
	}{
		{name: "invalid", password: "wrong", passed: false},
		{name: "valid", password: "secret", passed: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			form := url.Values{
				"form_challenge[return_to]":        {"/settings/otp_authentication"},
				"form_challenge[current_password]": {test.password},
			}
			req := httptest.NewRequest(http.MethodPost, "/settings/otp_authentication", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			rec := httptest.NewRecorder()
			c := echo.NewContext(req, rec, echo.New())

			handled, err := server.settingsRequireChallenge(c, user, "/settings/otp_authentication", "en")
			if err != nil {
				t.Fatal(err)
			}
			if test.passed {
				if handled {
					t.Fatal("valid password must continue the protected action")
				}
				cookie := cookiesByName(rec.Result().Cookies())[browserSessionCookieName]
				if cookie == nil {
					t.Fatal("valid password did not persist challenge state")
				}
				verifyReq := httptest.NewRequest(http.MethodPost, "/settings/otp_authentication", nil)
				verifyReq.AddCookie(cookie)
				verifyContext := echo.NewContext(verifyReq, httptest.NewRecorder(), echo.New())
				if !server.browserChallengePassedRecently(verifyContext, user.ID) {
					t.Fatal("persisted challenge state was not accepted for the user")
				}
				return
			}
			if !handled || rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Invalid password") {
				t.Fatalf("invalid password handled=%v status=%d body=%s", handled, rec.Code, rec.Body.String())
			}
		})
	}
}

func TestSettingsTwoFactorConfirmationRequiresRailsRootParam(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/settings/two_factor_authentication/confirmation", strings.NewReader("otp_attempt=123456"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c := echo.NewContext(req, httptest.NewRecorder(), echo.New())
	_, err := twoFactorConfirmationAttempt(c)
	if !errors.Is(err, errTwoFactorConfirmationParamsMissing) {
		t.Fatalf("top-level fallback form should be rejected like Rails params.require, got %v", err)
	}

	req = httptest.NewRequest(http.MethodPost, "/settings/two_factor_authentication/confirmation", strings.NewReader("form_two_factor_confirmation%5Botp_attempt%5D=123456"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c = echo.NewContext(req, httptest.NewRecorder(), echo.New())
	attempt, err := twoFactorConfirmationAttempt(c)
	if err != nil {
		t.Fatal(err)
	}
	if attempt != "123456" {
		t.Fatalf("nested form parsed otp=%q", attempt)
	}

	src, err := os.ReadFile("two_factor_settings.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(src), `c.FormValue("otp_attempt")`) {
		t.Fatal("createSettingsTwoFactorConfirmation must not accept top-level otp_attempt fallback")
	}
}

func TestSettingsOTPAuthenticationRequiresWebAuthentication(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodGet, "/settings/otp_authentication", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())

	if err := s.settingsOTPAuthenticationPage(c); err != nil {
		t.Fatal(err)
	}
	want := "/auth/sign_in?redirect_to=" + url.QueryEscape("/settings/otp_authentication")
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != want {
		t.Fatalf("status = %d location = %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestSettingsTwoFactorConfirmationRequiresWebAuthentication(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodPost, "/settings/two_factor_authentication/confirmation", strings.NewReader("form_two_factor_confirmation%5Botp_attempt%5D=123456"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "paon_new_otp_secret", Value: "ABCDEF234567"})
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())

	if err := s.createSettingsTwoFactorConfirmation(c); err != nil {
		t.Fatal(err)
	}
	want := "/auth/sign_in?redirect_to=" + url.QueryEscape("/settings/two_factor_authentication/confirmation")
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != want {
		t.Fatalf("status = %d location = %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestSettingsRecoveryCodesRequireWebAuthentication(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodPost, "/settings/two_factor_authentication/recovery_codes", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())

	if err := s.createSettingsRecoveryCodes(c); err != nil {
		t.Fatal(err)
	}
	want := "/auth/sign_in?redirect_to=" + url.QueryEscape("/settings/two_factor_authentication/recovery_codes")
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != want {
		t.Fatalf("status = %d location = %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestGenerateOTPSecretMatchesMastodonLength(t *testing.T) {
	secret, err := generateOTPSecret(32)
	if err != nil {
		t.Fatal(err)
	}
	if len(secret) != 32 {
		t.Fatalf("len(secret) = %d, want 32", len(secret))
	}
	if strings.Contains(secret, "=") {
		t.Fatalf("secret contains base32 padding: %q", secret)
	}
}

func TestTwoFactorConfirmationHTMLDoesNotExposeRawMarkup(t *testing.T) {
	html := twoFactorConfirmationHTML("<secret>", "", "en")
	if strings.Contains(html, "<secret>") || !strings.Contains(html, "&lt;secret&gt;") {
		t.Fatalf("secret was not escaped: %s", html)
	}
}

func TestTOTPMatchesRFCVectorWithSixDigits(t *testing.T) {
	secret := "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
	if !validTOTP(secret, "287082", time.Unix(59, 0).UTC()) {
		t.Fatal("expected RFC 6238 SHA1 vector to validate as six-digit TOTP")
	}
	if validTOTP(secret, "000000", time.Unix(59, 0).UTC()) {
		t.Fatal("unexpected invalid TOTP acceptance")
	}
}

func TestGoOTPSecretFromUserRequiresPrefix(t *testing.T) {
	user := &models.User{EncryptedOTPSecret: sql.NullString{String: goOTPSecretPrefix + " abcd ef23 ", Valid: true}}
	secret, ok := goOTPSecretFromUser(user)
	if !ok || secret != "ABCDEF23" {
		t.Fatalf("secret = %q ok = %v", secret, ok)
	}
	legacy := &models.User{EncryptedOTPSecret: sql.NullString{String: "rails-encrypted-value", Valid: true}}
	if _, ok := goOTPSecretFromUser(legacy); ok {
		t.Fatal("legacy Rails encrypted OTP secret should not be treated as Go-managed")
	}
}

func TestLegacyOTPSecretDecryptsAttrEncryptedGCM(t *testing.T) {
	key := "1fc2b87989afa6351912abeebe31ffc5c476ead9bf8b3d74cbc4a302c7b69a45b40b1bbef3506ddad73e942e15ed5ca4b402bf9a66423626051104f4b5f05109"
	secret := "JBSWY3DPEHPK3PXP"
	encrypted, iv, salt := legacyOTPEncryptedFixture(t, secret, key)
	got, err := decryptLegacyOTPSecret(encrypted, iv, salt, key)
	if err != nil {
		t.Fatal(err)
	}
	if got != secret {
		t.Fatalf("legacy secret = %q, want %q", got, secret)
	}

	server := &Server{cfg: config.Config{OTPSecret: key}}
	model := &models.User{
		EncryptedOTPSecret:     sql.NullString{String: encrypted, Valid: true},
		EncryptedOTPSecretIV:   sql.NullString{String: iv, Valid: true},
		EncryptedOTPSecretSalt: sql.NullString{String: salt, Valid: true},
	}
	if got, ok := server.legacyOTPSecretFromUser(model); !ok || got != secret {
		t.Fatalf("legacyOTPSecretFromUser = %q/%v", got, ok)
	}
}

func legacyOTPEncryptedFixture(t *testing.T, secret string, otpSecretKey string) (string, string, string) {
	t.Helper()
	iv := []byte("123456789012")
	salt := []byte("abcdefghijklmnop")
	key := pbkdf2.Key([]byte(otpSecretKey), salt, 2000, 32, sha1.New)
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	ciphertext := gcm.Seal(nil, iv, []byte(secret), []byte(""))
	return base64.StdEncoding.EncodeToString(ciphertext),
		base64.StdEncoding.EncodeToString(iv),
		"$" + base64.StdEncoding.EncodeToString(salt)
}

func TestRecoveryCodesAreGeneratedAndMatchedByHash(t *testing.T) {
	codes, err := generateRecoveryCodes(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(codes) != 10 {
		t.Fatalf("len(codes) = %d", len(codes))
	}
	seen := map[string]struct{}{}
	for _, code := range codes {
		if len(code) != 10 {
			t.Fatalf("recovery code length = %d for %q", len(code), code)
		}
		if _, ok := seen[code]; ok {
			t.Fatalf("duplicate recovery code %q", code)
		}
		seen[code] = struct{}{}
	}

	hash, err := recoveryCodeHash("ABCD-EF23")
	if err != nil {
		t.Fatal(err)
	}
	if !recoveryCodeMatches(hash, "abcd ef23") {
		t.Fatal("recovery code did not match bcrypt hash after normalization")
	}
	if recoveryCodeMatches(hash, "wrong-code") {
		t.Fatal("invalid recovery code matched bcrypt hash")
	}
}

func TestRecoveryCodesHTMLDoesNotExposeRawMarkup(t *testing.T) {
	html := recoveryCodesHTML("notice", []string{"<code>"}, "en")
	if strings.Contains(html, "<code></code>") || !strings.Contains(html, "&lt;code&gt;") {
		t.Fatalf("recovery code was not escaped: %s", html)
	}
}

func TestAuthFormExposesOTPAttemptButPasswordGrantRejectsOTPUsersLikeRails(t *testing.T) {
	authSrc, err := os.ReadFile("auth.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`name="user[otp_attempt]"`,
		`user, err := s.authenticateUserPassword(email, password)`,
		`if user.OTPRequiredForLogin {`,
		`s.validateAndConsumeUserOTP(user, otpAttempt, time.Now().UTC())`,
	} {
		if !strings.Contains(string(authSrc), want) {
			t.Fatalf("auth OTP path missing %q", want)
		}
	}
	if functionBodyContains(t, authSrc, "oauthToken", `authenticateUserWithOTP`) ||
		functionBodyContains(t, authSrc, "oauthToken", `otp_attempt`) {
		t.Fatal("OAuth password grant must not accept OTP attempts; Rails Doorkeeper rejects OTP users for password grant")
	}

	settingsSrc, err := os.ReadFile("two_factor_settings.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`goOTPSecretPrefix + secret`,
		`"otp_required_for_login":    true`,
		`validTOTP(secret, attempt, time.Now().UTC())`,
		`s.regenerateRecoveryCodesForUser(user.ID)`,
		`s.consumeUserRecoveryCode(user, attempt, now)`,
		`s.legacyOTPSecretFromUser(user)`,
		`"consumed_timestep": step`,
		`"otp_backup_codes": hashes`,
	} {
		if !strings.Contains(string(settingsSrc), want) {
			t.Fatalf("settings OTP path missing %q", want)
		}
	}
}
