package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
)

func TestZeroDeliveryHistoriesReturnsHourlySeries(t *testing.T) {
	now := time.Date(2026, 6, 18, 12, 34, 0, 0, time.UTC)
	history := zeroDeliveryHistories(now, 3)
	if len(history) != 4 {
		t.Fatalf("len = %d", len(history))
	}
	if history[0].Time != "2026-06-18T09:00:00.000Z" || history[3].Time != "2026-06-18T12:00:00.000Z" {
		t.Fatalf("history = %#v", history)
	}
	for _, point := range history {
		if point.SuccessCount != 0 || point.FailureCount != 0 {
			t.Fatalf("point = %#v", point)
		}
	}
}

func TestDeliveryStatsRedisKeysMatchRailsKeyFormat(t *testing.T) {
	histories := []deliveryHistory{
		{Time: "2026-06-18T09:00:00.000Z"},
		{Time: "2026-06-18T10:00:00Z"},
	}
	keys := deliveryStatsRedisKeys("mastodon:", "remote.example", histories)
	want := []string{
		"mastodon:delivery_stats:remote.example:success:20260618T09",
		"mastodon:delivery_stats:remote.example:failure:20260618T09",
		"mastodon:delivery_stats:remote.example:success:20260618T10",
		"mastodon:delivery_stats:remote.example:failure:20260618T10",
	}
	if len(keys) != len(want) {
		t.Fatalf("keys = %#v", keys)
	}
	for i := range want {
		if keys[i] != want[i] {
			t.Fatalf("keys = %#v, want %#v", keys, want)
		}
	}
}

func TestApplyDeliveryStatsValuesMapsSuccessFailurePairs(t *testing.T) {
	histories := []deliveryHistory{
		{Time: "2026-06-18T09:00:00Z"},
		{Time: "2026-06-18T10:00:00Z"},
	}
	applyDeliveryStatsValues(histories, []any{"7", "2", int64(3), ""})
	if histories[0].SuccessCount != 7 || histories[0].FailureCount != 2 {
		t.Fatalf("first = %#v", histories[0])
	}
	if histories[1].SuccessCount != 3 || histories[1].FailureCount != 0 {
		t.Fatalf("second = %#v", histories[1])
	}
}

func TestNormalizeDeliveryStatsHost(t *testing.T) {
	cases := map[string]string{
		"HTTPS://Remote.Example/inbox": "remote.example",
		" @Remote.Example ":            "remote.example",
		"https://bücher.example/inbox": "xn--bcher-kva.example",
		"bücher.example":               "xn--bcher-kva.example",
		"bad domain":                   "",
	}
	for input, want := range cases {
		if got := normalizeDeliveryStatsHost(input); got != want {
			t.Fatalf("normalizeDeliveryStatsHost(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestInstanceStatsV2DisablesResponseCaching(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/api/v2/instance_stats/remote.example", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)
	c.SetPathValues(echo.PathValues{{Name: "domain", Value: "remote.example"}})
	s := &Server{cfg: config.Config{RedisHost: "127.0.0.1", RedisPort: "1"}}

	if err := s.instanceStatsV2(c); err != nil {
		t.Fatal(err)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	if got := rec.Header().Get("Vary"); got != "Authorization" {
		t.Fatalf("Vary = %q, want Authorization", got)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestNormalizePeerSearch(t *testing.T) {
	cases := map[string]string{
		"HTTPS://Remote.Example/path": "remote.example",
		" @remote.example ":           "remote.example",
		"bad domain":                  "",
		"":                            "",
	}
	for input, want := range cases {
		if got := normalizePeerSearch(input); got != want {
			t.Fatalf("normalizePeerSearch(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestNormalizeRegistrationsMode(t *testing.T) {
	cases := map[string]string{
		"open":     "open",
		"approved": "approved",
		"none":     "none",
		"bad":      "none",
		" open ":   "open",
	}
	for input, want := range cases {
		if got := normalizeRegistrationsMode(input); got != want {
			t.Fatalf("normalizeRegistrationsMode(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestInstanceV1IncludesLegacyRegistrationKeys(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/instance", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)
	s := &Server{cfg: config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https", StreamingAPIBaseURL: "wss://streaming.example.test/"}}

	if err := s.instanceV1(c); err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if out["registrations"] != false || out["approval_required"] != false || out["invites_enabled"] != true {
		t.Fatalf("legacy registration fields = registrations:%#v approval_required:%#v invites_enabled:%#v", out["registrations"], out["approval_required"], out["invites_enabled"])
	}
	if out["thumbnail"] != "https://example.test/packs/media/images/preview.png" {
		t.Fatalf("thumbnail = %#v", out["thumbnail"])
	}
	urls, ok := out["urls"].(map[string]any)
	if !ok || urls["streaming_api"] != "wss://streaming.example.test/" {
		t.Fatalf("urls = %#v", out["urls"])
	}
	stats, ok := out["stats"].(map[string]any)
	if !ok || stats["user_count"] != float64(0) || stats["status_count"] != float64(0) || stats["domain_count"] != float64(0) {
		t.Fatalf("stats = %#v", out["stats"])
	}
	configuration, ok := out["configuration"].(map[string]any)
	if !ok {
		t.Fatalf("configuration = %#v", out["configuration"])
	}
	if _, ok := configuration["urls"]; ok {
		t.Fatalf("v1 configuration must not include v2 urls key: %#v", configuration)
	}
	if _, ok := configuration["translation"]; ok {
		t.Fatalf("v1 configuration must not include v2 translation key: %#v", configuration)
	}
	statuses, ok := configuration["statuses"].(map[string]any)
	if !ok || statuses["max_characters"] != float64(5000) || statuses["max_media_attachments"] != float64(4) {
		t.Fatalf("configuration.statuses = %#v", configuration["statuses"])
	}
	media, ok := configuration["media_attachments"].(map[string]any)
	if !ok || media["image_matrix_limit"] == nil {
		t.Fatalf("configuration.media_attachments = %#v", configuration["media_attachments"])
	}
}

func TestInstanceV1InvitesEnabledUsesEveryoneRolePermission(t *testing.T) {
	enabled, err := (&Server{}).instanceInvitesEnabled()
	if err != nil {
		t.Fatal(err)
	}
	if !enabled {
		t.Fatal("instanceInvitesEnabled without DB should match Rails UserRole.everyone default invite permission")
	}

	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`invitesEnabled, err := s.instanceInvitesEnabled()`,
		`"invites_enabled":   invitesEnabled`,
		`everyone, err := s.userRoleByID(-99)`,
		`return everyone.Permissions&rolePermissionInviteUsers == rolePermissionInviteUsers, nil`,
	} {
		if !strings.Contains(string(src), want) {
			t.Fatalf("server.go missing v1 instance invite compatibility %q", want)
		}
	}
	if strings.Contains(string(src), `"invites_enabled":   registrationsEnabled`) {
		t.Fatal("v1 instance invites_enabled must not be tied to registrations_enabled")
	}
}

func TestInstancePeersExcludeBlockedDomainsCaseInsensitively(t *testing.T) {
	src, err := os.ReadFile("instances.go")
	if err != nil {
		t.Fatal(err)
	}
	want := `Where("NOT EXISTS (SELECT 1 FROM domain_blocks WHERE lower(domain_blocks.domain) = lower(instances.domain))")`
	for _, functionName := range []string{"instancePeers", "peerSearch"} {
		if !functionBodyContains(t, src, functionName, want) {
			t.Fatalf("%s must exclude domain_blocks case-insensitively", functionName)
		}
		if functionBodyContains(t, src, functionName, `Where("NOT EXISTS (SELECT 1 FROM domain_blocks WHERE domain_blocks.domain = instances.domain)")`) {
			t.Fatalf("%s must not use a case-sensitive domain block comparison", functionName)
		}
	}
}
