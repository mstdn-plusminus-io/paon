package api

import (
	"encoding/json"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/mstdn-plusminus-io/paon/internal/paon/serializer"
)

func TestStreamingPayloadAccountExtraction(t *testing.T) {
	payload := map[string]any{
		"account": map[string]any{"id": "42", "acct": "alice@remote.example"},
		"mentions": []any{
			map[string]any{"id": "7"},
			map[string]any{"id": float64(42)},
		},
	}

	if got := streamingTargetAccountIDs(payload); !reflect.DeepEqual(got, []int64{42, 7}) {
		t.Fatalf("ids = %#v", got)
	}
	if got := streamingStatusAccountDomain(payload); got != "remote.example" {
		t.Fatalf("domain = %q", got)
	}
}

func TestStreamingSearchableTextUsesStatusFields(t *testing.T) {
	payload := map[string]any{
		"spoiler_text": "CW",
		"content":      "<p>Hello<br />world</p>",
		"poll": map[string]any{
			"options": []any{
				map[string]any{"title": "Poll choice"},
			},
		},
		"media_attachments": []any{
			map[string]any{"description": "Image description"},
		},
	}

	got := streamingSearchableText(payload)
	for _, want := range []string{"CW", "Hello\nworld", "Poll choice", "Image description"} {
		if !strings.Contains(got, want) {
			t.Fatalf("searchable text %q missing %q", got, want)
		}
	}
}

func TestStreamingFilterMessagePassesNonStatusEvents(t *testing.T) {
	message := redisMessage{Event: "delete", Payload: json.RawMessage(`"100"`)}
	filtered, ok := (&Server{}).filterStreamingMessage(streamingSession{}, message, "public")
	if !ok {
		t.Fatal("delete event was suppressed")
	}
	if filtered.Event != "delete" || string(filtered.Payload) != `"100"` {
		t.Fatalf("message = %#v", filtered)
	}
}

func TestStreamingLanguageFilterSuppressesUnchosenLanguage(t *testing.T) {
	message := redisMessage{Event: "update", Payload: json.RawMessage(`{"id":"100","language":"en"}`)}
	_, ok := (&Server{}).filterStreamingMessage(streamingSession{ChosenLanguages: []string{"ja"}}, message, "public")
	if ok {
		t.Fatal("message with unchosen language was not suppressed")
	}

	filtered, ok := (&Server{}).filterStreamingMessage(streamingSession{ChosenLanguages: []string{"ja", "en"}}, message, "public")
	if !ok {
		t.Fatal("message with chosen language was suppressed")
	}
	if string(filtered.Payload) != string(message.Payload) {
		t.Fatalf("payload = %s", filtered.Payload)
	}
}

func TestStreamingStatusBlockedUsesCaseInsensitiveDomainBlocks(t *testing.T) {
	src, err := os.ReadFile("streaming_filter.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, src, "streamingStatusBlocked", `Where("account_id = ? AND lower(domain) = lower(?)", accountID, domain)`) {
		t.Fatal("streaming domain-block suppression must use case-insensitive account_domain_blocks comparison")
	}
	if functionBodyContains(t, src, "streamingStatusBlocked", `Where("account_id = ? AND domain = ?", accountID, domain)`) {
		t.Fatal("streaming domain-block suppression must not use a case-sensitive comparison")
	}
}

func TestStreamingChannelFilterContext(t *testing.T) {
	for _, channel := range []string{"public", "public:local", "public:remote:media", "hashtag", "hashtag:local"} {
		if got := streamingChannelFilterContext(channel); got != "public" {
			t.Fatalf("%s context = %q", channel, got)
		}
	}
	for _, channel := range []string{"user"} {
		if got := streamingChannelFilterContext(channel); got != "home" {
			t.Fatalf("%s context = %q", channel, got)
		}
	}
	if got := streamingChannelFilterContext("list"); got != "" {
		t.Fatalf("list context = %q", got)
	}
	if got := streamingChannelFilterContext("user:notification"); got != "notifications" {
		t.Fatalf("notification context = %q", got)
	}
	if got := streamingChannelFilterContext("direct"); got != "public" {
		t.Fatalf("direct context = %q", got)
	}
	if got := streamingChannelFilterContext("unknown"); got != "" {
		t.Fatalf("user context = %q", got)
	}
}

func TestStreamingFilterResultsAttachFilteredPayload(t *testing.T) {
	filter := streamingFilter{
		ID:           "9",
		Title:        "Noise",
		Context:      []string{"public"},
		FilterAction: "warn",
		Keywords:     []any{},
		Statuses:     []any{},
		regexp:       mustRegexp("(?i)spoiler"),
	}
	payload := map[string]any{"content": "<p>A spoiler appears</p>"}
	results := streamingFilterResultsFromFilters(payload, []streamingFilter{filter}, "public")
	if len(results) != 1 || results[0].Filter.ID != "9" || len(results[0].KeywordMatches) != 1 {
		t.Fatalf("results = %#v", results)
	}
}

func TestStreamingFilterResultKeysMatchRESTFilterResultSerializer(t *testing.T) {
	actual, err := json.Marshal(streamingFilterResult{
		Filter:         streamingFilter{ID: "9"},
		KeywordMatches: []string{"spoiler"},
		StatusMatches:  []string{"100"},
	})
	if err != nil {
		t.Fatal(err)
	}
	expected, err := json.Marshal(serializer.FilterResult{
		Filter:         map[string]any{"id": "9"},
		KeywordMatches: []string{"spoiler"},
		StatusMatches:  []string{"100"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"filter"`, `"keyword_matches"`, `"status_matches"`} {
		if !strings.Contains(string(actual), key) || !strings.Contains(string(expected), key) {
			t.Fatalf("filter result key %s missing: actual=%s expected=%s", key, string(actual), string(expected))
		}
	}
}

func TestStreamingFilterResultsHonorContext(t *testing.T) {
	publicFilter := streamingFilter{
		ID:           "9",
		Title:        "Public",
		Context:      []string{"public"},
		FilterAction: "warn",
		Keywords:     []any{},
		Statuses:     []any{},
		regexp:       mustRegexp("(?i)spoiler"),
	}
	homeFilter := streamingFilter{
		ID:           "10",
		Title:        "Home",
		Context:      []string{"home"},
		FilterAction: "warn",
		Keywords:     []any{},
		Statuses:     []any{},
		regexp:       mustRegexp("(?i)spoiler"),
	}
	payload := map[string]any{"content": "<p>A spoiler appears</p>"}

	results := streamingFilterResultsFromFilters(payload, []streamingFilter{publicFilter, homeFilter}, "public")
	if len(results) != 1 || results[0].Filter.ID != "9" {
		t.Fatalf("results = %#v", results)
	}
}

func TestStreamingFilterResultsAttachStatusMatches(t *testing.T) {
	filter := streamingFilter{
		ID:           "9",
		Title:        "Pinned filter",
		Context:      []string{"public"},
		FilterAction: "warn",
		Keywords:     []any{},
		Statuses:     []any{},
		statusIDs:    []int64{100, 200},
	}
	payload := map[string]any{
		"id":      "300",
		"content": "",
		"reblog":  map[string]any{"id": "200"},
	}

	results := streamingFilterResultsFromFilters(payload, []streamingFilter{filter}, "public")
	if len(results) != 1 || results[0].Filter.ID != "9" {
		t.Fatalf("results = %#v", results)
	}
	matches, ok := results[0].StatusMatches.([]string)
	if !ok || !reflect.DeepEqual(matches, []string{"200"}) {
		t.Fatalf("status matches = %#v", results[0].StatusMatches)
	}
	if len(results[0].KeywordMatches) != 0 {
		t.Fatalf("keyword matches = %#v", results[0].KeywordMatches)
	}
}

func TestStreamingPayloadStatusIDsIncludesReblog(t *testing.T) {
	payload := map[string]any{
		"id":     float64(100),
		"reblog": map[string]any{"id": "200"},
	}

	if got := streamingPayloadStatusIDs(payload); !reflect.DeepEqual(got, []int64{100, 200}) {
		t.Fatalf("status ids = %#v", got)
	}
}

func mustRegexp(pattern string) *regexp.Regexp {
	return regexp.MustCompile(pattern)
}
