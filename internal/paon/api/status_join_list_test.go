package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestFavouritesRequireAuth(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/v1/favourites", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())
	s := &Server{}

	if err := s.favourites(c); err == nil {
		t.Fatal("expected favourites to require authentication")
	} else if apiErr, ok := err.(apiHTTPError); !ok || apiErr.status != http.StatusUnauthorized {
		t.Fatalf("error = %#v", err)
	}
}

func TestBookmarkFeedCacheIsWiredToBookmarkToggles(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`var bookmark *models.Bookmark`,
		`bookmark = &row`,
		`s.addBookmarkToFeedCache(c.Request().Context(), *bookmark)`,
		`s.removeBookmarkFromFeedCache(c.Request().Context(), account.ID, status.ID)`,
	} {
		if !functionBodyContains(t, src, "toggleStatusJoin", want) {
			t.Fatalf("bookmark feed cache wiring missing %q", want)
		}
	}
	feedSrc, err := os.ReadFile("bookmark_feed_cache.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"ZADD", key, strconv.FormatInt(bookmark.ID, 10), strconv.FormatInt(bookmark.StatusID, 10)`,
		`"ZCARD", key`,
		`"EXPIRE", key, strconv.FormatInt(int64(bookmarkFeedTTL(count)/time.Second), 10)`,
		`"ZREM", bookmarkFeedRedisKey(s.cfg.RedisNamespace, accountID), strconv.FormatInt(statusID, 10)`,
	} {
		if !strings.Contains(string(feedSrc), want) {
			t.Fatalf("bookmark feed cache helper missing %q", want)
		}
	}
}

func TestStatusJoinRemovalUsesExistingJoinBeforeVisibilityLikeRails(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	for fn, wants := range map[string][]string{
		"toggleStatusJoin": {
			`if create {`,
			`status, err = s.findVisibleStatusForAccount(account, c.Param("id"))`,
			`status, err = s.findStatusForJoinRemoval(account, c.Param("id"), table)`,
			`refreshed := status`,
			`if create {`,
			`} else if changed {`,
			`refreshed, err = s.findStatus(strconv.FormatInt(status.ID, 10))`,
		},
		"findStatusForJoinRemoval": {
			`var row models.Favourite`,
			`var row models.Bookmark`,
			`return s.findStatus(strconv.FormatInt(row.StatusID, 10))`,
			`return s.findVisibleStatusForAccount(account, id)`,
		},
	} {
		for _, want := range wants {
			if !functionBodyContains(t, src, fn, want) {
				t.Fatalf("server.go:%s does not contain %q", fn, want)
			}
		}
	}
}

func TestOrderStatusesByJoinRowsKeepsJoinOrder(t *testing.T) {
	statuses := []models.Status{
		{ID: 10, Text: "older"},
		{ID: 30, Text: "newer"},
		{ID: 20, Text: "middle"},
	}
	rows := []statusJoinRow{
		{ID: 103, StatusID: 20},
		{ID: 102, StatusID: 30},
		{ID: 101, StatusID: 10},
	}

	ordered := orderStatusesByJoinRows(rows, statuses)
	if len(ordered) != 3 {
		t.Fatalf("len = %d", len(ordered))
	}
	if ordered[0].ID != 20 || ordered[1].ID != 30 || ordered[2].ID != 10 {
		t.Fatalf("ordered = %#v", ordered)
	}
}

func TestMinIDPaginationReversesImmediateRowsToNewestFirst(t *testing.T) {
	statuses := []models.Status{{ID: 101}, {ID: 102}, {ID: 103}}
	reverseStatuses(statuses)
	if statuses[0].ID != 103 || statuses[1].ID != 102 || statuses[2].ID != 101 {
		t.Fatalf("statuses = %#v", statuses)
	}

	rows := []statusJoinRow{{ID: 101, StatusID: 1}, {ID: 102, StatusID: 2}, {ID: 103, StatusID: 3}}
	reverseStatusJoinRows(rows)
	if rows[0].ID != 103 || rows[1].ID != 102 || rows[2].ID != 101 {
		t.Fatalf("rows = %#v", rows)
	}
}

func TestStatusListPaginationLinkUsesRailsAllowedQueryParams(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest("GET", "/api/v1/timelines/tag/golang?limit=5&local=true&remote=true&only_media=true&any[]=rust&extra=1&min_id=90", nil)
	req.Host = "social.example"
	c := echo.NewContext(req, httptest.NewRecorder(), e)

	got := statusListPaginationLink(c, 110, 100, true)
	for _, want := range []string{"limit=5", "local=true", "only_media=true", "max_id=100", "min_id=110"} {
		if !strings.Contains(got, want) {
			t.Fatalf("Link missing %q: %q", want, got)
		}
	}
	for _, unwanted := range []string{"remote=", "any%5B%5D=", "extra=", "since_id="} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("Link should omit Rails-filtered param %q: %q", unwanted, got)
		}
	}

	req = httptest.NewRequest("GET", "/api/v1/accounts/1/statuses?limit=5&pinned=1&tagged=go&exclude_replies=true&extra=1", nil)
	req.Host = "social.example"
	c = echo.NewContext(req, httptest.NewRecorder(), e)
	got = statusListPaginationLink(c, 110, 100, true)
	for _, want := range []string{"limit=5", "pinned=1", "tagged=go", "exclude_replies=true"} {
		if !strings.Contains(got, want) {
			t.Fatalf("account status Link missing %q: %q", want, got)
		}
	}
	if strings.Contains(got, "extra=") {
		t.Fatalf("account status Link should omit extra params: %q", got)
	}
}

func TestStatusJoinPaginationLinkUsesRailsLimitOnly(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/v1/favourites?limit=5&local=true&extra=1", nil)
	req.Host = "social.example"
	c := echo.NewContext(req, httptest.NewRecorder(), echo.New())

	got := statusJoinPaginationLink(c, 110, 100, true)
	if !strings.Contains(got, "limit=5") || !strings.Contains(got, "max_id=100") || !strings.Contains(got, "min_id=110") {
		t.Fatalf("status join Link missing Rails params: %q", got)
	}
	if strings.Contains(got, "local=") || strings.Contains(got, "extra=") {
		t.Fatalf("status join Link should only preserve limit: %q", got)
	}
}

func TestStatusListIncludesNextMatchesRailsTimelineAndCollectionRules(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest("GET", "/api/v1/timelines/public", nil)
	c := echo.NewContext(req, httptest.NewRecorder(), e)
	if !statusListIncludesNext(c, 3, 20) {
		t.Fatal("timeline status lists should include Rails next links for non-empty pages")
	}

	req = httptest.NewRequest("GET", "/api/v1/accounts/1/statuses", nil)
	c = echo.NewContext(req, httptest.NewRecorder(), e)
	if statusListIncludesNext(c, 3, 20) {
		t.Fatal("account status lists should keep Rails records_continue next-link behavior")
	}
	if !statusListIncludesNext(c, 20, 20) {
		t.Fatal("account status lists should include next links when the page is full")
	}
}

func TestPaginationLinkWithPrevParamDropsStaleCursorParams(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/v1/timelines/public?limit=5&local=true&min_id=90&max_id=120&since_id=80", nil)
	req.Host = "social.example"
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())

	got := paginationLinkWithPrevParam(c, 110, 100, "min_id")
	wantNext := `<http://social.example/api/v1/timelines/public?limit=5&local=true&max_id=100>; rel="next"`
	wantPrev := `<http://social.example/api/v1/timelines/public?limit=5&local=true&min_id=110>; rel="prev"`
	if got != wantNext+", "+wantPrev {
		t.Fatalf("Link = %q", got)
	}
}

func TestPaginationLinkMatrixKeepsRailsCursorAndParamBoundaries(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/v1/timelines/home?limit=20&local=true&only_media=false&max_id=9&min_id=8&since_id=7&offset=6", nil)
	req.Host = "social.example"
	req.Header.Set("X-Forwarded-Proto", "https")
	c := echo.NewContext(req, httptest.NewRecorder(), echo.New())

	full := paginationLinkWithAllowedParams(c, 110, 100, "since_id", true, true, []string{"limit", "local"})
	wantFullNext := `<https://social.example/api/v1/timelines/home?limit=20&local=true&max_id=100>; rel="next"`
	wantFullPrev := `<https://social.example/api/v1/timelines/home?limit=20&local=true&since_id=110>; rel="prev"`
	if full != wantFullNext+", "+wantFullPrev {
		t.Fatalf("full pagination Link = %q", full)
	}
	for _, unwanted := range []string{"only_media=", "min_id=", "offset=", "max_id=9", "since_id=7"} {
		if strings.Contains(full, unwanted) {
			t.Fatalf("full pagination Link leaked stale param %q: %q", unwanted, full)
		}
	}

	nextOnly := paginationLinkWithOptions(c, 110, 100, "min_id", true, false)
	if strings.Contains(nextOnly, `rel="prev"`) || !strings.Contains(nextOnly, `rel="next"`) || !strings.Contains(nextOnly, "max_id=100") {
		t.Fatalf("next-only Link = %q", nextOnly)
	}

	prevOnly := paginationLinkWithOptions(c, 110, 100, "min_id", false, true)
	if strings.Contains(prevOnly, `rel="next"`) || !strings.Contains(prevOnly, `rel="prev"`) || !strings.Contains(prevOnly, "min_id=110") {
		t.Fatalf("prev-only Link = %q", prevOnly)
	}
}
