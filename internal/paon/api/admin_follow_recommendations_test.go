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

func TestAdminFollowRecommendationsRequireSession(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/follow_recommendations?language=ja", nil)
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	want := "/auth/sign_in?redirect_to=" + url.QueryEscape("/admin/follow_recommendations?language=ja")
	if got := rec.Header().Get("Location"); got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}
}

func TestParseAdminFollowRecommendationAccountIDs(t *testing.T) {
	form := url.Values{}
	form.Add("form_account_batch[account_ids][]", "4")
	form.Add("form_account_batch[account_ids][]", "bad")
	form.Add("form_account_batch[account_ids]", "5")

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/admin/follow_recommendations", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	got := parseAdminFollowRecommendationAccountIDs(c)
	if len(got) != 1 || got[0] != 4 {
		t.Fatalf("ids = %#v", got)
	}
}

func TestAdminFollowRecommendationAction(t *testing.T) {
	e := echo.New()
	for body, want := range map[string]string{
		"suppress=1":                       "suppress_follow_recommendation",
		"suppress=":                        "suppress_follow_recommendation",
		"unsuppress=1":                     "unsuppress_follow_recommendation",
		"unsuppress=":                      "unsuppress_follow_recommendation",
		"form_account_batch[action]=other": "",
	} {
		req := httptest.NewRequest(http.MethodPost, "/admin/follow_recommendations", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		c := echo.NewContext(req, rec, e)
		if got := adminFollowRecommendationAction(c); got != want {
			t.Fatalf("action for %q = %q, want %q", body, got, want)
		}
	}
}

func TestAdminFollowRecommendationsHTMLIncludesRailsFields(t *testing.T) {
	html := adminFollowRecommendationsHTML([]models.Account{{
		ID:       7,
		Username: "alice",
		AccountStat: models.AccountStat{
			StatusesCount:  12,
			FollowersCount: 34,
			LastStatusAt:   sql.NullTime{Time: time.Date(2026, 6, 19, 1, 2, 3, 0, time.UTC), Valid: true},
		},
	}}, "saved", "", "", "ja")
	for _, want := range []string{
		"Follow recommendations",
		`action="/admin/follow_recommendations"`,
		`name="_method" value="patch"`,
		`<select name="language">`,
		`<option value="ja" selected>Japanese</option>`,
		`<option value="zh-CN">Chinese (China)</option>`,
		`name="suppress" value="1"`,
		`name="form_account_batch[account_ids][]" value="7"`,
		`class="account account--minimal"`,
		`class="accounts-table"`,
		"alice",
		"12",
		"34",
		"saved",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("follow recommendations html missing %q: %s", want, html)
		}
	}
}

func TestAdminFollowRecommendationRedirectQuery(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/admin/follow_recommendations", strings.NewReader("language=ja&status=suppressed"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	got := adminFollowRecommendationRedirectQuery(c, "notice", "ok")
	values, err := url.ParseQuery(got)
	if err != nil {
		t.Fatal(err)
	}
	if values.Get("language") != "ja" || values.Get("status") != "suppressed" || values.Get("notice") != "ok" {
		t.Fatalf("query = %q", got)
	}

	req = httptest.NewRequest(http.MethodPost, "/admin/follow_recommendations", strings.NewReader("language=&status=other"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	c = echo.NewContext(req, rec, e)
	got = adminFollowRecommendationFilterQuery(c)
	values, err = url.ParseQuery(got)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := values["language"]; !ok || values.Get("language") != "" || values.Get("status") != "other" {
		t.Fatalf("raw filter query = %q, values = %#v", got, values)
	}
}

func TestFollowRecommendationsRedisKeyMatchesRailsNamespaceShape(t *testing.T) {
	cfg := config.Config{RedisNamespace: "mastodon:"}
	if got, want := followRecommendationsRedisKey(cfg.RedisNamespace, "ja"), "mastodon:follow_recommendations:ja"; got != want {
		t.Fatalf("redis key = %q, want %q", got, want)
	}
	if len(railsI18nAvailableLocales) == 0 || railsI18nAvailableLocales[0] != "af" || railsI18nAvailableLocales[len(railsI18nAvailableLocales)-1] != "zh-TW" {
		t.Fatalf("available locales are not aligned with Rails config: %#v", railsI18nAvailableLocales)
	}
}

func TestAdminFollowRecommendationsUseRailsRedisOrder(t *testing.T) {
	src, err := os.ReadFile("admin_follow_recommendations.go")
	if err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		functionName string
		want         string
	}{
		{"adminFollowRecommendationAccounts", `s.adminFollowRecommendationAccountsFromRedis(redisCtx, adminFollowRecommendationLanguage(c, defaultLocale))`},
		{"adminFollowRecommendationAccountsFromRedis", `"ZREVRANGE", followRecommendationsRedisKey(s.cfg.RedisNamespace, locale), "0", "-1"`},
		{"adminFollowRecommendationAccountsFromRedis", `followRecommendationsRedisKey(s.cfg.RedisNamespace, locale)`},
		{"adminFollowRecommendationAccountsFromRedis", `adminFollowRecommendationIDsFromRedisMembers(members)`},
		{"adminFollowRecommendationAccountsFromRedis", `adminFollowRecommendationAccountsInRedisOrder(accounts, accountIDs)`},
		{"adminFollowRecommendationAccounts", `query.Find(&accounts)`},
		{"adminFollowRecommendationFallbackAccounts", `Find(&accounts)`},
	}
	for _, check := range checks {
		if !functionBodyContains(t, src, check.functionName, check.want) {
			t.Fatalf("%s missing %q", check.functionName, check.want)
		}
	}
}

func TestAdminFollowRecommendationIDsFromRedisMembersPreserveAllRailsIDs(t *testing.T) {
	got := adminFollowRecommendationIDsFromRedisMembers([]string{"9", "bad", "8", "8", "7", "0", "-1", "6"})
	if len(got) != 4 || got[0] != 9 || got[1] != 8 || got[2] != 7 || got[3] != 6 {
		t.Fatalf("ids = %#v", got)
	}
}

func TestAdminFollowRecommendationAccountsInRedisOrder(t *testing.T) {
	got := adminFollowRecommendationAccountsInRedisOrder(
		[]models.Account{{ID: 3, Username: "carol"}, {ID: 1, Username: "alice"}},
		[]int64{1, 2, 3},
	)
	if len(got) != 2 || got[0].ID != 1 || got[1].ID != 3 {
		t.Fatalf("accounts = %#v", got)
	}
}
