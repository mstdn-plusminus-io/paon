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

func TestInviteFromRequestRejectsInvalidValues(t *testing.T) {
	tests := map[string]string{
		"invite[comment]": strings.Repeat("x", 421),
	}
	for key, value := range tests {
		t.Run(key, func(t *testing.T) {
			form := url.Values{}
			form.Set(key, value)
			req := httptest.NewRequest(http.MethodPost, "/invites", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			rec := httptest.NewRecorder()
			c := echo.NewContext(req, rec, echo.New())
			if _, err := inviteFromRequest(c, 42, time.Now().UTC()); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestInviteHelpers(t *testing.T) {
	code, err := randomInviteCode()
	if err != nil {
		t.Fatal(err)
	}
	if len(code) != 8 {
		t.Fatalf("code = %q", code)
	}
	if strings.ContainsAny(code, "01IlO") {
		t.Fatalf("code contains ambiguous character: %q", code)
	}

	now := time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)
	if !inviteExpired(models.Invite{ExpiresAt: sql.NullTime{Time: now.Add(-time.Second), Valid: true}}, now) {
		t.Fatal("past invite should be expired")
	}
	if inviteExpired(models.Invite{ExpiresAt: sql.NullTime{Time: now.Add(time.Second), Valid: true}}, now) {
		t.Fatal("future invite should be available")
	}
	if got := inviteUsesText(models.Invite{Uses: 2, MaxUses: sql.NullInt64{Int64: 5, Valid: true}}); got != "2/5" {
		t.Fatalf("uses = %q", got)
	}
}

func TestInvitesRequireWebAuthentication(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodGet, "/invites?x=1", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())

	if err := s.invitesPage(c); err != nil {
		t.Fatal(err)
	}
	want := "/auth/sign_in?redirect_to=" + url.QueryEscape("/invites?x=1")
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != want {
		t.Fatalf("status = %d location = %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestAdminInvitesRequireWebAuthentication(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodGet, "/admin/invites?available=1", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())

	if err := s.adminInvitesPage(c); err != nil {
		t.Fatal(err)
	}
	want := "/auth/sign_in?redirect_to=" + url.QueryEscape("/admin/invites?available=1")
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != want {
		t.Fatalf("status = %d location = %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestInvitesHTMLRendersRailsCopyTable(t *testing.T) {
	html := invitesHTML("https://example.test", []models.Invite{{
		ID:        9,
		Code:      "abc123",
		Uses:      1,
		MaxUses:   sql.NullInt64{Int64: 5, Valid: true},
		ExpiresAt: sql.NullTime{Time: time.Date(2026, 7, 19, 10, 0, 0, 0, time.UTC), Valid: true},
	}}, "", "en")
	for _, want := range []string{
		`class="simple_form new_invite"`,
		`id="new_invite"`,
		`novalidate="novalidate"`,
		`class="fields-row"`,
		`class="input with_label select optional invite_max_uses"`,
		`class="label_input__wrapper"`,
		`name="invite[max_uses]"`,
		`name="invite[expires_in]"`,
		`name="invite[autofollow]"`,
		`class="input with_label boolean optional invite_autofollow field_with_hint"`,
		`class="checkbox"`,
		`class="hint"`,
		`class="button"`,
		`class="table-wrapper simple_form"`,
		`class="table table--invites"`,
		`class="input-copy"`,
		`value="https://example.test/invite/abc123"`,
		`class="fa fa-user fa-fw"`,
		`1/5`,
		`class="formatted"`,
		`/invites/9`,
		`class="table-action-link"`,
		`<body class="admin theme-`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("invites html missing %q: %s", want, html)
		}
	}
}

func TestInviteFormUsesRailsJapaneseLabelsAndPluralFallback(t *testing.T) {
	html := invitesHTML("https://example.test", nil, "", "ja")
	for _, want := range []string{
		`使用できる回数`,
		`<option value="1">1</option>`,
		`招待から参加後、あなたをフォロー`,
		`招待から登録した人が自動的にあなたをフォローするようになります`,
		`class="button"`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("Japanese invite form missing Rails fragment %q: %s", want, html)
		}
	}
	for _, unwanted := range []string{`1 use`, `Invite to follow your account`, `No invites`} {
		if strings.Contains(html, unwanted) {
			t.Fatalf("Japanese invite form contains fallback literal %q: %s", unwanted, html)
		}
	}
}

func TestInviteRedirectErrorsUseLocaleKeys(t *testing.T) {
	src, err := os.ReadFile("invites.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, fn := range []string{"createInvite", "destroyInvite", "createAdminInvite", "deactivateAllAdminInvites"} {
		if !functionBodyContains(t, src, fn, `settingsDatabaseUnavailableMessage(locale)`) {
			t.Fatalf("%s must use localized database-unavailable flash", fn)
		}
		if functionBodyContains(t, src, fn, `QueryEscape("DATABASE_URL is not set")`) {
			t.Fatalf("%s must not redirect with fixed Go-only database flash", fn)
		}
	}
}

func TestAdminInvitesHTMLRendersRailsPaginationLinks(t *testing.T) {
	invites := make([]models.Invite, adminRailsDefaultPageSize)
	for i := range invites {
		invites[i] = models.Invite{ID: int64(i + 1), Code: "abc123"}
	}
	html := adminInvitesHTML("https://example.test", invites, "", false, adminInviteFilters{Page: "2", Available: "1"})
	for _, want := range []string{
		`href="/admin/invites?available=1&amp;page=1"`,
		`href="/admin/invites?available=1&amp;page=3"`,
		"Previous",
		"Next",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("admin invites html missing pagination %q: %s", want, html)
		}
	}
}

func TestAdminInvitesUseRailsPageSizeAndOffset(t *testing.T) {
	src, err := os.ReadFile("invites.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		functionName string
		want         string
	}{
		{"adminInvitesPage", "adminInvitesHTML(s.cfg.BaseURL(), invites, c.QueryParam(\"error\"), s.userCan(user, rolePermissionInviteUsers), filters, locale, theme)"},
		{"adminInvites", "Offset(adminRailsPageOffset(c))"},
		{"adminInvites", "Limit(adminRailsDefaultPageSize)"},
	} {
		if !functionBodyContains(t, src, check.functionName, check.want) {
			t.Fatalf("%s missing %q", check.functionName, check.want)
		}
	}
}

func TestInviteUnavailableIncludesMaxUses(t *testing.T) {
	now := time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)
	if !inviteUnavailable(models.Invite{Uses: 2, MaxUses: sql.NullInt64{Int64: 2, Valid: true}}, now) {
		t.Fatal("invite at max uses should be unavailable")
	}
	if inviteUnavailable(models.Invite{Uses: 1, MaxUses: sql.NullInt64{Int64: 2, Valid: true}}, now) {
		t.Fatal("invite below max uses should be available")
	}
}
