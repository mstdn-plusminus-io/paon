package api

import (
	"database/sql"
	"encoding/json"
	"io"
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
)

func TestNormalizeSearchQueryReplacesEquivalentQuotes(t *testing.T) {
	got := normalizeSearchQuery("  「hello」  ")
	if got != `"hello"` {
		t.Fatalf("query = %q", got)
	}
}

func TestSearchTagQueryRemovesHashPrefix(t *testing.T) {
	got := searchTagQuery(" #Go ")
	if got != "Go" {
		t.Fatalf("tag query = %q", got)
	}
}

func TestNormalizedSearchTagNameMatchesRailsNormalizer(t *testing.T) {
	if got := normalizedSearchTagName("#ＧｏCafé!"); got != "gocafe" {
		t.Fatalf("normalizedSearchTagName = %q, want gocafe", got)
	}
	if got := normalizedSearchTagName("#bad/tag"); got != "badtag" {
		t.Fatalf("normalizedSearchTagName invalid chars = %q, want badtag", got)
	}
}

func TestSearchIncludesType(t *testing.T) {
	if !searchIncludesType("", "accounts") {
		t.Fatal("empty type should include accounts")
	}
	if !searchIncludesType("statuses", "statuses") {
		t.Fatal("matching type should be included")
	}
	if searchIncludesType("hashtags", "statuses") {
		t.Fatal("non-matching type should be excluded")
	}
}

func TestEmptySearchResultUsesRailsSearchSerializerShape(t *testing.T) {
	body, err := json.Marshal(emptySearchResult())
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != `{"accounts":[],"statuses":[],"hashtags":[]}` {
		t.Fatalf("empty search result = %s", string(body))
	}
}

func TestSearchHashtagResultsUseRailsTagSerializerShape(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	tags := []models.Tag{
		{ID: 10, Name: "golang", DisplayName: sql.NullString{String: "GoLang", Valid: true}},
		{ID: 11, Name: "rust"},
	}

	authenticated := searchHashtagResults(cfg, tags, map[int64]bool{10: true}, true)
	body, err := json.Marshal(authenticated)
	if err != nil {
		t.Fatal(err)
	}
	var payload []map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload) != 2 {
		t.Fatalf("payload = %s", string(body))
	}
	if payload[0]["name"] != "GoLang" || payload[0]["url"] != "https://example.test/tags/golang" {
		t.Fatalf("first hashtag = %#v", payload[0])
	}
	if history, ok := payload[0]["history"].([]any); !ok || len(history) != 0 {
		t.Fatalf("history = %#v in %s", payload[0]["history"], string(body))
	}
	if payload[0]["following"] != true || payload[1]["following"] != false {
		t.Fatalf("following flags = %#v / %#v in %s", payload[0]["following"], payload[1]["following"], string(body))
	}

	anonymous := searchHashtagResults(cfg, tags[:1], nil, false)
	body, err = json.Marshal(anonymous)
	if err != nil {
		t.Fatal(err)
	}
	payload = nil
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	if _, ok := payload[0]["following"]; ok {
		t.Fatalf("anonymous hashtag serialized following: %s", string(body))
	}
}

func TestSearchKeepsRailsAuthorizationVary(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v2/search?q=alice", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())
	s := &Server{}

	if err := s.search(c); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Vary"); got != "Authorization" {
		t.Fatalf("Vary = %q, want Authorization", got)
	}
}

func TestSearchRequiresQParamLikeRails(t *testing.T) {
	for _, target := range []string{"/api/v2/search", "/api/v2/search?q=", "/api/v2/search?q=+"} {
		t.Run(target, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, target, nil)
			rec := httptest.NewRecorder()
			c := echo.NewContext(req, rec, echo.New())
			s := &Server{}

			err := s.search(c)
			if err == nil {
				t.Fatal("expected missing q error")
			}
			handleAPIError(c, err)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "param is missing or the value is empty: q") {
				t.Fatalf("body = %s", rec.Body.String())
			}
		})
	}
}

func TestAnonymousSearchAllowsWhitespaceOffsetLikeRailsPresent(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v2/search?q=alice&offset=+++", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())
	s := &Server{}

	if err := s.search(c); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "pagination is not supported") {
		t.Fatalf("whitespace offset should be absent like Rails present?: %s", rec.Body.String())
	}
}

func TestSearchBooleanParamsUseRailsTruthySemantics(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	for _, want := range []string{
		`s.authorizeTokenScopeIfPresent(c, "read", "read:search")`,
		`if strings.TrimSpace(c.QueryParam("q")) == ""`,
		`resolve := truthy(c.QueryParam("resolve"))`,
		`following := truthy(c.QueryParam("following"))`,
		`excludeUnreviewed := truthy(c.QueryParam("exclude_unreviewed"))`,
		`if limitValue < 1 {`,
		`return c.JSON(http.StatusOK, emptySearchResult())`,
		`offsetValue := searchOffsetValue(searchType, c.QueryParam("offset"))`,
		`resolveAccountSearchExact(q, account, resolve, following, offsetValue)`,
		`searchMeiliTagIDs(c.Request().Context(), q, excludeUnreviewed, limitValue, offsetValue)`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("server.go missing Rails truthy search handling %q", want)
		}
	}
	for _, forbidden := range []string{
		`QueryParam("resolve") == "true"`,
		`QueryParam("following") == "true"`,
		`QueryParam("exclude_unreviewed") == "true"`,
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("server.go should not use exact boolean comparison %q", forbidden)
		}
	}
}

func TestSearchMeiliIDsUsesConfiguredIndexAndKey(t *testing.T) {
	var requestPath string
	var authorization string
	var body map[string]any
	originalClient := meiliHTTPClient
	meiliHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requestPath = req.URL.Path
		authorization = req.Header.Get("Authorization")
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"hits":[{"id":"42"},{"id":7}]}`)),
		}, nil
	})}
	defer func() { meiliHTTPClient = originalClient }()

	server := &Server{cfg: config.Config{MeiliEnabled: true, MeiliHost: "http://meili.test", MeiliMasterKey: "secret", MeiliPrefix: "test_"}}
	ids, err := server.searchMeiliIDs(t.Context(), "accounts", "alice", meiliSearchOptions{Limit: 2, Offset: 3, Filter: "reviewed = true", Sort: []string{"usage:desc"}})
	if err != nil {
		t.Fatalf("searchMeiliIDs returned error: %v", err)
	}
	if requestPath != "/indexes/test_accounts/search" {
		t.Fatalf("path = %q", requestPath)
	}
	if authorization != "Bearer secret" {
		t.Fatalf("Authorization = %q", authorization)
	}
	if body["q"] != "alice" || body["filter"] != "reviewed = true" {
		t.Fatalf("body = %#v", body)
	}
	if body["limit"].(float64) != 2 || body["offset"].(float64) != 3 {
		t.Fatalf("pagination body = %#v", body)
	}
	if got := strings.Join([]string{body["sort"].([]any)[0].(string)}, ","); got != "usage:desc" {
		t.Fatalf("sort = %#v", body["sort"])
	}
	if len(ids) != 2 || ids[0] != 42 || ids[1] != 7 {
		t.Fatalf("ids = %#v", ids)
	}
}

func TestSearchMeiliAccountIDsSeparatesAutocompleteAndFullSearch(t *testing.T) {
	var bodies []map[string]any
	originalClient := meiliHTTPClient
	meiliHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		var body map[string]any
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		bodies = append(bodies, body)
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"hits":[]}`))}, nil
	})}
	defer func() { meiliHTTPClient = originalClient }()

	server := &Server{cfg: config.Config{MeiliEnabled: true, MeiliHost: "http://meili.test"}}
	if _, err := server.searchMeiliAccountIDs(t.Context(), "alice", nil, false, false, 20, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := server.searchMeiliAccountIDs(t.Context(), "alice profile", nil, false, true, 20, 0); err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 2 {
		t.Fatalf("requests = %#v", bodies)
	}
	if got := jsonStringSlice(bodies[0]["attributesToSearchOn"]); strings.Join(got, ",") != "username,display_name" {
		t.Fatalf("autocomplete attributes = %#v", got)
	}
	if _, exists := bodies[0]["matchingStrategy"]; exists {
		t.Fatalf("autocomplete matching strategy = %#v", bodies[0])
	}
	if got := jsonStringSlice(bodies[1]["attributesToSearchOn"]); strings.Join(got, ",") != "username,display_name,text" {
		t.Fatalf("full attributes = %#v", got)
	}
	if bodies[1]["matchingStrategy"] != "all" {
		t.Fatalf("full matching strategy = %#v", bodies[1])
	}
}

func jsonStringSlice(value any) []string {
	raw, _ := value.([]any)
	result := make([]string, 0, len(raw))
	for _, item := range raw {
		if text, ok := item.(string); ok {
			result = append(result, text)
		}
	}
	return result
}

func TestMeiliHTTPClientHasDefaultTimeoutAndBodyLimit(t *testing.T) {
	if meiliHTTPClient == nil {
		t.Fatal("meiliHTTPClient is nil")
	}
	if meiliHTTPClient.Timeout != meiliHTTPTimeout {
		t.Fatalf("meiliHTTPClient.Timeout = %s, want %s", meiliHTTPClient.Timeout, meiliHTTPTimeout)
	}

	originalClient := meiliHTTPClient
	meiliHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusOK,
			Status:        "200 OK",
			Header:        http.Header{"Content-Type": []string{"application/json"}},
			Body:          io.NopCloser(strings.NewReader(`{"hits":[]}`)),
			ContentLength: maxMeiliResponseBodySize + 1,
			Request:       req,
		}, nil
	})}
	defer func() { meiliHTTPClient = originalClient }()

	server := &Server{cfg: config.Config{MeiliEnabled: true, MeiliHost: "http://meili.test"}}
	if _, err := server.searchMeiliIDs(t.Context(), "accounts", "alice", meiliSearchOptions{Limit: 2}); err == nil {
		t.Fatal("expected advertised oversized Meilisearch response to fail")
	}

	meiliHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusOK,
			Status:        "200 OK",
			Header:        http.Header{"Content-Type": []string{"application/json"}},
			Body:          io.NopCloser(strings.NewReader(strings.Repeat("x", maxMeiliResponseBodySize+1))),
			ContentLength: -1,
			Request:       req,
		}, nil
	})}
	if _, err := server.searchMeiliIDs(t.Context(), "accounts", "alice", meiliSearchOptions{Limit: 2}); err == nil {
		t.Fatal("expected streamed oversized Meilisearch response to fail")
	}
}

func TestMeiliAvailableChecksHealthEndpoint(t *testing.T) {
	var requestPath string
	var authorization string
	originalClient := meiliHTTPClient
	meiliHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requestPath = req.URL.Path
		authorization = req.Header.Get("Authorization")
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"status":"available"}`)),
		}, nil
	})}
	defer func() { meiliHTTPClient = originalClient }()

	cfg := config.Config{MeiliEnabled: true, MeiliHost: "http://meili.test/", MeiliMasterKey: "secret"}
	if err := MeiliAvailable(t.Context(), cfg); err != nil {
		t.Fatalf("MeiliAvailable returned error: %v", err)
	}
	if requestPath != "/health" {
		t.Fatalf("path = %q", requestPath)
	}
	if authorization != "Bearer secret" {
		t.Fatalf("Authorization = %q", authorization)
	}
}

func TestWaitForMeiliAvailableRetriesUntilHealthy(t *testing.T) {
	var calls int
	originalClient := meiliHTTPClient
	meiliHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return &http.Response{
				StatusCode: http.StatusServiceUnavailable,
				Status:     "503 Service Unavailable",
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"status":"unavailable"}`)),
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"status":"available"}`)),
		}, nil
	})}
	defer func() { meiliHTTPClient = originalClient }()

	cfg := config.Config{MeiliEnabled: true, MeiliHost: "http://meili.test", MeiliMasterKey: "secret"}
	if err := WaitForMeiliAvailable(t.Context(), cfg, 2*time.Second); err != nil {
		t.Fatalf("WaitForMeiliAvailable returned error: %v", err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
}

func TestMeiliStatusQueryDateFiltersUseCurrentUserTimeZone(t *testing.T) {
	server := &Server{}
	_, filters, err := server.meiliStatusQueryFilters(t.Context(), "after:2026-06-01 during:2026-06-15", &models.Account{
		ID:   7,
		User: models.User{TimeZone: sql.NullString{String: "Asia/Tokyo", Valid: true}},
	})
	if err != nil {
		t.Fatalf("meiliStatusQueryFilters returned error: %v", err)
	}
	for _, want := range []string{
		"created_at_timestamp >= 1780239600",
		"created_at_timestamp >= 1781449200 AND created_at_timestamp < 1781535600",
	} {
		if !stringSliceContains(filters, want) {
			t.Fatalf("filters %#v missing %q", filters, want)
		}
	}
}

func TestSearchMeiliInstanceDomainsUsesInstancesIndex(t *testing.T) {
	var requestPath string
	var body map[string]any
	originalClient := meiliHTTPClient
	meiliHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requestPath = req.URL.Path
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"hits":[{"domain":"remote.example"},{"domain":"social.example"}]}`)),
		}, nil
	})}
	defer func() { meiliHTTPClient = originalClient }()

	server := &Server{cfg: config.Config{MeiliEnabled: true, MeiliHost: "http://meili.test", MeiliPrefix: "test_"}}
	domains, err := server.searchMeiliInstanceDomains(t.Context(), "remote", 10)
	if err != nil {
		t.Fatalf("searchMeiliInstanceDomains returned error: %v", err)
	}
	if requestPath != "/indexes/test_instances/search" {
		t.Fatalf("path = %q", requestPath)
	}
	if body["q"] != "remote" || body["limit"].(float64) != 10 {
		t.Fatalf("body = %#v", body)
	}
	if got := body["sort"].([]any)[0].(string); got != "accounts_count:desc" {
		t.Fatalf("sort = %#v", body["sort"])
	}
	if strings.Join(domains, ",") != "remote.example,social.example" {
		t.Fatalf("domains = %#v", domains)
	}
}

func TestMeiliUpsertDocumentsUsesConfiguredIndexAndKey(t *testing.T) {
	var method string
	var requestPath string
	var authorization string
	var body []map[string]any
	originalClient := meiliHTTPClient
	meiliHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		method = req.Method
		requestPath = req.URL.Path
		authorization = req.Header.Get("Authorization")
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		return &http.Response{
			StatusCode: http.StatusAccepted,
			Status:     "202 Accepted",
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"taskUid":1}`)),
		}, nil
	})}
	defer func() { meiliHTTPClient = originalClient }()

	server := &Server{cfg: config.Config{MeiliEnabled: true, MeiliHost: "http://meili.test", MeiliMasterKey: "secret", MeiliPrefix: "test_"}}
	err := server.meiliUpsertDocuments(t.Context(), "statuses", []map[string]any{{"id": 42, "visibility": "public"}})
	if err != nil {
		t.Fatalf("meiliUpsertDocuments returned error: %v", err)
	}
	if method != http.MethodPost || requestPath != "/indexes/test_statuses/documents" {
		t.Fatalf("request = %s %s", method, requestPath)
	}
	if authorization != "Bearer secret" {
		t.Fatalf("Authorization = %q", authorization)
	}
	if len(body) != 1 || body[0]["visibility"] != "public" {
		t.Fatalf("body = %#v", body)
	}
}

func TestMeiliDeleteDocumentUsesDocumentEndpoint(t *testing.T) {
	var method string
	var requestPath string
	originalClient := meiliHTTPClient
	meiliHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		method = req.Method
		requestPath = req.URL.Path
		return &http.Response{
			StatusCode: http.StatusAccepted,
			Status:     "202 Accepted",
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"taskUid":2}`)),
		}, nil
	})}
	defer func() { meiliHTTPClient = originalClient }()

	server := &Server{cfg: config.Config{MeiliEnabled: true, MeiliHost: "http://meili.test", MeiliPrefix: "test_"}}
	if err := server.meiliDeleteDocument(t.Context(), "statuses", 42); err != nil {
		t.Fatalf("meiliDeleteDocument returned error: %v", err)
	}
	if method != http.MethodDelete || requestPath != "/indexes/test_statuses/documents/42" {
		t.Fatalf("request = %s %s", method, requestPath)
	}
}

func TestMeiliDeleteAllDocumentsUsesIndexDocumentsEndpoint(t *testing.T) {
	var method string
	var requestPath string
	originalClient := meiliHTTPClient
	meiliHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		method = req.Method
		requestPath = req.URL.Path
		return &http.Response{
			StatusCode: http.StatusAccepted,
			Status:     "202 Accepted",
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"taskUid":3}`)),
		}, nil
	})}
	defer func() { meiliHTTPClient = originalClient }()

	server := &Server{cfg: config.Config{MeiliEnabled: true, MeiliHost: "http://meili.test", MeiliPrefix: "test_"}}
	if err := server.meiliDeleteAllDocuments(t.Context(), "accounts"); err != nil {
		t.Fatalf("meiliDeleteAllDocuments returned error: %v", err)
	}
	if method != http.MethodDelete || requestPath != "/indexes/test_accounts/documents" {
		t.Fatalf("request = %s %s", method, requestPath)
	}
}

func TestSyncMeiliIndexesConfiguresRailsCompatibleSettings(t *testing.T) {
	type requestRecord struct {
		Method string
		Path   string
		Body   map[string]any
	}
	var requests []requestRecord
	originalClient := meiliHTTPClient
	meiliHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		record := requestRecord{Method: req.Method, Path: req.URL.Path}
		if req.Body != nil {
			if err := json.NewDecoder(req.Body).Decode(&record.Body); err != nil && err != io.EOF {
				t.Fatalf("decode request: %v", err)
			}
		}
		requests = append(requests, record)
		if req.Method == http.MethodPost && req.URL.Path == "/indexes" {
			statusCode := http.StatusAccepted
			status := "202 Accepted"
			if record.Body["uid"] == "test_statuses" {
				statusCode = http.StatusConflict
				status = "409 Conflict"
			}
			return &http.Response{
				StatusCode: statusCode,
				Status:     status,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"taskUid":1}`)),
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusAccepted,
			Status:     "202 Accepted",
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"taskUid":2}`)),
		}, nil
	})}
	defer func() { meiliHTTPClient = originalClient }()

	server := &Server{cfg: config.Config{MeiliEnabled: true, MeiliHost: "http://meili.test", MeiliPrefix: "test_"}}
	if err := server.syncMeiliIndexes(t.Context()); err != nil {
		t.Fatalf("syncMeiliIndexes returned error: %v", err)
	}

	if len(requests) != 8 {
		t.Fatalf("requests = %#v", requests)
	}
	if requests[0].Method != http.MethodPost || requests[0].Path != "/indexes" || requests[0].Body["uid"] != "test_accounts" || requests[0].Body["primaryKey"] != "id" {
		t.Fatalf("account create request = %#v", requests[0])
	}
	settings := map[string]map[string]any{}
	for _, request := range requests {
		if request.Method == http.MethodPatch && strings.HasSuffix(request.Path, "/settings") {
			settings[request.Path] = request.Body
		}
	}
	statusSettings := settings["/indexes/test_statuses/settings"]
	for _, want := range []string{"text", "tags"} {
		if !jsonArrayContainsString(statusSettings["searchableAttributes"], want) {
			t.Fatalf("status searchableAttributes missing %q: %#v", want, statusSettings)
		}
	}
	for _, want := range []string{"visibility", "searchable_by", "created_at_timestamp"} {
		if !jsonArrayContainsString(statusSettings["filterableAttributes"], want) {
			t.Fatalf("status filterableAttributes missing %q: %#v", want, statusSettings)
		}
	}
	for _, want := range []string{"created_at_timestamp", "favourites_count"} {
		if !jsonArrayContainsString(statusSettings["sortableAttributes"], want) {
			t.Fatalf("status sortableAttributes missing %q: %#v", want, statusSettings)
		}
	}
	if !jsonArrayContainsString(statusSettings["rankingRules"], "created_at_timestamp:desc") {
		t.Fatalf("status rankingRules = %#v", statusSettings["rankingRules"])
	}

	accountSettings := settings["/indexes/test_accounts/settings"]
	if !jsonArrayContainsString(accountSettings["filterableAttributes"], "discoverable") || !jsonArrayContainsString(accountSettings["rankingRules"], "followers_count:desc") {
		t.Fatalf("account settings = %#v", accountSettings)
	}
	tagSettings := settings["/indexes/test_tags/settings"]
	if !jsonArrayContainsString(tagSettings["filterableAttributes"], "trendable") || !jsonArrayContainsString(tagSettings["rankingRules"], "usage:desc") {
		t.Fatalf("tag settings = %#v", tagSettings)
	}
	instanceSettings := settings["/indexes/test_instances/settings"]
	if !jsonArrayContainsString(instanceSettings["searchableAttributes"], "domain") || !jsonArrayContainsString(instanceSettings["sortableAttributes"], "accounts_count") {
		t.Fatalf("instance settings = %#v", instanceSettings)
	}
}

func TestMeiliIndexSyncIsStarted(t *testing.T) {
	src, err := os.ReadFile("activitypub_retry.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, src, "StartBackgroundWorkers", `s.runSchedulerWithRedisLock(ctx, "meili_index_definition_scheduler"`) ||
		!functionBodyContains(t, src, "StartBackgroundWorkers", "s.syncMeiliIndexesBestEffort(ctx)") {
		t.Fatal("StartBackgroundWorkers does not start Meilisearch index sync")
	}
}

func TestMeiliPrivateStatusSearchableByRefreshHooks(t *testing.T) {
	checks := map[string]map[string]string{
		"relationships.go": {
			"followAccount":       `s.meiliReindexPrivateStatusesForAccountsBestEffort(c.Request().Context(), target.ID)`,
			"unfollowAccount":     `s.meiliReindexPrivateStatusesForAccountsBestEffort(c.Request().Context(), target.ID)`,
			"removeFromFollowers": `s.meiliReindexPrivateStatusesForAccountsBestEffort(c.Request().Context(), account.ID)`,
			"blockAccount":        `s.meiliReindexPrivateStatusesForAccountsBestEffort(c.Request().Context(), account.ID, target.ID)`,
		},
		"follow_requests.go": {
			"authorizeFollowRequest": `s.meiliReindexPrivateStatusesForAccountsBestEffort(c.Request().Context(), account.ID)`,
		},
		"activitypub_inbox.go": {
			"processActivityPubFollow":                  `s.meiliReindexPrivateStatusesForAccountsBestEffort(context.Background(), target.ID)`,
			"processActivityPubUndoFollowWithTombstone": `s.meiliReindexPrivateStatusesForAccountsBestEffort(context.Background(), target.ID)`,
			"processActivityPubUndoBlockWithTombstone":  `s.meiliReindexPrivateStatusesForAccountsBestEffort(context.Background(), actor.ID, target.ID)`,
		},
		"web_relationships.go": {
			"applyRelationshipBatch": `s.meiliReindexPrivateStatusesForAccountsBestEffort(ctx, targetIDs...)`,
		},
		"imports.go": {
			"processRelationshipImport": `s.meiliReindexPrivateStatusesForAccountsBestEffort(context.Background(), targetID)`,
			"processListImportRow":      `s.meiliReindexPrivateStatusesForAccountsBestEffort(context.Background(), affectedFollowTargetID)`,
		},
		"domain_blocks.go": {
			"runAfterAccountDomainBlockEffects": `s.meiliReindexPrivateStatusesForAccountsBestEffort(ctx, append(cleanup.PrivateStatusAccountIDs, accountID)...)`,
		},
		"meili_index.go": {
			"meiliReindexPrivateStatusesForAccountBestEffort": `Where("account_id = ? AND visibility = ? AND deleted_at IS NULL AND id > ?", accountID, 2, lastID)`,
		},
	}
	for file, bodyChecks := range checks {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for fn, want := range bodyChecks {
			if !functionBodyContains(t, src, fn, want) {
				t.Fatalf("%s:%s does not contain %q", file, fn, want)
			}
		}
	}
}

func TestMeiliLibraryOnlyStatusIndexingUsesLocalInteractionScope(t *testing.T) {
	src, err := os.ReadFile("meili_index.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`if !s.cfg.MeiliLibraryOnly`,
		`return s.meiliRemoteStatusHasLocalInteraction(ctx, status.ID)`,
		`COALESCE(statuses.local, statuses.uri IS NULL) = TRUE`,
		`FROM favourites`,
		`FROM bookmarks`,
		`FROM statuses reblogs`,
		`FROM mentions`,
		`favourite_accounts.domain IS NULL OR favourite_accounts.domain = ''`,
		`bookmark_accounts.domain IS NULL OR bookmark_accounts.domain = ''`,
		`reblog_accounts.domain IS NULL OR reblog_accounts.domain = ''`,
		`mention_accounts.domain IS NULL OR mention_accounts.domain = ''`,
		`Where(meiliLibraryOnlyStatusSQL())`,
	} {
		if !strings.Contains(string(src), want) {
			t.Fatalf("meili_index.go missing library-only status indexing fragment %q", want)
		}
	}

	deploySrc, err := os.ReadFile("meili_deploy.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, deploySrc, "meiliStatusDeployQuery", `if s.cfg.MeiliLibraryOnly`) ||
		!functionBodyContains(t, deploySrc, "meiliStatusDeployQuery", `query = query.Where(meiliLibraryOnlyStatusSQL())`) {
		t.Fatal("meiliStatusDeployQuery must apply MEILI_LIBRARY_ONLY during full index rebuilds")
	}
}

func TestMeiliLibraryOnlyInteractionHooksRefreshTargetStatus(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	checks := map[string][]string{
		"reblogStatus": {
			`s.meiliIndexStatusBestEffort(c.Request().Context(), target.ID)`,
		},
		"unreblogStatus": {
			`s.meiliIndexStatusBestEffort(c.Request().Context(), target.ID)`,
		},
		"toggleStatusJoin": {
			`if (table == "favourites" && favourite != nil) || (table == "bookmarks" && bookmark != nil)`,
			`s.meiliIndexStatusBestEffort(c.Request().Context(), joinStatus.ID)`,
			`} else if changed {`,
			`s.meiliIndexStatusBestEffort(c.Request().Context(), status.ID)`,
		},
	}
	for fn, wants := range checks {
		for _, want := range wants {
			if !functionBodyContains(t, src, fn, want) {
				t.Fatalf("server.go:%s missing library-only Meili refresh hook %q", fn, want)
			}
		}
	}
}

func TestMeiliActivityPubStatusIndexHooks(t *testing.T) {
	src, err := os.ReadFile("activitypub_inbox.go")
	if err != nil {
		t.Fatal(err)
	}
	checks := map[string]string{
		"processActivityPubCreateNote":        `s.meiliIndexTagsBestEffort(context.Background(), affectedTagIDs)`,
		"processActivityPubUpdate":            `s.meiliIndexTagsBestEffort(context.Background(), affectedTagIDs)`,
		"processActivityPubDeleteWithContext": `s.meiliIndexTagsBestEffort(context.Background(), affectedTagIDs)`,
		"activityPubStatusTagIDs":             `tx.Table("statuses_tags").Select("tag_id AS id").Where("status_id = ?", statusID).Find(&rows).Error`,
	}
	for fn, want := range checks {
		if !functionBodyContains(t, src, fn, want) {
			t.Fatalf("activitypub_inbox.go:%s does not contain %q", fn, want)
		}
	}
	for fn, want := range map[string]string{
		"processActivityPubCreateNote":        `s.meiliIndexStatusBestEffort(context.Background(), createdStatusID)`,
		"processActivityPubUpdate":            `s.meiliIndexStatusBestEffort(context.Background(), status.ID)`,
		"processActivityPubDeleteWithContext": `s.meiliDeleteStatusBestEffort(context.Background(), status.ID)`,
	} {
		if !functionBodyContains(t, src, fn, want) {
			t.Fatalf("activitypub_inbox.go:%s does not contain %q", fn, want)
		}
	}
}

func TestMeiliActivityPubActorIndexHooks(t *testing.T) {
	src, err := os.ReadFile("activitypub_inbox.go")
	if err != nil {
		t.Fatal(err)
	}
	checks := map[string]string{
		"updateActivityPubActor":              `s.meiliIndexAccountBestEffort(context.Background(), actor.ID)`,
		"processActivityPubDeleteWithContext": `s.meiliIndexAccountBestEffort(ctx, actor.ID)`,
	}
	for fn, want := range checks {
		if !functionBodyContains(t, src, fn, want) {
			t.Fatalf("activitypub_inbox.go:%s does not contain %q", fn, want)
		}
	}
}

func jsonArrayContainsString(value any, want string) bool {
	items, ok := value.([]any)
	if !ok {
		return false
	}
	for _, item := range items {
		if got, ok := item.(string); ok && got == want {
			return true
		}
	}
	return false
}

func TestMeiliStatusDocumentMatchesRailsSearchFields(t *testing.T) {
	server := &Server{}
	status := models.Status{
		ID:          42,
		AccountID:   7,
		Text:        "<p>Hello <strong>world</strong></p>",
		SpoilerText: "CW",
		Visibility:  0,
		Sensitive:   true,
		CreatedAt:   time.Unix(1781740800, 0).UTC(),
		Language:    sql.NullString{String: "ja", Valid: true},
		InReplyToID: sql.NullInt64{Int64: 11, Valid: true},
		StatusStat:  models.StatusStat{FavouritesCount: 3, ReblogsCount: 2, RepliesCount: 1},
		Tags:        []models.Tag{{Name: "go"}, {Name: "mastodon"}},
		MediaAttachments: []models.MediaAttachment{
			{Type: 0},
			{Type: 2},
		},
		PreviewCards: []models.PreviewCard{{Type: 2}},
		Poll:         &models.Poll{Options: models.StringArray{"yes", "no"}},
	}
	doc := server.meiliStatusDocument(status)
	if doc.ID != 42 || doc.AccountID != 7 || doc.Visibility != "public" || !doc.Sensitive {
		t.Fatalf("doc core fields = %#v", doc)
	}
	if doc.Language == nil || *doc.Language != "ja" || doc.InReplyToID == nil || *doc.InReplyToID != 11 {
		t.Fatalf("doc nullable fields = %#v", doc)
	}
	for _, want := range []string{"Hello", "world", "CW", "yes", "no"} {
		if !strings.Contains(doc.Text, want) {
			t.Fatalf("doc text %q missing %q", doc.Text, want)
		}
	}
	if strings.Contains(doc.Text, "<strong>") {
		t.Fatalf("doc text kept HTML: %q", doc.Text)
	}
	if !doc.HasMedia || !doc.HasImage || !doc.HasVideo || !doc.HasPoll || !doc.HasLink || !doc.HasEmbed || !doc.IsReply {
		t.Fatalf("doc booleans = %#v", doc)
	}
	if doc.CreatedAtTimestamp != 1781740800 || doc.FavouritesCount != 3 || doc.ReblogsCount != 2 || doc.RepliesCount != 1 {
		t.Fatalf("doc counters = %#v", doc)
	}
	if strings.Join(doc.Tags, ",") != "go,mastodon" {
		t.Fatalf("doc tags = %#v", doc.Tags)
	}
}

func TestMeiliDirectStatusDocumentSearchableByMentionedAccounts(t *testing.T) {
	server := &Server{}
	status := models.Status{
		ID:         42,
		AccountID:  7,
		Text:       "secret",
		Visibility: 3,
		CreatedAt:  time.Unix(1781740800, 0).UTC(),
		Mentions:   []models.Mention{{AccountID: models.MentionAccountID(9)}, {AccountID: models.MentionAccountID(9)}, {AccountID: models.MentionAccountID(10)}},
	}
	doc := server.meiliStatusDocument(status)
	if doc.Visibility != "direct" {
		t.Fatalf("visibility = %q", doc.Visibility)
	}
	if strings.Join(searchTestInt64Strings(doc.SearchableBy), ",") != "9,10,7" {
		t.Fatalf("searchable_by = %#v", doc.SearchableBy)
	}
}

func TestMeiliAccountDocumentMatchesRailsSearchFields(t *testing.T) {
	server := &Server{}
	account := models.Account{
		ID:          7,
		Username:    "alice",
		DisplayName: "Alice",
		Domain:      sql.NullString{String: "remote.example", Valid: true},
		Note:        "<p>Hello <em>profile</em></p>",
		Locked:      true,
		Discoverable: sql.NullBool{
			Bool:  true,
			Valid: true,
		},
		Indexable: true,
		ActorType: sql.NullString{String: "Service", Valid: true},
		CreatedAt: time.Unix(1781740800, 0).UTC(),
		AccountStat: models.AccountStat{
			FollowersCount: 12,
			FollowingCount: 5,
			StatusesCount:  33,
			LastStatusAt:   sql.NullTime{Time: time.Unix(1781827200, 0).UTC(), Valid: true},
		},
	}
	doc := server.meiliAccountDocument(account)
	if doc.ID != 7 || doc.Username != "alice@remote.example" || doc.DisplayName != "Alice" {
		t.Fatalf("doc core fields = %#v", doc)
	}
	if doc.Domain == nil || *doc.Domain != "remote.example" {
		t.Fatalf("doc domain = %#v", doc.Domain)
	}
	if !doc.Bot || !doc.Locked || !doc.Discoverable || !doc.Indexable {
		t.Fatalf("doc booleans = %#v", doc)
	}
	if strings.Contains(doc.Text, "<em>") || !strings.Contains(doc.Text, "Hello") || !strings.Contains(doc.Text, "profile") {
		t.Fatalf("doc text = %q", doc.Text)
	}
	if doc.FollowersCount != 12 || doc.FollowingCount != 5 || doc.StatusesCount != 33 || doc.LastStatusAt != 1781827200 || doc.CreatedAtTimestamp != 1781740800 {
		t.Fatalf("doc counters = %#v", doc)
	}
}

func TestMeiliAccountSearchableMatchesRailsGuard(t *testing.T) {
	discoverable := models.Account{Discoverable: sql.NullBool{Bool: true, Valid: true}}
	if !meiliAccountSearchable(discoverable) {
		t.Fatal("discoverable unsuspended account should be searchable")
	}
	if meiliAccountSearchable(models.Account{}) {
		t.Fatal("non-discoverable account should not be searchable")
	}
	if meiliAccountSearchable(models.Account{Discoverable: sql.NullBool{Bool: true, Valid: true}, SuspendedAt: sql.NullTime{Time: time.Now(), Valid: true}}) {
		t.Fatal("suspended account should not be searchable")
	}
	if meiliAccountSearchable(models.Account{Discoverable: sql.NullBool{Bool: true, Valid: true}, MovedToAccountID: sql.NullInt64{Int64: 9, Valid: true}}) {
		t.Fatal("moved account should not be searchable")
	}
}

func TestMeiliInstanceDocumentMatchesRailsSearchFields(t *testing.T) {
	instance := models.Instance{Domain: "remote.example", AccountsCount: 42}
	document := meiliInstanceDocument{ID: instance.Domain, Domain: instance.Domain, AccountsCount: instance.AccountsCount}
	if document.ID != "remote.example" || document.Domain != "remote.example" || document.AccountsCount != 42 {
		t.Fatalf("document = %#v", document)
	}
	if !meiliInstanceSearchable(instance) {
		t.Fatal("instance with domain should be searchable")
	}
	if meiliInstanceSearchable(models.Instance{}) {
		t.Fatal("blank instance domain should not be searchable")
	}
}

func TestMeiliUnsearchableStatusBestEffortDeletesStaleDocument(t *testing.T) {
	src, err := os.ReadFile("meili_index.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`if !s.meiliStatusIndexable(ctx, *status)`,
		`_ = s.meiliDeleteDocument(ctx, "statuses", statusID)`,
		`return`,
	} {
		if !functionBodyContains(t, src, "meiliIndexStatusBestEffort", want) {
			t.Fatalf("meiliIndexStatusBestEffort must delete stale private/direct documents; missing %q", want)
		}
	}
	if functionBodyContains(t, src, "meiliStatusSearchable", `status.Visibility >= 0 && status.Visibility <= 3`) {
		t.Fatal("private/direct statuses must not remain searchable via broad visibility range")
	}
}

func searchTestInt64Strings(values []int64) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, strconv.FormatInt(value, 10))
	}
	return out
}
