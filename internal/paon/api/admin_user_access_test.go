package api

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestAdminUserRoleRequiresSession(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/users/12/role", nil)
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	want := "/auth/sign_in?redirect_to=" + url.QueryEscape("/admin/users/12/role")
	if got := rec.Header().Get("Location"); got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}
}

func TestAdminUserTwoFactorRequiresSession(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/admin/users/12/two_factor_authentication", strings.NewReader("_method=delete"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	want := "/auth/sign_in?redirect_to=" + url.QueryEscape("/admin/users/12/two_factor_authentication")
	if got := rec.Header().Get("Location"); got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}
}

func TestAdminUserRoleHTMLIncludesRailsFields(t *testing.T) {
	user := models.User{
		ID:        12,
		AccountID: 7,
		RoleID:    sql.NullInt64{Int64: 3, Valid: true},
		Account:   &models.Account{ID: 7, Username: "alice"},
	}
	html := adminUserRoleHTML(user, []models.UserRole{{ID: 3, Name: "Moderators"}, {ID: 4, Name: "Admins"}}, "bad")
	for _, want := range []string{
		"Change role",
		`action="/admin/users/12/role"`,
		`class="simple_form edit_user"`,
		`class="fields-group"`,
		`name="_method" value="patch"`,
		`name="user[role_id]" id="user_role_id"`,
		`<option value="">No role</option>`,
		`<option value="3" selected>Moderators</option>`,
		`<option value="4">Admins</option>`,
		"bad",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("user role html missing %q: %s", want, html)
		}
	}
}

func TestAdminAccountHTMLIncludesUserAccessLinks(t *testing.T) {
	html := adminAccountHTML(models.Account{
		ID:       7,
		Username: "alice",
		User: models.User{
			ID:                  12,
			Email:               "alice@example.test",
			Approved:            true,
			OTPRequiredForLogin: true,
			Role:                models.UserRole{ID: 3, Name: "Moderators"},
		},
	}, "", "")
	for _, want := range []string{
		`href="/admin/users/12/role"`,
		"Moderators",
		`data-method="delete" href="/admin/users/12/two_factor_authentication"`,
		"Disable 2FA",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("account html missing %q: %s", want, html)
		}
	}
}

func TestAdminUserRoleSelectSupportsNoRole(t *testing.T) {
	html := adminUserRoleSelect(sql.NullInt64{}, []models.UserRole{{ID: 3, Name: "Moderators"}})
	for _, want := range []string{`name="user[role_id]"`, `<option value="">No role</option>`, `<option value="3">Moderators</option>`} {
		if !strings.Contains(html, want) {
			t.Fatalf("role select missing %q: %s", want, html)
		}
	}
}
