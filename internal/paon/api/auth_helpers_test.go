package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
)

func TestSafeLocalReturnPath(t *testing.T) {
	for raw, want := range map[string]string{
		"/settings/delete":       "/settings/delete",
		"":                       "/",
		"https://evil.test/path": "/",
		"//evil.test/path":       "/",
	} {
		if got := safeLocalReturnPath(raw); got != want {
			t.Fatalf("safeLocalReturnPath(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestAuthChallengeRequiresWebAuthentication(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodPost, "/auth/challenge", strings.NewReader("return_to=%2Fsettings"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())

	if err := s.createAuthChallenge(c); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusFound || !strings.HasPrefix(rec.Header().Get("Location"), "/auth/sign_in?redirect_to=") {
		t.Fatalf("status = %d location = %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestAuthChallengeInvalidPasswordUsesLocaleKey(t *testing.T) {
	src, err := os.ReadFile("auth_helpers.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, src, "createAuthChallenge", `settingsT(locale, "challenge.invalid_password", "Invalid password")`) {
		t.Fatal("createAuthChallenge must localize invalid password via challenge.invalid_password")
	}
	if functionBodyContains(t, src, "createAuthChallenge", `authChallengeHTML(returnTo, "Invalid password"`) {
		t.Fatal("createAuthChallenge must not pass fixed English directly into authChallengeHTML")
	}
	if got := settingsT("ja", "challenge.invalid_password", "Invalid password"); got == "Invalid password" || !strings.Contains(got, "パスワード") {
		t.Fatalf("Japanese auth challenge invalid-password copy did not resolve locale key: %q", got)
	}
}

func TestAuthChallengeParamsRequireRailsRootParameter(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/auth/challenge", strings.NewReader("return_to=%2Fsettings&current_password=pass"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c := echo.NewContext(req, httptest.NewRecorder(), echo.New())
	if _, _, err := authChallengeParams(c); !errors.Is(err, errAuthChallengeParamsMissing) {
		t.Fatalf("flat challenge params should be rejected like Rails params.require(:form_challenge), got %v", err)
	}

	req = httptest.NewRequest(http.MethodPost, "/auth/challenge", strings.NewReader("form_challenge%5Breturn_to%5D=%2Fsettings&form_challenge%5Bcurrent_password%5D=pass"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c = echo.NewContext(req, httptest.NewRecorder(), echo.New())
	returnTo, password, err := authChallengeParams(c)
	if err != nil {
		t.Fatal(err)
	}
	if returnTo != "/settings" || password != "pass" {
		t.Fatalf("return_to=%q password=%q", returnTo, password)
	}
}

func TestConfirmCaptchaRedirectsConfirmationToken(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodPost, "/auth/captcha_confirmation", strings.NewReader("confirmation_token=abc"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())

	if err := s.confirmCaptcha(c); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/auth/confirmation?confirmation_token=abc" {
		t.Fatalf("status = %d location = %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestConfirmCaptchaWithoutTokenUsesCaptchaLocaleCopy(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodPost, "/auth/captcha_confirmation", nil)
	req.Header.Set("Accept-Language", "en")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())

	if err := s.confirmCaptcha(c); err != nil {
		t.Fatal(err)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"Captcha confirmation is not required.",
		">Continue</a>",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("captcha confirmation body missing %q: %s", want, body)
		}
	}
	if strings.Contains(body, ">Log in</a>") {
		t.Fatalf("captcha confirmation should not reuse auth.login for the continue link: %s", body)
	}
}
