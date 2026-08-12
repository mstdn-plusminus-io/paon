package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"github.com/mstdn-plusminus-io/paon/internal/paon/serializer"
)

func TestNormalizeTagNameLowercasesAndKeepsDisplayName(t *testing.T) {
	normalized, display, ok := normalizeTagName("#GoLang")
	if !ok {
		t.Fatal("expected tag name to be valid")
	}
	if normalized != "golang" {
		t.Fatalf("normalized = %q", normalized)
	}
	if display != "GoLang" {
		t.Fatalf("display = %q", display)
	}
}

func TestNormalizeTagNameUsesRailsHashtagNormalizer(t *testing.T) {
	normalized, display, ok := normalizeTagName("#ＧｏＣａｆé")
	if !ok {
		t.Fatal("expected full-width accented tag name to be valid")
	}
	if normalized != "gocafe" {
		t.Fatalf("normalized = %q, want Rails-style gocafe", normalized)
	}
	if display != "ＧｏＣａｆé" {
		t.Fatalf("display = %q", display)
	}
}

func TestNormalizeTagNamePreservesJapaneseDakuten(t *testing.T) {
	tests := []string{
		"えあいさんちの今日のごはん",
		"しばふさんちの今日のごはん",
	}
	for _, want := range tests {
		normalized, display, ok := normalizeTagName("#" + want)
		if !ok {
			t.Fatalf("normalizeTagName(%q) rejected a valid Japanese hashtag", want)
		}
		if normalized != want || display != want {
			t.Fatalf("normalizeTagName(%q) = %q, %q; want both %q", want, normalized, display, want)
		}
	}
}

func TestNormalizeTagNameComposesJapaneseDakuten(t *testing.T) {
	const decomposed = "えあいさんちの今日のこ\u3099はん"
	const composed = "えあいさんちの今日のごはん"
	normalized, display, ok := normalizeTagName("#" + decomposed)
	if !ok {
		t.Fatal("expected decomposed Japanese hashtag to be valid after normalization")
	}
	if normalized != composed || display != composed {
		t.Fatalf("normalizeTagName(decomposed) = %q, %q; want both %q", normalized, display, composed)
	}
}

func TestNormalizeTagNameComposesHalfWidthKatakanaDakuten(t *testing.T) {
	normalized, display, ok := normalizeTagName("#ﾊﾞﾅﾅ")
	if !ok || normalized != "バナナ" || display != "ﾊﾞﾅﾅ" {
		t.Fatalf("normalizeTagName = %q, %q, %t; want バナナ, ﾊﾞﾅﾅ, true", normalized, display, ok)
	}
}

func TestNormalizeTagNameRejectsInvalidCharacters(t *testing.T) {
	if _, _, ok := normalizeTagName("bad/tag"); ok {
		t.Fatal("expected slash to be rejected")
	}
	if _, _, ok := normalizeTagName("%E6%97%A5"); ok {
		t.Fatal("expected percent-encoded tag name to be rejected like Rails")
	}
	if _, _, ok := normalizeTagName(""); ok {
		t.Fatal("expected empty tag to be rejected")
	}
}

func TestSerializerTrendingTagIncludesHistory(t *testing.T) {
	following := true
	trendHistory := []any{map[string]string{"day": "1781827200", "uses": "12", "accounts": "3"}}
	out := serializer.TagDetailFromModelWithRelationships(
		config.Config{Scheme: "https", WebDomain: "example.com", LocalDomain: "example.com"},
		models.Tag{ID: 10, Name: "golang", DisplayName: sql.NullString{String: "GoLang", Valid: true}},
		&following, nil, trendHistory,
	)
	if out.Name != "GoLang" || out.URL != "https://example.com/tags/golang" {
		t.Fatalf("tag = %#v", out)
	}
	if out.Following == nil || !*out.Following {
		t.Fatalf("following = %#v", out.Following)
	}
	if len(out.History) != 1 {
		t.Fatalf("history = %#v", out.History)
	}
	historyEntry := out.History[0].(map[string]string)
	if historyEntry["day"] != "1781827200" || historyEntry["uses"] != "12" || historyEntry["accounts"] != "3" {
		t.Fatalf("history = %#v", historyEntry)
	}
}

func TestTagHistoryForUnsavedTagReturnsSevenZeroDays(t *testing.T) {
	s := &Server{}
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	history := s.tagHistory(context.Background(), 0, now)
	if len(history) != 7 {
		t.Fatalf("history length = %d, want 7", len(history))
	}
	for i, item := range history {
		day, ok := item.(map[string]string)
		if !ok {
			t.Fatalf("history[%d] = %#v", i, item)
		}
		wantDay := dayStart(now.AddDate(0, 0, -i)).Unix()
		if day["day"] != strconv.FormatInt(wantDay, 10) || day["uses"] != "0" || day["accounts"] != "0" {
			t.Fatalf("history[%d] = %#v", i, day)
		}
	}
}

func TestPublicTagActivityPubCollection(t *testing.T) {
	s, err := NewServer(config.Config{Scheme: "https", WebDomain: "example.com", LocalDomain: "example.com", Title: "Paon"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/tags/GoLang", nil)
	req.Header.Set("Accept", "application/activity+json")
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "application/activity+json; charset=utf-8" {
		t.Fatalf("content-type = %q", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "max-age=180, public" {
		t.Fatalf("Cache-Control = %q", got)
	}
	if got := rec.Header().Get("Vary"); got != "Accept, Accept-Language, Cookie" {
		t.Fatalf("Vary = %q", got)
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["id"] != "https://example.com/tags/golang" || out["type"] != "OrderedCollection" {
		t.Fatalf("collection = %#v", out)
	}
	if out["@context"] != "https://www.w3.org/ns/activitystreams" {
		t.Fatalf("context should match Rails ActivityPub::CollectionSerializer: %#v", out["@context"])
	}
	if _, ok := out["name"]; ok {
		t.Fatalf("Rails tag ActivityPub collection should omit name: %#v", out)
	}
	if _, ok := out["totalItems"]; ok {
		t.Fatalf("Rails tag ActivityPub collection should omit totalItems when presenter size is blank: %#v", out)
	}
}

func TestPublicTagHTMLRouteMatchesRailsAnonymousCache(t *testing.T) {
	s, err := NewServer(config.Config{Scheme: "https", WebDomain: "example.com", LocalDomain: "example.com", Title: "Paon"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/tags/GoLang", nil)
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Fatalf("content-type = %q", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "max-age=15, public, stale-while-revalidate=30, stale-if-error=3600" {
		t.Fatalf("Cache-Control = %q", got)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`<meta name="robots" content="noindex"`,
		`<link rel="alternate" type="application/rss&#43;xml" href="https://example.com/tags/golang"`,
		`<link rel="alternate" type="application/activity&#43;json" href="https://example.com/tags/golang"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("tag HTML missing %q: %s", want, body)
		}
	}
}

func TestPublicTagRSSCollection(t *testing.T) {
	s, err := NewServer(config.Config{Scheme: "https", WebDomain: "example.com", LocalDomain: "example.com", Title: "Paon"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/tags/GoLang.rss", nil)
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/rss+xml") {
		t.Fatalf("content-type = %q", got)
	}
	body := rec.Body.String()
	for _, want := range []string{`<rss version="2.0"`, `<title>#GoLang</title>`, `<link>https://example.com/tags/golang</link>`} {
		if !strings.Contains(body, want) {
			t.Fatalf("RSS body missing %q: %s", want, body)
		}
	}
}

func TestPublicTagAcceptsRSSHeader(t *testing.T) {
	s, err := NewServer(config.Config{Scheme: "https", WebDomain: "example.com", LocalDomain: "example.com", Title: "Paon"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/tags/GoLang", nil)
	req.Header.Set("Accept", "application/rss+xml")
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/rss+xml") {
		t.Fatalf("content-type = %q", got)
	}
}

func TestPublicTagRSSOptionsUseRailsLocalParam(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/tags/GoLang.rss?local=1", nil)
	c := echo.NewContext(req, httptest.NewRecorder(), e)

	if !publicTagRSSOptionsFromRequest(c).Local {
		t.Fatal("local=1 should enable local-only tag RSS filtering")
	}

	req = httptest.NewRequest(http.MethodGet, "/tags/GoLang.rss?local=false", nil)
	c = echo.NewContext(req, httptest.NewRecorder(), e)
	if publicTagRSSOptionsFromRequest(c).Local {
		t.Fatal("local=false should not enable local-only tag RSS filtering")
	}
}

func TestPublicTagRSSLimitMatchesRailsTagsController(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/tags/GoLang.rss", nil)
	c := echo.NewContext(req, httptest.NewRecorder(), e)
	if got := publicTagRSSLimit(c); got != 20 {
		t.Fatalf("default limit = %d, want Rails PAGE_SIZE 20", got)
	}

	req = httptest.NewRequest(http.MethodGet, "/tags/GoLang.rss?limit=150", nil)
	c = echo.NewContext(req, httptest.NewRecorder(), e)
	if got := publicTagRSSLimit(c); got != 150 {
		t.Fatalf("limit = %d, want requested value", got)
	}

	req = httptest.NewRequest(http.MethodGet, "/tags/GoLang.rss?limit=500", nil)
	c = echo.NewContext(req, httptest.NewRecorder(), e)
	if got := publicTagRSSLimit(c); got != 200 {
		t.Fatalf("max limit = %d, want Rails PAGE_SIZE_MAX 200", got)
	}
}

func TestRSSGeneratorMatchesRailsMastodonVersionFormat(t *testing.T) {
	if got := rssGenerator(config.Config{}); got != "Mastodon v4.2.29" {
		t.Fatalf("default generator = %q", got)
	}
	if got := rssGenerator(config.Config{MastodonVersion: "4.3.0"}); got != "Mastodon v4.3.0" {
		t.Fatalf("custom generator = %q", got)
	}
}

func TestStatusRSSDescriptionIncludesRailsContentWarningLabel(t *testing.T) {
	english := models.Status{SpoilerText: "spoiler", Text: "body"}
	if got := statusRSSDescription(english); got != "<p><strong>Content warning:</strong> spoiler</p><hr><p>body</p>" {
		t.Fatalf("english CW description = %q", got)
	}

	japanese := models.Status{
		SpoilerText: "ネタバレ",
		Text:        "本文",
		Language:    sql.NullString{String: "ja", Valid: true},
	}
	if got := statusRSSDescription(japanese); got != "<p><strong>閲覧注意:</strong> ネタバレ</p><hr><p>本文</p>" {
		t.Fatalf("japanese CW description = %q", got)
	}
}

func TestStatusRSSDescriptionIncludesRailsPollOptions(t *testing.T) {
	status := models.Status{
		Text: "body",
		Poll: &models.Poll{Options: models.StringArray{"yes", "no <maybe>"}},
	}
	if got := statusRSSDescription(status); got != `<p>body</p><p><radio disabled="disabled">yes</radio><br><radio disabled="disabled">no &lt;maybe&gt;</radio></p>` {
		t.Fatalf("single-choice poll description = %q", got)
	}

	status.Poll.Multiple = true
	if got := statusRSSPollOptions(status.Poll); got != `<p><checkbox disabled="disabled">yes</checkbox><br><checkbox disabled="disabled">no &lt;maybe&gt;</checkbox></p>` {
		t.Fatalf("multiple-choice poll options = %q", got)
	}
}

func TestPublicTagRSSHydratesCustomEmojisBeforeRendering(t *testing.T) {
	src, err := os.ReadFile("tags.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, src, "publicTagRSS", `if err := s.hydrateStatusesCustomEmojis(statuses); err != nil`) {
		t.Fatal("tag RSS must hydrate status custom emojis before rendering like Rails FeedManager")
	}
	if !functionBodyContains(t, src, "renderTagRSS", `Description:   statusRSSDescriptionWithConfig(s.cfg, status)`) {
		t.Fatal("tag RSS must render descriptions with config-aware custom emoji URLs")
	}
}

func TestTagRESTEndpointsMatchRailsRequestSpecSemantics(t *testing.T) {
	src, err := os.ReadFile("tags.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		fn   string
		want string
	}{
		{"showTag", `tag, err := s.findOrBuildTag(c.Param("name"))`},
		{"showTag", `return apiError(c, http.StatusNotFound, "Record not found")`},
		{"showTag", `s.tagDetailWithHistoryAndRelationships(c.Request().Context(), *tag, following, featuring)`},
		{"followTag", `s.requireAccountScope(c, "follow", "write", "write:follows")`},
		{"followTag", `tag, err := s.findOrCreateTag(c.Param("name"))`},
		{"followTag", `s.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&follow)`},
		{"followTag", `s.tagDetailWithHistoryAndRelationships(c.Request().Context(), *tag, &following, s.tagFeaturing(c, tag.ID))`},
		{"unfollowTag", `s.requireAccountScope(c, "follow", "write", "write:follows")`},
		{"unfollowTag", `tag, err := s.findOrBuildTag(c.Param("name"))`},
		{"unfollowTag", `s.db.Where("account_id = ? AND tag_id = ?", account.ID, tag.ID).Delete(&models.TagFollow{})`},
		{"unfollowTag", `s.tagDetailWithHistoryAndRelationships(c.Request().Context(), *tag, &following, s.tagFeaturing(c, tag.ID))`},
		{"tagDetailWithHistoryAndRelationships", `serializer.TagDetailFromModelWithRelationships(s.cfg, tag, following, featuring, s.tagHistory(ctx, tag.ID, time.Now().UTC()))`},
	} {
		if !functionBodyContains(t, src, check.fn, check.want) {
			t.Fatalf("tags.go:%s does not contain Rails-compatible behavior %q", check.fn, check.want)
		}
	}
}

func TestReverseTagFollowsKeepsNewestFirstForMinIDPagination(t *testing.T) {
	rows := []models.TagFollow{{ID: 101}, {ID: 102}, {ID: 103}}
	reverseTagFollows(rows)
	if rows[0].ID != 103 || rows[1].ID != 102 || rows[2].ID != 101 {
		t.Fatalf("rows = %#v", rows)
	}
}

func TestFollowedTagsUseRailsMinIDPagination(t *testing.T) {
	src, err := os.ReadFile("tags.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`if minID := c.QueryParam("min_id"); queryParamValuePresent(c, "min_id")`,
		`query = query.Where("tag_follows.id > ?", minID).Order("tag_follows.id ASC")`,
		`if maxID := c.QueryParam("max_id"); queryParamValuePresent(c, "max_id")`,
		`if sinceID := c.QueryParam("since_id"); queryParamValuePresent(c, "since_id")`,
		`query = query.Order("tag_follows.id DESC")`,
		`limitValue := limit(c, 100, 200)`,
		`if queryParamValuePresent(c, "min_id")`,
		`reverseTagFollows(follows)`,
		`limitOnlyPaginationLink(c, follows[0].ID, follows[len(follows)-1].ID, "since_id", len(follows) == limitValue)`,
	} {
		if !functionBodyContains(t, src, "followedTags", want) {
			t.Fatalf("tags.go:followedTags does not contain %q", want)
		}
	}
}

func TestUnfollowTagUnmergesRailsHomeFeed(t *testing.T) {
	src, err := os.ReadFile("tags.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, src, "unfollowTag", `s.unmergeTagFromHomeBestEffort(c.Request().Context(), tag.ID, account.ID)`) {
		t.Fatal("unfollowTag must schedule Rails TagUnmergeWorker equivalent after deleting a tag follow")
	}
	if !functionBodyContains(t, src, "unmergeTagFromHomeBestEffort", `if s.enqueueTagUnmergeTask(tagID, accountID) {`) {
		t.Fatal("unmergeTagFromHomeBestEffort must enqueue Rails TagUnmergeWorker equivalent before fallback")
	}
	for _, want := range []string{
		`"ZRANGE", homeFeedRedisKey(redisConfig(s.cfg).prefix, accountID), "0", "-1"`,
		`Joins("JOIN statuses_tags removed_tag ON removed_tag.status_id = statuses.id AND removed_tag.tag_id = ?", tagID)`,
		`Where("statuses.account_id <> ?", accountID)`,
		`follows.account_id = ?`,
		`follows.target_account_id = statuses.account_id`,
		`JOIN tag_follows remaining_tag_follows`,
		`remaining_tag_follows.account_id = ?`,
		`s.removeStatusFromFeedContext(ctx, "home", accountID, status, aggregateReblogs)`,
	} {
		if !functionBodyContains(t, src, "unmergeTagFromHome", want) {
			t.Fatalf("unmergeTagFromHome missing Rails-compatible behavior %q", want)
		}
	}
}

func TestRenderTagRSSIncludesMediaAttachments(t *testing.T) {
	s := &Server{cfg: config.Config{Scheme: "https", WebDomain: "example.com", LocalDomain: "example.com"}}
	tag := models.Tag{
		ID:          10,
		Name:        "golang",
		DisplayName: sql.NullString{String: "GoLang", Valid: true},
	}
	status := models.Status{
		ID:        123,
		Text:      "hello",
		CreatedAt: time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC),
		Account:   models.Account{ID: 7, Username: "alice"},
		MediaAttachments: []models.MediaAttachment{{
			ID:              8,
			Type:            0,
			FileFileName:    sql.NullString{String: "photo.png", Valid: true},
			FileContentType: sql.NullString{String: "image/png", Valid: true},
			FileFileSize:    sql.NullInt64{Int64: 1234, Valid: true},
			Description:     sql.NullString{String: "alt text", Valid: true},
		}},
	}

	body, err := s.renderTagRSS(tag, []models.Status{status})
	if err != nil {
		t.Fatal(err)
	}
	xml := string(body)
	for _, want := range []string{
		`<title>#GoLang</title>`,
		`xmlns:webfeeds="http://webfeeds.org/rss/1.0"`,
		`<generator>Mastodon v4.2.29</generator>`,
		`<media:content url="https://example.com/system/media_attachments/files/000/000/008/original/photo.png" type="image/png" fileSize="1234" medium="image">`,
		`<media:rating scheme="urn:simple">nonadult</media:rating>`,
		`<media:description type="plain">alt text</media:description>`,
	} {
		if !strings.Contains(xml, want) {
			t.Fatalf("tag RSS missing %q: %s", want, xml)
		}
	}
	if strings.Contains(xml, "<title>Status by ") {
		t.Fatalf("RSS item must not include Go-only status titles: %s", xml)
	}
	if strings.Contains(xml, "xmlns:atom=") {
		t.Fatalf("RSS root must not include Go-only atom namespace: %s", xml)
	}
}

func TestRSSMediaURLsUseStorageHostForPaperclipAssets(t *testing.T) {
	cfg := config.Config{WebDomain: "example.com", Scheme: "https", StorageHost: "https://media.example.com/"}
	attachment := models.MediaAttachment{
		ID:                8,
		FileFileName:      sql.NullString{String: "photo.png", Valid: true},
		ThumbnailFileName: sql.NullString{String: "thumb.png", Valid: true},
	}
	if got := rssMediaURL(cfg, attachment); got != "https://media.example.com/media_attachments/files/000/000/008/original/photo.png" {
		t.Fatalf("rss media URL = %q", got)
	}
	if got := rssMediaThumbnailURL(cfg, attachment); got != "https://media.example.com/media_attachments/thumbnails/000/000/008/original/thumb.png" {
		t.Fatalf("rss thumbnail URL = %q", got)
	}
}
