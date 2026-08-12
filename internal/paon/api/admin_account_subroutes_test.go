package api

import (
	"database/sql"
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
)

func TestAdminAccountStatusesRequireSession(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/accounts/7/statuses?media=1", nil)
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	want := "/auth/sign_in?redirect_to=" + url.QueryEscape("/admin/accounts/7/statuses?media=1")
	if got := rec.Header().Get("Location"); got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}
}

func TestParseAdminStatusBatchIDs(t *testing.T) {
	form := url.Values{}
	form.Add("admin_status_batch_action[status_ids][]", "11")
	form.Add("admin_status_batch_action[status_ids][]", "bad")
	form.Add("admin_status_batch_action[status_ids]", "12")

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/admin/accounts/7/statuses/batch", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	got := parseAdminStatusBatchIDs(c)
	if len(got) != 1 || got[0] != 11 {
		t.Fatalf("ids = %#v", got)
	}
}

func TestAdminStatusBatchActionFromRequest(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/admin/accounts/7/statuses/batch", strings.NewReader("remove_from_report=1"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	if got := adminStatusBatchActionFromRequest(c); got != "remove_from_report" {
		t.Fatalf("action = %q", got)
	}

	req = httptest.NewRequest(http.MethodPost, "/admin/accounts/7/statuses/batch", strings.NewReader("delete="))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c = echo.NewContext(req, httptest.NewRecorder(), e)
	if got := adminStatusBatchActionFromRequest(c); got != "delete" {
		t.Fatalf("empty-valued submit button action = %q", got)
	}
}

func TestAdminAccountSubrouteModelsUseRailsPageSizesAndOffsets(t *testing.T) {
	src, err := os.ReadFile("admin_account_subroutes.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		fn   string
		want string
	}{
		{"adminAccountStatusModels", "Offset(adminPageOffset(c, adminAccountStatusesPageSize))"},
		{"adminAccountStatusModels", "Limit(adminAccountStatusesPageSize)"},
		{"adminAccountStatusModels", `Preload("Poll")`},
		{"adminAccountRelationshipModels", "Offset(adminRailsPageOffset(c))"},
		{"adminAccountRelationshipModels", "Limit(adminRailsDefaultPageSize)"},
	} {
		if !functionBodyContains(t, src, check.fn, check.want) {
			t.Fatalf("%s missing %q", check.fn, check.want)
		}
	}
}

func TestAdminModerationStatusRowDisplaysPoll(t *testing.T) {
	html := adminAccountStatusRowHTML("en", 7, models.Status{
		ID:   11,
		Poll: &models.Poll{Options: models.StringArray{"one", "two"}},
	})
	for _, want := range []string{`class="poll"`, `role="radio" aria-label="one"`, `disabled>Vote</button>`} {
		if !strings.Contains(html, want) {
			t.Fatalf("moderation row missing poll markup %q: %s", want, html)
		}
	}
}

func TestAdminAccountStatusHiddenFields(t *testing.T) {
	html := adminAccountStatusHiddenFields(adminAccountStatusFilters{Page: "3", Media: "1", ReportID: "5"})
	for _, want := range []string{
		`name="page" value="3"`,
		`name="media" value="1"`,
		`name="report_id" value="5"`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("status hidden fields missing %q: %s", want, html)
		}
	}
}

func TestAdminAccountStatusesRedirectURLPreservesFilters(t *testing.T) {
	form := url.Values{}
	form.Set("page", "4")
	form.Set("media", "1")

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/admin/accounts/7/statuses/batch", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	got := adminAccountStatusesRedirectURL(c, "7", "notice", "ok")
	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Path != "/admin/accounts/7/statuses" {
		t.Fatalf("path = %q", parsed.Path)
	}
	values := parsed.Query()
	for key, want := range map[string]string{"page": "4", "media": "1", "notice": "ok"} {
		if got := values.Get(key); got != want {
			t.Fatalf("%s = %q, want %q in %s", key, got, want, parsed.RawQuery)
		}
	}
}

func TestAdminAccountStatusBatchRedirectURLUsesReportPathWhenPresent(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/admin/accounts/7/statuses/batch", strings.NewReader("page=4&media=1"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c := echo.NewContext(req, httptest.NewRecorder(), e)

	if got := adminAccountStatusBatchRedirectURL(c, "7", 5, "", ""); got != "/admin/reports/5" {
		t.Fatalf("report redirect path = %q", got)
	}
	got := adminAccountStatusBatchRedirectURL(c, "7", 5, "error", "No statuses selected")
	if got != "/admin/reports/5?error=No+statuses+selected" {
		t.Fatalf("report error redirect path = %q", got)
	}
	got = adminAccountStatusBatchRedirectURL(c, "7", 0, "", "")
	if got != "/admin/accounts/7/statuses?media=1&page=4" {
		t.Fatalf("account statuses redirect path = %q", got)
	}
}

func TestAdminAccountStatusHTMLIncludesMetadataAndHistory(t *testing.T) {
	html := adminAccountStatusHTML(models.Account{ID: 7, Username: "alice"}, models.Status{
		ID:          11,
		AccountID:   7,
		Text:        "hello",
		Visibility:  2,
		Language:    sql.NullString{String: "ja", Valid: true},
		Application: &models.OAuthApplication{Name: "Paon Mobile"},
		StatusStat:  models.StatusStat{ReblogsCount: 2, FavouritesCount: 3},
		Poll:        &models.Poll{Options: models.StringArray{"red", "blue"}, Multiple: true},
	}, []models.StatusEdit{{Text: "original", PollOptions: models.StringArray{"yes", "no"}, CreatedAt: time.Date(2026, 6, 18, 0, 0, 0, 0, time.UTC)}, {Text: "changed", CreatedAt: time.Date(2026, 6, 19, 0, 0, 0, 0, time.UTC)}}, "", "")
	for _, want := range []string{
		"Account posts",
		`class="table-wrapper"`,
		`class="table horizontal-table"`,
		"@alice",
		"Paon Mobile",
		"Japanese",
		"Followers-only",
		"Original post",
		"Post changed",
		"original",
		"changed",
		"Reblogs",
		"Favorites",
		`class="history"`,
		`class="poll"`,
		`role="checkbox" aria-label="red"`,
		`role="radio" aria-label="yes"`,
		`class="button button-secondary" disabled`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("status html missing %q: %s", want, html)
		}
	}
}

func TestAdminAccountRelationshipsHTMLIncludesFiltersAndBatch(t *testing.T) {
	html := adminAccountRelationshipsHTML(models.Account{ID: 7, Username: "alice"}, []models.Account{{
		ID:       9,
		Username: "bob",
		Domain:   sql.NullString{String: "remote.example", Valid: true},
	}}, "", "", adminAccountRelationshipFilters{Page: "2", Relationship: "followed_by", Location: "remote", Status: "moved", Order: "active", Activity: "dormant", ByDomain: "remote.example"})
	for _, want := range []string{
		"Follows and followers",
		`class="filters"`,
		`class="filter-subset"`,
		`class="back-link"`,
		`href="/admin/accounts/7"`,
		`relationship=mutual`,
		`location=remote`,
		`action="/admin/accounts/batch"`,
		`class="new_form_account_batch"`,
		`class="batch-table__toolbar"`,
		`class="batch-table__body"`,
		`name="page" value="2"`,
		`name="relationship" value="followed_by"`,
		`name="location" value="remote"`,
		`name="status" value="moved"`,
		`name="order" value="active"`,
		`name="activity" value="dormant"`,
		`name="by_domain" value="remote.example"`,
		`name="form_account_batch[account_ids][]" value="9"`,
		"@bob@remote.example",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("relationships html missing %q: %s", want, html)
		}
	}
}

func TestAdminAccountChangeEmailHTMLIncludesRailsFields(t *testing.T) {
	html := adminAccountChangeEmailHTML(models.Account{
		ID:       7,
		Username: "alice",
		User:     models.User{ID: 9, Email: "alice@example.test", UnconfirmedEmail: sql.NullString{String: "new@example.test", Valid: true}},
	}, "bad")
	for _, want := range []string{
		"Change email",
		`action="/admin/accounts/7/change_email"`,
		`name="user[email]" value="alice@example.test"`,
		`type="email" disabled`,
		`name="user[unconfirmed_email]" value="new@example.test"`,
		"bad",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("change email html missing %q: %s", want, html)
		}
	}
}

func TestAdminAccountChangeEmailParamsRequireRailsRootParameter(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/admin/accounts/7/change_email", strings.NewReader("unconfirmed_email=new%40example.test"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c := echo.NewContext(req, httptest.NewRecorder(), echo.New())
	if _, err := adminAccountChangeEmailParams(c); !errors.Is(err, errAdminAccountChangeEmailParamsMissing) {
		t.Fatalf("flat unconfirmed_email should be rejected like Rails params.require(:user), got %v", err)
	}

	req = httptest.NewRequest(http.MethodPost, "/admin/accounts/7/change_email", strings.NewReader("user%5Bunconfirmed_email%5D=new%40example.test"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c = echo.NewContext(req, httptest.NewRecorder(), echo.New())
	email, err := adminAccountChangeEmailParams(c)
	if err != nil {
		t.Fatal(err)
	}
	if email != "new@example.test" {
		t.Fatalf("email = %q", email)
	}
}

func TestAdminAccountChangeEmailMessagesResolveJapaneseLocale(t *testing.T) {
	if got := adminAccountChangeEmailMessage("en", "errors.database_unavailable", "DATABASE_URL is not set"); got != "DATABASE_URL is not set" {
		t.Fatalf("en database message = %q", got)
	}
	if got := adminAccountChangeEmailMessage("ja", "errors.database_unavailable", "DATABASE_URL is not set"); got == "DATABASE_URL is not set" || !strings.Contains(got, "DATABASE_URL") {
		t.Fatalf("ja database message = %q", got)
	}
}

func TestInt64ArrayHelpers(t *testing.T) {
	removed := removeInt64s(models.Int64Array{1, 2, 3}, []int64{2, 4})
	if len(removed) != 2 || removed[0] != 1 || removed[1] != 3 {
		t.Fatalf("removed = %#v", removed)
	}
	added := appendUniqueInt64s(models.Int64Array{1, 2}, []int64{2, 3})
	if len(added) != 3 || added[0] != 1 || added[1] != 2 || added[2] != 3 {
		t.Fatalf("added = %#v", added)
	}
}
