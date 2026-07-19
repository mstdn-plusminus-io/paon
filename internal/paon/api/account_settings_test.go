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

func TestAuthEditRequiresAuthentication(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/auth/edit", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())
	s := &Server{}

	if err := s.authEditPage(c); err != nil {
		t.Fatal(err)
	}
	want := "/auth/sign_in?redirect_to=" + url.QueryEscape("/auth/edit")
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != want {
		t.Fatalf("status = %d location = %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestAuthSetupRequiresAuthentication(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/auth/setup", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())
	s := &Server{}

	if err := s.authSetupPage(c); err != nil {
		t.Fatal(err)
	}
	want := "/auth/sign_in?redirect_to=" + url.QueryEscape("/auth/setup")
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != want {
		t.Fatalf("status = %d location = %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestAuthSetupHTMLPostsFallbackRoute(t *testing.T) {
	html := authSetupHTML("alice@example.com", "", "", "/packs/js/sign_up-hash.js")
	if !strings.Contains(html, `action="/auth/setup"`) || !strings.Contains(html, `value="alice@example.com"`) {
		t.Fatalf("unexpected auth setup html: %s", html)
	}
	if strings.Contains(html, "handled outside this lightweight Go path") {
		t.Fatalf("auth setup html still claims confirmation mail is external: %s", html)
	}
	for _, want := range []string{
		`class="form-container"`,
		`class="simple_form edit_user"`,
		`class="progress-tracker"`,
		`class="title"`,
		`class="lead"`,
		`class="fields-group"`,
		`name="user[email]"`,
		`class="actions"`,
		`class="button timer-button" type="submit" disabled`,
		`class="form-footer"`,
		`href="/auth/edit"`,
		`data-method="delete" href="/auth/sign_out"`,
		`src="/packs/js/sign_up-hash.js"`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("auth setup html missing %q: %s", want, html)
		}
	}
}

func TestAuthEditHTMLRendersRailsStatusAndSuspendedGating(t *testing.T) {
	now := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	html := authEditHTMLWithOptions("alice@example.test", "", "", authEditHTMLOptions{
		Locale: "en",
		User: &models.User{
			ID:                2,
			Email:             "alice@example.test",
			Approved:          true,
			ConfirmedAt:       sql.NullTime{Time: now.Add(-24 * time.Hour), Valid: true},
			EncryptedPassword: "present",
		},
		Account: &models.Account{
			ID:          3,
			Username:    "alice",
			SuspendedAt: sql.NullTime{Time: now.Add(-time.Hour), Valid: true},
		},
		Strikes: []models.AccountWarning{{
			ID:        7,
			Action:    1000,
			CreatedAt: now.Add(-time.Hour),
		}},
		EncryptedPassword: "present",
	})

	for _, want := range []string{
		`Account status`,
		`You can no longer use your account`,
		`href="/disputes/strikes/7"`,
		`href="/disputes/strikes"`,
		`name="user[email]"`,
		`aria-label="E-mail address" required disabled`,
		`name="user[current_password]"`,
		`type="submit" disabled`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("auth edit suspended html missing %q: %s", want, html)
		}
	}
	for _, forbidden := range []string{`href="/settings/migration"`, `href="/settings/aliases"`, `href="/settings/delete"`} {
		if strings.Contains(html, forbidden) {
			t.Fatalf("auth edit suspended html must hide %q: %s", forbidden, html)
		}
	}
}

func TestConfirmationUpdateForConfirmedEmailChangeUsesReconfirmableColumns(t *testing.T) {
	now := time.Date(2026, 6, 20, 1, 2, 3, 0, time.UTC)
	updates, delivery := (&Server{}).confirmationUpdateForEmailChange(models.User{
		Email:       "old@example.test",
		ConfirmedAt: sql.NullTime{Time: now.Add(-time.Hour), Valid: true},
	}, " New@Example.TEST ", now)

	if updates["email"] != nil {
		t.Fatalf("confirmed e-mail change should not replace email directly: %#v", updates)
	}
	if updates["unconfirmed_email"] != "new@example.test" {
		t.Fatalf("unconfirmed_email = %#v, updates = %#v", updates["unconfirmed_email"], updates)
	}
	if updates["confirmed_at"] != nil {
		t.Fatalf("confirmed e-mail change should not clear confirmed_at: %#v", updates)
	}
	if updates["confirmation_sent_at"] != now || delivery.Email != "new@example.test" || delivery.Token == "" || !delivery.Reconfirmation {
		t.Fatalf("delivery/confirmation updates = %#v %#v", updates, delivery)
	}
}

func TestConfirmationUpdateForUnconfirmedEmailChangeKeepsInitialConfirmationShape(t *testing.T) {
	now := time.Date(2026, 6, 20, 1, 2, 3, 0, time.UTC)
	updates, delivery := (&Server{}).confirmationUpdateForEmailChange(models.User{}, " Alice@Example.TEST ", now)

	if updates["email"] != "alice@example.test" {
		t.Fatalf("email = %#v, updates = %#v", updates["email"], updates)
	}
	if _, ok := updates["unconfirmed_email"]; !ok || updates["unconfirmed_email"] != nil {
		t.Fatalf("unconfirmed e-mail change should clear unconfirmed_email: %#v", updates)
	}
	if _, ok := updates["confirmed_at"]; !ok || updates["confirmed_at"] != nil {
		t.Fatalf("unconfirmed e-mail change should keep account unconfirmed: %#v", updates)
	}
	if delivery.Email != "alice@example.test" || delivery.Token == "" || delivery.Reconfirmation {
		t.Fatalf("delivery = %#v", delivery)
	}
}

func TestAccountSettingsEmailChangesSendConfirmationMail(t *testing.T) {
	src, err := os.ReadFile("account_settings.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, fn := range []string{"updateUserRegistration", "updateAuthSetup"} {
		if !functionBodyContains(t, src, fn, "s.sendConfirmationDelivery(delivery)") {
			t.Fatalf("%s does not send confirmation instructions after refreshing token", fn)
		}
	}
}

func TestAuthEditSendsDeviseAccountChangeNotifications(t *testing.T) {
	src, err := os.ReadFile("account_settings.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"s.sendEmailChangedMail(*user, changedEmail)",
		"s.sendPasswordChangedMail(*user)",
	} {
		if !functionBodyContains(t, src, "updateUserRegistration", want) {
			t.Fatalf("updateUserRegistration missing %q", want)
		}
	}
}
