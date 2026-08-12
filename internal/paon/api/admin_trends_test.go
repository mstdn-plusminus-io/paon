package api

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestAdminTrendTagsRequireAdminToken(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/v1/admin/trends/tags", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())
	s := &Server{}

	if err := s.adminTrendTags(c); err == nil {
		t.Fatal("expected admin trend tags to require authentication")
	} else if apiErr, ok := err.(apiHTTPError); !ok || apiErr.status != http.StatusUnauthorized {
		t.Fatalf("error = %#v", err)
	}
}

func TestAdminTrendListsUseRailsPagination(t *testing.T) {
	src, err := os.ReadFile("admin_trends.go")
	if err != nil {
		t.Fatal(err)
	}
	for fn, checks := range map[string][]string{
		"adminTrendTags": {
			`limitValue := limit(c, 100, 200)`,
			`offsetValue := offset(c)`,
			`offsetPaginationLinkWithPathAndAllowedParams(c, "/api/v1/trends/tags", offsetValue, limitValue, len(tags), adminLimitPaginationParams)`,
			`out = append(out, s.adminTagFromModel(c, tag))`,
		},
		"adminTrendLinks": {
			`limitValue := limit(c, 100, 200)`,
			`offsetValue := offset(c)`,
			`offsetPaginationLinkWithPathAndAllowedParams(c, "/api/v1/trends/links", offsetValue, limitValue, len(cards), adminLimitPaginationParams)`,
		},
		"adminTrendStatuses": {
			`limitValue := limit(c, 100, 200)`,
			`offsetValue := offset(c)`,
			`offsetPaginationLinkWithPathAndAllowedParams(c, "/api/v1/trends/statuses", offsetValue, limitValue, len(statuses), adminLimitPaginationParams)`,
		},
		"adminPreviewCardProviders": {
			`limitValue := limit(c, 100, 200)`,
			`applyIDPagination(c, s.db.Model(&models.PreviewCardProvider{}), "preview_card_providers.id")`,
			`Order("preview_card_providers.id DESC")`,
			`reverseRows(providers)`,
			`paginationLinkWithAllowedParams(c, providers[0].ID, providers[len(providers)-1].ID, "min_id", len(providers) == limitValue, true, adminLimitPaginationParams)`,
		},
	} {
		for _, want := range checks {
			if !functionBodyContains(t, src, fn, want) {
				t.Fatalf("admin_trends.go:%s does not contain %q", fn, want)
			}
		}
	}
	if functionBodyContains(t, src, "adminPreviewCardProviders", `reviewed_at`) {
		t.Fatal("adminPreviewCardProviders must use Rails Paginable id ordering, not web review-status ordering")
	}
	if !functionBodyContains(t, src, "reviewAdminTrendTag", `return c.JSON(http.StatusOK, s.adminTagFromModel(c, tag))`) {
		t.Fatal("reviewAdminTrendTag must return AdminTag with Rails tag history")
	}
}

func TestAdminTrendOffsetPaginationLinksUseInheritedRailsPublicPaths(t *testing.T) {
	for _, tt := range []struct {
		name   string
		target string
		path   string
		want   []string
		deny   []string
	}{
		{
			name:   "tags",
			target: "/api/v1/admin/trends/tags?limit=10&offset=20&junk=1",
			path:   "/api/v1/trends/tags",
			want:   []string{`<http://example.com/api/v1/trends/tags?limit=10&offset=30>; rel="next"`, `<http://example.com/api/v1/trends/tags?limit=10&offset=10>; rel="prev"`},
			deny:   []string{"/api/v1/admin/trends/tags", "junk="},
		},
		{
			name:   "links",
			target: "/api/v1/admin/trends/links?limit=10&offset=20&language=ja",
			path:   "/api/v1/trends/links",
			want:   []string{`<http://example.com/api/v1/trends/links?limit=10&offset=30>; rel="next"`, `<http://example.com/api/v1/trends/links?limit=10&offset=10>; rel="prev"`},
			deny:   []string{"/api/v1/admin/trends/links", "language="},
		},
		{
			name:   "statuses",
			target: "/api/v1/admin/trends/statuses?limit=10&offset=20&local=true",
			path:   "/api/v1/trends/statuses",
			want:   []string{`<http://example.com/api/v1/trends/statuses?limit=10&offset=30>; rel="next"`, `<http://example.com/api/v1/trends/statuses?limit=10&offset=10>; rel="prev"`},
			deny:   []string{"/api/v1/admin/trends/statuses", "local="},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.target, nil)
			req.Host = "example.com"
			c := echo.NewContext(req, httptest.NewRecorder(), echo.New())
			link := offsetPaginationLinkWithPathAndAllowedParams(c, tt.path, 20, 10, 10, adminLimitPaginationParams)
			for _, want := range tt.want {
				if !strings.Contains(link, want) {
					t.Fatalf("Link = %q, want %q", link, want)
				}
			}
			for _, deny := range tt.deny {
				if strings.Contains(link, deny) {
					t.Fatalf("Link = %q, must not contain %q", link, deny)
				}
			}
		})
	}
}

func TestAdminTrendIndexesUseRailsTrendQueries(t *testing.T) {
	src, err := os.ReadFile("admin_trends.go")
	if err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		src  []byte
		fn   string
		want string
	}{
		{src, "adminTrendsTagWebRecords", `Joins("JOIN tag_trends ON tag_trends.tag_id = tags.id")`},
		{src, "adminTrendTags", `Joins("JOIN tag_trends ON tag_trends.tag_id = tags.id")`},
		{src, "adminTrendTags", `Order("tag_trends.score DESC")`},
		{src, "adminTrendingPreviewCardRefs", `Table("preview_card_trends")`},
		{src, "adminTrendingPreviewCardRefs", `Order("preview_card_trends.score DESC")`},
		{src, "adminTrendStatuses", `Joins("JOIN status_trends ON status_trends.status_id = statuses.id")`},
		{src, "adminTrendStatuses", `Order("status_trends.score DESC")`},
		{src, "adminTrendStatuses", `filters := s.accountFilters(account)`},
		{src, "adminTrendStatuses", `item.Status = statusWithAllFilterContexts(s.cfg, status, account, filters)`},
		{src, "reviewAdminTrendStatus", `item.Status = statusWithAllFilterContexts(s.cfg, statuses[0], account, s.accountFilters(account))`},
	}
	for _, check := range checks {
		if !functionBodyContains(t, check.src, check.fn, check.want) {
			t.Fatalf("%s missing %q", check.fn, check.want)
		}
	}
}

func TestAdminTrendTagsWebRequireSession(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/trends/tags?status=pending_review", nil)
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	want := "/auth/sign_in?redirect_to=" + url.QueryEscape("/admin/trends/tags?status=pending_review")
	if got := rec.Header().Get("Location"); got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}
}

func TestAdminTrendLinksWebRequireSession(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/trends/links?trending=allowed", nil)
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	want := "/auth/sign_in?redirect_to=" + url.QueryEscape("/admin/trends/links?trending=allowed")
	if got := rec.Header().Get("Location"); got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}
}

func TestAdminTrendLinkPublishersWebRequireSession(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/trends/links/publishers?status=pending_review", nil)
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	want := "/auth/sign_in?redirect_to=" + url.QueryEscape("/admin/trends/links/publishers?status=pending_review")
	if got := rec.Header().Get("Location"); got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}
}

func TestAdminTrendStatusesWebRequireSession(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/trends/statuses?trending=allowed&locale=ja", nil)
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	want := "/auth/sign_in?redirect_to=" + url.QueryEscape("/admin/trends/statuses?trending=allowed&locale=ja")
	if got := rec.Header().Get("Location"); got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}
}

func TestParseAdminTrendsTagIDs(t *testing.T) {
	form := url.Values{}
	form.Add("trends_tag_batch[tag_ids][]", "4")
	form.Add("trends_tag_batch[tag_ids][]", "bad")
	form.Add("trends_tag_batch[tag_ids]", "5")

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/admin/trends/tags/batch", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	got := parseAdminTrendsTagIDs(c)
	if len(got) != 1 || got[0] != 4 {
		t.Fatalf("ids = %#v", got)
	}
}

func TestAdminTrendsTagBatchAction(t *testing.T) {
	e := echo.New()
	for body, want := range map[string]string{
		"approve=1":                          "approve",
		"approve=":                           "approve",
		"reject=1":                           "reject",
		"reject=":                            "reject",
		"trends_tag_batch[action]=something": "",
	} {
		req := httptest.NewRequest(http.MethodPost, "/admin/trends/tags/batch", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		c := echo.NewContext(req, rec, e)
		if got := adminTrendsTagBatchAction(c); got != want {
			t.Fatalf("action for %q = %q, want %q", body, got, want)
		}
	}
}

func TestAdminTrendTagReviewRefreshesSearchIndex(t *testing.T) {
	src, err := os.ReadFile("admin_trends.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, src, "applyAdminTrendsTagBatch", "s.meiliIndexTagsBestEffort(ctx, ids)") {
		t.Fatal("applyAdminTrendsTagBatch does not refresh Meilisearch tag documents")
	}
	if !functionBodyContains(t, src, "applyAdminTrendsTagBatch", "logAdminAction(tx, actorAccountID, action, tagAuditLogTarget(tag), now)") {
		t.Fatal("applyAdminTrendsTagBatch does not write audit logs")
	}
	if !functionBodyContains(t, src, "reviewAdminTrendTag", "s.meiliIndexTagsBestEffort(c.Request().Context(), []int64{id})") {
		t.Fatal("reviewAdminTrendTag does not refresh the Meilisearch tag document")
	}
	if !functionBodyContains(t, src, "reviewAdminTrendTag", "logAdminAction(tx, user.AccountID, action, tagAuditLogTarget(tag), now)") {
		t.Fatal("reviewAdminTrendTag does not write audit logs")
	}
	for fn, checks := range map[string][]string{
		"adminTrendLinks": {
			`return c.JSON(http.StatusOK, s.serializeAdminTrendLinks(c, cards, time.Now().UTC()))`,
		},
		"reviewAdminTrendLink": {
			`links := s.serializeAdminTrendLinks(c, []models.PreviewCard{card}, time.Now().UTC())`,
		},
		"serializeAdminTrendLinks": {
			`serializer.AdminTrendLinkFromModelWithHistory(s.cfg, card, s.linkHistory((*c).Request().Context(), card.ID, now), s.previewCardRequiresReview(card))`,
		},
		"linkHistory": {
			`out := make([]any, 0, 7)`,
			`uses, accounts := s.linkHistoryDay(ctx, previewCardID, day)`,
			`"day":      strconv.FormatInt(day.Unix(), 10)`,
		},
		"linkHistoryDay": {
			`s.redisCommand(usesCtx, "GET", linkHistoryRedisKey(s.cfg.RedisNamespace, previewCardID, day, false))`,
			`s.redisCommand(accountsCtx, "PFCOUNT", linkHistoryRedisKey(s.cfg.RedisNamespace, previewCardID, day, true))`,
		},
	} {
		for _, want := range checks {
			if !functionBodyContains(t, src, fn, want) {
				t.Fatalf("admin_trends.go:%s missing %q", fn, want)
			}
		}
	}
	for _, check := range []struct {
		fn   string
		want string
	}{
		{"updateTrendStatusesWithAudit", "logAdminAction(tx, actorAccountID, action, statusAuditLogTarget(status), now)"},
		{"applyAdminTrendsStatusBatch", "logAdminAction(tx, actorAccountID, auditAction, accountAuditLogTarget(account), now)"},
		{"updateTrendPreviewCardsWithAudit", "logAdminAction(tx, actorAccountID, action, previewCardAuditLogTarget(card), now)"},
		{"applyAdminTrendsLinkBatch", "logAdminAction(tx, actorAccountID, auditAction, previewCardProviderAuditLogTarget(provider), now)"},
		{"applyAdminTrendsLinkPublisherBatch", "logAdminAction(tx, actorAccountID, action, previewCardProviderAuditLogTarget(provider), now)"},
		{"reviewAdminTrendLink", "logAdminAction(tx, user.AccountID, action, previewCardAuditLogTarget(card), now)"},
		{"reviewAdminTrendStatus", "logAdminAction(tx, user.AccountID, action, statusAuditLogTarget(status), now)"},
		{"reviewAdminPreviewCardProvider", "logAdminAction(tx, user.AccountID, action, previewCardProviderAuditLogTarget(provider), now)"},
	} {
		if !functionBodyContains(t, src, check.fn, check.want) {
			t.Fatalf("%s does not write audit logs", check.fn)
		}
	}
}

func TestAdminTrendsTagsHTMLIncludesRailsFields(t *testing.T) {
	html := adminTrendsTagsHTML([]models.Tag{{
		ID:                7,
		Name:              "golang",
		DisplayName:       sql.NullString{String: "GoLang", Valid: true},
		RequestedReviewAt: sql.NullTime{Time: time.Date(2026, 6, 19, 1, 2, 3, 0, time.UTC), Valid: true},
		LastStatusAt:      sql.NullTime{Time: time.Date(2026, 6, 19, 2, 3, 4, 0, time.UTC), Valid: true},
	}}, "saved", "", "pending_review", "3")
	for _, want := range []string{
		"Trending hashtags",
		`action="/admin/trends/tags/batch"`,
		`name="page" value="3"`,
		`name="status" value="pending_review"`,
		`name="approve" value="1"`,
		`name="reject" value="1"`,
		`<div class="batch-table">`,
		`class="batch-table__row batch-table__row--attention"`,
		`class="batch-table__row__select batch-table__row__select--aligned batch-checkbox"`,
		`class="batch-table__row__content pending-account"`,
		`name="trends_tag_batch[tag_ids][]" value="7"`,
		`href="/admin/tags/7"`,
		`target="_blank" href="/tags/golang"`,
		"#GoLang",
		"Pending review",
		"2026-06-19T01:02:03Z",
		"2026-06-19T02:03:04Z",
		"saved",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("trend tags html missing %q: %s", want, html)
		}
	}
}

func assertAdminNothingHereEmptyState(t *testing.T, html string, forbidden ...string) {
	t.Helper()
	if !strings.Contains(html, `class="nothing-here nothing-here--under-tabs"`) {
		t.Fatalf("empty html missing Rails nothing_here class: %s", html)
	}
	forbidden = append(forbidden, `class="muted-hint center-text"`)
	for _, value := range forbidden {
		if strings.Contains(html, value) {
			t.Fatalf("empty html should not contain Rails-incompatible fragment %q: %s", value, html)
		}
	}
}

func TestAdminTrendsPageValueDefaultsAndPreservesQuery(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/admin/trends/tags?page=4", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)
	if got := adminTrendsPageValue(c); got != "4" {
		t.Fatalf("page = %q", got)
	}
	if got := adminRailsPageOffset(c); got != 120 {
		t.Fatalf("offset = %d", got)
	}

	req = httptest.NewRequest(http.MethodGet, "/admin/trends/tags", nil)
	rec = httptest.NewRecorder()
	c = echo.NewContext(req, rec, e)
	if got := adminTrendsPageValue(c); got != "1" {
		t.Fatalf("default page = %q", got)
	}
	if got := adminRailsPageOffset(c); got != 0 {
		t.Fatalf("default offset = %d", got)
	}
}

func TestAdminTrendsTagRedirectQuery(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/admin/trends/tags/batch", strings.NewReader("page=2&status=pending_review"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	got := adminTrendsTagRedirectQuery(c, "notice", "ok")
	values, err := url.ParseQuery(got)
	if err != nil {
		t.Fatal(err)
	}
	if values.Get("page") != "2" || values.Get("status") != "pending_review" || values.Get("notice") != "ok" {
		t.Fatalf("query = %q", got)
	}
	got = adminTrendsTagRedirectQuery(c, "", "")
	values, err = url.ParseQuery(got)
	if err != nil {
		t.Fatal(err)
	}
	if values.Get("page") != "2" || values.Get("status") != "pending_review" || values.Has("") || values.Has("notice") || values.Has("error") {
		t.Fatalf("filter-only query = %q", got)
	}
}

func TestAdminTrendsTagReviewStatus(t *testing.T) {
	now := time.Now()
	cases := map[string]models.Tag{
		"pending_review": {},
		"approved":       {ReviewedAt: sql.NullTime{Time: now, Valid: true}, Trendable: sql.NullBool{Bool: true, Valid: true}},
		"rejected":       {ReviewedAt: sql.NullTime{Time: now, Valid: true}, Trendable: sql.NullBool{Bool: false, Valid: true}},
		"reviewed":       {ReviewedAt: sql.NullTime{Time: now, Valid: true}},
	}
	for want, tag := range cases {
		if got := adminTrendsTagReviewStatus(tag); got != want {
			t.Fatalf("status = %q, want %q", got, want)
		}
	}
}

func TestParseAdminTrendsLinkIDs(t *testing.T) {
	form := url.Values{}
	form.Add("trends_preview_card_batch[preview_card_ids][]", "4")
	form.Add("trends_preview_card_batch[preview_card_ids][]", "bad")
	form.Add("trends_preview_card_batch[preview_card_ids]", "5")

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/admin/trends/links/batch", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	got := parseAdminTrendsLinkIDs(c)
	if len(got) != 1 || got[0] != 4 {
		t.Fatalf("ids = %#v", got)
	}
}

func TestAdminTrendsLinkBatchAction(t *testing.T) {
	e := echo.New()
	for body, want := range map[string]string{
		"approve=1":           "approve",
		"approve=":            "approve",
		"approve_providers=1": "approve_providers",
		"approve_providers=":  "approve_providers",
		"reject=1":            "reject",
		"reject=":             "reject",
		"reject_providers=1":  "reject_providers",
		"reject_providers=":   "reject_providers",
		"trends_preview_card_batch[action]=approve":        "",
		"trends_preview_card_batch[action]=reject_unknown": "",
	} {
		req := httptest.NewRequest(http.MethodPost, "/admin/trends/links/batch", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		c := echo.NewContext(req, rec, e)
		if got := adminTrendsLinkBatchAction(c); got != want {
			t.Fatalf("action for %q = %q, want %q", body, got, want)
		}
	}
}

func TestAdminTrendsLinksHTMLIncludesRailsFields(t *testing.T) {
	html := adminTrendsLinksHTML([]models.PreviewCard{{
		ID:           7,
		URL:          "https://example.com/news",
		Title:        "News",
		ProviderName: "Example",
		Language:     sql.NullString{String: "ja", Valid: true},
	}}, "saved", "", "allowed", "ja", "5")
	for _, want := range []string{
		"トレンドリンク",
		`action="/admin/trends/links/batch"`,
		`name="page" value="5"`,
		`name="trending" value="allowed"`,
		`name="locale" value="ja"`,
		`<select name="locale"><option value=""></option><option value="ja" selected>Japanese</option></select>`,
		`name="approve" value="1"`,
		`name="approve_providers" value="1"`,
		`name="reject" value="1"`,
		`name="reject_providers" value="1"`,
		`<div class="batch-table">`,
		`class="batch-table__row batch-table__row--attention"`,
		`class="batch-table__row__select batch-table__row__select--aligned batch-checkbox"`,
		`class="batch-table__row__content pending-account"`,
		`name="trends_preview_card_batch[preview_card_ids][]" value="7"`,
		`href="https://example.com/news"`,
		"Example",
		"saved",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("trend links html missing %q: %s", want, html)
		}
	}
}

func TestAdminTrendsLinksHTMLIncludesRailsPagination(t *testing.T) {
	cards := make([]models.PreviewCard, adminRailsDefaultPageSize)
	for i := range cards {
		cards[i] = models.PreviewCard{ID: int64(i + 1), URL: "https://example.com/" + strconv.Itoa(i+1), Title: "Link"}
	}
	html := adminTrendsLinksHTML(cards, "", "", "allowed", "ja", "2", "en")
	for _, want := range []string{
		`<nav class="pagination">`,
		`rel="prev" href="/admin/trends/links?locale=ja&amp;trending=allowed"`,
		`rel="next" href="/admin/trends/links?locale=ja&amp;page=3&amp;trending=allowed"`,
		`>Prev</a>`,
		`>Next</a>`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("trend links pagination missing %q: %s", want, html)
		}
	}
}

func TestAdminTrendsLinkRedirectQuery(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/admin/trends/links/batch", strings.NewReader("page=2&trending=allowed&locale=ja"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	got := adminTrendsLinkRedirectQuery(c, "notice", "ok")
	values, err := url.ParseQuery(got)
	if err != nil {
		t.Fatal(err)
	}
	if values.Get("page") != "2" || values.Get("trending") != "allowed" || values.Get("locale") != "ja" || values.Get("notice") != "ok" {
		t.Fatalf("query = %q", got)
	}
	got = adminTrendsLinkRedirectQuery(c, "", "")
	values, err = url.ParseQuery(got)
	if err != nil {
		t.Fatal(err)
	}
	if values.Get("page") != "2" || values.Get("trending") != "allowed" || values.Get("locale") != "ja" || values.Has("") || values.Has("notice") || values.Has("error") {
		t.Fatalf("filter-only query = %q", got)
	}
}

func TestAdminTrendsLinkReviewStatus(t *testing.T) {
	cases := map[string]models.PreviewCard{
		"pending_review": {},
		"approved":       {Trendable: sql.NullBool{Bool: true, Valid: true}},
		"rejected":       {Trendable: sql.NullBool{Bool: false, Valid: true}},
	}
	for want, card := range cases {
		if got := adminTrendsLinkReviewStatus(card); got != want {
			t.Fatalf("status = %q, want %q", got, want)
		}
	}
}

func TestParseAdminTrendsLinkPublisherIDs(t *testing.T) {
	form := url.Values{}
	form.Add("trends_preview_card_provider_batch[preview_card_provider_ids][]", "4")
	form.Add("trends_preview_card_provider_batch[preview_card_provider_ids][]", "bad")
	form.Add("trends_preview_card_provider_batch[preview_card_provider_ids]", "5")

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/admin/trends/links/publishers/batch", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	got := parseAdminTrendsLinkPublisherIDs(c)
	if len(got) != 1 || got[0] != 4 {
		t.Fatalf("ids = %#v", got)
	}
}

func TestAdminTrendsLinkPublisherBatchAction(t *testing.T) {
	e := echo.New()
	for body, want := range map[string]string{
		"approve=1": "approve",
		"approve=":  "approve",
		"reject=1":  "reject",
		"reject=":   "reject",
		"trends_preview_card_provider_batch[action]=approve":     "",
		"trends_preview_card_provider_batch[action]=unknown_one": "",
	} {
		req := httptest.NewRequest(http.MethodPost, "/admin/trends/links/publishers/batch", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		c := echo.NewContext(req, rec, e)
		if got := adminTrendsLinkPublisherBatchAction(c); got != want {
			t.Fatalf("action for %q = %q, want %q", body, got, want)
		}
	}
}

func TestAdminTrendsLinkPublishersHTMLIncludesRailsFields(t *testing.T) {
	html := adminTrendsLinkPublishersHTML([]models.PreviewCardProvider{{
		ID:                7,
		Domain:            "example.com",
		RequestedReviewAt: sql.NullTime{Time: time.Date(2026, 6, 19, 1, 2, 3, 0, time.UTC), Valid: true},
	}}, "saved", "", "pending_review", "6")
	for _, want := range []string{
		"Trend publishers",
		`action="/admin/trends/links/publishers/batch"`,
		`class="filters"`,
		`class="filter-subset"`,
		`class="back-link"`,
		`class="batch-table"`,
		`class="batch-table__toolbar"`,
		`class="table-action-link" name="approve" value="1"`,
		`class="table-action-link" name="reject" value="1"`,
		`data-confirm="Are you sure?"`,
		`class="batch-table__row batch-table__row--attention"`,
		`class="batch-table__row__content pending-account"`,
		`name="page" value="6"`,
		`name="status" value="pending_review"`,
		`name="approve" value="1"`,
		`name="reject" value="1"`,
		`name="trends_preview_card_provider_batch[preview_card_provider_ids][]" value="7"`,
		"example.com",
		"Pending review",
		"2026-06-19T01:02:03Z",
		"saved",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("trend publishers html missing %q: %s", want, html)
		}
	}
}

func TestAdminTrendsLinkPublishersHTMLIncludesRailsPagination(t *testing.T) {
	providers := make([]models.PreviewCardProvider, adminRailsDefaultPageSize)
	for i := range providers {
		providers[i] = models.PreviewCardProvider{ID: int64(i + 1), Domain: "example" + strconv.Itoa(i+1) + ".com"}
	}
	html := adminTrendsLinkPublishersHTML(providers, "", "", "pending_review", "3", "en")
	for _, want := range []string{
		`<nav class="pagination">`,
		`rel="prev" href="/admin/trends/links/publishers?page=2&amp;status=pending_review"`,
		`rel="next" href="/admin/trends/links/publishers?page=4&amp;status=pending_review"`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("trend publishers pagination missing %q: %s", want, html)
		}
	}
}

func TestAdminTrendsLinkPublisherRedirectQuery(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/admin/trends/links/publishers/batch", strings.NewReader("page=2&status=pending_review"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	got := adminTrendsLinkPublisherRedirectQuery(c, "notice", "ok")
	values, err := url.ParseQuery(got)
	if err != nil {
		t.Fatal(err)
	}
	if values.Get("page") != "2" || values.Get("status") != "pending_review" || values.Get("notice") != "ok" {
		t.Fatalf("query = %q", got)
	}
	got = adminTrendsLinkPublisherRedirectQuery(c, "", "")
	values, err = url.ParseQuery(got)
	if err != nil {
		t.Fatal(err)
	}
	if values.Get("page") != "2" || values.Get("status") != "pending_review" || values.Has("") || values.Has("notice") || values.Has("error") {
		t.Fatalf("filter-only query = %q", got)
	}
}

func TestAdminTrendsLinkPublisherReviewStatus(t *testing.T) {
	now := time.Now()
	cases := map[string]models.PreviewCardProvider{
		"pending_review": {},
		"approved":       {ReviewedAt: sql.NullTime{Time: now, Valid: true}, Trendable: sql.NullBool{Bool: true, Valid: true}},
		"rejected":       {ReviewedAt: sql.NullTime{Time: now, Valid: true}, Trendable: sql.NullBool{Bool: false, Valid: true}},
		"reviewed":       {ReviewedAt: sql.NullTime{Time: now, Valid: true}},
	}
	for want, provider := range cases {
		if got := adminTrendsLinkPublisherReviewStatus(provider); got != want {
			t.Fatalf("status = %q, want %q", got, want)
		}
	}
}

func TestAdminRailsPaginationHTMLMatchesRailsPaginateShape(t *testing.T) {
	if got := adminRailsPaginationHTML("en", "/admin/trends/tags", "1", adminTrendsFilterValues("status", "approved"), 1); got != "" {
		t.Fatalf("short first page pagination = %q", got)
	}
	got := adminRailsPaginationHTML("en", "/admin/trends/tags", "2", adminTrendsFilterValues("status", "approved"), adminRailsDefaultPageSize)
	for _, want := range []string{
		`rel="prev" href="/admin/trends/tags?status=approved"`,
		`rel="next" href="/admin/trends/tags?page=3&amp;status=approved"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("pagination missing %q: %s", want, got)
		}
	}
}

func TestParseAdminTrendsStatusIDs(t *testing.T) {
	form := url.Values{}
	form.Add("trends_status_batch[status_ids][]", "4")
	form.Add("trends_status_batch[status_ids][]", "bad")
	form.Add("trends_status_batch[status_ids]", "5")

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/admin/trends/statuses/batch", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	got := parseAdminTrendsStatusIDs(c)
	if len(got) != 1 || got[0] != 4 {
		t.Fatalf("ids = %#v", got)
	}
}

func TestAdminTrendsStatusBatchAction(t *testing.T) {
	e := echo.New()
	for body, want := range map[string]string{
		"approve=1":                              "approve",
		"approve=":                               "approve",
		"approve_accounts=1":                     "approve_accounts",
		"approve_accounts=":                      "approve_accounts",
		"reject=1":                               "reject",
		"reject=":                                "reject",
		"reject_accounts=1":                      "reject_accounts",
		"reject_accounts=":                       "reject_accounts",
		"trends_status_batch[action]=approve":    "",
		"trends_status_batch[action]=unexpected": "",
	} {
		req := httptest.NewRequest(http.MethodPost, "/admin/trends/statuses/batch", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		c := echo.NewContext(req, rec, e)
		if got := adminTrendsStatusBatchAction(c); got != want {
			t.Fatalf("action for %q = %q, want %q", body, got, want)
		}
	}
}

func TestAdminTrendsStatusesHTMLIncludesRailsFields(t *testing.T) {
	html := adminTrendsStatusesHTML("https://example.com", []models.Status{{
		ID:        7,
		AccountID: 3,
		Text:      "Hello trend",
		Language:  sql.NullString{String: "ja", Valid: true},
		Account:   models.Account{ID: 3, Username: "alice"},
		MediaAttachments: []models.MediaAttachment{{
			FileFileName: sql.NullString{String: "clip.mp4", Valid: true},
			Description:  sql.NullString{String: "demo clip", Valid: true},
		}},
		StatusStat: models.StatusStat{
			ReblogsCount:    2,
			FavouritesCount: 5,
		},
	}}, "saved", "", "allowed", "ja", "7")
	for _, want := range []string{
		"トレンド投稿",
		`action="/admin/trends/statuses/batch"`,
		`name="page" value="7"`,
		`name="trending" value="allowed"`,
		`name="locale" value="ja"`,
		`<select name="locale"><option value=""></option><option value="ja" selected>Japanese</option></select>`,
		`name="approve" value="1"`,
		`name="approve_accounts" value="1"`,
		`name="reject" value="1"`,
		`name="reject_accounts" value="1"`,
		`<div class="batch-table">`,
		`class="batch-table__row batch-table__row--attention"`,
		`class="batch-table__row__content pending-account__header"`,
		`target="_blank" class="emojify" rel="noopener noreferrer"`,
		`name="trends_status_batch[status_ids][]" value="7"`,
		`href="https://example.com/@alice/7"`,
		`href="/admin/accounts/3"`,
		"Hello trend",
		`title="demo clip"`,
		"clip.mp4",
		"alice",
		"Japanese",
		"7",
		"saved",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("trend statuses html missing %q: %s", want, html)
		}
	}
}

func TestAdminTrendsStatusRedirectQuery(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/admin/trends/statuses/batch", strings.NewReader("page=2&trending=allowed&locale=ja"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	got := adminTrendsStatusRedirectQuery(c, "notice", "ok")
	values, err := url.ParseQuery(got)
	if err != nil {
		t.Fatal(err)
	}
	if values.Get("page") != "2" || values.Get("trending") != "allowed" || values.Get("locale") != "ja" || values.Get("notice") != "ok" {
		t.Fatalf("query = %q", got)
	}
	got = adminTrendsStatusRedirectQuery(c, "", "")
	values, err = url.ParseQuery(got)
	if err != nil {
		t.Fatal(err)
	}
	if values.Get("page") != "2" || values.Get("trending") != "allowed" || values.Get("locale") != "ja" || values.Has("") || values.Has("notice") || values.Has("error") {
		t.Fatalf("filter-only query = %q", got)
	}
}

func TestAdminTrendsStatusReviewStatus(t *testing.T) {
	cases := map[string]models.Status{
		"pending_review": {},
		"approved":       {Trendable: sql.NullBool{Bool: true, Valid: true}},
		"rejected":       {Trendable: sql.NullBool{Bool: false, Valid: true}},
	}
	for want, status := range cases {
		if got := adminTrendsStatusReviewStatus(status); got != want {
			t.Fatalf("status = %q, want %q", got, want)
		}
	}
}

func TestAdminTrendsStatusURLFallsBackToAdminStatus(t *testing.T) {
	got := adminTrendsStatusURL("https://example.com", models.Status{ID: 7, AccountID: 3})
	if got != "/admin/accounts/3/statuses/7" {
		t.Fatalf("status url = %q", got)
	}
}

func TestApproveAdminTrendLinkRequiresAdminToken(t *testing.T) {
	req := httptest.NewRequest("POST", "/api/v1/admin/trends/links/123/approve", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())
	s := &Server{}

	if err := s.approveAdminTrendLink(c); err == nil {
		t.Fatal("expected admin trend link approval to require authentication")
	} else if apiErr, ok := err.(apiHTTPError); !ok || apiErr.status != http.StatusUnauthorized {
		t.Fatalf("error = %#v", err)
	}
}

func TestDomainSuffixesLongestFirst(t *testing.T) {
	got := domainSuffixes("news.example.com")
	want := []string{"news.example.com", "example.com", "com"}
	if len(got) != len(want) {
		t.Fatalf("domainSuffixes length = %d; want %d (%#v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("domainSuffixes[%d] = %q; want %q", i, got[i], want[i])
		}
	}
}

func TestPreviewCardHostNormalizesWWW(t *testing.T) {
	if got := previewCardHost("https://www.Example.COM/path"); got != "example.com" {
		t.Fatalf("previewCardHost = %q", got)
	}
}
