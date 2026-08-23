package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
)

func TestAdminMetricsRequireAdminRead(t *testing.T) {
	for name, handler := range map[string]func(*echo.Context) error{
		"measures":   (&Server{}).adminMeasures,
		"dimensions": (&Server{}).adminDimensions,
		"retention":  (&Server{}).adminRetention,
	} {
		req := httptest.NewRequest("POST", "/api/v1/admin/"+name, strings.NewReader(`{"keys":["new_users"]}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		c := echo.NewContext(req, rec, echo.New())

		if err := handler(c); err == nil {
			t.Fatalf("%s: expected authentication error", name)
		} else if apiErr, ok := err.(apiHTTPError); !ok || apiErr.status != http.StatusUnauthorized {
			t.Fatalf("%s: error = %#v", name, err)
		}
	}
}

func TestParseAdminMetricsPayloadReadsNestedParams(t *testing.T) {
	req := httptest.NewRequest("POST", "/api/v1/admin/measures", strings.NewReader(`{
		"keys":["instance_accounts"],
		"start_at":"2026-06-01",
		"end_at":"2026-06-18",
		"instance_accounts":{"domain":"remote.example","include_subdomains":true}
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())

	payload, err := parseAdminMetricsPayload(c)
	if err != nil {
		t.Fatal(err)
	}
	if len(payload.Keys) != 1 || payload.Keys[0] != "instance_accounts" {
		t.Fatalf("keys = %#v", payload.Keys)
	}
	param := payload.Params["instance_accounts"]
	if param.Domain != "remote.example" || !param.IncludeSubdomains {
		t.Fatalf("params = %#v", param)
	}
}

func TestParseAdminMetricsPayloadAcceptsFormNestedParams(t *testing.T) {
	body := "keys%5B%5D=instance_accounts&keys=software_versions%2Cservers&keys=servers&keys=tag_servers&keys=instance_languages&start_at=2026-06-01&end_at=2026-06-18&limit=8&instance_accounts%5Bdomain%5D=remote.example&instance_accounts%5Binclude_subdomains%5D=1&params%5Bservers%5D%5Bid%5D=42&tag_servers%5Bid%5D=99&instance_languages%5Bdomain%5D=remote.example"
	req := httptest.NewRequest("POST", "/api/v1/admin/dimensions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())

	payload, err := parseAdminMetricsPayload(c)
	if err != nil {
		t.Fatal(err)
	}
	wantKeys := []string{"instance_accounts", "software_versions,servers", "servers", "tag_servers", "instance_languages"}
	if strings.Join(payload.Keys, ",") != strings.Join(wantKeys, ",") {
		t.Fatalf("keys = %#v, want %#v", payload.Keys, wantKeys)
	}
	if payload.StartAt != "2026-06-01" || payload.EndAt != "2026-06-18" || payload.Limit != 8 {
		t.Fatalf("payload = %#v", payload)
	}
	accountParam := payload.Params["instance_accounts"]
	if accountParam.Domain != "remote.example" || !accountParam.IncludeSubdomains {
		t.Fatalf("instance_accounts params = %#v", accountParam)
	}
	if payload.Params["servers"].ID != "42" {
		t.Fatalf("servers params = %#v", payload.Params["servers"])
	}
	if payload.Params["tag_servers"].ID != "99" {
		t.Fatalf("tag_servers params = %#v", payload.Params["tag_servers"])
	}
	if payload.Params["instance_languages"].Domain != "remote.example" {
		t.Fatalf("instance_languages params = %#v", payload.Params["instance_languages"])
	}
}

func TestAdminMetricsRangeIncludesEndDay(t *testing.T) {
	start, end := adminMetricsRange("2026-06-01", "2026-06-02")
	days := metricDays(start, end)
	if len(days) != 2 {
		t.Fatalf("days = %#v", days)
	}
	if !days[0].Equal(time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("first day = %s", days[0])
	}
}

func TestAdminMetricsMastodon4421RequiredParameters(t *testing.T) {
	tests := []struct {
		name        string
		payload     adminMetricsPayload
		requireKeys bool
		want        string
	}{
		{name: "measures keys first", payload: adminMetricsPayload{}, requireKeys: true, want: "keys"},
		{name: "measures start", payload: adminMetricsPayload{Keys: []string{"new_users"}}, requireKeys: true, want: "start_at"},
		{name: "measures end", payload: adminMetricsPayload{Keys: []string{"new_users"}, StartAt: "2026-01-01"}, requireKeys: true, want: "end_at"},
		{name: "measures complete", payload: adminMetricsPayload{Keys: []string{"new_users"}, StartAt: "2026-01-01", EndAt: "2026-07-01"}, requireKeys: true},
		{name: "retention does not require keys", payload: adminMetricsPayload{StartAt: "2026-01-01", EndAt: "2026-07-01"}, want: ""},
		{name: "retention start", payload: adminMetricsPayload{EndAt: "2026-07-01"}, want: "start_at"},
		{name: "retention blank end", payload: adminMetricsPayload{StartAt: "2026-01-01", EndAt: "  "}, want: "end_at"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := adminMetricsMissingRequiredParameter(test.payload, test.requireKeys); got != test.want {
				t.Fatalf("missing parameter = %q, want %q", got, test.want)
			}
		})
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/measures", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())
	err := adminMetricsRequiredParameterError(c, "keys")
	apiErr, ok := err.(apiHTTPError)
	if !ok || apiErr.status != http.StatusBadRequest || apiErr.message != "param is missing or the value is empty: keys" {
		t.Fatalf("required parameter error = %#v", err)
	}
}

func TestAdminMetricsMastodon4421RangeLimits(t *testing.T) {
	start, end := adminMetricsRange("2020-01-01", "2026-06-18")
	wantTwoYears := time.Date(2024, 6, 18, 0, 0, 0, 0, time.UTC)
	if got := adminMetricsTwoYearStart(start, end); !got.Equal(wantTwoYears) {
		t.Fatalf("two-year measure start = %s, want %s", got, wantTwoYears)
	}
	recent := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	if got := adminMetricsTwoYearStart(recent, end); !got.Equal(recent) {
		t.Fatalf("recent measure start = %s, want unchanged %s", got, recent)
	}

	wantDaily := time.Date(2026, 5, 18, 0, 0, 0, 0, time.UTC)
	if got := adminRetentionMaximumStart(start, end, "day"); !got.Equal(wantDaily) {
		t.Fatalf("daily retention start = %s, want %s", got, wantDaily)
	}
	wantMonthly := time.Date(2025, 6, 18, 0, 0, 0, 0, time.UTC)
	if got := adminRetentionMaximumStart(start, end, "month"); !got.Equal(wantMonthly) {
		t.Fatalf("monthly retention start = %s, want %s", got, wantMonthly)
	}
}

func TestAdminRedisMeasuresReturnReactCompatibleZeroSeriesWithoutDB(t *testing.T) {
	start, end := adminMetricsRange("2026-06-01", "2026-06-02")
	for _, key := range []string{"active_users", "interactions"} {
		measure := (&Server{}).adminMeasure(key, start, end, adminMetricKeyParam{})
		if measure.Key != key || measure.Total != "0" || measure.PreviousTotal == nil || *measure.PreviousTotal != "0" {
			t.Fatalf("%s measure = %#v", key, measure)
		}
		if len(measure.Data) != 2 {
			t.Fatalf("%s data = %#v", key, measure.Data)
		}
		for _, item := range measure.Data {
			if item.Value != "0" {
				t.Fatalf("%s data item = %#v", key, item)
			}
		}
	}
}

func TestAdminMetricsCacheKeysMirrorRailsFamilies(t *testing.T) {
	if adminMetricsCacheTTL != 5*time.Minute {
		t.Fatalf("adminMetricsCacheTTL = %s", adminMetricsCacheTTL)
	}
	start := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC)
	params := adminMetricKeyParam{Domain: "remote.example", ID: "42", IncludeSubdomains: true}

	measureKey := adminMeasureCacheKey("tag_uses", start, end, params)
	if !strings.HasPrefix(measureKey, "metrics/measure/tag_uses;") || !strings.Contains(measureKey, ";domain=remote.example;id=42;include_subdomains=true") {
		t.Fatalf("measure cache key = %q", measureKey)
	}
	dimensionKey := adminDimensionCacheKey("tag_servers", start, end, 20, params)
	if !strings.HasPrefix(dimensionKey, "metrics/dimension/tag_servers;") || !strings.Contains(dimensionKey, ";20;domain=remote.example;id=42;include_subdomains=true") {
		t.Fatalf("dimension cache key = %q", dimensionKey)
	}
	retentionKey := adminRetentionCacheKey(start, "2026-06-08", "day")
	if retentionKey != "metrics/retention;2026-06-01;2026-06-08;day" {
		t.Fatalf("retention cache key = %q", retentionKey)
	}
}

func TestAdminMetricsHandlersUseRailsFiveMinuteCacheBoundary(t *testing.T) {
	src, err := os.ReadFile("admin_metrics.go")
	if err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		function string
		want     string
	}{
		{"adminMeasures", `s.cachedAdminMeasure(key, start, end, payload.Params[key])`},
		{"adminDimensions", `s.cachedAdminDimension(key, payload.Limit, payload.Params[key], start, end)`},
		{"adminRetention", `s.cachedAdminRetentionCohorts(start, payload.EndAt, frequency)`},
		{"adminMetricsCacheRead", `"GET", redisConfig(s.cfg).prefix+cacheKey`},
		{"adminMetricsCacheWrite", `"SETEX", redisConfig(s.cfg).prefix+cacheKey`},
		{"adminMetricsCacheWrite", `strconv.FormatInt(int64(adminMetricsCacheTTL/time.Second), 10)`},
	}
	for _, check := range checks {
		if !functionBodyContains(t, src, check.function, check.want) {
			t.Fatalf("admin_metrics.go:%s missing %q", check.function, check.want)
		}
	}
}

func TestAdminMetricsPrewarmWorkerUsesDashboardShape(t *testing.T) {
	workerSrc, err := os.ReadFile("admin_metrics_worker.go")
	if err != nil {
		t.Fatal(err)
	}
	startupSrc, err := os.ReadFile("activitypub_retry.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`const adminMetricsPrewarmWorkerInterval = adminMetricsCacheTTL`,
		`"new_users"`,
		`"active_users"`,
		`"interactions"`,
		`"opened_reports"`,
		`"resolved_reports"`,
		`{Key: "sources", Limit: 8}`,
		`{Key: "languages", Limit: 8}`,
		`{Key: "servers", Limit: 8}`,
		`{Key: "software_versions", Limit: 4}`,
		`{Key: "space_usage", Limit: 3}`,
		`s.cachedAdminMeasure(key, start, end, adminMetricKeyParam{})`,
		`s.cachedAdminDimension(dimension.Key, dimension.Limit, adminMetricKeyParam{}, start, end)`,
		`s.cachedAdminRetentionCohorts(retentionStart, endValue, "month")`,
	} {
		if !sourceContains(workerSrc, want) {
			t.Fatalf("admin_metrics_worker.go missing %q", want)
		}
	}
	if !functionBodyContains(t, startupSrc, "StartBackgroundWorkers", "workers.Go(ctx, s.runAdminMetricsPrewarmWorker)") {
		t.Fatal("StartBackgroundWorkers does not start admin metrics prewarm worker")
	}
}

func TestAdminInstanceMediaAttachmentsMeasureMatchesRailsSerializerShape(t *testing.T) {
	start, end := adminMetricsRange("2026-06-01", "2026-06-02")
	measure := (&Server{}).adminMeasure("instance_media_attachments", start, end, adminMetricKeyParam{})
	if measure.Unit == nil || *measure.Unit != "bytes" {
		t.Fatalf("unit = %#v", measure.Unit)
	}
	if measure.HumanValue == nil || *measure.HumanValue != "0 Bytes" {
		t.Fatalf("human value = %#v", measure.HumanValue)
	}
	if measure.PreviousTotal != nil {
		t.Fatalf("previous_total should be omitted for Rails total_in_time_range=false measure: %#v", measure.PreviousTotal)
	}
	body, err := json.Marshal(measure)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["previous_total"]; ok {
		t.Fatalf("previous_total should be omitted: %s", body)
	}
	if raw["unit"] != "bytes" || raw["human_value"] != "0 Bytes" {
		t.Fatalf("media measure json = %#v", raw)
	}
}

func TestAdminInstanceMeasuresOmitPreviousTotalLikeRailsTotalInTimeRange(t *testing.T) {
	for _, key := range []string{"instance_accounts", "instance_follows", "instance_followers", "instance_statuses", "instance_reports", "instance_media_attachments"} {
		if adminMeasureTotalInTimeRange(key) {
			t.Fatalf("%s should not include previous_total", key)
		}
	}
	for _, key := range []string{"active_users", "interactions", "new_users", "opened_reports", "resolved_reports", "tag_accounts", "tag_uses", "tag_servers"} {
		if !adminMeasureTotalInTimeRange(key) {
			t.Fatalf("%s should include previous_total", key)
		}
	}
}

func TestAdminInstanceMediaAttachmentsDailyTotalUsesRailsByteSum(t *testing.T) {
	src, err := os.ReadFile("admin_metrics.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`case "instance_media_attachments":`,
		`COALESCE(SUM(COALESCE(file_file_size, 0) + COALESCE(thumbnail_file_size, 0)), 0)`,
		`Where("media_attachments.created_at >= ? AND media_attachments.created_at < ?", day, next)`,
	} {
		if !functionBodyContains(t, src, "adminMeasureDailyTotal", want) {
			t.Fatalf("admin_metrics.go:adminMeasureDailyTotal does not contain %q", want)
		}
	}
}

func TestMastodon4515AdminMediaStorageMetricMatchesOfficialSources(t *testing.T) {
	want := [...]adminMediaStorageSource{
		{table: "media_attachments", expression: "COALESCE(file_file_size, 0) + COALESCE(thumbnail_file_size, 0)"},
		{table: "custom_emojis", expression: "COALESCE(image_file_size, 0)"},
		{table: "preview_cards", expression: "COALESCE(image_file_size, 0)"},
		{table: "accounts", expression: "COALESCE(avatar_file_size, 0) + COALESCE(header_file_size, 0)"},
		{table: "backups", expression: "COALESCE(dump_file_size, 0)"},
		{table: "site_uploads", expression: "COALESCE(file_file_size, 0)"},
	}
	if len(adminMediaStorageSources) != len(want) {
		t.Fatalf("media storage sources = %#v, want %#v", adminMediaStorageSources, want)
	}
	for index := range want {
		if adminMediaStorageSources[index] != want[index] {
			t.Fatalf("media storage source %d = %#v, want %#v", index, adminMediaStorageSources[index], want[index])
		}
	}
	for _, source := range adminMediaStorageSources {
		if source.table == "imports" || source.table == "bulk_imports" {
			t.Fatalf("database-backed import state must not be counted as attached media: %#v", source)
		}
	}
}

func TestAdminDashboardDimensionsUseRailsTimeAndStatusScopes(t *testing.T) {
	src, err := os.ReadFile("admin_metrics.go")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		function string
		want     []string
	}{
		{
			function: "adminLanguagesDimension",
			want: []string{
				`users.current_sign_in_at >= ? AND users.current_sign_in_at < ?`,
				`users.locale IS NOT NULL`,
			},
		},
		{
			function: "adminSourcesDimension",
			want: []string{
				`LEFT JOIN oauth_applications ON oauth_applications.id = users.created_by_application_id`,
				`users.created_at >= ? AND users.created_at < ?`,
			},
		},
		{
			function: "adminServersDimension",
			want: []string{
				`"statuses"`,
				`JOIN accounts ON accounts.id = statuses.account_id`,
				`statuses.id BETWEEN ? AND ?`,
			},
		},
		{
			function: "adminTagStatusScope",
			want: []string{
				`JOIN statuses_tags ON statuses_tags.status_id = statuses.id`,
				`statuses.id BETWEEN ? AND ?`,
			},
		},
		{
			function: "adminInstanceLanguagesDimension",
			want: []string{
				`accounts.domain = ?`,
				`statuses.reblog_of_id IS NULL`,
				`statuses.id BETWEEN ? AND ?`,
			},
		},
	}
	for _, tt := range cases {
		for _, want := range tt.want {
			if !functionBodyContains(t, src, tt.function, want) {
				t.Fatalf("admin_metrics.go:%s does not contain %q", tt.function, want)
			}
		}
	}
}

func TestAdminSoftwareVersionsDimensionUsesRailsCompatibleKeys(t *testing.T) {
	versions := (&Server{cfg: config.Config{Version: "1.2.3"}}).adminSoftwareVersionsDimension()
	if len(versions) == 0 {
		t.Fatal("software_versions dimension returned no data")
	}
	first := versions[0]
	if first.Key != "mastodon" || first.HumanKey != "Paon" || first.HumanValue == nil || *first.HumanValue != first.Value {
		t.Fatalf("first software version item = %#v", first)
	}
	src, err := os.ReadFile("admin_metrics.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`adminVersionDimensionItem("postgresql", "PostgreSQL", version)`,
		`adminVersionDimensionItem("redis", name, version)`,
		`adminVersionDimensionItem("meilisearch", "Meilisearch", version)`,
		`strings.TrimRight(s.cfg.MeiliHost, "/")+"/version"`,
		`req.Header.Set("Authorization", "Bearer "+s.cfg.MeiliMasterKey)`,
		`meiliHTTPClient.Do(req)`,
	} {
		if !functionBodyContains(t, src, "adminSoftwareVersionsDimension", want) && !strings.Contains(string(src), want) {
			t.Fatalf("admin_metrics.go does not contain %q", want)
		}
	}
}

func TestAdminMeiliVersionUsesSharedMeiliClient(t *testing.T) {
	previous := meiliHTTPClient
	t.Cleanup(func() { meiliHTTPClient = previous })
	var gotAuth string
	var gotPath string
	meiliHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		gotAuth = req.Header.Get("Authorization")
		gotPath = req.URL.Path
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Body:       io.NopCloser(strings.NewReader(`{"pkgVersion":"1.12.3"}`)),
			Header:     http.Header{},
			Request:    req,
		}, nil
	})}
	got := (&Server{cfg: config.Config{MeiliEnabled: true, MeiliHost: "https://meili.example/", MeiliMasterKey: "master"}}).adminMeiliVersion()
	if got != "1.12.3" {
		t.Fatalf("meili version = %q", got)
	}
	if gotAuth != "Bearer master" || gotPath != "/version" {
		t.Fatalf("request auth/path = %q %q", gotAuth, gotPath)
	}

	meiliHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusOK,
			Status:        "200 OK",
			Body:          io.NopCloser(strings.NewReader(`{"pkgVersion":"1.12.3"}`)),
			Header:        http.Header{},
			ContentLength: maxMeiliResponseBodySize + 1,
			Request:       req,
		}, nil
	})}
	if got := (&Server{cfg: config.Config{MeiliEnabled: true, MeiliHost: "https://meili.example/"}}).adminMeiliVersion(); got != "" {
		t.Fatalf("oversized meili version = %q, want blank", got)
	}
}

func TestAdminDimensionHumanKeysMatchRailsHelpers(t *testing.T) {
	if got := adminMetricStandardLocaleName("ja"); got != "Japanese" {
		t.Fatalf("ja human key = %q", got)
	}
	if got := adminMetricStandardLocaleName(""); got != "None" {
		t.Fatalf("blank locale human key = %q", got)
	}
	if got := adminMetricStandardLocaleName("und"); got != "und" {
		t.Fatalf("unknown locale human key = %q", got)
	}
	for key, want := range map[string]string{
		"postgresql": "PostgreSQL",
		"redis":      "Redis",
		"media":      "Media storage",
		"other":      "other",
	} {
		if got := adminMetricSpaceUsageHumanKey(key); got != want {
			t.Fatalf("%s space human key = %q, want %q", key, got, want)
		}
	}

	src, err := os.ReadFile("admin_metrics.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		functionName string
		want         string
	}{
		{"adminLanguagesDimension", `adminMetricStandardLocaleName`},
		{"adminTagLanguagesDimension", `adminMetricStandardLocaleName`},
		{"adminInstanceLanguagesDimension", `adminMetricStandardLocaleName`},
		{"adminSourcesDimension", `adminT("en", "admin.dashboard.website", "Website")`},
		{"adminSpaceUsageItem", `adminMetricSpaceUsageHumanKey(key)`},
	} {
		if !functionBodyContains(t, src, check.functionName, check.want) {
			t.Fatalf("admin_metrics.go:%s missing %q", check.functionName, check.want)
		}
	}
}

func TestAdminSoftwareVersionParsers(t *testing.T) {
	if got := postgreSQLVersionFromBanner("PostgreSQL 16.3 (Debian 16.3-1.pgdg120+1)"); got != "16.3" {
		t.Fatalf("postgres version = %q", got)
	}
	if got := postgreSQLVersionFromBanner("15.7 custom"); got != "15.7" {
		t.Fatalf("postgres version without prefix = %q", got)
	}
	info := "# Server\r\nredis_version:7.2.5\r\n# Memory\r\nused_memory:123456\r\n"
	if got := redisInfoValue(info, "redis_version"); got != "7.2.5" {
		t.Fatalf("redis version = %q", got)
	}
	for _, test := range []struct {
		info        string
		wantName    string
		wantVersion string
	}{
		{"redis_version:7.2.5\r\n", "Redis", "7.2.5"},
		{"redis_version:7.2.5\r\nvalkey_version:8.1.3\r\n", "Valkey", "8.1.3"},
		{"redis_version:7.2.5\r\ndragonfly_version:1.30.0\r\n", "Dragonfly", "1.30.0"},
	} {
		name, version := redisStoreIdentity(test.info)
		if name != test.wantName || version != test.wantVersion {
			t.Fatalf("store identity = %q %q, want %q %q", name, version, test.wantName, test.wantVersion)
		}
	}
}

func TestAdminMetricStatusIDRangeUsesMastodonSnowflakeIDs(t *testing.T) {
	start := time.Date(2026, 6, 18, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 6, 19, 0, 0, 0, 0, time.UTC)
	earliest, latest := adminMetricStatusIDRange(start, end)
	if earliest != mastodonSnowflakeIDAt(start, false) || latest != mastodonSnowflakeIDAt(end, false) {
		t.Fatalf("status id range = %d..%d", earliest, latest)
	}
	earliest, latest = adminMetricAccountIDRange(start, end)
	if earliest != mastodonSnowflakeIDAt(start, false) || latest != mastodonSnowflakeIDAt(end, false) {
		t.Fatalf("account id range = %d..%d", earliest, latest)
	}

	src, err := os.ReadFile("admin_metrics.go")
	if err != nil {
		t.Fatal(err)
	}
	body := functionBody(t, src, "adminMeasureDailyTotal")
	for _, want := range []string{
		`adminMetricAccountIDRange(day, next)`,
		`Where("account_id >= ? AND account_id < ?", earliestAccountID, latestAccountID)`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("new-users measure missing Mastodon 4.5.14 query constraint %q", want)
		}
	}
}

func TestAdminRetentionCohortsBuildsReactCompatibleMatrix(t *testing.T) {
	start := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	cohorts := (&Server{}).adminRetentionCohorts(start, "2026-06-03", "day")
	if len(cohorts) != 3 {
		t.Fatalf("cohorts = %#v", cohorts)
	}
	if cohorts[0].Period != "2026-06-01T00:00:00Z" || cohorts[0].Frequency != "day" {
		t.Fatalf("first cohort = %#v", cohorts[0])
	}
	if len(cohorts[0].Data) != 3 || len(cohorts[1].Data) != 2 || len(cohorts[2].Data) != 1 {
		t.Fatalf("cohort data = %#v", cohorts)
	}
	for _, cohort := range cohorts {
		for _, item := range cohort.Data {
			if item.Value != "0" || item.Rate != 0 {
				t.Fatalf("nil DB retention item = %#v", item)
			}
		}
	}
}

func TestAdminRetentionUsesRailsStylePostgreSQLGridQuery(t *testing.T) {
	src, err := os.ReadFile("admin_metrics.go")
	if err != nil {
		t.Fatal(err)
	}
	body := functionBody(t, src, "adminRetentionCohortsPostgreSQL")
	for _, want := range []string{
		`s.db.Dialector.Name() != "postgres"`,
		`generate_series(date_trunc(?, ?::timestamp)::date, date_trunc(?, ?::timestamp)::date, ('1 ' || ?)::interval)`,
		`retention_period >= cohort_period`,
		`users.account_id >= (date_part('epoch', date_trunc(?, axis.cohort_period)::date) * 1000)::bigint << 16`,
		`users.account_id < ((date_part('epoch', date_trunc(?, axis.cohort_period)::date + ('1 ' || ?)::interval)) * 1000)::bigint << 16`,
		`date_trunc(?, users.current_sign_in_at) >= axis.retention_period`,
		`GREATEST(count(*), 1) FROM new_users`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("adminRetentionCohortsPostgreSQL missing %q", want)
		}
	}
}

func TestAdminRetentionRowsToCohortsMatchesRailsSerializerShape(t *testing.T) {
	rows := []adminRetentionRow{
		{
			CohortPeriod:    time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
			RetentionPeriod: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
			Value:           2,
			Rate:            1,
		},
		{
			CohortPeriod:    time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
			RetentionPeriod: time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC),
			Value:           1,
			Rate:            0.5,
		},
		{
			CohortPeriod:    time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC),
			RetentionPeriod: time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC),
			Value:           3,
			Rate:            1,
		},
	}
	cohorts := adminRetentionRowsToCohorts(rows, "day")
	if len(cohorts) != 2 {
		t.Fatalf("cohorts = %#v", cohorts)
	}
	if cohorts[0].Period != "2026-06-01T00:00:00Z" || cohorts[0].Frequency != "day" || len(cohorts[0].Data) != 2 {
		t.Fatalf("first cohort = %#v", cohorts[0])
	}
	if cohorts[0].Data[1].Date != "2026-06-02T00:00:00Z" || cohorts[0].Data[1].Value != "1" || cohorts[0].Data[1].Rate != 0.5 {
		t.Fatalf("second data item = %#v", cohorts[0].Data[1])
	}
	if cohorts[1].Period != "2026-06-02T00:00:00Z" || len(cohorts[1].Data) != 1 || cohorts[1].Data[0].Value != "3" {
		t.Fatalf("second cohort = %#v", cohorts[1])
	}
}

func TestMetricPeriodsSupportsMonthFrequency(t *testing.T) {
	start := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC)
	periods := metricPeriods(start, end, "month")
	if len(periods) != 3 {
		t.Fatalf("periods = %#v", periods)
	}
	if !periods[0].Equal(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)) ||
		!periods[2].Equal(time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("periods = %#v", periods)
	}
}

func TestAdminMetricIDAcceptsPositiveNumericIDs(t *testing.T) {
	if id, ok := adminMetricID(" 42 "); !ok || id != 42 {
		t.Fatalf("adminMetricID valid = %d, %v", id, ok)
	}
	for _, value := range []string{"", "0", "-1", "abc"} {
		if id, ok := adminMetricID(value); ok {
			t.Fatalf("adminMetricID(%q) = %d, true", value, id)
		}
	}
}

func TestRedisUsedMemoryFromInfoParsesMemorySection(t *testing.T) {
	info := "# Memory\r\nused_memory:123456\r\nused_memory_human:120.56K\r\n"
	if got := redisUsedMemoryFromInfo(info); got != 123456 {
		t.Fatalf("redisUsedMemoryFromInfo = %d", got)
	}
	if got := redisUsedMemoryFromInfo("used_memory:not-a-number\n"); got != 0 {
		t.Fatalf("invalid redis memory = %d", got)
	}
}

func TestHumanBytesMatchesDashboardReadableShape(t *testing.T) {
	for _, tt := range []struct {
		value int64
		want  string
	}{
		{0, "0 Bytes"},
		{1, "1 Byte"},
		{1024, "1 KB"},
		{1536, "1.5 KB"},
		{1048576, "1 MB"},
	} {
		if got := humanBytes(tt.value); got != tt.want {
			t.Fatalf("humanBytes(%d) = %q, want %q", tt.value, got, tt.want)
		}
	}
}

func TestAdminSpaceUsageItemUsesBytesUnitAndHumanValue(t *testing.T) {
	item := adminSpaceUsageItem("media", 1536, "bytes")
	if item.Key != "media" || item.HumanKey != "Media storage" || item.Value != "1536" {
		t.Fatalf("space usage item = %#v", item)
	}
	if item.Unit == nil || *item.Unit != "bytes" {
		t.Fatalf("unit = %#v", item.Unit)
	}
	if item.HumanValue == nil || *item.HumanValue != "1.5 KB" {
		t.Fatalf("human value = %#v", item.HumanValue)
	}
}

func TestTagHistoryRedisKeysMatchRailsTrendHistoryShape(t *testing.T) {
	day := time.Date(2026, 6, 18, 14, 30, 0, 0, time.UTC)
	if got, want := tagHistoryRedisKey("mastodon:", 42, day, false), "mastodon:activity:tags:42:1781740800"; got != want {
		t.Fatalf("uses key = %q, want %q", got, want)
	}
	if got, want := tagHistoryRedisKey("mastodon:", 42, day, true), "mastodon:activity:tags:42:1781740800:accounts"; got != want {
		t.Fatalf("accounts key = %q, want %q", got, want)
	}
	keys := tagHistoryRedisKeys("mastodon:", 42, time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC), time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC), false)
	want := []string{"mastodon:activity:tags:42:1781740800", "mastodon:activity:tags:42:1781827200"}
	if strings.Join(keys, ",") != strings.Join(want, ",") {
		t.Fatalf("keys = %#v, want %#v", keys, want)
	}
}

func TestAdminDashboardActivityRedisKeysMatchRailsTrackers(t *testing.T) {
	day := time.Date(2026, 6, 18, 14, 30, 0, 0, time.UTC)
	if got, want := activityTrackerDailyKey("mastodon:", "activity:logins", day), "mastodon:activity:logins:1781740800"; got != want {
		t.Fatalf("active_users daily key = %q, want %q", got, want)
	}
	if got, want := activityTrackerDailyKey("mastodon:", "activity:interactions", day), "mastodon:activity:interactions:1781740800"; got != want {
		t.Fatalf("interactions daily key = %q, want %q", got, want)
	}
	keys := activityTrackerRedisKeys("mastodon:", "activity:logins", time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC), time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC))
	wantPrefix := []string{"mastodon:activity:logins:1781740800", "mastodon:activity:logins:25"}
	for i, want := range wantPrefix {
		if keys[i] != want {
			t.Fatalf("keys = %#v, want prefix %#v", keys, wantPrefix)
		}
	}
}
