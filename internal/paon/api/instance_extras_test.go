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

func TestInstanceDomainBlocksDisabledWhenSettingUnavailable(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest("GET", "/api/v1/instance/domain_blocks", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)
	s := &Server{}

	if err := s.instanceDomainBlocks(c); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := rec.Header().Get("Vary"); got != "Authorization" {
		t.Fatalf("Vary = %q, want Authorization", got)
	}
}

func TestInstanceDomainBlocksMatchesRailsConditionalVary(t *testing.T) {
	src, err := os.ReadFile("instance_extras.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`c.Response().Header().Set("Vary", "Authorization")`,
		`if showBlocks == "all"`,
		`c.Response().Header().Set("Vary", "")`,
		`s.publicRESTCacheEvenIfAuthenticated(c, 300)`,
	} {
		if !functionBodyContains(t, src, "instanceDomainBlocks", want) {
			t.Fatalf("instanceDomainBlocks missing %q", want)
		}
	}
}

func TestLimitedFederationPublicInstanceAPIsRequireAuthenticationLikeRails(t *testing.T) {
	handlers := map[string]func(*Server, *echo.Context) error{
		"/api/v1/instance/translation_languages": (*Server).translationLanguages,
		"/api/v1/instance/extended_description":  (*Server).instanceExtendedDescription,
		"/api/v1/instance/privacy_policy":        (*Server).instancePrivacyPolicy,
		"/api/v1/instance/domain_blocks":         (*Server).instanceDomainBlocks,
		"/api/v1/instance/rules":                 (*Server).instanceRules,
		"/api/v1/instance/languages":             (*Server).instanceLanguages,
		"/api/v1/instance/peers":                 (*Server).instancePeers,
		"/api/v1/instance/activity":              (*Server).instanceActivity,
		"/api/v1/custom_emojis":                  (*Server).customEmojis,
	}

	for path, handler := range handlers {
		t.Run(path, func(t *testing.T) {
			e := echo.New()
			req := httptest.NewRequest("GET", path, nil)
			rec := httptest.NewRecorder()
			c := echo.NewContext(req, rec, e)
			s := &Server{cfg: config.Config{LimitedFederationMode: true}}

			err := handler(s, c)
			apiErr, ok := err.(apiHTTPError)
			if !ok || apiErr.status != http.StatusUnauthorized || apiErr.message != "This method requires an authenticated user" {
				t.Fatalf("err = %#v", err)
			}
			if got := rec.Header().Get("Vary"); got != "Authorization" {
				t.Fatalf("Vary = %q, want Authorization", got)
			}
		})
	}
}

func TestLimitedFederationInstanceAPIsRemainPublicLikeMastodon44(t *testing.T) {
	handlers := map[string]func(*Server, *echo.Context) error{
		"/api/v1/instance": (*Server).instanceV1,
		"/api/v2/instance": (*Server).instanceV2,
	}

	for path, handler := range handlers {
		t.Run(path, func(t *testing.T) {
			e := echo.New()
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()
			c := echo.NewContext(req, rec, e)
			s := &Server{cfg: config.Config{
				LimitedFederationMode: true,
				Scheme:                "https",
				LocalDomain:           "example.com",
				WebDomain:             "example.com",
			}}

			if err := handler(s, c); err != nil {
				t.Fatal(err)
			}
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			// Limited federation still exposes these discovery endpoints in 4.4,
			// but it must not mark their responses as publicly cacheable.
			if got := rec.Header().Get("Cache-Control"); got != "" {
				t.Fatalf("Cache-Control = %q, want empty", got)
			}
			if got := rec.Header().Get("Vary"); got != "" {
				t.Fatalf("Vary = %q, want empty", got)
			}
		})
	}
}

func TestMastodon45InstanceTimelineAccessShape(t *testing.T) {
	s := &Server{cfg: config.Config{
		Scheme:      "https",
		LocalDomain: "example.test",
		WebDomain:   "example.test",
	}}
	access := s.instanceMetadata().TimelinesAccess
	for _, key := range []string{"live_feeds", "hashtag_feeds", "trending_link_feeds"} {
		values, ok := access[key].(map[string]string)
		if !ok {
			t.Fatalf("timelines_access.%s = %#v", key, access[key])
		}
		if values["local"] != timelineAccessPublic || values["remote"] != timelineAccessPublic {
			t.Fatalf("timelines_access.%s = %#v", key, values)
		}
	}

	v1 := instanceV1Configuration(map[string]any{
		"statuses":           map[string]any{"max_characters": 500},
		"timelines_access":   access,
		"limited_federation": false,
	})
	if _, ok := v1["timelines_access"]; ok {
		t.Fatalf("legacy v1 instance leaked v2 timelines_access: %#v", v1)
	}
	if _, ok := v1["limited_federation"]; ok {
		t.Fatalf("legacy v1 instance leaked v2 limited_federation: %#v", v1)
	}
}

func TestLimitedFederationGenericAPIRoutesRequireAuthentication(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com", LimitedFederationMode: true}, nil)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/timelines/public", nil)
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "This method requires an authenticated user") {
		t.Fatalf("body = %s", rec.Body.String())
	}
	if got := rec.Header().Get("Vary"); got != "Authorization" {
		t.Fatalf("Vary = %q, want Authorization", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Fatalf("Cache-Control = %q, want private, no-store", got)
	}
}

func TestInstanceExtrasSettingsRecordNotFoundBoundaryMatchesRails(t *testing.T) {
	src, err := os.ReadFile("instance_extras.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		fn   string
		want string
	}{
		{"instanceExtendedDescription", `!errors.Is(err, gorm.ErrRecordNotFound)`},
		{"instancePrivacyPolicy", `!errors.Is(err, gorm.ErrRecordNotFound)`},
	} {
		if !functionBodyContains(t, src, check.fn, check.want) {
			t.Fatalf("%s must tolerate wrapped gorm.ErrRecordNotFound for missing instance setting", check.fn)
		}
		if functionBodyContains(t, src, check.fn, `err != gorm.ErrRecordNotFound`) {
			t.Fatalf("%s must not directly compare gorm.ErrRecordNotFound", check.fn)
		}
	}
}

func TestLimitedFederationOEmbedSkipsGenericAPIGate(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com", LimitedFederationMode: true}, nil)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/oembed", nil)
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "This method requires an authenticated user") {
		t.Fatalf("oembed was gated by API authentication: %s", rec.Body.String())
	}
}

func TestLimitedFederationUnknownAPIRoutesStayNotFound(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com", LimitedFederationMode: true}, nil)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/does_not_exist", nil)
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "This method requires an authenticated user") {
		t.Fatalf("unknown API route was gated by authentication: %s", rec.Body.String())
	}
}

func TestPublicInstanceAPIsMatchRailsCacheHeaders(t *testing.T) {
	tests := []struct {
		path     string
		handler  func(*Server, *echo.Context) error
		cache    string
		localURL bool
	}{
		{path: "/api/v1/instance", handler: (*Server).instanceV1, cache: "max-age=300, public, stale-while-revalidate=30, stale-if-error=86400", localURL: true},
		{path: "/api/v2/instance", handler: (*Server).instanceV2, cache: "max-age=300, public, stale-while-revalidate=30, stale-if-error=86400", localURL: true},
		{path: "/api/v1/instance/translation_languages", handler: (*Server).translationLanguages, cache: "max-age=300, public, stale-while-revalidate=30, stale-if-error=86400"},
		{path: "/api/v1/instance/extended_description", handler: (*Server).instanceExtendedDescription, cache: "max-age=300, public, stale-while-revalidate=30, stale-if-error=86400"},
		{path: "/api/v1/instance/privacy_policy", handler: (*Server).instancePrivacyPolicy, cache: "max-age=300, public, stale-while-revalidate=30, stale-if-error=86400", localURL: true},
		{path: "/api/v1/instance/rules", handler: (*Server).instanceRules, cache: "max-age=300, public, stale-while-revalidate=30, stale-if-error=86400"},
		{path: "/api/v1/instance/languages", handler: (*Server).instanceLanguages, cache: "max-age=300, public, stale-while-revalidate=30, stale-if-error=86400"},
		{path: "/api/v1/custom_emojis", handler: (*Server).customEmojis, cache: "max-age=300, public, stale-while-revalidate=30, stale-if-error=86400"},
		{path: "/api/v1/instance/peers", handler: (*Server).instancePeers, cache: "max-age=86400, public, stale-while-revalidate=30, stale-if-error=86400"},
		{path: "/api/v1/instance/activity", handler: (*Server).instanceActivity, cache: "max-age=86400, public, stale-while-revalidate=30, stale-if-error=86400"},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			e := echo.New()
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			rec := httptest.NewRecorder()
			c := echo.NewContext(req, rec, e)
			s := &Server{}
			if tt.localURL {
				s.cfg = config.Config{Scheme: "https", LocalDomain: "example.com", WebDomain: "example.com"}
			}

			if err := tt.handler(s, c); err != nil {
				t.Fatal(err)
			}
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get("Cache-Control"); got != tt.cache {
				t.Fatalf("Cache-Control = %q, want %q", got, tt.cache)
			}
			if got := rec.Header().Get("Vary"); got != "" {
				t.Fatalf("Vary = %q, want empty", got)
			}
		})
	}
}

func TestInstanceDomainBlocksCacheHeaderMatchesRailsAllMode(t *testing.T) {
	src, err := os.ReadFile("instance_extras.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`if showBlocks == "all" {`,
		`s.publicRESTCacheEvenIfAuthenticated(c, 300)`,
		`showBlocks == "users" && functionalOrMoved`,
		`showRationale == "users" && functionalOrMoved`,
	} {
		if !functionBodyContains(t, src, "instanceDomainBlocks", want) {
			t.Fatalf("instanceDomainBlocks missing %q", want)
		}
	}
	for _, want := range []string{
		`user, _, err := s.currentUserIncludingDisabled(c)`,
		`return s.userFunctionalOrMoved(*user), nil`,
	} {
		if !functionBodyContains(t, src, "currentUserFunctionalOrMoved", want) {
			t.Fatalf("currentUserFunctionalOrMoved missing %q", want)
		}
	}
	for _, want := range []string{
		`return userCanUseAuthenticatedAPI(user) && !s.userAccountSuspendedOrMemorial(user)`,
	} {
		if !functionBodyContains(t, src, "userFunctionalOrMoved", want) {
			t.Fatalf("userFunctionalOrMoved missing %q", want)
		}
	}
	for _, want := range []string{
		`return account.SuspendedAt.Valid || account.Memorial`,
		`Select("id", "suspended_at", "memorial")`,
		`return loaded.SuspendedAt.Valid || loaded.Memorial`,
	} {
		if !functionBodyContains(t, src, "userAccountSuspendedOrMemorial", want) {
			t.Fatalf("userAccountSuspendedOrMemorial missing %q", want)
		}
	}
}

func TestInstanceLanguagesShape(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest("GET", "/api/v1/instance/languages", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)
	s := &Server{}

	if err := s.instanceLanguages(c); err != nil {
		t.Fatal(err)
	}
	var out []map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK || len(out) == 0 || out[0]["code"] == "" || out[0]["name"] == "" {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if len(out) < 180 || out[0]["code"] != "aa" || out[0]["name"] != "Afar" {
		t.Fatalf("unexpected supported languages prefix/count: len=%d first=%#v", len(out), out[0])
	}
	foundJapanese := false
	for _, language := range out {
		if language["code"] == "ja" {
			foundJapanese = language["name"] == "Japanese"
			break
		}
	}
	if !foundJapanese {
		t.Fatalf("Japanese language entry missing or incompatible: %#v", out)
	}
}

func TestInstanceActivityReturnsTwelveWeeks(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest("GET", "/api/v1/instance/activity", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)
	s := &Server{}

	if err := s.instanceActivity(c); err != nil {
		t.Fatal(err)
	}
	var out []map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK || len(out) != 12 || out[0]["statuses"] != "0" {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestInstanceActivityRequiresAuthenticationInLimitedFederationMode(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest("GET", "/api/v1/instance/activity", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)
	s := &Server{cfg: config.Config{LimitedFederationMode: true}}

	err := s.instanceActivity(c)
	apiErr, ok := err.(apiHTTPError)
	if !ok || apiErr.status != http.StatusUnauthorized {
		t.Fatalf("err = %#v", err)
	}
	if got := rec.Header().Get("Vary"); got != "Authorization" {
		t.Fatalf("Vary = %q, want Authorization", got)
	}
}

func TestInstancePeersDefaultsToPublishedEmptyListWithoutDB(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest("GET", "/api/v1/instance/peers", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)
	s := &Server{}

	if err := s.instancePeers(c); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK || rec.Body.String() != "[]\n" {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestInstancePeersRequiresAuthenticationInLimitedFederationMode(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest("GET", "/api/v1/instance/peers", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)
	s := &Server{cfg: config.Config{LimitedFederationMode: true}}

	err := s.instancePeers(c)
	apiErr, ok := err.(apiHTTPError)
	if !ok || apiErr.status != http.StatusUnauthorized {
		t.Fatalf("err = %#v", err)
	}
	if got := rec.Header().Get("Vary"); got != "Authorization" {
		t.Fatalf("Vary = %q, want Authorization", got)
	}
}

func TestPeerSearchHiddenInLimitedFederationMode(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest("GET", "/api/v1/peers/search?q=example", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)
	s := &Server{cfg: config.Config{LimitedFederationMode: true}}

	if err := s.peerSearch(c); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestPeerSearchUsesMeiliBeforeDatabaseFallback(t *testing.T) {
	originalClient := meiliHTTPClient
	meiliHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"hits":[{"domain":"remote.example"}]}`)),
		}, nil
	})}
	defer func() { meiliHTTPClient = originalClient }()

	e := echo.New()
	req := httptest.NewRequest("GET", "/api/v1/peers/search?q=remote", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)
	s := &Server{cfg: config.Config{MeiliEnabled: true, MeiliHost: "http://meili.test"}}

	if err := s.peerSearch(c); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var domains []string
	if err := json.Unmarshal(rec.Body.Bytes(), &domains); err != nil {
		t.Fatal(err)
	}
	if strings.Join(domains, ",") != "remote.example" {
		t.Fatalf("domains = %#v", domains)
	}
}

func TestZeroInstanceActivityWeeksUsesRequestedCountAndRailsShape(t *testing.T) {
	now := time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)
	out := zeroInstanceActivityWeeks(now, 2)
	if len(out) != 2 {
		t.Fatalf("weeks = %#v", out)
	}
	if out[0].Week != "1781863200" || out[1].Week != "1781258400" {
		t.Fatalf("week values = %#v", out)
	}
	if out[0].Statuses != "0" || out[0].Logins != "0" || out[0].Registrations != "0" {
		t.Fatalf("counters = %#v", out[0])
	}
}

func TestActivityTrackerRedisKeysMatchRailsDailyAndLegacyKeys(t *testing.T) {
	start := time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 0, 6)
	keys := activityTrackerRedisKeys("mastodon:", "activity:statuses:local", start, end)
	if len(keys) != 8 {
		t.Fatalf("keys = %#v", keys)
	}
	wantPrefix := []string{
		"mastodon:activity:statuses:local:1781827200",
		"mastodon:activity:statuses:local:25",
	}
	for i, want := range wantPrefix {
		if keys[i] != want {
			t.Fatalf("keys = %#v, want prefix %#v", keys, wantPrefix)
		}
	}
	if keys[5] != "mastodon:activity:statuses:local:26" {
		t.Fatalf("keys = %#v", keys)
	}
}

func TestActivityTrackerWriteHelpersMatchRailsRedisCommands(t *testing.T) {
	src, err := os.ReadFile("instances.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		fn   string
		want string
	}{
		{"activityTrackerIncrementBasic", `"INCRBY", key`},
		{"activityTrackerIncrementBasic", `"EXPIRE", key, ttl`},
		{"activityTrackerRecordUnique", `"PFADD", key`},
		{"activityTrackerRecordUnique", `"EXPIRE", key, ttl`},
		{"activityTrackerIncrementBasic", `activityTrackerDailyKey(s.cfg.RedisNamespace, trackerPrefix, at)`},
		{"activityTrackerRecordUnique", `activityTrackerDailyKey(s.cfg.RedisNamespace, trackerPrefix, at)`},
	} {
		if !functionBodyContains(t, src, check.fn, check.want) {
			t.Fatalf("instances.go:%s missing %q", check.fn, check.want)
		}
	}
}

func TestActivityTrackerWritesOnRailsActivityEvents(t *testing.T) {
	checks := map[string][]string{
		"auth.go": {
			`activityTrackerRecordUnique(context.Background(), "activity:logins", now, user.ID)`,
		},
		"registrations.go": {
			`activityTrackerIncrementBasic(ctx, "activity:accounts:local", now, 1)`,
			`activityTrackerRecordUnique(ctx, "activity:logins", now, approvedUser.ID)`,
		},
		"relationships.go": {
			`activityTrackerIncrementBasic(c.Request().Context(), "activity:interactions", now, 1)`,
		},
		"server.go": {
			`if statusCountsTowardLocalActivity(createdStatus.Visibility) {`,
			`activityTrackerIncrementBasic(c.Request().Context(), "activity:statuses:local", createdStatus.CreatedAt, 1)`,
			`activityTrackerIncrementBasic(c.Request().Context(), "activity:interactions", createdStatus.CreatedAt, 1)`,
			`activityTrackerIncrementBasic(c.Request().Context(), "activity:interactions", favourite.CreatedAt, 1)`,
		},
		"local_status_postcommit.go": {
			`if statusCountsTowardLocalActivity(created.Visibility) {`,
			`activityTrackerIncrementBasic(ctx, "activity:statuses:local", created.CreatedAt, 1)`,
			`if created.InReplyToAccountID.Valid && created.InReplyToAccountID.Int64 != effects.Account.ID {`,
			`activityTrackerIncrementBasic(ctx, "activity:interactions", created.CreatedAt, 1)`,
		},
		"scheduled_status_publish.go": {
			`if statusCountsTowardLocalActivity(created.Visibility) {`,
			`activityTrackerIncrementBasic(ctx, "activity:statuses:local", created.CreatedAt, 1)`,
			`if created.InReplyToAccountID.Valid && created.InReplyToAccountID.Int64 != account.ID {`,
			`activityTrackerIncrementBasic(ctx, "activity:interactions", created.CreatedAt, 1)`,
		},
		"polls.go": {
			`activityTrackerIncrementBasic(c.Request().Context(), "activity:interactions", createdVotes[0].CreatedAt, 1)`,
		},
	}
	for file, wants := range checks {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range wants {
			if !strings.Contains(string(src), want) {
				t.Fatalf("%s missing %q", file, want)
			}
		}
	}
	registrationsSrc, err := os.ReadFile("registrations.go")
	if err != nil {
		t.Fatal(err)
	}
	if functionBodyContains(t, registrationsSrc, "createAccount", `activityTrackerIncrementBasic(c.Request().Context(), "activity:accounts:local"`) {
		t.Fatal("REST registration must not record local-account activity before Rails prepare_new_user! boundary")
	}
	if functionBodyContains(t, registrationsSrc, "createWebRegistration", `activityTrackerIncrementBasic(c.Request().Context(), "activity:accounts:local"`) {
		t.Fatal("web registration must not record local-account activity before Rails prepare_new_user! boundary")
	}
}
