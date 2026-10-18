package api

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestDisputeStrikeAppealFormOnlyForTargetLikeRailsCanAppeal(t *testing.T) {
	strike := models.AccountWarning{ID: 7, Action: 1000, Text: "warning", CreatedAt: time.Now().UTC(), TargetAccountID: sql.NullInt64{Int64: 3, Valid: true}}

	html := disputeStrikeHTMLWithOptions(strike, nil, "", "", disputeStrikeViewOptions{CanAppeal: true}, "en")
	if !strings.Contains(html, `action="/disputes/strikes/7/appeal"`) {
		t.Fatalf("target viewer should get the appeal form: %s", html)
	}

	// A moderator viewing someone else's strike must not get a form that
	// POSTs to a route scoped to their own strikes (404).
	html = disputeStrikeHTMLWithOptions(strike, nil, "", "", disputeStrikeViewOptions{CanAppeal: false, CanDecideOnAppeals: true}, "en")
	if strings.Contains(html, `/disputes/strikes/7/appeal`) || strings.Contains(html, `name="appeal[text]"`) {
		t.Fatalf("non-target viewer must not get the appeal form: %s", html)
	}
	if strings.Contains(html, "Appeal period has expired") || strings.Contains(html, "too old") {
		t.Fatalf("Rails renders nothing when can?(:appeal) is false: %s", html)
	}

	expired := strike
	expired.CreatedAt = time.Now().UTC().Add(-30 * 24 * time.Hour)
	html = disputeStrikeHTML(expired, nil, "", "", "en")
	if strings.Contains(html, `name="appeal[text]"`) {
		t.Fatalf("expired strike must not render the appeal form: %s", html)
	}
}

func TestDisputeStrikeHeadingRendersApproveRejectForPendingAppeal(t *testing.T) {
	strike := models.AccountWarning{ID: 7, Action: 1000, CreatedAt: time.Now().UTC()}
	pending := &models.Appeal{ID: 11, AccountWarningID: 7, Text: "please", CreatedAt: time.Now().UTC()}

	html := disputeStrikeHTMLWithOptions(strike, pending, "", "", disputeStrikeViewOptions{CanDecideOnAppeals: true}, "en")
	for _, want := range []string{
		`<div class="content__heading__actions">`,
		`<a class="button" rel="nofollow" data-method="post" href="/admin/disputes/appeals/11/approve">Approve appeal</a>`,
		`<a class="button button--destructive" rel="nofollow" data-method="post" href="/admin/disputes/appeals/11/reject">Reject appeal</a>`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("html missing %q: %s", want, html)
		}
	}

	ja := disputeStrikeHTMLWithOptions(strike, pending, "", "", disputeStrikeViewOptions{CanDecideOnAppeals: true}, "ja")
	if !strings.Contains(ja, "異議申立を承認") || !strings.Contains(ja, "異議申立を却下") {
		t.Fatalf("approve/reject labels must be localized: %s", ja)
	}

	cases := map[string]struct {
		appeal  *models.Appeal
		options disputeStrikeViewOptions
	}{
		"no manage_appeals": {pending, disputeStrikeViewOptions{}},
		"no appeal":         {nil, disputeStrikeViewOptions{CanDecideOnAppeals: true}},
		"unsaved appeal":    {&models.Appeal{Text: "draft"}, disputeStrikeViewOptions{CanDecideOnAppeals: true}},
		"approved appeal":   {&models.Appeal{ID: 11, ApprovedAt: sql.NullTime{Time: time.Now(), Valid: true}}, disputeStrikeViewOptions{CanDecideOnAppeals: true}},
		"rejected appeal":   {&models.Appeal{ID: 11, RejectedAt: sql.NullTime{Time: time.Now(), Valid: true}}, disputeStrikeViewOptions{CanDecideOnAppeals: true}},
	}
	for name, tc := range cases {
		html := disputeStrikeHTMLWithOptions(strike, tc.appeal, "", "", tc.options, "en")
		if strings.Contains(html, "/admin/disputes/appeals/") {
			t.Fatalf("%s: approve/reject actions must be hidden: %s", name, html)
		}
	}
}

func TestDisputeStrikeAssociatedReportRequiresManageReports(t *testing.T) {
	strike := models.AccountWarning{ID: 7, Action: 1000, CreatedAt: time.Now().UTC(), ReportID: sql.NullInt64{Int64: 42, Valid: true}}

	html := disputeStrikeHTMLWithOptions(strike, nil, "", "", disputeStrikeViewOptions{CanAppeal: true}, "en")
	if strings.Contains(html, `href="/admin/reports/42"`) || strings.Contains(html, "Associated report") {
		t.Fatalf("viewer without manage_reports must not see the report link: %s", html)
	}
	html = disputeStrikeHTMLWithOptions(strike, nil, "", "", disputeStrikeViewOptions{CanShowReport: true}, "en")
	if !strings.Contains(html, `href="/admin/reports/42"`) || !strings.Contains(html, "Associated report") {
		t.Fatalf("viewer with manage_reports should see the report link: %s", html)
	}
}

func TestDisputeStrikeAppealSubmittedAtOnlyForPersistedAppeal(t *testing.T) {
	strike := models.AccountWarning{ID: 7, Action: 1000, CreatedAt: time.Now().UTC()}

	html := disputeStrikeHTML(strike, &models.Appeal{Text: "   "}, "", "Appeal text is invalid", "en")
	if strings.Contains(html, "Appeal submitted") || strings.Contains(html, "0001-01-01") {
		t.Fatalf("unsaved appeal must not render a submitted-at row: %s", html)
	}
	if !strings.Contains(html, `name="appeal[text]"`) {
		t.Fatalf("unsaved appeal should re-render the form: %s", html)
	}

	submitted := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	html = disputeStrikeHTML(strike, &models.Appeal{ID: 11, Text: "please", CreatedAt: submitted}, "", "", "en")
	if !strings.Contains(html, "Appeal submitted") || !strings.Contains(html, `datetime="2026-10-01T12:00:00Z"`) {
		t.Fatalf("persisted appeal should render the submitted-at row: %s", html)
	}
}
