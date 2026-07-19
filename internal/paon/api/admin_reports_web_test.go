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

func TestAdminReportsWebRequiresSession(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/reports?resolved=1", nil)
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	want := "/auth/sign_in?redirect_to=" + url.QueryEscape("/admin/reports?resolved=1")
	if got := rec.Header().Get("Location"); got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}
}

func TestAdminReportActionFromRequest(t *testing.T) {
	e := echo.New()
	for _, tc := range []struct {
		form string
		want string
	}{
		{"moderation_action=suspend", "suspend"},
		{"moderation_action=+suspend+", ""},
		{"delete=", "delete"},
		{"mark_as_sensitive=", "mark_as_sensitive"},
		{"silence=", "silence"},
		{"suspend=", "suspend"},
	} {
		req := httptest.NewRequest(http.MethodPost, "/admin/reports/1/actions", strings.NewReader(tc.form))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		c := echo.NewContext(req, rec, e)
		if got := adminReportActionFromRequest(c); got != tc.want {
			t.Fatalf("form %q action = %q, want %q", tc.form, got, tc.want)
		}
	}
}

func TestAdminReportRedirectErrorsUseLocaleKeys(t *testing.T) {
	src, err := os.ReadFile("admin_reports_web.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		fn   string
		want string
	}{
		{"createAdminReportNoteWeb", `adminReportMessage(loc, "errors.database_unavailable", "DATABASE_URL is not set")`},
		{"updateAdminReportWeb", `adminReportMessage(s.webLocale(c, nil), "errors.database_unavailable", "DATABASE_URL is not set")`},
	} {
		if !functionBodyContains(t, src, check.fn, check.want) {
			t.Fatalf("%s missing localized report redirect error %q", check.fn, check.want)
		}
	}
	for _, forbidden := range []struct {
		fn   string
		text string
	}{
		{"createAdminReportNoteWeb", `QueryEscape("DATABASE_URL is not set")`},
		{"updateAdminReportWeb", `QueryEscape("DATABASE_URL is not set")`},
	} {
		if functionBodyContains(t, src, forbidden.fn, forbidden.text) {
			t.Fatalf("%s still contains non-localized redirect literal %q", forbidden.fn, forbidden.text)
		}
	}
}

func TestAdminReportMessagesResolveJapaneseLocale(t *testing.T) {
	if got := adminReportMessage("en", "errors.database_unavailable", "DATABASE_URL is not set"); got != "DATABASE_URL is not set" {
		t.Fatalf("en database message = %q", got)
	}
	if got := adminReportMessage("ja", "errors.database_unavailable", "DATABASE_URL is not set"); got == "DATABASE_URL is not set" || !strings.Contains(got, "DATABASE_URL") {
		t.Fatalf("ja database message = %q", got)
	}
}

func TestAdminReportModelsUseRailsKaminariPageSizeAndOffset(t *testing.T) {
	src, err := os.ReadFile("admin_reports_web.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Offset(adminRailsPageOffset(c))",
		"Limit(adminRailsDefaultPageSize)",
	} {
		if !functionBodyContains(t, src, "adminReportModels", want) {
			t.Fatalf("adminReportModels missing %q", want)
		}
	}
}

func TestAdminReportFilterHiddenFieldsPreserveRailsKeys(t *testing.T) {
	html := adminReportFilterHiddenFields(adminReportFilters{
		Page:            "3",
		Resolved:        "1",
		AccountID:       "4",
		TargetAccountID: "5",
		TargetOrigin:    "remote",
	})
	for _, want := range []string{
		`name="page" value="3"`,
		`name="resolved" value="1"`,
		`name="account_id" value="4"`,
		`name="target_account_id" value="5"`,
		`name="target_origin" value="remote"`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("hidden fields missing %q: %s", want, html)
		}
	}
}

func TestAdminReportsHTMLIncludesRailsFilterFields(t *testing.T) {
	html := adminReportsHTML([]models.Report{{
		ID:              7,
		Comment:         "spam",
		Category:        reportCategoryValue("spam"),
		StatusIDs:       models.Int64Array{11, 12},
		Account:         models.Account{ID: 3, Username: "reporter"},
		TargetAccountID: 4,
		TargetAccount:   models.Account{ID: 4, Username: "target", Domain: sql.NullString{String: "remote.example", Valid: true}},
	}}, "saved", "", adminReportFilters{Page: "2", Resolved: "1", AccountID: "3", TargetAccountID: "4", TargetOrigin: "remote", ByTargetDomain: "remote.example"})
	for _, want := range []string{
		"Reports",
		`resolved=1`,
		`name="page" value="2"`,
		`name="account_id" value="3"`,
		`name="target_account_id" value="4"`,
		`name="target_origin" value="remote"`,
		`name="by_target_domain" id="by_target_domain" value="remote.example"`,
		`href="/admin/reports/7"`,
		"@target@remote.example",
		`class="fa fa-comment"`,
		"</i> 2</span>",
		"saved",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("reports html missing %q: %s", want, html)
		}
	}
}

func TestAdminReportHTMLIncludesActionsAndNotes(t *testing.T) {
	report := models.Report{
		ID:              7,
		Comment:         "bad post",
		Category:        reportCategoryValue("violation"),
		RuleIDs:         models.Int64Array{2},
		CreatedAt:       time.Date(2026, 6, 19, 0, 0, 0, 0, time.UTC),
		TargetAccountID: 4,
		TargetAccount:   models.Account{ID: 4, Username: "target"},
		Account:         models.Account{ID: 3, Username: "reporter"},
	}
	html := adminReportHTML(report, []models.ReportNote{{ID: 5, AccountID: 9, Content: "note", Account: models.Account{ID: 9, Username: "mod"}}}, []models.Status{{ID: 11, AccountID: 4, Text: "hello", MediaAttachments: []models.MediaAttachment{{ID: 12}}}}, []models.Rule{{ID: 2, Text: "No spam"}}, "", "")
	for _, want := range []string{
		"Report #7",
		`href="/admin/reports/7/resolve" data-method="post"`,
		`class="report-header"`,
		`class="account-card"`,
		`data-admin-component="ReportReasonSelector"`,
		`&#34;category&#34;:&#34;violation&#34;`,
		`&#34;rule_ids&#34;:[&#34;2&#34;]`,
		`action="/admin/reports/7/actions/preview"`,
		`name="mark_as_sensitive" value="1"`,
		`name="suspend" value="1"`,
		`class="report-actions"`,
		`class="batch-table__body"`,
		`name="report_note[report_id]" value="7"`,
		`name="report_note[content]"`,
		`href="/admin/report_notes/5"`,
		`data-method="delete"`,
		"bad post",
		"hello",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("report html missing %q: %s", want, html)
		}
	}
}

func TestAdminReportActionPreviewHTMLPostsConfirmedAction(t *testing.T) {
	report := models.Report{
		ID:              7,
		TargetAccountID: 4,
		TargetAccount:   models.Account{ID: 4, Username: "target"},
	}
	body := adminReportActionPreviewHTML(report, []models.Status{{ID: 11, AccountID: 4, Text: strings.Repeat("hello ", 40)}}, "suspend", `bad <reason>`)
	for _, want := range []string{
		"Confirm action for report #7",
		`action="/admin/reports/7/actions"`,
		`name="moderation_action" value="suspend"`,
		`bad &lt;reason&gt;`,
		"@target will be suspended.",
		`href="/admin/accounts/4/statuses/11"`,
		`class="strike-card"`,
		`class="actions"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("preview html missing %q: %s", want, body)
		}
	}
	if strings.Contains(body, strings.Repeat("hello ", 30)) {
		t.Fatalf("preview status text was not compacted: %s", body)
	}
}

func TestAdminReportPreviewRouteRequiresSession(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/admin/reports/7/actions/preview", strings.NewReader("suspend=1"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	want := "/auth/sign_in?redirect_to=" + url.QueryEscape("/admin/reports/7/actions/preview")
	if got := rec.Header().Get("Location"); got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}
}

func TestAdminReportActionPreviewRouteStaysRegistered(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), `e.POST("/admin/reports/:report_id/actions/preview", s.previewAdminReportActionWeb)`) {
		t.Fatal("admin report action preview route is not registered")
	}
}

func TestAdminReportSuspendAppliesRailsSuspensionWorkerEffects(t *testing.T) {
	src, err := os.ReadFile("admin_reports_web.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`if action == "suspend"`,
		`s.publishStreamingKillForLocalAccount(*suspendedAccount)`,
		`s.enqueueAdminSuspensionOrRun(context.Background(), s.db, report.TargetAccountID)`,
	} {
		if !functionBodyContains(t, src, "createAdminReportActionWeb", want) {
			t.Fatalf("createAdminReportActionWeb missing %q", want)
		}
	}
}
