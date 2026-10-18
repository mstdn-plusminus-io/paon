package api

import (
	"bytes"
	"database/sql"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestAuthFormValueSentDistinguishesBlankFromMissing(t *testing.T) {
	parse := func(t *testing.T, req *http.Request) *echo.Context {
		t.Helper()
		c := echo.NewContext(req, httptest.NewRecorder(), echo.New())
		_ = c.FormValue("user[password]")
		return c
	}

	form := url.Values{"user[password]": {"newpassword"}, "user[password_confirmation]": {""}}
	req := httptest.NewRequest(http.MethodPost, "/auth", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	value, sent := authFormValueSent(parse(t, req), "user[password_confirmation]")
	if !sent || value != "" {
		t.Fatalf("blank confirmation = %q, %v; want submitted blank (Devise validates it)", value, sent)
	}
	if value == "newpassword" {
		t.Fatal("blank confirmation must not match the new password")
	}

	form = url.Values{"user[password]": {"newpassword"}}
	req = httptest.NewRequest(http.MethodPost, "/auth", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if _, sent := authFormValueSent(parse(t, req), "user[password_confirmation]"); sent {
		t.Fatal("missing confirmation key should be treated as nil (validation skipped)")
	}

	form = url.Values{"user[password_confirmation]": {"first", "last"}}
	req = httptest.NewRequest(http.MethodPost, "/auth", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if value, sent := authFormValueSent(parse(t, req), "user[password_confirmation]"); !sent || value != "last" {
		t.Fatalf("duplicate confirmation = %q, %v; want last value like Rails params", value, sent)
	}

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	_ = writer.WriteField("user[password]", "newpassword")
	_ = writer.WriteField("user[password_confirmation]", "")
	_ = writer.Close()
	req = httptest.NewRequest(http.MethodPost, "/auth", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	if value, sent := authFormValueSent(parse(t, req), "user[password_confirmation]"); !sent || value != "" {
		t.Fatalf("multipart blank confirmation = %q, %v", value, sent)
	}
}

func TestUpdateUserRegistrationRejectsBlankPasswordConfirmation(t *testing.T) {
	src, err := os.ReadFile("account_settings.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, src, "updateUserRegistration", `authFormValueSent(c, "user[password_confirmation]"); sent && confirmation != password`) {
		t.Fatal("updateUserRegistration must reject a submitted blank password confirmation")
	}
}

func TestAuthEditStatusShowsStrikesLinkForOlderStrikes(t *testing.T) {
	now := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	user := &models.User{ID: 2, Approved: true, ConfirmedAt: sql.NullTime{Time: now, Valid: true}}
	account := &models.Account{ID: 3, Username: "alice"}

	html := authEditStatusHTML(user, account, nil, true, "en")
	if !strings.Contains(html, `<p class="muted-hint"><a href="/disputes/strikes">`) {
		t.Fatalf("account with only older strikes should still link to strikes: %s", html)
	}
	html = authEditStatusHTML(user, account, nil, false, "en")
	if strings.Contains(html, `href="/disputes/strikes"`) {
		t.Fatalf("account without strikes must not link to strikes: %s", html)
	}

	html = authEditHTMLWithOptions("alice@example.test", "", "", authEditHTMLOptions{Locale: "en", User: user, Account: account, HasAnyStrikes: true, EncryptedPassword: "present"})
	if !strings.Contains(html, `href="/disputes/strikes"`) {
		t.Fatalf("auth edit should pass HasAnyStrikes to the status section: %s", html)
	}

	if has, err := (&Server{}).authEditHasAnyStrikes(3); err != nil || has {
		t.Fatalf("nil db: has = %v err = %v", has, err)
	}
}

func TestAuthSetupHTMLSubmitsPatchLikeRailsSimpleForm(t *testing.T) {
	page := authSetupHTML("alice@example.com", "", "", "")
	if !strings.Contains(page, `action="/auth/setup"><input type="hidden" name="_method" value="patch"`) {
		t.Fatalf("auth setup form must carry _method=patch: %s", page)
	}

	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{}
	for _, match := range regexp.MustCompile(`<input type="hidden" name="([^"]+)" value="([^"]*)"`).FindAllStringSubmatch(page, -1) {
		form.Add(match[1], match[2])
	}
	form.Set("user[email]", "alice@example.com")
	req := httptest.NewRequest(http.MethodPost, "/auth/setup", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)
	if rec.Code == http.StatusNotFound || rec.Code == http.StatusMethodNotAllowed {
		t.Fatalf("rendered /auth/setup form did not reach updateAuthSetup: status = %d body = %s", rec.Code, rec.Body.String())
	}
}

func TestSignInFormShowsNoticeFromRedirect(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodGet, "/auth/sign_in?notice="+url.QueryEscape("Your account has been deleted"), nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())
	if err := s.signInForm(c); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `<div class="flash-message notice"><strong>Your account has been deleted</strong></div>`) {
		t.Fatalf("sign-in form must show the redirect notice: %s", rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/auth/sign_in?notice="+url.QueryEscape("<script>x</script>"), nil)
	rec = httptest.NewRecorder()
	c = echo.NewContext(req, rec, echo.New())
	if err := s.signInForm(c); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rec.Body.String(), "<script>x</script>") {
		t.Fatalf("notice must be escaped: %s", rec.Body.String())
	}
}
