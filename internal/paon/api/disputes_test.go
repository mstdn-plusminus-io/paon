package api

import (
	"errors"
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

func TestDisputeStrikesHTMLRendersRows(t *testing.T) {
	html := disputeStrikesHTML([]models.AccountWarning{{
		ID:        7,
		Action:    3000,
		Text:      "spam",
		CreatedAt: time.Date(2026, 6, 19, 0, 0, 0, 0, time.UTC),
	}}, map[int64]models.Appeal{
		7: {AccountWarningID: 7},
	}, "saved", "problem", "en", "default", "example.test")
	for _, want := range []string{"/disputes/strikes/7", "Limitation of account", "spam", "staff of example.test", `class="log-entry"`, `class="indicator-icon failure"`, `class="account-strikes"`, `class="warning-hint"`, "You have submitted an appeal", `class="flash-message notice"`, `flash-message alert`, `<body class="admin theme-`, `class="sidebar"`} {
		if !strings.Contains(html, want) {
			t.Fatalf("html missing %q: %s", want, html)
		}
	}
}

func TestDisputeStrikeHTMLRendersAppealFormOrExistingAppeal(t *testing.T) {
	strike := models.AccountWarning{ID: 7, Action: 1000, Text: "warning", CreatedAt: time.Now().UTC()}
	html := disputeStrikeHTML(strike, nil, "", "")
	for _, want := range []string{`/disputes/strikes/7/appeal`, `name="appeal[text]"`, `maxlength="500"`, `class="simple_form"`, `class="fields-group"`, `class="actions"`, `class="report-header"`, `class="strike-card"`, `<body class="admin theme-`, `class="sidebar"`} {
		if !strings.Contains(html, want) {
			t.Fatalf("html missing %q: %s", want, html)
		}
	}
	if strings.Contains(html, `maxlength="2000"`) {
		t.Fatalf("html missing appeal form: %s", html)
	}
	html = disputeStrikeHTML(strike, &models.Appeal{Text: "please review"}, "saved", "invalid")
	if !strings.Contains(html, `name="appeal[text]"`) || !strings.Contains(html, "please review") || !strings.Contains(html, `flash-message alert`) || strings.Contains(html, `class="report-notes"`) {
		t.Fatalf("unsaved invalid appeal should re-render the form like Rails: %s", html)
	}
	html = disputeStrikeHTML(strike, &models.Appeal{ID: 11, Text: "please review", CreatedAt: time.Date(2026, 6, 19, 1, 2, 3, 0, time.UTC)}, "", "")
	if !strings.Contains(html, "please review") || !strings.Contains(html, `class="report-notes"`) || !strings.Contains(html, `class="report-notes__item__avatar"`) || !strings.Contains(html, `class="relative-formatted"`) || !strings.Contains(html, `title="2026-06-19T01:02:03Z"`) || strings.Contains(html, `name="appeal[text]"`) {
		t.Fatalf("html missing existing appeal: %s", html)
	}
}

func TestDisputeAppealTextRequiresRailsRootParameter(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/disputes/strikes/7/appeal", strings.NewReader("text=raw+appeal"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c := echo.NewContext(req, httptest.NewRecorder(), echo.New())
	if _, err := disputeAppealText(c); !errors.Is(err, errDisputeAppealParamsMissing) {
		t.Fatalf("flat text should be rejected like Rails params.require(:appeal), got %v", err)
	}

	req = httptest.NewRequest(http.MethodPost, "/disputes/strikes/7/appeal", strings.NewReader("appeal%5Btext%5D=+raw+appeal+"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c = echo.NewContext(req, httptest.NewRecorder(), echo.New())
	text, err := disputeAppealText(c)
	if err != nil {
		t.Fatal(err)
	}
	if text != " raw appeal " {
		t.Fatalf("text = %q", text)
	}
}

func TestDisputeStrikesRequireWebAuthentication(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodGet, "/disputes/strikes", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())

	if err := s.disputeStrikesPage(c); err != nil {
		t.Fatal(err)
	}
	want := "/auth/sign_in?redirect_to=" + url.QueryEscape("/disputes/strikes")
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != want {
		t.Fatalf("status = %d location = %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestCreateDisputeAppealKeepsStaffMailSideEffect(t *testing.T) {
	src, err := os.ReadFile("disputes.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, src, "createDisputeAppeal", "s.sendStaffNewAppealMails(appeal)") {
		t.Fatal("createDisputeAppeal does not send staff appeal mail")
	}
}
