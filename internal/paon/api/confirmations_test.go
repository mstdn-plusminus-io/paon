package api

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestNewAuthConfirmationRendersForm(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodGet, "/auth/confirmation/new?email=alice%40example.test", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())

	if err := s.newAuthConfirmation(c); err != nil {
		t.Fatal(err)
	}
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, `value="alice@example.test"`) || !strings.Contains(body, `action="/auth/confirmation"`) {
		t.Fatalf("status = %d body = %s", rec.Code, body)
	}
}

func TestShowAuthConfirmationWithoutTokenRendersResendForm(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodGet, "/auth/confirmation", nil)
	req.Header.Set("Accept-Language", "en")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())

	if err := s.showAuthConfirmation(c); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Resend confirmation") {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
}

func TestShowAuthConfirmationRejectsInvalidTokenWithoutDatabase(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodGet, "/auth/confirmation?confirmation_token=bad", nil)
	req.Header.Set("Accept-Language", "en")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())

	if err := s.showAuthConfirmation(c); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "Confirmation token is invalid") {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
}

func TestCreateAuthConfirmationRequiresEmail(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodPost, "/auth/confirmation", strings.NewReader("user%5Bemail%5D=bad"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())

	if err := s.createAuthConfirmation(c); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "Email is invalid") {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
}

func TestConfirmCaptchaRedirectsToConfirmationRoute(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodPost, "/auth/captcha_confirmation", strings.NewReader("confirmation_token=abc"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())

	if err := s.confirmCaptcha(c); err != nil {
		t.Fatal(err)
	}
	want := "/auth/confirmation?confirmation_token=" + url.QueryEscape("abc")
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != want {
		t.Fatalf("status = %d location = %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestConfirmCaptchaRedirectPreservesOpaqueToken(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodPost, "/auth/captcha_confirmation?confirmation_token=query-token", strings.NewReader("confirmation_token=+abc+"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())

	if err := s.confirmCaptcha(c); err != nil {
		t.Fatal(err)
	}
	want := "/auth/confirmation?confirmation_token=" + url.QueryEscape(" abc ")
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != want {
		t.Fatalf("status = %d location = %q want %q", rec.Code, rec.Header().Get("Location"), want)
	}
}

func TestAuthConfirmationUsesCaptchaGateWhenConfigured(t *testing.T) {
	src, err := os.ReadFile("confirmations.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`required, err := s.confirmationCaptchaRequired(token)`,
		`return c.HTML(http.StatusOK, s.confirmationCaptchaHTML(token, "", locale))`,
		`return s.redirectAfterConfirmation(c, user)`,
		`return !user.ConfirmedAt.Valid, nil`,
	} {
		if !strings.Contains(string(src), want) {
			t.Fatalf("confirmations.go missing %q", want)
		}
	}
}

func TestConfirmCaptchaVerifiesRailsCaptchaBeforeConfirmingToken(t *testing.T) {
	src, err := os.ReadFile("auth_helpers.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`required, err := s.confirmationCaptchaRequired(token)`,
		`s.checkHCaptcha(c, c.FormValue("h-captcha-response"))`,
		`s.checkCloudflareTurnstile(c, c.FormValue("cf-turnstile-response"))`,
		`s.confirmUserByToken(token)`,
		`return s.redirectAfterConfirmation(c, user)`,
	} {
		if !strings.Contains(string(src), want) {
			t.Fatalf("auth_helpers.go missing %q", want)
		}
	}
}

func TestAuthConfirmationMessagesUseRailsLocaleKeys(t *testing.T) {
	confirmationSrc, err := os.ReadFile("confirmations.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`authInvalidEmailMessage(locale)`,
		`authConfirmationEmailNotFoundMessage(locale)`,
		`authConfirmationInstructionsQueuedMessage(locale)`,
		`authConfirmationTokenInvalidMessage(locale)`,
		`settingsT(locale, "devise.confirmations.confirmed"`,
	} {
		if !strings.Contains(string(confirmationSrc), want) {
			t.Fatalf("confirmations.go missing localized message fragment %q", want)
		}
	}
	helperSrc, err := os.ReadFile("auth_helpers.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`settingsT(locale, "users.invalid_email"`,
		`settingsT(locale, "auth.confirmations.invalid_token"`,
		`settingsT(locale, "auth.confirmations.email_not_found"`,
		`authConfirmationTokenInvalidMessage(locale)`,
	} {
		if !strings.Contains(string(helperSrc), want) {
			t.Fatalf("auth_helpers.go missing localized message fragment %q", want)
		}
	}
}

func TestConfirmationCaptchaHTMLIncludesHCaptchaWhenConfigured(t *testing.T) {
	s := &Server{}
	s.cfg.HCaptchaSiteKey = `h-site"><script>`
	s.cfg.HCaptchaSecretKey = "h-secret"

	body := s.confirmationCaptchaHTML(`abc"><script>`, "bad captcha", "en")
	for _, want := range []string{
		`action="/auth/captcha_confirmation"`,
		`name="confirmation_token"`,
		`value="abc&#34;&gt;&lt;script&gt;"`,
		`https://js.hcaptcha.com/1/api.js`,
		`class="h-captcha"`,
		`data-sitekey="h-site&#34;&gt;&lt;script&gt;"`,
		`bad captcha`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("captcha HTML missing %q: %s", want, body)
		}
	}
	if strings.Contains(body, "challenges.cloudflare.com") {
		t.Fatalf("hCaptcha should take precedence over Turnstile: %s", body)
	}
}

func TestConfirmationCaptchaHTMLIncludesTurnstileAndToken(t *testing.T) {
	s := &Server{}
	s.cfg.CloudflareTurnstileEnabled = true
	s.cfg.CloudflareTurnstileSiteKey = `site-key"><script>`

	body := s.confirmationCaptchaHTML(`abc"><script>`, "bad captcha", "en")
	for _, want := range []string{
		`action="/auth/captcha_confirmation"`,
		`name="confirmation_token"`,
		`value="abc&#34;&gt;&lt;script&gt;"`,
		`https://challenges.cloudflare.com/turnstile/v0/api.js`,
		`data-sitekey="site-key&#34;&gt;&lt;script&gt;"`,
		`bad captcha`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("captcha HTML missing %q: %s", want, body)
		}
	}
}

func TestAuthConfirmationHTMLEscapesEmail(t *testing.T) {
	body := authConfirmationHTML(`"><script>`, "", "", "en")
	if strings.Contains(body, `"><script>`) || !strings.Contains(body, "&#34;&gt;&lt;script&gt;") {
		t.Fatalf("email was not escaped: %s", body)
	}
}

func TestConfirmationTokenExpiryMatchesDeviseConfirmWithin(t *testing.T) {
	now := time.Date(2026, 6, 20, 1, 2, 3, 0, time.UTC)
	for _, tt := range []struct {
		name string
		user models.User
		want bool
	}{
		{
			name: "fresh",
			user: models.User{ConfirmationSentAt: sql.NullTime{Time: now.Add(-confirmationTokenValidFor + time.Second), Valid: true}},
			want: false,
		},
		{
			name: "exact-boundary",
			user: models.User{ConfirmationSentAt: sql.NullTime{Time: now.Add(-confirmationTokenValidFor), Valid: true}},
			want: false,
		},
		{
			name: "expired",
			user: models.User{ConfirmationSentAt: sql.NullTime{Time: now.Add(-confirmationTokenValidFor - time.Second), Valid: true}},
			want: true,
		},
		{
			name: "missing-sent-at",
			user: models.User{},
			want: true,
		},
	} {
		if got := confirmationTokenExpired(tt.user, now); got != tt.want {
			t.Fatalf("%s expired = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestConfirmationDeliveryMarksPendingReconfirmation(t *testing.T) {
	token := sql.NullString{String: "tok", Valid: true}
	delivery := confirmationDeliveryForUser(&models.User{
		Email:             "old@example.test",
		UnconfirmedEmail:  sql.NullString{String: "new@example.test", Valid: true},
		ConfirmedAt:       sql.NullTime{Time: time.Now().UTC(), Valid: true},
		ConfirmationToken: token,
	})
	if delivery.Email != "new@example.test" || delivery.Token != "tok" || !delivery.Reconfirmation {
		t.Fatalf("reconfirmation delivery = %#v", delivery)
	}

	delivery = confirmationDeliveryForUser(&models.User{
		Email:             "new@example.test",
		ConfirmationToken: token,
	})
	if delivery.Email != "new@example.test" || delivery.Token != "tok" || delivery.Reconfirmation {
		t.Fatalf("initial confirmation delivery = %#v", delivery)
	}
}

func TestConfirmUserByTokenKeepsPendingAccountMailSideEffect(t *testing.T) {
	src, err := os.ReadFile("confirmations.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"if newUser && !user.Approved",
		"s.sendStaffNewPendingAccountMails(*user)",
	} {
		if !functionBodyContains(t, src, "confirmUserByToken", want) {
			t.Fatalf("confirmUserByToken does not contain %q", want)
		}
	}
	if !functionBodyContains(t, src, "confirmUserByToken", "confirmationTokenExpired(*user, now)") {
		t.Fatal("confirmUserByToken does not enforce confirm_within expiry")
	}
}

func TestConfirmationApprovesUserForRailsConfirm(t *testing.T) {
	if !confirmationApprovesUserForRailsConfirm("open", signUpIPRestriction{}) {
		t.Fatal("open registrations without IP approval restriction should approve on confirmation")
	}
	if confirmationApprovesUserForRailsConfirm("open", signUpIPRestriction{RequiresApproval: true}) {
		t.Fatal("sign_up_requires_approval IP should remain pending after confirmation")
	}
	if confirmationApprovesUserForRailsConfirm("approved", signUpIPRestriction{}) {
		t.Fatal("manual review registrations should not approve on confirmation")
	}
}
