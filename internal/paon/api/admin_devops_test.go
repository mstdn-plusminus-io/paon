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

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
)

func TestDevopsPagesRequireWebSession(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/asynq", "/asynq/stats", "/asynq/queues", "/sidekiq", "/sidekiq/stats", "/sidekiq/retries", "/pghero", "/pghero/space"} {
		req := httptest.NewRequest(http.MethodGet, path+"?x=1", nil)
		rec := httptest.NewRecorder()
		s.echo.ServeHTTP(rec, req)
		if rec.Code != http.StatusFound {
			t.Fatalf("%s status = %d body = %s", path, rec.Code, rec.Body.String())
		}
		want := "/auth/sign_in?redirect_to=" + url.QueryEscape(path+"?x=1")
		if got := rec.Header().Get("Location"); got != want {
			t.Fatalf("%s Location = %q, want %q", path, got, want)
		}
	}
	retryRequest := httptest.NewRequest(http.MethodPost, "/asynq/tasks/retry?x=1", strings.NewReader(url.Values{
		"source_state": {"retry"},
		"queue":        {"default"},
		"task_id":      {"task"},
	}.Encode()))
	retryRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	retryRecorder := httptest.NewRecorder()
	s.echo.ServeHTTP(retryRecorder, retryRequest)
	if retryRecorder.Code != http.StatusFound {
		t.Fatalf("unauthenticated Asynq retry status = %d body = %s", retryRecorder.Code, retryRecorder.Body.String())
	}
	want := "/auth/sign_in?redirect_to=" + url.QueryEscape("/asynq/tasks/retry?x=1")
	if got := retryRecorder.Header().Get("Location"); got != want {
		t.Fatalf("unauthenticated Asynq retry Location = %q, want %q", got, want)
	}
}

func TestAsynqAndPgHeroHTMLHelpers(t *testing.T) {
	asynqHTML := asynqPageHTML(asynqDashboardSnapshot{
		Timestamp: "2026-07-14T10:00:00Z",
		Available: true,
		Summary:   asynqDashboardSummary{ProcessedTotal: 12, FailedTotal: 2, Pending: 3, Active: 1},
		Queues: []asynqQueueView{
			{Name: "default", DisplayName: "default", Pending: 3, Active: 1, Latency: "2s", Memory: "1.0 KB", Status: "healthy", StatusLabel: "Healthy"},
			{Name: "pull", DisplayName: "pull", Status: "idle", StatusLabel: "Idle"},
		},
		Servers: []asynqServerView{},
		Issues:  []asynqDashboardIssue{},
		History: []asynqHistoryView{{Date: "2026-07-14", Processed: 12, Failed: 2, Succeeded: 10}},
	}, nil, "overview", "en")
	for _, want := range []string{"default", "pull", "Pending", "Active", "Servers and running tasks", "History", "2026-07-14"} {
		if !strings.Contains(asynqHTML, want) {
			t.Fatalf("Asynq HTML missing %q: %s", want, asynqHTML)
		}
	}
	for _, want := range []string{`data-asynq-dashboard`, `data-stats-url="/asynq/stats"`, `id="asynq_polling_enabled"`, `id="asynq_polling_interval"`, `min="2" max="20"`, `<option value="7" selected>`, `data-asynq-queue-body`, `data-queue="default"`} {
		if !strings.Contains(asynqHTML, want) {
			t.Fatalf("Asynq HTML missing polling control %q: %s", want, asynqHTML)
		}
	}
	for _, unwanted := range []string{`href="/admin/dashboard"`, `<h2>Asynq</h2>`, `<option value="30" selected>`, ">Review<"} {
		if strings.Contains(asynqHTML, unwanted) {
			t.Fatalf("Asynq HTML retained redundant navigation %q: %s", unwanted, asynqHTML)
		}
	}

	pghero := pgHeroPageHTML([]pgHeroRelationStatus{{
		Schema:     sql.NullString{String: "public", Valid: true},
		Relation:   sql.NullString{String: "statuses", Valid: true},
		Size:       sql.NullInt64{Int64: 1536, Valid: true},
		CapturedAt: sql.NullTime{Time: time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC), Valid: true},
	}}, "en")
	for _, want := range []string{"PgHero", "public.statuses", "1.5 KB", "2026-06-30T12:00:00Z"} {
		if !strings.Contains(pghero, want) {
			t.Fatalf("pghero HTML missing %q: %s", want, pghero)
		}
	}
}

func TestDevopsHTMLHelpersUseLocaleKeys(t *testing.T) {
	asynqHTML := asynqPageHTML(asynqDashboardSnapshot{
		Available: true,
		Queues:    []asynqQueueView{{Name: "default", DisplayName: "default", Status: "healthy", StatusLabel: "正常"}},
		Servers:   []asynqServerView{},
		Issues:    []asynqDashboardIssue{},
		History:   []asynqHistoryView{},
	}, nil, "overview", "ja")
	for _, want := range []string{"概要", "キュー", "待機中", "実行中", "状態", "サーバーと実行中タスク", "履歴", "自動更新", "ポーリング周期"} {
		if !strings.Contains(asynqHTML, want) {
			t.Fatalf("localized Asynq HTML missing %q: %s", want, asynqHTML)
		}
	}

	pghero := pgHeroPageHTML([]pgHeroRelationStatus{{
		Schema:     sql.NullString{String: "public", Valid: true},
		Relation:   sql.NullString{String: "statuses", Valid: true},
		Size:       sql.NullInt64{Int64: 1536, Valid: true},
		CapturedAt: sql.NullTime{Time: time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC), Valid: true},
	}}, "ja")
	for _, want := range []string{"リレーション", "サイズ", "取得日時"} {
		if !strings.Contains(pghero, want) {
			t.Fatalf("localized pghero HTML missing %q: %s", want, pghero)
		}
	}
}

func TestAsynqUnavailableHTMLDoesNotPresentZeroCountsAndCanRecover(t *testing.T) {
	snapshot := asynqUnavailableSnapshot("en", errors.New("redis unavailable"))
	page := asynqPageHTML(snapshot, nil, "overview", "en")
	for _, want := range []string{`data-asynq-counter="pending">—`, `data-asynq-error role="alert"`, `data-asynq-recovery-content hidden`, `data-asynq-queue-body`, `data-asynq-server-body`, `data-asynq-history-body`} {
		if !strings.Contains(page, want) {
			t.Fatalf("unavailable Asynq HTML missing %q: %s", want, page)
		}
	}
	if strings.Contains(page, `data-asynq-counter="pending">0`) || strings.Contains(page, `data-asynq-last-updated data-label="Last updated">2026`) {
		t.Fatalf("unavailable Asynq HTML presented successful zero/fresh data: %s", page)
	}
}

func TestAsynqHistoryHTMLDefaultsToSevenDays(t *testing.T) {
	history := make([]asynqHistoryView, 10)
	for i := range history {
		history[i] = asynqHistoryView{Date: time.Date(2026, 7, i+1, 0, 0, 0, 0, time.UTC).Format("2006-01-02")}
	}
	rows := asynqHistoryRowsHTML(history, "en")
	if got := strings.Count(rows, "<tr>"); got != 7 {
		t.Fatalf("history row count = %d, want 7: %s", got, rows)
	}
	if strings.Contains(rows, "2026-07-03") || !strings.Contains(rows, "2026-07-04") || !strings.Contains(rows, "2026-07-10") {
		t.Fatalf("history rows do not contain the latest seven days: %s", rows)
	}
}

func TestPgHeroLatestRelationStatsNilDB(t *testing.T) {
	stats, err := (&Server{}).pgHeroLatestRelationStats()
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 0 {
		t.Fatalf("stats = %#v", stats)
	}

	src, err := os.ReadFile("admin_devops.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`db := s.pgHeroStatsDatabase()`,
		`db.Model(&models.PgHeroSpaceStat{})`,
		`Where("captured_at = (?)", db.Model(&models.PgHeroSpaceStat{}).Select("MAX(captured_at)"))`,
	} {
		if !strings.Contains(string(src), want) {
			t.Fatalf("admin_devops.go missing PgHero stats DB fragment %q", want)
		}
	}
}
