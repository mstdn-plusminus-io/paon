package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestParseScheduledAtAcceptsRFC3339(t *testing.T) {
	got, err := parseScheduledAt("2026-06-18T12:34:56Z")
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 6, 18, 12, 34, 56, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("scheduled_at = %s, want %s", got, want)
	}
}

func TestParseScheduledAtAcceptsRailsDateTimeLocalValues(t *testing.T) {
	cases := map[string]time.Time{
		"2026-06-18T12:34":       time.Date(2026, 6, 18, 12, 34, 0, 0, time.UTC),
		"2026-06-18 12:34":       time.Date(2026, 6, 18, 12, 34, 0, 0, time.UTC),
		"2026-06-18T12:34+09:00": time.Date(2026, 6, 18, 3, 34, 0, 0, time.UTC),
	}
	for input, want := range cases {
		got, err := parseScheduledAt(input)
		if err != nil {
			t.Fatalf("parseScheduledAt(%q): %v", input, err)
		}
		if !got.Equal(want) {
			t.Fatalf("parseScheduledAt(%q) = %s, want %s", input, got, want)
		}
	}
}

func TestScheduledStatusTooSoonMatchesRailsOffset(t *testing.T) {
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	if !scheduledStatusTooSoon(now.Add(5*time.Minute), now) {
		t.Fatal("exactly five minutes ahead should be too soon")
	}
	if scheduledStatusTooSoon(now.Add(5*time.Minute+time.Second), now) {
		t.Fatal("more than five minutes ahead should be allowed")
	}
	if !scheduledStatusTooSoon(now.Add(-time.Second), now) {
		t.Fatal("past scheduled_at should not be accepted as a scheduled status")
	}
}

func TestScheduledStatusValidationMessagesMatchRailsLocales(t *testing.T) {
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	err := (&Server{}).validateScheduledStatus(nil, 0, now.Add(5*time.Minute), now)
	apiErr, ok := err.(apiHTTPError)
	if !ok || apiErr.status != http.StatusUnprocessableEntity || apiErr.message != railsScheduledStatusTooSoonMessage {
		t.Fatalf("too soon error = %#v", err)
	}
	if railsScheduledStatusTooSoonMessage != "Validation failed: Scheduled at The scheduled date must be in the future" {
		t.Fatalf("too soon message drifted: %q", railsScheduledStatusTooSoonMessage)
	}
	if railsScheduledStatusTotalLimitMessage != "Validation failed: You have exceeded the limit of 300 scheduled posts" {
		t.Fatalf("total limit message drifted: %q", railsScheduledStatusTotalLimitMessage)
	}
	if railsScheduledStatusDailyLimitMessage != "Validation failed: You have exceeded the limit of 25 scheduled posts for today" {
		t.Fatalf("daily limit message drifted: %q", railsScheduledStatusDailyLimitMessage)
	}
}

func TestParseScheduledStatusUpdatePayloadTracksOmittedScheduledAtLikeRails(t *testing.T) {
	req := httptest.NewRequest("PUT", "/api/v1/scheduled_statuses/1", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	payload, err := parseScheduledStatusUpdatePayload(echo.NewContext(req, httptest.NewRecorder(), echo.New()))
	if err != nil {
		t.Fatal(err)
	}
	if payload.HasScheduledAt {
		t.Fatalf("omitted JSON scheduled_at should be a no-op payload: %#v", payload)
	}

	req = httptest.NewRequest("PUT", "/api/v1/scheduled_statuses/1", strings.NewReader(`{"scheduled_at":"2026-06-18T12:34:56Z"}`))
	req.Header.Set("Content-Type", "application/json")
	payload, err = parseScheduledStatusUpdatePayload(echo.NewContext(req, httptest.NewRecorder(), echo.New()))
	if err != nil {
		t.Fatal(err)
	}
	if !payload.HasScheduledAt || payload.ScheduledAt != "2026-06-18T12:34:56Z" {
		t.Fatalf("JSON scheduled_at payload = %#v", payload)
	}

	req = httptest.NewRequest("PUT", "/api/v1/scheduled_statuses/1", strings.NewReader(`{"scheduled_at":null}`))
	req.Header.Set("Content-Type", "application/json")
	payload, err = parseScheduledStatusUpdatePayload(echo.NewContext(req, httptest.NewRecorder(), echo.New()))
	if err != nil {
		t.Fatal(err)
	}
	if !payload.HasScheduledAt || !payload.ClearScheduledAt {
		t.Fatalf("JSON null scheduled_at should clear like Rails datetime params: %#v", payload)
	}

	req = httptest.NewRequest("PUT", "/api/v1/scheduled_statuses/1", strings.NewReader(""))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	payload, err = parseScheduledStatusUpdatePayload(echo.NewContext(req, httptest.NewRecorder(), echo.New()))
	if err != nil {
		t.Fatal(err)
	}
	if payload.HasScheduledAt {
		t.Fatalf("omitted form scheduled_at should be a no-op payload: %#v", payload)
	}

	req = httptest.NewRequest("PUT", "/api/v1/scheduled_statuses/1", strings.NewReader("scheduled_at="))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	payload, err = parseScheduledStatusUpdatePayload(echo.NewContext(req, httptest.NewRecorder(), echo.New()))
	if err != nil {
		t.Fatal(err)
	}
	if !payload.HasScheduledAt || !payload.ClearScheduledAt {
		t.Fatalf("blank form scheduled_at should clear like Rails datetime params: %#v", payload)
	}
}

func TestUpdateScheduledStatusOmittedScheduledAtReturnsCurrentStatusLikeRails(t *testing.T) {
	src, err := os.ReadFile("scheduled_statuses.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`if !payload.HasScheduledAt {`,
		`return c.JSON(http.StatusOK, serializer.ScheduledStatusFromModel(s.cfg, status))`,
		`payload.HasScheduledAt = true`,
		`payload.ClearScheduledAt = true`,
		`validateScheduledStatusNullable`,
	} {
		if !strings.Contains(string(src), want) {
			t.Fatalf("scheduled status no-op update parity missing %q", want)
		}
	}
}

func TestScheduledStatusesRequireAuth(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest("GET", "/api/v1/scheduled_statuses", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	s := &Server{}
	err := s.scheduledStatuses(c)
	if err == nil {
		t.Fatal("expected auth error")
	}
	handleAPIError(c, err)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "The access token is invalid") {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestScheduledStatusesApplicationTokenMatchesRailsRequireUser(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`var errApplicationTokenRequiresUser = errors.New("application token requires authenticated user")`,
		`return nil, "", errApplicationTokenRequiresUser`,
		`errors.Is(err, errApplicationTokenRequiresUser)`,
		`apiError(c, http.StatusUnprocessableEntity, "This method requires an authenticated user")`,
	} {
		if !strings.Contains(string(src), want) {
			t.Fatalf("Rails application-token require_user! parity missing %q", want)
		}
	}
}

func TestReverseScheduledStatusesKeepsNewestFirstForMinIDPagination(t *testing.T) {
	rows := []models.ScheduledStatus{{ID: 101}, {ID: 102}, {ID: 103}}
	reverseScheduledStatuses(rows)
	if rows[0].ID != 103 || rows[1].ID != 102 || rows[2].ID != 101 {
		t.Fatalf("rows = %#v", rows)
	}
}

func TestScheduledStatusRunsCallbacksAfterPersistenceTransaction(t *testing.T) {
	src, err := os.ReadFile("scheduled_status_publish.go")
	if err != nil {
		t.Fatal(err)
	}
	body := functionBody(t, src, "publishScheduledStatus")
	transactionIndex := strings.Index(body, `s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {`)
	effectsIndex := strings.Index(body, `s.runLocalStatusAfterCreateCommitEffects(s.db.WithContext(ctx), created, account, nil, replyTo`)
	findIndex := strings.Index(body, `s.findStatus(strconv.FormatInt(status.ID, 10))`)
	if transactionIndex < 0 || effectsIndex < 0 || findIndex < 0 || !(transactionIndex < effectsIndex && effectsIndex < findIndex) {
		t.Fatal("publishScheduledStatus must run Rails Status callbacks after its persistence transaction and before serialization lookup")
	}
	for _, forbidden := range []string{
		`s.storeLocalStatusURI(tx, &status, account, now)`,
		`incrementStatusStatCounter(tx, replyTo.ID, statusStatCounterReplies, 1)`,
		`upsertAccountStatForStatus(tx, account.ID, status.Visibility, now)`,
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("publishScheduledStatus still runs after-create-commit effect inside its transaction: %q", forbidden)
		}
	}
}
