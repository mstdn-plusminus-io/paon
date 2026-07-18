package api

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestRelationshipIDs(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest("GET", "/api/v1/accounts/relationships?id[]=10&id[]=20,30&id=10&id=bad&id=+40x", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	got := relationshipIDs(c)
	want := []int64{10, 20, 10, 40}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("relationshipIDs = %#v, want %#v", got, want)
	}
}

func TestRelationshipIDsPreserveRailsRequestedOrderAndDuplicates(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest("GET", "/api/v1/accounts/relationships?id[]=7&id[]=7&id[]=8", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	got := relationshipIDs(c)
	want := []int64{7, 7, 8}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("relationshipIDs = %#v, want %#v", got, want)
	}
}

func TestRelationshipCacheRedisKeysMatchRailsCacheNamespaceCandidates(t *testing.T) {
	got := relationshipCacheRedisKeys(config.Config{RedisNamespace: "mastodon:"}, 10, 20, []int64{10, 20})
	want := []string{
		"relationship/10/20",
		"cache:relationship/10/20",
		"mastodon:relationship/10/20",
		"mastodon_cache:relationship/10/20",
		"relationship/20/10",
		"cache:relationship/20/10",
		"mastodon:relationship/20/10",
		"mastodon_cache:relationship/20/10",
		"exclude_account_ids_for:10",
		"cache:exclude_account_ids_for:10",
		"mastodon:exclude_account_ids_for:10",
		"mastodon_cache:exclude_account_ids_for:10",
		"exclude_account_ids_for:20",
		"cache:exclude_account_ids_for:20",
		"mastodon:exclude_account_ids_for:20",
		"mastodon_cache:exclude_account_ids_for:20",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("relationship cache keys = %#v, want %#v", got, want)
	}
}

func TestFollowRelationshipCacheRedisKeysIncludeFollowersHash(t *testing.T) {
	got := followRelationshipCacheRedisKeys(config.Config{RedisNamespace: "mastodon:"}, models.Account{ID: 10}, 20)
	want := []string{
		"relationship/10/20",
		"cache:relationship/10/20",
		"mastodon:relationship/10/20",
		"mastodon_cache:relationship/10/20",
		"relationship/20/10",
		"cache:relationship/20/10",
		"mastodon:relationship/20/10",
		"mastodon_cache:relationship/20/10",
		"followers_hash:20:local",
		"cache:followers_hash:20:local",
		"mastodon:followers_hash:20:local",
		"mastodon_cache:followers_hash:20:local",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("follow relationship cache keys = %#v, want %#v", got, want)
	}

	remotePrefix := accountSynchronizationURIPrefix(models.Account{URI: "https://remote.example/users/alice", Domain: sql.NullString{String: "remote.example", Valid: true}})
	if remotePrefix != "https://remote.example/" {
		t.Fatalf("remote synchronization prefix = %q", remotePrefix)
	}
}

func TestRemoveFromFollowersRequiresAuth(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest("POST", "/api/v1/accounts/123/remove_from_followers", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)
	s := &Server{}

	if err := s.removeFromFollowers(c); err == nil {
		t.Fatal("expected remove from followers to require authentication")
	} else if apiErr, ok := err.(apiHTTPError); !ok || apiErr.status != http.StatusUnauthorized {
		t.Fatalf("error = %#v", err)
	}
}

func TestBooleanRelationshipParams(t *testing.T) {
	for _, value := range []string{"true", "1", "on", "yes", "t", "bad", "no", " ", " false ", " off "} {
		if !truthy(value) {
			t.Fatalf("truthy(%q) = false, want true", value)
		}
	}
	for _, value := range []string{"", "false", "FALSE", "0", "f", "F", "off", "OFF"} {
		if truthy(value) {
			t.Fatalf("truthy(%q) = true, want false", value)
		}
	}
	for _, value := range []string{"false", "FALSE", "0", "f", "F", "off", "OFF"} {
		if !falseParam(value) {
			t.Fatalf("falseParam(%q) = false, want true", value)
		}
	}
	for _, value := range []string{"true", "1", "bad", "no", "", " false ", " off ", "0 "} {
		if falseParam(value) {
			t.Fatalf("falseParam(%q) = true, want false", value)
		}
	}
}

func TestRelationshipLanguageValuesAcceptsRailsFollowParams(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest("POST", "/api/v1/accounts/1/follow", strings.NewReader("languages%5B%5D=JA,en&languages%5B%5D=ja&languages=FR"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c := echo.NewContext(req, httptest.NewRecorder(), e)

	got := relationshipLanguageValues(c)
	want := []string{"ja,en", "ja", "fr"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("languages = %#v, want %#v", got, want)
	}
}

func TestParseFollowPayloadAcceptsJSONAndForm(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/accounts/1/follow", strings.NewReader(`{"reblogs":false,"notify":true,"languages":["JA,en","ja","FR"]}`))
	req.Header.Set("Content-Type", "application/json")
	c := echo.NewContext(req, httptest.NewRecorder(), e)

	payload, err := parseFollowPayload(c)
	if err != nil {
		t.Fatal(err)
	}
	if payload.ShowReblogs || !payload.Notify || !reflect.DeepEqual(payload.Languages, []string{"ja,en", "ja", "fr"}) {
		t.Fatalf("json payload = %#v", payload)
	}
	if !payload.HasShowReblogs || !payload.HasNotify || !payload.HasLanguages {
		t.Fatalf("json presence flags = %#v", payload)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/accounts/1/follow", strings.NewReader("reblogs=false&notify=1&languages%5B%5D=de&languages=EN"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c = echo.NewContext(req, httptest.NewRecorder(), e)

	payload, err = parseFollowPayload(c)
	if err != nil {
		t.Fatal(err)
	}
	if payload.ShowReblogs || !payload.Notify || !reflect.DeepEqual(payload.Languages, []string{"de", "en"}) {
		t.Fatalf("form payload = %#v", payload)
	}
	if !payload.HasShowReblogs || !payload.HasNotify || !payload.HasLanguages {
		t.Fatalf("form presence flags = %#v", payload)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/accounts/1/follow", strings.NewReader("reblogs=&notify="))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c = echo.NewContext(req, httptest.NewRecorder(), e)

	payload, err = parseFollowPayload(c)
	if err != nil {
		t.Fatal(err)
	}
	if payload.ShowReblogs || payload.Notify {
		t.Fatalf("empty submit-button-style booleans should cast false like Rails truthy_param?, got %#v", payload)
	}
	if !payload.HasShowReblogs || !payload.HasNotify {
		t.Fatalf("empty form boolean presence flags = %#v", payload)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/accounts/1/follow", nil)
	c = echo.NewContext(req, httptest.NewRecorder(), e)
	payload, err = parseFollowPayload(c)
	if err != nil {
		t.Fatal(err)
	}
	if !payload.ShowReblogs || payload.Notify || payload.Languages != nil {
		t.Fatalf("default payload = %#v", payload)
	}
	if payload.HasShowReblogs || payload.HasNotify || payload.HasLanguages {
		t.Fatalf("default presence flags = %#v", payload)
	}
}

func TestFollowPayloadUpdatesOnlyIncludesRailsPresentParams(t *testing.T) {
	at := time.Now()
	updates := followPayloadUpdates(followPayload{ShowReblogs: true, Notify: false}, at)
	if len(updates) != 0 {
		t.Fatalf("updates without present params = %#v", updates)
	}

	updates = followPayloadUpdates(followPayload{Notify: true, HasNotify: true}, at)
	if len(updates) != 2 || updates["notify"] != true {
		t.Fatalf("notify updates = %#v", updates)
	}
	if _, ok := updates["show_reblogs"]; ok {
		t.Fatalf("show_reblogs should not be reset when absent: %#v", updates)
	}

	updates = followPayloadUpdates(followPayload{Languages: []string{"ja"}, HasLanguages: true}, at)
	if _, ok := updates["languages"].(models.StringArray); !ok {
		t.Fatalf("languages update = %#v", updates)
	}
}

func TestFollowRequiresRequestMatchesRailsFollowService(t *testing.T) {
	source := &models.Account{ID: 1}
	local := &models.Account{ID: 2}
	locked := &models.Account{ID: 3, Locked: true}
	remoteActivityPub := &models.Account{ID: 4, Domain: sql.NullString{String: "remote.example", Valid: true}, Protocol: 1}
	silenced := &models.Account{ID: 5, SilencedAt: sql.NullTime{Valid: true}}

	if followRequiresRequest(source, local) {
		t.Fatal("unlocked local target should direct-follow")
	}
	if !followRequiresRequest(source, locked) {
		t.Fatal("locked target should use follow request")
	}
	if !followRequiresRequest(silenced, local) {
		t.Fatal("silenced source should use follow request")
	}
	if !followRequiresRequest(source, remoteActivityPub) {
		t.Fatal("remote ActivityPub target should use follow request")
	}
}

func TestFollowNotAllowedMatchesRailsLocalFastChecks(t *testing.T) {
	source := &models.Account{ID: 1}
	moved := &models.Account{ID: 2, MovedToAccountID: sql.NullInt64{Int64: 3, Valid: true}}
	remoteOStatus := &models.Account{ID: 4, Domain: sql.NullString{String: "remote.example", Valid: true}, Protocol: 0}
	remoteActivityPub := &models.Account{ID: 5, Domain: sql.NullString{String: "remote.example", Valid: true}, Protocol: 1}
	s := &Server{}

	disallowed, err := s.followNotAllowed(source, moved)
	if err != nil || !disallowed {
		t.Fatalf("moved target disallowed = %v, err = %v", disallowed, err)
	}
	disallowed, err = s.followNotAllowed(source, remoteOStatus)
	if err != nil || !disallowed {
		t.Fatalf("remote OStatus target disallowed = %v, err = %v", disallowed, err)
	}
	disallowed, err = s.followNotAllowed(source, remoteActivityPub)
	if err != nil || disallowed {
		t.Fatalf("remote ActivityPub target disallowed = %v, err = %v", disallowed, err)
	}
}

func TestDomainControlVariantsMatchRailsRuleForParentDomains(t *testing.T) {
	got := domainControlVariants("Sub.Remote.Example")
	want := []string{"sub.remote.example", "remote.example", "example"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("domain variants = %#v, want %#v", got, want)
	}
}

func TestBoolFromFollowPrefersFollowOverRequest(t *testing.T) {
	follow := &relationshipFollow{ShowReblogs: true, Notify: false}
	request := &relationshipFollow{ShowReblogs: false, Notify: true}

	if !boolFromFollow(follow, request, "reblogs") {
		t.Fatal("expected follow show_reblogs to win")
	}
	if boolFromFollow(follow, request, "notify") {
		t.Fatal("expected follow notify to win")
	}
	if !boolFromFollow(nil, request, "notify") {
		t.Fatal("expected request notify fallback")
	}
}

func TestLanguagesFromFollowMatchesRailsRelationshipSerializer(t *testing.T) {
	follow := &relationshipFollow{Languages: []string{"ja", "en"}}
	request := &relationshipFollow{Languages: []string{"fr"}}

	got := languagesFromFollow(follow, request)
	if !reflect.DeepEqual(got, []string{"ja", "en"}) {
		t.Fatalf("languages = %#v", got)
	}
	got[0] = "mutated"
	if follow.Languages[0] != "ja" {
		t.Fatalf("languagesFromFollow leaked source slice: %#v", follow.Languages)
	}

	if got := languagesFromFollow(nil, request); !reflect.DeepEqual(got, []string{"fr"}) {
		t.Fatalf("request languages = %#v", got)
	}
	if got := languagesFromFollow(nil, nil); got != nil {
		t.Fatalf("empty languages = %#v, want nil", got)
	}
}

func TestParseAccountMutePayloadAcceptsJSONAndForm(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/accounts/2/mute", strings.NewReader(`{"notifications":false,"duration":3600}`))
	req.Header.Set("Content-Type", "application/json")
	c := echo.NewContext(req, httptest.NewRecorder(), e)

	payload, err := parseAccountMutePayload(c)
	if err != nil {
		t.Fatal(err)
	}
	if payload.Notifications || payload.Duration != 3600 {
		t.Fatalf("json payload = %#v", payload)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/accounts/2/mute", strings.NewReader("notifications=true&duration=600"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c = echo.NewContext(req, httptest.NewRecorder(), e)

	payload, err = parseAccountMutePayload(c)
	if err != nil {
		t.Fatal(err)
	}
	if !payload.Notifications || payload.Duration != 600 {
		t.Fatalf("form payload = %#v", payload)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/accounts/2/mute", strings.NewReader("notifications=&duration="))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c = echo.NewContext(req, httptest.NewRecorder(), e)
	payload, err = parseAccountMutePayload(c)
	if err != nil {
		t.Fatal(err)
	}
	if payload.Notifications || payload.Duration != 0 {
		t.Fatalf("empty form notifications should cast false like Rails truthy_param?, got %#v", payload)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/accounts/2/mute", strings.NewReader(`{"duration":-1}`))
	req.Header.Set("Content-Type", "application/json")
	c = echo.NewContext(req, httptest.NewRecorder(), e)
	payload, err = parseAccountMutePayload(c)
	if err != nil {
		t.Fatal(err)
	}
	if !payload.Notifications || payload.Duration != -1 {
		t.Fatalf("negative json duration payload = %#v", payload)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/accounts/2/mute", strings.NewReader("duration=-2"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c = echo.NewContext(req, httptest.NewRecorder(), e)
	payload, err = parseAccountMutePayload(c)
	if err != nil {
		t.Fatal(err)
	}
	if !payload.Notifications || payload.Duration != -2 {
		t.Fatalf("negative form duration payload = %#v", payload)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/accounts/2/mute", nil)
	c = echo.NewContext(req, httptest.NewRecorder(), e)
	payload, err = parseAccountMutePayload(c)
	if err != nil {
		t.Fatal(err)
	}
	if !payload.Notifications || payload.Duration != 0 {
		t.Fatalf("default payload = %#v", payload)
	}
}

func TestMuteAccountDurationUsesRailsToISemantics(t *testing.T) {
	src, err := os.ReadFile("relationships.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, src, "muteAccount", `if payload.Duration != 0 {`) {
		t.Fatal("muteAccount should set expires_at for any non-zero Rails duration")
	}
}

func TestUnblockAccountDeliversActivityPubUndoBlock(t *testing.T) {
	src, err := os.ReadFile("relationships.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`First(&block)`,
		`s.deliverActivityPubUndoBlock(*account, *target, block.ID, string(block.URI))`,
	} {
		if !functionBodyContains(t, src, "unblockAccount", want) {
			t.Fatalf("unblockAccount should preserve Rails UnblockService side effect %q", want)
		}
	}
}
