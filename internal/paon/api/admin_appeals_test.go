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
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestAdminAppealsRequireSession(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/disputes/appeals?status=pending", nil)
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	want := "/auth/sign_in?redirect_to=" + url.QueryEscape("/admin/disputes/appeals?status=pending")
	if got := rec.Header().Get("Location"); got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}
}

func TestAdminAppealStatus(t *testing.T) {
	e := echo.New()
	for raw, want := range map[string]string{
		"":         "pending",
		"pending":  "pending",
		"approved": "approved",
		"rejected": "rejected",
		"other":    "other",
	} {
		req := httptest.NewRequest(http.MethodGet, "/admin/disputes/appeals?status="+url.QueryEscape(raw), nil)
		if raw == "" {
			req = httptest.NewRequest(http.MethodGet, "/admin/disputes/appeals", nil)
		}
		rec := httptest.NewRecorder()
		c := echo.NewContext(req, rec, e)
		if got := adminAppealStatus(c); got != want {
			t.Fatalf("status %q = %q, want %q", raw, got, want)
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/admin/disputes/appeals?status=", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)
	if got := adminAppealStatus(c); got != "" {
		t.Fatalf("blank status = %q, want empty", got)
	}
}

func TestAdminAppealModelsUseRailsKaminariPageSizeAndOffset(t *testing.T) {
	src, err := os.ReadFile("admin_appeals.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Offset(adminRailsPageOffset(c))",
		"Limit(adminRailsDefaultPageSize)",
	} {
		if !functionBodyContains(t, src, "adminAppealModels", want) {
			t.Fatalf("adminAppealModels missing %q", want)
		}
	}
}

func TestAdminAppealsHTMLIncludesRailsLogEntries(t *testing.T) {
	html := adminAppealsHTML([]models.Appeal{{
		ID:               5,
		AccountID:        7,
		AccountWarningID: 9,
		Text:             "please review",
		CreatedAt:        time.Date(2026, 6, 19, 1, 2, 3, 0, time.UTC),
		Account:          models.Account{ID: 7, Username: "alice"},
		Strike: models.AccountWarning{
			ID:              9,
			Action:          3000,
			TargetAccountID: models.AccountWarningTargetAccountID(8),
			TargetAccount:   models.Account{ID: 8, Username: "bob"},
		},
	}}, 1, adminAppealFilters{Page: "2", Status: "pending"}, "saved", "")
	for _, want := range []string{
		"Appeals",
		`href="/admin/disputes/appeals?status=pending"`,
		`href="/admin/disputes/appeals?status=approved"`,
		`href="/admin/disputes/appeals?status=rejected"`,
		`href="/disputes/strikes/9"`,
		`class="log-entry"`,
		`class="log-entry__avatar"`,
		`class="warning-hint"`,
		"alice",
		"Limitation of account",
		"saved",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("appeals html missing %q: %s", want, html)
		}
	}
}

func TestAdminAppealStateSuppressesActionsWhenDecided(t *testing.T) {
	html := adminAppealRowHTML(models.Appeal{
		ID:               5,
		AccountWarningID: 9,
		Text:             "done",
		ApprovedAt:       sql.NullTime{Time: time.Now(), Valid: true},
		CreatedAt:        time.Now(),
		Strike:           models.AccountWarning{ID: 9},
	})
	if !strings.Contains(html, "Appealed") {
		t.Fatalf("row html missing approved state: %s", html)
	}
	if strings.Contains(html, "/approve") || strings.Contains(html, "/reject") {
		t.Fatalf("decided appeal rendered actions: %s", html)
	}
}

func TestAccountWarningStatusIDs(t *testing.T) {
	got := accountWarningStatusIDs(models.AccountWarning{StatusIDs: models.StringArray{"12", "bad", " 13 ", "0"}})
	if len(got) != 2 || got[0] != 12 || got[1] != 13 {
		t.Fatalf("ids = %#v", got)
	}
}
