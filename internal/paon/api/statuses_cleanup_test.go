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

func TestParseStatusesCleanupPayload(t *testing.T) {
	body := strings.Join([]string{
		"account_statuses_cleanup_policy%5Benabled%5D=0",
		"account_statuses_cleanup_policy%5Benabled%5D=1",
		"account_statuses_cleanup_policy%5Bmin_status_age%5D=604800",
		"account_statuses_cleanup_policy%5Bkeep_direct%5D=0",
		"account_statuses_cleanup_policy%5Bkeep_media%5D=1",
		"account_statuses_cleanup_policy%5Bmin_favs%5D=5",
		"account_statuses_cleanup_policy%5Bmin_reblogs%5D=",
	}, "&")
	req := httptest.NewRequest(http.MethodPut, "/statuses_cleanup", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c := echo.NewContext(req, httptest.NewRecorder(), echo.New())

	updates, err := parseStatusesCleanupPayload(c)
	if err != nil {
		t.Fatal(err)
	}
	if updates["enabled"] != true || updates["keep_direct"] != false || updates["keep_media"] != true || updates["min_status_age"] != 604800 {
		t.Fatalf("updates = %#v", updates)
	}
	if updates["min_favs"].(sql.NullInt64).Int64 != 5 || !updates["min_favs"].(sql.NullInt64).Valid {
		t.Fatalf("min_favs = %#v", updates["min_favs"])
	}
	if updates["min_reblogs"] != nil {
		t.Fatalf("min_reblogs = %#v", updates["min_reblogs"])
	}
}

func TestParseStatusesCleanupPayloadValidatesRailsPolicyInputs(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "invalid minimum status age",
			body: "account_statuses_cleanup_policy%5Bmin_status_age%5D=86400",
			want: "Minimum age is invalid",
		},
		{
			name: "zero favourite threshold",
			body: "account_statuses_cleanup_policy%5Bmin_favs%5D=0",
			want: "Interaction threshold is invalid",
		},
		{
			name: "negative reblog threshold",
			body: "account_statuses_cleanup_policy%5Bmin_reblogs%5D=-1",
			want: "Interaction threshold is invalid",
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPut, "/statuses_cleanup", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			c := echo.NewContext(req, httptest.NewRecorder(), echo.New())

			_, err := parseStatusesCleanupPayload(c)
			if err == nil || err.Error() != tt.want {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestStatusesCleanupAllowedMinimumAgesMatchRails(t *testing.T) {
	want := []int{604800, 1209600, 2629746, 5259492, 7889238, 15778476, 31556952, 63113904}
	if len(allowedCleanupMinStatusAges) != len(want) {
		t.Fatalf("allowed minimum age count = %d, want %d", len(allowedCleanupMinStatusAges), len(want))
	}
	for _, age := range want {
		if _, ok := allowedCleanupMinStatusAges[age]; !ok {
			t.Fatalf("allowed minimum ages missing %d", age)
		}
	}
}

func TestStatusesCleanupHTMLRendersPolicyValues(t *testing.T) {
	html := statusesCleanupHTML(models.AccountStatusesCleanupPolicy{
		Enabled:      true,
		MinStatusAge: 604800,
		KeepPinned:   true,
		MinFavs:      sql.NullInt64{Int64: 3, Valid: true},
	}, "", "", "en")
	for _, want := range []string{
		`class="simple_form"`,
		`id="edit_policy"`,
		`class="fields-row"`,
		`class="fields-row__column fields-row__column-6 fields-group"`,
		`class="input with_label boolean optional account_statuses_cleanup_policy_enabled field_with_hint"`,
		`class="label_input__wrapper"`,
		`class="flash-message"`,
		`/statuses_cleanup`,
		`value="604800" selected`,
		`name="account_statuses_cleanup_policy[min_favs]" type="number" min="1" placeholder="Ignore favorites" value="3"`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("html missing %q: %s", want, html)
		}
	}
	if strings.Contains(html, `class="actions"`) {
		t.Fatalf("statuses cleanup must use the Rails heading action instead of a bottom action: %s", html)
	}

	withShell := statusesCleanupHTML(models.AccountStatusesCleanupPolicy{}, "", "", "en", "default", `<nav></nav>`)
	if !strings.Contains(withShell, `class="content__heading__actions"`) || !strings.Contains(withShell, `form="edit_policy"`) {
		t.Fatalf("statuses cleanup shell missing Rails heading save action: %s", withShell)
	}
}

func TestStatusesCleanupRequiresWebAuthentication(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodGet, "/statuses_cleanup", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())

	if err := s.statusesCleanupPage(c); err != nil {
		t.Fatal(err)
	}
	want := "/auth/sign_in?redirect_to=" + url.QueryEscape("/statuses_cleanup")
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != want {
		t.Fatalf("status = %d location = %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestStatusesCleanupSnowflakeAndRedisKeys(t *testing.T) {
	at := time.Unix(100, 900*int64(time.Millisecond)).UTC()
	if got, want := mastodonSnowflakeIDAt(at, false), int64(100*1000)<<16; got != want {
		t.Fatalf("snowflake id = %d, want %d", got, want)
	}
	if got := statusesCleanupPolicyRedisKey(42); got != "account_cleanup:42" {
		t.Fatalf("redis key = %q", got)
	}
}

func TestStatusesCleanupBudgetMatchesRailsComputeBudgetShape(t *testing.T) {
	if got := (&Server{}).statusesCleanupPaonGoPushConcurrency(); got != paonGoAsynqQueueWeights()[asynqQueuePush] {
		t.Fatalf("paon-go cleanup fallback concurrency = %d", got)
	}

	src, err := os.ReadFile("statuses_cleanup_worker.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		functionName string
		want         string
	}{
		{"runStatusesCleanupWorker", `s.processAccountStatusesCleanup(ctx, s.statusesCleanupBudget(ctx))`},
		{"statusesCleanupBudget", `threads := s.statusesCleanupPushConcurrency(ctx)`},
		{"statusesCleanupBudget", `budget := statusesCleanupPerThread * threads`},
		{"statusesCleanupBudget", `if budget > statusesCleanupMaxBudget`},
		{"statusesCleanupPushConcurrency", `"SMEMBERS", redisConfig(s.cfg).prefix+"processes"`},
		{"statusesCleanupPushConcurrencyForIdentity", `sidekiqProcessQueuesFromRedis(queuesRaw)`},
		{"statusesCleanupPushConcurrencyForIdentity", `if queue == "push"`},
		{"statusesCleanupPushConcurrencyForIdentity", `"HGET", redisConfig(s.cfg).prefix+identity, "concurrency"`},
		{"statusesCleanupPushConcurrencyForIdentity", `redisInt(concurrencyValue)`},
		{"statusesCleanupPaonGoPushConcurrency", `paonGoAsynqQueueWeights()[asynqQueuePush]`},
	} {
		if !functionBodyContains(t, src, check.functionName, check.want) {
			t.Fatalf("%s missing Rails-compatible budget fragment %q", check.functionName, check.want)
		}
	}
}

func TestStatusesCleanupSchedulerCursorMatchesRailsKeyAndTTL(t *testing.T) {
	if statusesCleanupSchedulerKey != "account_statuses_cleanup_scheduler:last_policy_id" {
		t.Fatalf("scheduler key = %q", statusesCleanupSchedulerKey)
	}
	src, err := os.ReadFile("statuses_cleanup_worker.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`redisConfig(s.cfg).prefix+statusesCleanupSchedulerKey`,
		`"EX", "3600"`,
	} {
		if !functionBodyContains(t, src, "saveStatusesCleanupSchedulerLastPolicyID", want) {
			t.Fatalf("scheduler cursor save missing %q", want)
		}
	}
}

func TestSidekiqJobEnqueuedAt(t *testing.T) {
	got, ok := sidekiqJobEnqueuedAt(`{"class":"RemovalWorker","enqueued_at":100.25}`)
	if !ok {
		t.Fatal("enqueued_at was not parsed")
	}
	if got.Unix() != 100 || got.Nanosecond() != 250_000_000 {
		t.Fatalf("enqueued at = %s", got.Format(time.RFC3339Nano))
	}
	got, ok = sidekiqJobEnqueuedAt(`{"class":"RemovalWorker","created_at":101.5}`)
	if !ok || got.Unix() != 101 || got.Nanosecond() != 500_000_000 {
		t.Fatalf("created_at fallback = %s ok=%v", got.Format(time.RFC3339Nano), ok)
	}
	if _, ok := sidekiqJobEnqueuedAt(`{`); ok {
		t.Fatal("invalid JSON should not parse")
	}
	if _, ok := sidekiqJobEnqueuedAt(`{"class":"RemovalWorker"}`); ok {
		t.Fatal("missing timestamp should not parse")
	}
}

func TestStatusesCleanupWorkerUsesRailsLoadBackoff(t *testing.T) {
	src, err := os.ReadFile("statuses_cleanup_worker.go")
	if err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		functionName string
		want         string
	}{
		{"processAccountStatusesCleanup", `s.statusesCleanupUnderLoad(ctx, time.Now().UTC())`},
		{"statusesCleanupUnderLoad", `statusesCleanupQueueLatencyLimits`},
		{"statusesCleanupUnderLoad", `s.sidekiqQueueLatencyOver(ctx, queue.name, queue.maxLatency, now)`},
		{"sidekiqQueueLatencyOver", `"LINDEX", redisConfig(s.cfg).prefix+"queue:"+queueName, "-1"`},
		{"sidekiqQueueLatencyOver", `return now.Sub(enqueuedAt) > maxLatency`},
	}
	for _, check := range checks {
		if !functionBodyContains(t, src, check.functionName, check.want) {
			t.Fatalf("%s missing %q", check.functionName, check.want)
		}
	}
}
