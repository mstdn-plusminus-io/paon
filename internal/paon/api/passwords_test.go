package api

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
)

func TestNewAuthPasswordRendersForm(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodGet, "/auth/password/new?email=alice%40example.test", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())

	if err := s.newAuthPassword(c); err != nil {
		t.Fatal(err)
	}
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, `action="/auth/password"`) || !strings.Contains(body, `value="alice@example.test"`) {
		t.Fatalf("status = %d body = %s", rec.Code, body)
	}
}

func TestCreateAuthPasswordRequiresValidEmail(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodPost, "/auth/password", strings.NewReader("user%5Bemail%5D=bad"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())

	if err := s.createAuthPassword(c); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "Email is invalid") {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
}

func TestEditAuthPasswordRequiresToken(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodGet, "/auth/password/edit", nil)
	req.Header.Set("Accept-Language", "en")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())

	if err := s.editAuthPassword(c); err != nil {
		t.Fatal(err)
	}
	want := "/auth/password/new?error=" + url.QueryEscape("Password reset token is invalid or expired. Please request a new one.")
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != want {
		t.Fatalf("status = %d location = %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestUpdateAuthPasswordValidatesPassword(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodPut, "/auth/password", strings.NewReader("user%5Breset_password_token%5D=tok&user%5Bpassword%5D=short"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())

	if err := s.updateAuthPassword(c); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "Password is invalid") {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
}

func TestPostAuthPasswordDispatchesUpdateForMethodOverride(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodPost, "/auth/password", strings.NewReader("_method=put&user%5Breset_password_token%5D=tok&user%5Bpassword%5D=short"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())

	if err := s.postAuthPassword(c); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "Password is invalid") {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
}

func TestPostAuthPasswordDispatchesUpdateForOpaqueResetToken(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodPost, "/auth/password", strings.NewReader("user%5Breset_password_token%5D=+&user%5Bpassword%5D=short"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())

	if err := s.postAuthPassword(c); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "Password is invalid") {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
}

func TestUpdateAuthPasswordSendsDevisePasswordChangeNotification(t *testing.T) {
	src, err := os.ReadFile("passwords.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, src, "updateAuthPassword", "s.sendPasswordChangedMail(*user)") {
		t.Fatal("updateAuthPassword does not send password change notification after successful reset")
	}
}

func TestAuthPasswordMessagesUseRailsLocaleKeys(t *testing.T) {
	src, err := os.ReadFile("passwords.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`authInvalidEmailMessage(locale)`,
		`settingsT(locale, "devise.passwords.send_instructions"`,
		`authInvalidResetPasswordTokenMessage(locale)`,
		`authInvalidPasswordMessage(locale)`,
		`authPasswordConfirmationMismatchMessage(locale)`,
		`settingsT(locale, "devise.passwords.updated_not_active"`,
		`settingsDatabaseUnavailableMessage(locale)`,
	} {
		if !strings.Contains(string(src), want) {
			t.Fatalf("passwords.go missing localized message fragment %q", want)
		}
	}
	if functionBodyContains(t, src, "createAuthPassword", `"DATABASE_URL is not set"`) {
		t.Fatal("createAuthPassword must not inject raw English DATABASE_URL text into server-rendered HTML")
	}
}

func TestPasswordResetWindowMatchesDeviseSetting(t *testing.T) {
	if resetPasswordWindow.String() != "6h0m0s" {
		t.Fatalf("resetPasswordWindow = %s, want 6h0m0s", resetPasswordWindow)
	}
	src, err := os.ReadFile("passwords.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, src, "findUserByResetPasswordToken", "now.Add(-resetPasswordWindow)") {
		t.Fatal("findUserByResetPasswordToken does not enforce reset_password_within")
	}
}

func TestAuthPasswordHTMLEscapesValues(t *testing.T) {
	newBody := authPasswordNewHTML(`"><script>`, "", "", "en")
	editBody := authPasswordEditHTML(`"><script>`, "", "", "en")
	if strings.Contains(newBody, `"><script>`) || strings.Contains(editBody, `"><script>`) {
		t.Fatalf("password reset HTML did not escape values:\n%s\n%s", newBody, editBody)
	}
}
