package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
)

func TestTimelinePreviewEnabledDefaultsToRailsDefault(t *testing.T) {
	if !(&Server{}).timelinePreviewEnabled() {
		t.Fatal("timelinePreviewEnabled = false")
	}
}

func TestPublicTimelineReturnsEmptyListWithoutDBWhenPreviewEnabled(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest("GET", "/api/v1/timelines/public", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)
	s := &Server{}

	if err := s.publicTimeline(c); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK || rec.Body.String() != "[]\n" {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestTagTimelineReturnsEmptyListWithoutDBWhenPreviewEnabled(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest("GET", "/api/v1/timelines/tag/go", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)
	s := &Server{}

	if err := s.tagTimeline(c); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK || rec.Body.String() != "[]\n" {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestPublicAndTagTimelinesMatchRailsCacheAndVaryHeaders(t *testing.T) {
	tests := []struct {
		path    string
		handler func(*Server, *echo.Context) error
	}{
		{path: "/api/v1/timelines/public", handler: (*Server).publicTimeline},
		{path: "/api/v1/timelines/tag/go", handler: (*Server).tagTimeline},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			e := echo.New()
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			rec := httptest.NewRecorder()
			c := echo.NewContext(req, rec, e)
			s := &Server{}

			if err := tt.handler(s, c); err != nil {
				t.Fatal(err)
			}
			if rec.Code != http.StatusOK || rec.Body.String() != "[]\n" {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get("Vary"); got != "Authorization" {
				t.Fatalf("Vary = %q, want Authorization", got)
			}
			if got := rec.Header().Get("Cache-Control"); got != "max-age=15, public, stale-while-revalidate=30, stale-if-error=86400" {
				t.Fatalf("Cache-Control = %q", got)
			}
		})
	}
}

func TestTimelinePreviewDisabledRequiresUserWithRailsUnprocessableStatus(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, src, "requireTimelinePreviewAccess", `return apiError(c, http.StatusUnprocessableEntity, "This method requires an authenticated user")`) {
		t.Fatal("requireTimelinePreviewAccess must return Rails-compatible 422 when preview is disabled and no user is available")
	}
	if functionBodyContains(t, src, "requireTimelinePreviewAccess", `return err`) {
		t.Fatal("requireTimelinePreviewAccess should not leak requireAccount's generic 401 for anonymous preview-disabled timelines")
	}
}

func TestTagTimelineParamValuesMatchesRailsArrayParams(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest("GET", "/?any[]=Go,Rust&any[]=go&any[]=%23Ruby&any[]=Elixir&any[]=Python", nil)
	c := echo.NewContext(req, httptest.NewRecorder(), e)

	got := tagTimelineParamValues(c, "any", "any[]")
	want := []string{"gorust", "go", "ruby", "elixir"}
	if len(got) != len(want) {
		t.Fatalf("values = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("values = %#v, want %#v", got, want)
		}
	}
}

func timelineStringSlicesEqual(a []string, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestTagTimelineAnyValuesLimitIncludesPathTagLikeRails(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest("GET", "/?any[]=Rust,Ruby,Elixir,Python", nil)
	c := echo.NewContext(req, httptest.NewRecorder(), e)

	got := tagTimelineParamValuesWithInitial(c, []string{"Go"}, "any", "any[]")
	want := []string{"go", "rustrubyelixirpython"}
	if len(got) != len(want) {
		t.Fatalf("values = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("values = %#v, want %#v", got, want)
		}
	}
}

func TestStatusHasAnyTagSQLUsesInPredicate(t *testing.T) {
	if !strings.Contains(statusHasAnyTagSQL(), "IN ?") {
		t.Fatalf("sql = %s", statusHasAnyTagSQL())
	}
}

func TestTimelineLocationParamsMatchRailsMutualLocalRemoteMode(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest("GET", "/?local=1&remote=true", nil)
	c := echo.NewContext(req, httptest.NewRecorder(), e)

	localOnly, remoteOnly := timelineLocationParams(c)
	if localOnly || remoteOnly {
		t.Fatalf("localOnly = %v, remoteOnly = %v", localOnly, remoteOnly)
	}

	req = httptest.NewRequest("GET", "/?local=on", nil)
	c = echo.NewContext(req, httptest.NewRecorder(), e)
	localOnly, remoteOnly = timelineLocationParams(c)
	if !localOnly || remoteOnly {
		t.Fatalf("truthy local mode: localOnly = %v, remoteOnly = %v", localOnly, remoteOnly)
	}
}

func TestPublicAndTagTimelinesApplyRailsPublicFeedFilters(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	for fn, want := range map[string]string{
		"publicTimeline": `query := s.publicTimelineStatusQuery()`,
		"tagTimeline":    `query := s.publicTimelineStatusQuery()`,
	} {
		if !functionBodyContains(t, src, fn, want) {
			t.Fatalf("server.go:%s does not contain %q", fn, want)
		}
		if !functionBodyContains(t, src, fn, `applyPublicTimelineAccountFilters(query, current, localOnly)`) {
			t.Fatalf("server.go:%s does not apply authenticated public timeline account filters", fn)
		}
	}
	for _, fn := range []string{"publicTimeline", "tagTimeline"} {
		if !functionBodyContains(t, src, fn, `query = applyOnlyMediaFilter(c, query)`) {
			t.Fatalf("server.go:%s must keep Rails public/tag media-only filtering independent of account owner", fn)
		}
	}
	for fn, want := range map[string]string{
		"publicTimelineStatusQuery":         `timeline_accounts.suspended_at IS NULL AND timeline_accounts.silenced_at IS NULL`,
		"applyPublicTimelineAccountFilters": `timeline_blocked_by.target_account_id = ?`,
	} {
		if !functionBodyContains(t, src, fn, want) {
			t.Fatalf("server.go:%s does not contain %q", fn, want)
		}
	}
}

func TestHomeTimelineAppliesRailsFeedManagerFilters(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		fn   string
		want string
	}{
		{"homeTimeline", `query := s.homeTimelineQuery(account)`},
		{"homeTimelineQuery", `statuses.account_id = ? OR (follows.id IS NOT NULL AND statuses.visibility IN ?)`},
		{"applyHomeTimelineFilters", `array_length(follows.languages, 1) IS NULL`},
		{"applyHomeTimelineFilters", `home_exclusive_lists.exclusive = true`},
	} {
		if !functionBodyContains(t, src, check.fn, check.want) {
			t.Fatalf("server.go:%s does not contain %q", check.fn, check.want)
		}
	}
	for _, want := range []string{
		`home_blocks.target_account_id = statuses.account_id`,
		`home_mutes.target_account_id = statuses.account_id`,
		`home_blocked_by.target_account_id = ?`,
		`statuses.in_reply_to_account_id = statuses.account_id`,
		`follows.show_reblogs = true`,
		`home_reblog_domain_blocks.account_id = ?`,
	} {
		if !functionBodyContains(t, src, "applyHomeTimelineFilters", want) {
			t.Fatalf("server.go:applyHomeTimelineFilters does not contain %q", want)
		}
	}
}

func TestHomeTimelineUsesRailsRegenerationPartialStatus(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		fn   string
		want string
	}{
		{"homeTimeline", `if s.homeFeedRegenerating(c.Request().Context(), account.ID)`},
		{"homeTimeline", `statusCode = http.StatusPartialContent`},
		{"homeTimeline", `return s.statusListWithStatus(c, query, statusCode)`},
		{"homeFeedRegenerating", `"EXISTS", redisConfig(s.cfg).prefix+"account:"+strconv.FormatInt(accountID, 10)+":regeneration"`},
		{"statusList", `return s.statusListWithStatus(c, query, http.StatusOK)`},
		{"statusListWithStatus", `return c.JSON(statusCode, serializeStatusesWithFilterContext`},
	} {
		if !functionBodyContains(t, src, check.fn, check.want) {
			t.Fatalf("server.go:%s missing Rails regeneration partial behavior %q", check.fn, check.want)
		}
	}
}

func TestAccountStatusReblogsMayOccurMatchesRailsFilterGuard(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest("GET", "/?exclude_reblogs=false", nil)
	c := echo.NewContext(req, httptest.NewRecorder(), e)
	if !accountStatusReblogsMayOccur(c) {
		t.Fatal("expected account status reblog filters to run when no narrowing params are present")
	}

	req = httptest.NewRequest("GET", "/?only_media=true", nil)
	c = echo.NewContext(req, httptest.NewRecorder(), e)
	if accountStatusReblogsMayOccur(c) {
		t.Fatal("expected only_media to skip account status reblog filters")
	}

	req = httptest.NewRequest("GET", "/?exclude_reblogs=1", nil)
	c = echo.NewContext(req, httptest.NewRecorder(), e)
	if accountStatusReblogsMayOccur(c) {
		t.Fatal("expected truthy exclude_reblogs to skip account status reblog filters")
	}
}

func TestStatusListIncludesPaginationMatchesRailsPinnedAccountStatuses(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest("GET", "/api/v1/accounts/1/statuses?pinned=1", nil)
	c := echo.NewContext(req, httptest.NewRecorder(), e)
	if !statusListIncludesPagination(c) {
		t.Fatal("expected Mastodon 4.3 pinned account statuses to include pagination headers")
	}

	req = httptest.NewRequest("GET", "/api/v1/timelines/public?pinned=1", nil)
	c = echo.NewContext(req, httptest.NewRecorder(), e)
	if !statusListIncludesPagination(c) {
		t.Fatal("expected non-account status lists to keep pagination headers")
	}
}

func TestAccountStatusesUsesVisibilityGuard(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	for fn, want := range map[string]string{
		"accountStatuses":          `query := s.visibleStatusQuery(current).Where("statuses.account_id = ?", target.ID)`,
		"visibleSearchStatusQuery": `return s.visibleStatusQuery(account)`,
		"visibleStatusQuery":       `statuses.visibility IN ?`,
	} {
		if !functionBodyContains(t, src, fn, want) {
			t.Fatalf("server.go:%s does not contain %q", fn, want)
		}
	}
	for _, check := range []struct {
		fn   string
		want string
	}{
		{"accountStatuses", `blocked, err := s.accountBlocksAccountOrDomain(target.ID, current)`},
		{"accountStatuses", `JOIN status_pins ON status_pins.status_id = statuses.id AND status_pins.account_id = ?`},
		{"accountStatuses", `Order("status_pins.created_at DESC")`},
		{"accountStatuses", `accountStatusTagQueryValue(c.QueryParam("tagged"))`},
		{"accountStatuses", `query = applyOnlyMediaFilterForAccount(c, query, target.ID)`},
		{"applyAccountStatusReblogFilters", `account_status_reblog_blocked_by.target_account_id = ?`},
		{"accountStatusReblogsMayOccur", `strings.TrimSpace(c.QueryParam("tagged")) == ""`},
		{"accountStatusTagQueryValue", `normalizedSearchTagName(raw)`},
		{"accountBlocksAccountOrDomain", `models.AccountDomainBlock{}`},
		{"applyOnlyMediaFilterForAccount", `timeline_media.account_id = ?`},
	} {
		if !functionBodyContains(t, src, check.fn, check.want) {
			t.Fatalf("server.go:%s does not contain %q", check.fn, check.want)
		}
	}
}
