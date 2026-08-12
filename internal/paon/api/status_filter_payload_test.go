package api

import (
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestSerializeStatusesWithFilterContextAddsMatchingFilters(t *testing.T) {
	now := time.Date(2026, 6, 19, 0, 0, 0, 0, time.UTC)
	status := statusFilterFixtureStatus(now)
	items := serializeStatusesWithFilterContext(
		config.Config{LocalDomain: "example.test"},
		[]models.Status{status},
		&models.Account{ID: 9},
		statusFilterFixtureFilters(),
		"public",
	)
	if len(items) != 1 || len(items[0].Filtered) != 2 {
		t.Fatalf("items = %#v", items)
	}
	data, err := json.Marshal(items)
	if err != nil {
		t.Fatal(err)
	}
	payload := string(data)
	if !strings.Contains(payload, `"id":"9"`) || !strings.Contains(payload, `"id":"10"`) {
		t.Fatalf("payload = %s", payload)
	}
}

func TestSerializeStatusesWithFilterContextCopiesReblogFilters(t *testing.T) {
	now := time.Date(2026, 6, 19, 0, 0, 0, 0, time.UTC)
	reblogged := statusFilterFixtureStatus(now)
	wrapper := models.Status{
		ID:        200,
		AccountID: 8,
		Text:      "",
		CreatedAt: now,
		Account: models.Account{
			ID:        8,
			Username:  "bob",
			CreatedAt: now,
		},
		Reblog: &reblogged,
	}

	items := serializeStatusesWithFilterContext(
		config.Config{LocalDomain: "example.test"},
		[]models.Status{wrapper},
		&models.Account{ID: 9},
		statusFilterFixtureFilters(),
		"public",
	)
	if len(items) != 1 || len(items[0].Filtered) != 2 || items[0].Reblog == nil || len(items[0].Reblog.Filtered) != 2 {
		t.Fatalf("items = %#v", items)
	}
}

func TestStatusWithFilterContextMatchesRailsStatusMatchesFiltersAcrossContexts(t *testing.T) {
	now := time.Date(2026, 6, 19, 0, 0, 0, 0, time.UTC)
	status := statusFilterFixtureStatus(now)
	filters := []streamingFilter{
		{
			ID:           "11",
			Title:        "Thread",
			Context:      []string{"thread"},
			FilterAction: "warn",
			Keywords:     []any{},
			Statuses:     []any{},
			regexp:       regexp.MustCompile("(?i)spoiler"),
		},
		{
			ID:           "12",
			Title:        "Public only",
			Context:      []string{"public"},
			FilterAction: "warn",
			Keywords:     []any{},
			Statuses:     []any{},
			regexp:       regexp.MustCompile("(?i)spoiler"),
		},
	}

	item := statusWithFilterContext(config.Config{LocalDomain: "example.test"}, status, &models.Account{ID: 9}, filters, "thread")
	if len(item.Filtered) != 2 {
		t.Fatalf("filtered = %#v", item.Filtered)
	}
	data, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	payload := string(data)
	if !strings.Contains(payload, `"id":"11"`) || !strings.Contains(payload, `"id":"12"`) {
		t.Fatalf("payload = %s", payload)
	}
}

func TestStatusWithStreamingFilterContextHonorsTimelineContext(t *testing.T) {
	now := time.Date(2026, 6, 19, 0, 0, 0, 0, time.UTC)
	status := statusFilterFixtureStatus(now)
	filters := []streamingFilter{
		{
			ID:           "11",
			Title:        "Home",
			Context:      []string{"home"},
			FilterAction: "warn",
			Keywords:     []any{},
			Statuses:     []any{},
			regexp:       regexp.MustCompile("(?i)spoiler"),
		},
		{
			ID:           "12",
			Title:        "Public only",
			Context:      []string{"public"},
			FilterAction: "warn",
			Keywords:     []any{},
			Statuses:     []any{},
			regexp:       regexp.MustCompile("(?i)spoiler"),
		},
	}

	item := statusWithStreamingFilterContext(config.Config{LocalDomain: "example.test"}, status, &models.Account{ID: 9}, filters, "home")
	if len(item.Filtered) != 1 {
		t.Fatalf("filtered = %#v", item.Filtered)
	}
	data, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	payload := string(data)
	if !strings.Contains(payload, `"id":"11"`) || strings.Contains(payload, `"id":"12"`) {
		t.Fatalf("payload = %s", payload)
	}
}

func TestStatusWithAllFilterContextsMatchesRailsStatusMatchesFilters(t *testing.T) {
	now := time.Date(2026, 6, 19, 0, 0, 0, 0, time.UTC)
	status := statusFilterFixtureStatus(now)

	item := statusWithAllFilterContexts(config.Config{LocalDomain: "example.test"}, status, &models.Account{ID: 9}, statusFilterFixtureFilters())
	if len(item.Filtered) != 2 {
		t.Fatalf("filtered = %#v", item.Filtered)
	}
	data, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	payload := string(data)
	if !strings.Contains(payload, `"id":"9"`) || !strings.Contains(payload, `"id":"10"`) {
		t.Fatalf("payload = %s", payload)
	}
}

func TestStatusWithSourceAndFilterContextKeepsRailsDeleteResponseFilters(t *testing.T) {
	now := time.Date(2026, 6, 19, 0, 0, 0, 0, time.UTC)
	status := statusFilterFixtureStatus(now)

	item := statusWithSourceAndFilterContext(config.Config{LocalDomain: "example.test"}, status, &models.Account{ID: 9}, statusFilterFixtureFilters(), "public")
	if item.Text == nil || *item.Text != "a spoiler appears" {
		t.Fatalf("source text = %#v", item.Text)
	}
	if len(item.Filtered) != 2 {
		t.Fatalf("filtered = %#v", item.Filtered)
	}
	data, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatal(err)
	}
	if _, ok := payload["content"]; ok {
		t.Fatalf("source payload should omit content like Rails source_requested: %s", string(data))
	}
	if payload["text"] != "a spoiler appears" {
		t.Fatalf("text = %#v in %s", payload["text"], string(data))
	}
	if !strings.Contains(string(data), `"filtered":[`) || !strings.Contains(string(data), `"id":"9"`) || !strings.Contains(string(data), `"id":"10"`) {
		t.Fatalf("source payload missing filtered matches: %s", string(data))
	}
}

func TestStreamingSearchableTextUsesReblogPayload(t *testing.T) {
	payload := map[string]any{
		"id":      "200",
		"content": "<p>boost wrapper</p>",
		"reblog": map[string]any{
			"id":      "100",
			"content": "<p>reblog spoiler</p>",
		},
	}
	got := streamingSearchableText(payload)
	if strings.Contains(got, "boost wrapper") || !strings.Contains(got, "reblog spoiler") {
		t.Fatalf("searchable text = %q", got)
	}
}

func TestStatusListFilterContext(t *testing.T) {
	e := echo.New()
	cases := map[string]string{
		"/api/v1/timelines/home":        "home",
		"/api/v1/timelines/list/1":      "home",
		"/api/v1/accounts/1/statuses":   "account",
		"/api/v1/timelines/public":      "public",
		"/api/v1/timelines/tag/golang":  "public",
		"/api/v1/timelines/link":        "public",
		"/api/v1/trends/statuses":       "public",
		"/api/v2/search":                "public",
		"/api/v1/favourites":            "public",
		"/api/v1/bookmarks":             "public",
		"/api/v1/statuses/1/favourited": "",
	}
	for path, want := range cases {
		req := httptest.NewRequest("GET", path, nil)
		c := echo.NewContext(req, httptest.NewRecorder(), e)
		if got := statusListFilterContext(c); got != want {
			t.Fatalf("%s context = %q, want %q", path, got, want)
		}
	}
}

func statusFilterFixtureStatus(now time.Time) models.Status {
	return models.Status{
		ID:        100,
		AccountID: 7,
		Text:      "a spoiler appears",
		CreatedAt: now,
		Account: models.Account{
			ID:        7,
			Username:  "alice",
			CreatedAt: now,
		},
	}
}

func statusFilterFixtureFilters() []streamingFilter {
	return []streamingFilter{
		{
			ID:           "9",
			Title:        "Public",
			Context:      []string{"public"},
			FilterAction: "hide",
			Keywords:     []any{},
			Statuses:     []any{},
			regexp:       regexp.MustCompile("(?i)spoiler"),
		},
		{
			ID:           "10",
			Title:        "Home only",
			Context:      []string{"home"},
			FilterAction: "hide",
			Keywords:     []any{},
			Statuses:     []any{},
			regexp:       regexp.MustCompile("(?i)spoiler"),
		},
	}
}

func TestMatchingStatusIDsKeepsPayloadOrder(t *testing.T) {
	if got := matchingStatusIDs([]int64{200, 100}, []int64{100, 200}); !reflect.DeepEqual(got, []string{"200", "100"}) {
		t.Fatalf("matches = %#v", got)
	}
}
