package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestTrendingStatusesFilterForCurrentAccountLikeRails(t *testing.T) {
	src, err := os.ReadFile("trends.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`trend_status_blocks.account_id = ?`,
		`trend_status_blocks.target_account_id = statuses.account_id`,
		`trend_status_blocked_by.target_account_id = ?`,
		`trend_status_blocked_by.account_id = statuses.account_id`,
		`trend_status_mutes.account_id = ?`,
		`trend_status_mutes.target_account_id = statuses.account_id`,
		`account_domain_blocks trend_status_domain_blocks`,
		`lower(trend_status_domain_blocks.domain) = lower(trend_status_accounts.domain)`,
	} {
		if !functionBodyContains(t, src, "applyTrendStatusAccountFilters", want) {
			t.Fatalf("trends.go:applyTrendStatusAccountFilters missing Rails filtered_for fragment %q", want)
		}
	}
}

func TestTrendingLinksUseRailsPreviewCardTrendTable(t *testing.T) {
	src, err := os.ReadFile("trends.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`Table("preview_card_trends")`,
		`JOIN preview_cards ON preview_cards.id = preview_card_trends.preview_card_id`,
		`Where("preview_card_trends.allowed = ?", true)`,
		`Group("preview_cards.id, preview_card_trends.score")`,
		`applyTrendLanguageOrder(query, "preview_card_trends.language", preferredLanguages)`,
		`Order("preview_card_trends.score DESC")`,
	} {
		if !functionBodyContains(t, src, "trendingPreviewCardRefs", want) {
			t.Fatalf("trends.go:trendingPreviewCardRefs does not contain %q", want)
		}
	}
	for _, forbidden := range []string{
		`Order("preview_card_trends.rank ASC")`,
		`Order("preview_cards.id ASC")`,
		`Group("preview_cards.id, preview_card_trends.score, preview_card_trends.rank")`,
		`Where("preview_cards.title <> ''")`,
	} {
		if functionBodyContains(t, src, "trendingPreviewCardRefs", forbidden) {
			t.Fatalf("trends.go:trendingPreviewCardRefs should not contain Rails-incompatible fragment %q", forbidden)
		}
	}
}

func TestTrendLanguagePreferenceMatchesRailsShape(t *testing.T) {
	if got := normalizeTrendLanguages([]string{"ja-JP", "en", "ja", "  "}); len(got) != 2 || got[0] != "ja" || got[1] != "en" {
		t.Fatalf("normalizeTrendLanguages = %#v", got)
	}
	src, err := os.ReadFile("trends.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		fn   string
		want string
	}{
		{"trendPreferredLanguages", `if languages := normalizeTrendLanguages([]string(user.ChosenLanguages)); len(languages) > 0 {`},
		{"trendPreferredLanguages", `acceptLanguageCandidate((*c).Request().Header.Get("Accept-Language"))`},
		{"applyTrendLanguageOrder", `CASE WHEN `},
		{"applyTrendLanguageOrder", `IN (?) THEN 1 ELSE 0 END DESC`},
	} {
		if !functionBodyContains(t, src, check.fn, check.want) {
			t.Fatalf("trends.go:%s does not contain %q", check.fn, check.want)
		}
	}
}

func TestTrendingLinksUseRailsOffsetPaginationHeader(t *testing.T) {
	src, err := os.ReadFile("trends.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`offsetValue := offset(c)`,
		`limitValue := limit(c, 10, 20)`,
		`serializer.PreviewCardTrendLinkFromModelWithHistory(s.cfg, card, s.linkHistory((*c).Request().Context(), card.ID, now))`,
		`offsetPaginationLinkWithAllowedParams(c, offsetValue, limitValue, len(out), []string{"limit"})`,
	} {
		if !functionBodyContains(t, src, "trendingLinks", want) {
			t.Fatalf("trends.go:trendingLinks does not contain %q", want)
		}
	}
}

func TestTrendingTagsUseRailsOffsetPaginationHeader(t *testing.T) {
	src, err := os.ReadFile("tags.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`offsetValue := offset(c)`,
		`limitValue := limit(c, 10, 20)`,
		`offsetPaginationLinkWithAllowedParams(c, offsetValue, limitValue, len(out), []string{"limit"})`,
	} {
		if !functionBodyContains(t, src, "trendingTags", want) {
			t.Fatalf("tags.go:trendingTags does not contain %q", want)
		}
	}
}

func TestTrendEndpointsMatchRailsCacheAndVaryHeaders(t *testing.T) {
	tests := []struct {
		path    string
		handler func(*Server, *echo.Context) error
		vary    string
	}{
		{path: "/api/v1/trends", handler: (*Server).trendingTags, vary: "Authorization"},
		{path: "/api/v1/trends/tags", handler: (*Server).trendingTags, vary: "Authorization"},
		{path: "/api/v1/trends/links", handler: (*Server).trendingLinks, vary: "Authorization, Accept-Language"},
		{path: "/api/v1/trends/statuses", handler: (*Server).trendingStatuses, vary: "Authorization, Accept-Language"},
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
			if got := rec.Header().Get("Vary"); got != tt.vary {
				t.Fatalf("Vary = %q, want %q", got, tt.vary)
			}
			if got := rec.Header().Get("Cache-Control"); got != "max-age=15, public, stale-while-revalidate=30, stale-if-error=86400" {
				t.Fatalf("Cache-Control = %q", got)
			}
		})
	}
}

func TestTrendEndpointsDoNotPublicCacheAuthenticatedRequests(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/trends/links", nil)
	req.Header.Set("Authorization", "Bearer token")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)
	s := &Server{}

	if err := s.trendingLinks(c); err != nil {
		t.Fatal(err)
	}
	if got := rec.Header().Get("Cache-Control"); got != "" {
		t.Fatalf("Cache-Control = %q, want empty", got)
	}
	if got := rec.Header().Get("Vary"); got != "Authorization, Accept-Language" {
		t.Fatalf("Vary = %q", got)
	}
}

func TestTrendingTagsPreferRailsRedisTrendZSet(t *testing.T) {
	src, err := os.ReadFile("tags.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`s.trendingTagRowsFromRedis(ctx, limitValue, offsetValue, now, true)`,
		`key = s.cfg.RedisNamespace + "trending_tags:allowed"`,
		`s.adminTagUsesRedisDay(tag.ID, day)`,
		`s.adminTagAccountsRedisDay(tag.ID, day)`,
	} {
		if !functionBodyContains(t, src, "trendingTagRows", want) && !functionBodyContains(t, src, "trendingTagRowsFromRedis", want) {
			t.Fatalf("tags.go trending tag path does not contain %q", want)
		}
	}
}

func TestTrendingStatusesReuseStatusReverseHelper(t *testing.T) {
	statuses := []models.Status{{ID: 101}, {ID: 102}, {ID: 103}}
	reverseStatuses(statuses)
	if statuses[0].ID != 103 || statuses[1].ID != 102 || statuses[2].ID != 101 {
		t.Fatalf("statuses = %#v", statuses)
	}
}
