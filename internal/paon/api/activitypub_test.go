package api

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

var (
	railsActivityPubAttributePattern = regexp.MustCompile(`(?m)^  attribute :([A-Za-z_][A-Za-z0-9_]*)(?:.*key: :([A-Za-z_][A-Za-z0-9_]*))?`)
	railsActivityPubHasOnePattern    = regexp.MustCompile(`(?m)^  has_one :([A-Za-z_][A-Za-z0-9_]*)(?:.*key: :([A-Za-z_][A-Za-z0-9_]*))?`)
	railsActivityPubHasManyPattern   = regexp.MustCompile(`(?m)^  has_many :([A-Za-z_][A-Za-z0-9_]*)(?:.*key: :([A-Za-z_][A-Za-z0-9_]*))?`)
	railsActivityPubSymbolPattern    = regexp.MustCompile(`:([A-Za-z_][A-Za-z0-9_]*)`)
)

func TestNodeInfoHonorsSingleUserMode(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/nodeinfo/2.0", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)
	s := &Server{cfg: config.Config{SingleUserMode: true}}

	if err := s.nodeInfo(c); err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["openRegistrations"] != false {
		t.Fatalf("openRegistrations = %#v", out["openRegistrations"])
	}
	software, ok := out["software"].(map[string]any)
	if !ok || software["name"] != "paon" {
		t.Fatalf("software = %#v", out["software"])
	}
	usage, ok := out["usage"].(map[string]any)
	if !ok {
		t.Fatalf("usage = %#v", out["usage"])
	}
	if usage["localPosts"] != float64(0) {
		t.Fatalf("localPosts = %#v", usage["localPosts"])
	}
	users, ok := usage["users"].(map[string]any)
	if !ok || users["total"] != float64(0) || users["active_month"] != float64(0) || users["active_halfyear"] != float64(0) {
		t.Fatalf("usage users = %#v", usage["users"])
	}
	if got := rec.Header().Get("Cache-Control"); got != "max-age=1800, public" {
		t.Fatalf("Cache-Control = %q", got)
	}
}

func TestNodeInfoDiscoveryMatchesRailsAdvertisedSchema(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/.well-known/nodeinfo", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)
	s := &Server{cfg: config.Config{Scheme: "https", WebDomain: "example.com", LocalDomain: "example.com"}}

	if err := s.nodeInfoDiscovery(c); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "max-age=259200, public" {
		t.Fatalf("Cache-Control = %q", got)
	}
	var out map[string][]map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	links := map[string]string{}
	for _, link := range out["links"] {
		links[link["rel"]] = link["href"]
	}
	if len(out["links"]) != 1 {
		t.Fatalf("Rails NodeInfo discovery should advertise only the 2.0 schema: %#v", out["links"])
	}
	if links["http://nodeinfo.diaspora.software/ns/schema/2.0"] != "https://example.com/nodeinfo/2.0" {
		t.Fatalf("links = %#v", out["links"])
	}
	if _, ok := links["http://nodeinfo.diaspora.software/ns/schema/2.1"]; ok {
		t.Fatalf("links = %#v", out["links"])
	}
}

func TestHostMetaMatchesRailsXRDContentType(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/.well-known/host-meta", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)
	s := &Server{cfg: config.Config{Scheme: "https", WebDomain: "example.com", LocalDomain: "example.com"}}

	if err := s.hostMeta(c); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/xrd+xml") {
		t.Fatalf("Content-Type = %q", got)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`<XRD xmlns="http://docs.oasis-open.org/ns/xri/xrd-1.0">`,
		`rel="lrdd"`,
		`template="https://example.com/.well-known/webfinger?resource={uri}"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("host-meta missing %q: %s", want, body)
		}
	}
	if strings.Contains(body, `type="application/xrd+xml"`) {
		t.Fatalf("host-meta should match Rails and omit Link type: %s", body)
	}
	if got := rec.Header().Get("Cache-Control"); got != "max-age=259200, public" {
		t.Fatalf("Cache-Control = %q", got)
	}
}

func TestWebfingerMatchesRailsJRDContentType(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/.well-known/webfinger?resource=acct:mastodon.internal@example.com", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)
	s := &Server{cfg: config.Config{Scheme: "https", WebDomain: "example.com", LocalDomain: "example.com"}}

	if err := s.webfinger(c); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/jrd+json") {
		t.Fatalf("Content-Type = %q", got)
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["subject"] != "acct:mastodon.internal@example.com" {
		t.Fatalf("subject = %#v", out["subject"])
	}
	links, ok := out["links"].([]any)
	if !ok || len(links) != 3 {
		t.Fatalf("links = %#v", out["links"])
	}
	profileLink, ok := links[0].(map[string]any)
	if !ok || profileLink["rel"] != "http://webfinger.net/rel/profile-page" || profileLink["type"] != "text/html" || profileLink["href"] != "https://example.com/about/more?instance_actor=true" {
		t.Fatalf("profile link = %#v", links[0])
	}
	selfLink, ok := links[1].(map[string]any)
	if !ok || selfLink["rel"] != "self" || selfLink["type"] != "application/activity+json" || selfLink["href"] != "https://example.com/actor" {
		t.Fatalf("self link = %#v", links[1])
	}
	subscribeLink, ok := links[2].(map[string]any)
	if !ok || subscribeLink["rel"] != "http://ostatus.org/schema/1.0/subscribe" || subscribeLink["template"] != "https://example.com/authorize_interaction?uri={uri}" {
		t.Fatalf("subscribe link = %#v", links[2])
	}
	if got := rec.Header().Get("Cache-Control"); got != "max-age=259200, public" {
		t.Fatalf("Cache-Control = %q", got)
	}
}

func TestWebfingerErrorsMatchRailsEmptyResponses(t *testing.T) {
	tests := []struct {
		name   string
		target string
		code   int
	}{
		{name: "missing resource", target: "/.well-known/webfinger", code: http.StatusBadRequest},
		{name: "invalid bare username", target: "/.well-known/webfinger?resource=alice", code: http.StatusBadRequest},
		{name: "remote acct", target: "/.well-known/webfinger?resource=acct:alice@remote.example", code: http.StatusNotFound},
		{name: "missing local account", target: "/.well-known/webfinger?resource=acct:alice@example.com", code: http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := echo.New()
			req := httptest.NewRequest(http.MethodGet, tt.target, nil)
			rec := httptest.NewRecorder()
			c := echo.NewContext(req, rec, e)
			s := &Server{cfg: config.Config{Scheme: "https", WebDomain: "example.com", LocalDomain: "example.com"}}

			if err := s.webfinger(c); err != nil {
				t.Fatal(err)
			}
			if rec.Code != tt.code {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			if rec.Body.Len() != 0 {
				t.Fatalf("body = %q", rec.Body.String())
			}
			if got := rec.Header().Get("Cache-Control"); got != "max-age=180, public" {
				t.Fatalf("Cache-Control = %q", got)
			}
		})
	}
}

func TestWebfingerPermanentlySuspendedAccountsReturnGone(t *testing.T) {
	s := &Server{}
	permanent, err := s.accountSuspendedPermanently(&models.Account{
		ID:          123,
		Username:    "alice",
		SuspendedAt: sql.NullTime{Time: time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC), Valid: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !permanent {
		t.Fatal("suspended account without a deletion request should be permanent when no DB request row is present")
	}
	permanent, err = s.accountSuspendedPermanently(&models.Account{ID: 123, Username: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	if permanent {
		t.Fatal("active account should not be permanently suspended")
	}

	src, err := os.ReadFile("activitypub.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, src, "webfinger", `return webfingerError(c, http.StatusGone)`) {
		t.Fatal("webfinger must return 410 Gone for permanently suspended accounts")
	}
	if !functionBodyContains(t, src, "accountSuspendedPermanently", `Model(&models.AccountDeletionRequest{})`) {
		t.Fatal("permanent suspension check must inspect account_deletion_requests like Rails")
	}
}

func TestActivityPubAccountOwnedGuardMatchesRailsAccountOwnedConcern(t *testing.T) {
	now := time.Date(2026, 6, 23, 12, 0, 0, 0, time.UTC)
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/users/alice/outbox", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)
	s := &Server{}

	confirmed := &models.Account{
		ID:       1,
		Username: "alice",
		User: models.User{
			ID:          10,
			Approved:    true,
			ConfirmedAt: sql.NullTime{Time: now, Valid: true},
		},
	}
	if err := s.activityPubAccountOwnedGuard(c, confirmed, false); err != nil {
		t.Fatalf("confirmed account guard returned %v", err)
	}

	pending := *confirmed
	pending.User.Approved = false
	if err := s.activityPubAccountOwnedGuard(c, &pending, false); !apiErrorStatus(err, http.StatusNotFound) {
		t.Fatalf("pending account error = %T %v, want 404 api error", err, err)
	}

	unconfirmed := *confirmed
	unconfirmed.User = models.User{ID: 11, Approved: true}
	if err := s.activityPubAccountOwnedGuard(c, &unconfirmed, false); !apiErrorStatus(err, http.StatusNotFound) {
		t.Fatalf("unconfirmed account error = %T %v, want 404 api error", err, err)
	}

	suspended := *confirmed
	suspended.SuspendedAt = sql.NullTime{Time: now, Valid: true}
	err := s.activityPubAccountOwnedGuard(c, &suspended, false)
	var noContentErr noContentHTTPError
	if !errors.As(err, &noContentErr) || noContentErr.status != http.StatusGone {
		t.Fatalf("permanently suspended account error = %T %v, want 410 no-content error", err, err)
	}
	if got := rec.Header().Get("Cache-Control"); got != "max-age=180, public" {
		t.Fatalf("Cache-Control = %q", got)
	}
}

func apiErrorStatus(err error, status int) bool {
	var apiErr apiHTTPError
	return errors.As(err, &apiErr) && apiErr.status == status
}

func TestActivityPubNoteRepliesOnlyForLocalStatusesLikeRails(t *testing.T) {
	src, err := os.ReadFile("activitypub.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, src, "activityPubNoteWithError", `if status.Account.Local()`) {
		t.Fatal("ActivityPub Note replies must be conditional like Rails NoteSerializer local? guard")
	}

	server := &Server{cfg: config.Config{Scheme: "https", WebDomain: "example.com", LocalDomain: "example.com"}}
	remote := models.Status{
		ID:        123,
		AccountID: 42,
		Text:      "remote cached status",
		CreatedAt: time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC),
		URI:       sql.NullString{String: "https://remote.example/users/alice/statuses/123", Valid: true},
		Account: models.Account{
			ID:       42,
			Username: "alice",
			Domain:   sql.NullString{String: "remote.example", Valid: true},
			URI:      "https://remote.example/users/alice",
		},
	}
	if _, ok := activityPubNote(server, remote)["replies"]; ok {
		t.Fatal("remote ActivityPub Note must omit replies like Rails NoteSerializer local? guard")
	}
}

func TestActivityPubOutboxStatusesHydrateCustomEmojis(t *testing.T) {
	src, err := os.ReadFile("activitypub.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, src, "activityPubStatuses", `s.hydrateStatusesCustomEmojis(statuses)`) {
		t.Fatal("activityPubStatuses must hydrate custom emojis before serializing Note tags")
	}
}

func TestActivityPubStatusURIPrefersRailsCanonicalLocalURL(t *testing.T) {
	server := &Server{cfg: config.Config{Scheme: "https", WebDomain: "example.com", LocalDomain: "example.com"}}
	local := models.Status{
		ID:      123,
		URI:     sql.NullString{String: "https://stale.example/objects/123", Valid: true},
		URL:     sql.NullString{String: "https://stale.example/@alice/123", Valid: true},
		Account: models.Account{Username: "alice"},
	}
	if got := activityPubStatusURI(server, local); got != "https://example.com/users/alice/statuses/123" {
		t.Fatalf("local status URI = %q", got)
	}

	remote := models.Status{
		ID:      456,
		URI:     sql.NullString{String: "https://remote.example/objects/456", Valid: true},
		URL:     sql.NullString{String: "https://remote.example/@bob/456", Valid: true},
		Account: models.Account{Username: "bob", Domain: sql.NullString{String: "remote.example", Valid: true}},
	}
	if got := activityPubStatusURI(server, remote); got != "https://remote.example/objects/456" {
		t.Fatalf("remote status URI = %q", got)
	}
}

func TestActivityPubOStatusCompatibilityURIs(t *testing.T) {
	server := &Server{cfg: config.Config{Scheme: "https", WebDomain: "example.com", LocalDomain: "example.com"}}
	createdAt := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	status := models.Status{
		ID:        123,
		CreatedAt: createdAt,
		Account:   models.Account{Username: "alice"},
	}
	if got := activityPubStatusAtomURI(server, status); got != "tag:example.com,2026-06-18:objectId=123:objectType=Status" {
		t.Fatalf("status atom URI = %v", got)
	}
	status.URI = sql.NullString{String: "tag:example.com,2026-06-18:objectId=999:objectType=Status", Valid: true}
	if got := activityPubStatusAtomURI(server, status); got != "tag:example.com,2026-06-18:objectId=999:objectType=Status" {
		t.Fatalf("stored status atom URI = %v", got)
	}
	remote := models.Status{
		ID:        456,
		CreatedAt: createdAt,
		Account:   models.Account{Username: "bob", Domain: sql.NullString{String: "remote.example", Valid: true}},
	}
	if got := activityPubStatusAtomURI(server, remote); got != nil {
		t.Fatalf("remote note atom URI = %v", got)
	}
	conversation := models.Conversation{ID: 77, CreatedAt: createdAt}
	if got := activityPubConversationURI(server, conversation); got != "tag:example.com,2026-06-18:objectId=77:objectType=Conversation" {
		t.Fatalf("conversation URI = %v", got)
	}
	conversation.URI = sql.NullString{String: "https://remote.example/conversations/77", Valid: true}
	if got := activityPubConversationURI(server, conversation); got != "https://remote.example/conversations/77" {
		t.Fatalf("stored conversation URI = %v", got)
	}
}

func TestActivityPubReplyTargetMatchesRailsNonHTTPRemoteURI(t *testing.T) {
	server := &Server{cfg: config.Config{Scheme: "https", WebDomain: "example.com", LocalDomain: "example.com"}}
	remoteHTTP := models.Status{
		ID:  200,
		URI: sql.NullString{String: "https://remote.example/objects/200", Valid: true},
		URL: sql.NullString{String: "https://remote.example/@bob/200", Valid: true},
		Account: models.Account{
			Username: "bob",
			Domain:   sql.NullString{String: "remote.example", Valid: true},
		},
	}
	if got := activityPubReplyTargetURI(server, remoteHTTP); got != "https://remote.example/objects/200" {
		t.Fatalf("http remote reply target = %#v", got)
	}

	remoteOStatus := remoteHTTP
	remoteOStatus.URI = sql.NullString{String: "tag:remote.example,2026-06-18:objectId=200:objectType=Status", Valid: true}
	if got := activityPubReplyTargetURI(server, remoteOStatus); got != "https://remote.example/@bob/200" {
		t.Fatalf("non-http remote reply target = %#v", got)
	}

	remoteOStatus.URL = sql.NullString{}
	if got := activityPubReplyTargetURI(server, remoteOStatus); got != nil {
		t.Fatalf("non-http remote reply target without url = %#v", got)
	}
}

func TestActivityPubNoteContentFormatsParagraphs(t *testing.T) {
	server := &Server{cfg: config.Config{Scheme: "https", WebDomain: "example.com", LocalDomain: "example.com"}}
	status := models.Status{
		ID:        123,
		AccountID: 42,
		Text:      "first\r\nsecond\n\nthird <b>x</b>\n\n",
		CreatedAt: time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC),
		Language:  sql.NullString{String: "ja", Valid: true},
		Account:   models.Account{ID: 42, Username: "alice"},
	}

	note := activityPubNote(server, status)
	expected := "<p>first<br />second</p><p>third &lt;b&gt;x&lt;/b&gt;</p>"
	if note["content"] != expected {
		t.Fatalf("content = %v", note["content"])
	}
	contentMap, ok := note["contentMap"].(map[string]string)
	if !ok || contentMap["ja"] != expected {
		t.Fatalf("contentMap = %#v", note["contentMap"])
	}
}

func TestActivityPubNoteOmitsBlankPresenceFieldsLikeRails(t *testing.T) {
	server := &Server{cfg: config.Config{Scheme: "https", WebDomain: "example.com", LocalDomain: "example.com"}}
	status := models.Status{
		ID:          123,
		AccountID:   42,
		Text:        "hello",
		CreatedAt:   time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC),
		Language:    sql.NullString{String: " \t\n ", Valid: true},
		SpoilerText: " \t\n ",
		Account:     models.Account{ID: 42, Username: "alice"},
	}

	note := activityPubNote(server, status)
	if note["summary"] != nil {
		t.Fatalf("summary should match Rails presence semantics, got %#v", note["summary"])
	}
	if _, ok := note["contentMap"]; ok {
		t.Fatalf("contentMap should be omitted for blank language like Rails, got %#v", note["contentMap"])
	}
}

func TestActivityPubPollStatusSerializesQuestion(t *testing.T) {
	server := &Server{cfg: config.Config{Scheme: "https", WebDomain: "example.com", LocalDomain: "example.com"}}
	expiresAt := time.Now().UTC().Add(-time.Minute)
	status := models.Status{
		ID:        123,
		AccountID: 42,
		Text:      "choose",
		CreatedAt: time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 6, 18, 12, 5, 0, 0, time.UTC),
		Account:   models.Account{ID: 42, Username: "alice"},
		Poll: &models.Poll{
			ID:            77,
			Options:       models.StringArray{"red", "blue"},
			CachedTallies: models.Int64Array{3, 5},
			ExpiresAt:     sql.NullTime{Time: expiresAt, Valid: true},
			VotersCount:   sql.NullInt64{Int64: 8, Valid: true},
		},
	}

	note := activityPubNote(server, status)
	if note["type"] != "Question" || note["endTime"] == nil || note["closed"] == nil || note["votersCount"] != int64(8) {
		t.Fatalf("question poll fields = %#v", note)
	}
	options, ok := note["oneOf"].([]any)
	if !ok || len(options) != 2 {
		t.Fatalf("oneOf = %#v", note["oneOf"])
	}
	first := options[0].(map[string]any)
	replies := first["replies"].(map[string]any)
	if first["type"] != "Note" || first["name"] != "red" || replies["type"] != "Collection" || replies["totalItems"] != int64(3) {
		t.Fatalf("first option = %#v", first)
	}

	update := activityPubPollUpdate(server, status)
	if update["id"] != "https://example.com/users/alice/statuses/123#updates/1781784300" {
		t.Fatalf("poll update id = %v", update["id"])
	}
	if update["cc"] != nil {
		t.Fatalf("poll update should match Rails UpdatePollSerializer fields: %#v", update)
	}
	if update["object"].(map[string]any)["type"] != "Question" {
		t.Fatalf("poll update object = %#v", update["object"])
	}
}

func TestActivityPubAudienceMatchesMastodonVisibilityBasics(t *testing.T) {
	server := &Server{cfg: config.Config{Scheme: "https", WebDomain: "example.com", LocalDomain: "example.com"}}
	account := models.Account{ID: 42, Username: "alice"}
	mention := models.Mention{Account: models.Account{ID: 100, Username: "bob", Domain: sql.NullString{String: "remote.test", Valid: true}, URI: "https://remote.test/users/bob"}}

	to, cc := activityPubAudience(server, models.Status{Account: account, Visibility: 0, Mentions: []models.Mention{mention}})
	if !reflect.DeepEqual(to, []string{"https://www.w3.org/ns/activitystreams#Public"}) || !reflect.DeepEqual(cc, []string{"https://example.com/users/alice/followers", "https://remote.test/users/bob"}) {
		t.Fatalf("public audience to=%#v cc=%#v", to, cc)
	}
	to, cc = activityPubAudience(server, models.Status{Account: account, Visibility: 1, Mentions: []models.Mention{mention}})
	if !reflect.DeepEqual(to, []string{"https://example.com/users/alice/followers"}) || !reflect.DeepEqual(cc, []string{"https://www.w3.org/ns/activitystreams#Public", "https://remote.test/users/bob"}) {
		t.Fatalf("unlisted audience to=%#v cc=%#v", to, cc)
	}
	to, cc = activityPubAudience(server, models.Status{Account: account, Visibility: 2, Mentions: []models.Mention{mention}})
	if !reflect.DeepEqual(to, []string{"https://example.com/users/alice/followers"}) || !reflect.DeepEqual(cc, []string{"https://remote.test/users/bob"}) {
		t.Fatalf("private audience to=%#v cc=%#v", to, cc)
	}
	to, cc = activityPubAudience(server, models.Status{Account: account, Visibility: 3, Mentions: []models.Mention{mention}})
	if !reflect.DeepEqual(to, []string{"https://remote.test/users/bob"}) || len(cc) != 0 {
		t.Fatalf("direct audience to=%#v cc=%#v", to, cc)
	}
	to, cc = activityPubAudience(server, models.Status{Account: account, Visibility: 4, Mentions: []models.Mention{mention}})
	if !reflect.DeepEqual(to, []string{"https://remote.test/users/bob"}) || len(cc) != 0 {
		t.Fatalf("limited audience to=%#v cc=%#v", to, cc)
	}
}

func TestActivityPubInstanceActorShape(t *testing.T) {
	server := &Server{cfg: config.Config{Scheme: "https", WebDomain: "example.com", LocalDomain: "example.com"}}
	actor := activityPubInstanceActorObject(server, server.instanceActorAccount())

	if actor["id"] != "https://example.com/actor" || actor["type"] != "Application" {
		t.Fatalf("actor identity = %#v", actor)
	}
	if actor["preferredUsername"] != "mastodon.internal" || actor["inbox"] != "https://example.com/actor/inbox" || actor["outbox"] != "https://example.com/actor/outbox" {
		t.Fatalf("actor endpoints = %#v", actor)
	}
	publicKey := actor["publicKey"].(map[string]any)
	if publicKey["id"] != "https://example.com/actor#main-key" || publicKey["owner"] != "https://example.com/actor" {
		t.Fatalf("publicKey = %#v", publicKey)
	}
	endpoints := actor["endpoints"].(map[string]any)
	if endpoints["sharedInbox"] != "https://example.com/inbox" {
		t.Fatalf("endpoints = %#v", endpoints)
	}
	for _, omitted := range []string{"followers", "following", "featured", "featuredTags", "devices", "name", "summary", "discoverable", "indexable", "published", "memorial", "tag", "attachment"} {
		if actor[omitted] != nil {
			t.Fatalf("instance actor should omit Rails-restricted field %q: %#v", omitted, actor)
		}
	}
}

func TestActivityPubInstanceActorDoesNotRequireSignatureInLimitedFederationModeLikeRails(t *testing.T) {
	s, err := NewServer(config.Config{
		Title:                 "Paon",
		LocalDomain:           "example.com",
		WebDomain:             "example.com",
		Scheme:                "https",
		LimitedFederationMode: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/actor", nil)
	req.Header.Set("Accept", "application/activity+json")
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Vary"); got != "" {
		t.Fatalf("Vary = %q, want empty like Rails InstanceActorsController", got)
	}
}

func TestActivityJSONWithCacheKeepsActivityContentType(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/activity", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	if err := activityJSONWithCache(c, map[string]string{"type": "Note"}, 180); err != nil {
		t.Fatal(err)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/activity+json") {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "max-age=180, public" {
		t.Fatalf("Cache-Control = %q", got)
	}
}

func TestActivityPubActorObjectIncludesDevicesCollection(t *testing.T) {
	server := &Server{cfg: config.Config{Scheme: "https", WebDomain: "example.com", LocalDomain: "example.com"}}
	actor := activityPubActorObject(server, models.Account{ID: 42, Username: "alice"})
	if actor["devices"] != "https://example.com/users/alice/collections/devices" {
		t.Fatalf("devices = %#v", actor["devices"])
	}
}

func TestActivityPubActorObjectContextMatchesRailsExtensions(t *testing.T) {
	server := &Server{cfg: config.Config{Scheme: "https", WebDomain: "example.com", LocalDomain: "example.com"}}
	actor := activityPubActorObject(server, models.Account{ID: 42, Username: "alice"})
	contexts, ok := actor["@context"].([]any)
	if !ok || len(contexts) != 3 || contexts[0] != "https://www.w3.org/ns/activitystreams" || contexts[1] != "https://w3id.org/security/v1" {
		t.Fatalf("actor context = %#v", actor["@context"])
	}
	extension, ok := contexts[2].(map[string]any)
	if !ok {
		t.Fatalf("actor context extension = %#v", contexts[2])
	}
	for _, want := range []string{
		"manuallyApprovesFollowers",
		"featured",
		"featuredTags",
		"alsoKnownAs",
		"movedTo",
		"PropertyValue",
		"discoverable",
		"indexable",
		"memorial",
		"suspended",
		"Device",
		"devices",
	} {
		if _, ok := extension[want]; !ok {
			t.Fatalf("actor context extension missing %q: %#v", want, extension)
		}
	}
	if extension["toot"] != "http://joinmastodon.org/ns#" || extension["schema"] != "http://schema.org#" {
		t.Fatalf("actor context namespaces = %#v", extension)
	}
	if movedTo, ok := extension["movedTo"].(map[string]any); !ok || movedTo["@id"] != "as:movedTo" || movedTo["@type"] != "@id" {
		t.Fatalf("movedTo context = %#v", extension["movedTo"])
	}
	for _, unwanted := range []string{"Hashtag", "Emoji"} {
		if _, ok := extension[unwanted]; ok {
			t.Fatalf("actor context without virtual tags should not include nested tag extension %q: %#v", unwanted, extension)
		}
	}
}

func TestActivityPubActorObjectMapsRailsActorTypes(t *testing.T) {
	server := &Server{cfg: config.Config{Scheme: "https", WebDomain: "example.com", LocalDomain: "example.com"}}
	tests := []struct {
		name     string
		account  models.Account
		wantType string
	}{
		{name: "person", account: models.Account{ID: 42, Username: "alice"}, wantType: "Person"},
		{name: "bot service", account: models.Account{ID: 42, Username: "alice", ActorType: sql.NullString{String: "Service", Valid: true}}, wantType: "Service"},
		{name: "bot application", account: models.Account{ID: 42, Username: "alice", ActorType: sql.NullString{String: "Application", Valid: true}}, wantType: "Service"},
		{name: "group", account: models.Account{ID: 42, Username: "alice", ActorType: sql.NullString{String: "Group", Valid: true}}, wantType: "Group"},
		{name: "instance actor", account: server.instanceActorAccount(), wantType: "Application"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actor := activityPubActorObject(server, tt.account)
			if actor["type"] != tt.wantType {
				t.Fatalf("type = %#v, want %q", actor["type"], tt.wantType)
			}
		})
	}
}

func TestActivityPubActorObjectFormatsSummaryAndFieldsLikeRails(t *testing.T) {
	server := &Server{cfg: config.Config{Scheme: "https", WebDomain: "example.com", LocalDomain: "example.com"}}
	actor := activityPubActorObject(server, models.Account{
		ID:        42,
		Username:  "alice",
		Note:      "hello #GoLang gemini://example.com/docs",
		Fields:    []byte(`[{"name":"Site","value":"ipfs://bafybeigdyrzt"}]`),
		CreatedAt: time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC),
	})

	summary, ok := actor["summary"].(string)
	if !ok || !strings.Contains(summary, `href="https://example.com/tags/golang"`) || !strings.Contains(summary, `href="gemini://example.com/docs"`) {
		t.Fatalf("summary = %#v", actor["summary"])
	}
	attachments, ok := actor["attachment"].([]any)
	if !ok || len(attachments) != 1 {
		t.Fatalf("attachments = %#v", actor["attachment"])
	}
	field, ok := attachments[0].(map[string]any)
	if !ok || field["type"] != "PropertyValue" || field["name"] != "Site" {
		t.Fatalf("field = %#v", attachments[0])
	}
	value, ok := field["value"].(string)
	if !ok || strings.Contains(value, "<p>") || !strings.Contains(value, `href="ipfs://bafybeigdyrzt"`) {
		t.Fatalf("field value = %#v", field["value"])
	}
}

func TestActivityPubActorObjectIncludesIconAndImageWhenPresent(t *testing.T) {
	server := &Server{cfg: config.Config{Scheme: "https", WebDomain: "example.com", LocalDomain: "example.com"}}
	actor := activityPubActorObject(server, models.Account{
		ID:                42,
		Username:          "alice",
		AvatarFileName:    sql.NullString{String: "avatar.png", Valid: true},
		AvatarContentType: sql.NullString{String: "image/png", Valid: true},
		HeaderFileName:    sql.NullString{String: "header.jpg", Valid: true},
		HeaderContentType: sql.NullString{String: "image/jpeg", Valid: true},
	})

	icon, ok := actor["icon"].(map[string]any)
	if !ok || icon["type"] != "Image" || icon["mediaType"] != "image/png" || icon["url"] != "https://example.com/system/accounts/avatars/000/000/042/original/avatar.png" {
		t.Fatalf("icon = %#v", actor["icon"])
	}
	image, ok := actor["image"].(map[string]any)
	if !ok || image["type"] != "Image" || image["mediaType"] != "image/jpeg" || image["url"] != "https://example.com/system/accounts/headers/000/000/042/original/header.jpg" {
		t.Fatalf("image = %#v", actor["image"])
	}
}

func TestActivityPubActorObjectOmitsSuspendedIconAndImage(t *testing.T) {
	server := &Server{cfg: config.Config{Scheme: "https", WebDomain: "example.com", LocalDomain: "example.com"}}
	actor := activityPubActorObject(server, models.Account{
		ID:                42,
		Username:          "alice",
		AvatarFileName:    sql.NullString{String: "avatar.png", Valid: true},
		AvatarContentType: sql.NullString{String: "image/png", Valid: true},
		HeaderFileName:    sql.NullString{String: "header.jpg", Valid: true},
		HeaderContentType: sql.NullString{String: "image/jpeg", Valid: true},
		SuspendedAt:       sql.NullTime{Time: time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC), Valid: true},
	})

	if actor["icon"] != nil || actor["image"] != nil {
		t.Fatalf("suspended actor should omit icon/image: %#v", actor)
	}
}

func TestActivityPubDeviceCollectionContextMatchesRailsOLMShape(t *testing.T) {
	contexts := activityPubOLMContext()
	if len(contexts) != 2 || contexts[0] != "https://www.w3.org/ns/activitystreams" {
		t.Fatalf("contexts = %#v", contexts)
	}
	olm, ok := contexts[1].(map[string]any)
	if !ok {
		t.Fatalf("olm context = %#v", contexts[1])
	}
	if olm["toot"] != "http://joinmastodon.org/ns#" || olm["Device"] != "toot:Device" || olm["EncryptedMessage"] != "toot:EncryptedMessage" || olm["devices"].(map[string]any)["@id"] != "toot:devices" {
		t.Fatalf("olm context = %#v", olm)
	}
	if _, ok := olm["fingerprintKey"].(map[string]any); !ok || olm["publicKeyBase64"] != "toot:publicKeyBase64" {
		t.Fatalf("device key context = %#v", olm)
	}
	if activityPubTootJSONLDContext()["EncryptedMessage"] != "toot:EncryptedMessage" || activityPubActivityStreamsJSONLDContext()["EncryptedMessage"] != "toot:EncryptedMessage" {
		t.Fatalf("LD Signature OLM context should match Rails EncryptedMessage IRI")
	}
	if activityPubTootJSONLDContext()["Digest"] != "as:Digest" || activityPubActivityStreamsJSONLDContext()["digestValue"] != "https://www.w3.org/ns/activitystreams#digestValue" {
		t.Fatalf("LD Signature encrypted-message digest context should match Rails shape")
	}
	if activityPubTootJSONLDContext()["Emoji"] != "toot:Emoji" || activityPubActivityStreamsJSONLDContext()["Emoji"] != "toot:Emoji" || activityPubActivityStreamsJSONLDContext()["Hashtag"] != "https://www.w3.org/ns/activitystreams#Hashtag" {
		t.Fatalf("LD Signature tag context should match Rails Emoji/Hashtag IRIs")
	}
}

func TestActivityPubMovePayloadMatchesRailsShape(t *testing.T) {
	server := &Server{cfg: config.Config{Scheme: "https", WebDomain: "example.com", LocalDomain: "example.com"}}
	move := activityPubMove(server, models.AccountMigration{
		ID: 77,
		Account: models.Account{
			ID:       42,
			Username: "alice",
		},
		TargetAccount: models.Account{
			ID:       43,
			Username: "carol",
			Domain:   sql.NullString{String: "remote.example", Valid: true},
			URI:      "https://remote.example/users/carol",
		},
	})

	if move["type"] != "Move" || move["id"] != "https://example.com/users/alice#moves/77" {
		t.Fatalf("move identity = %#v", move)
	}
	if move["@context"] != "https://www.w3.org/ns/activitystreams" {
		t.Fatalf("move context should match Rails MoveSerializer: %#v", move["@context"])
	}
	if move["actor"] != "https://example.com/users/alice" || move["object"] != "https://example.com/users/alice" || move["target"] != "https://remote.example/users/carol" {
		t.Fatalf("move endpoints = %#v", move)
	}
}

func TestActivityPubHTMLRoutesRedirectToWebURLs(t *testing.T) {
	e := echo.New()
	e.HTTPErrorHandler = handleAPIError
	s := &Server{}
	e.GET("/users/:username", s.activityPubActor)
	e.GET("/users/:username/followers", s.activityPubFollowers)
	e.GET("/users/:username/following", s.activityPubFollowing)
	e.GET("/users/:username/statuses/:id", s.activityPubStatus)
	cases := []struct {
		name   string
		target string
		want   string
	}{
		{
			name:   "actor",
			target: "/users/alice",
			want:   "/@alice",
		},
		{
			name:   "followers",
			target: "/users/alice/followers",
			want:   "/@alice/followers",
		},
		{
			name:   "following",
			target: "/users/alice/following",
			want:   "/@alice/following",
		},
		{
			name:   "status",
			target: "/users/alice/statuses/123",
			want:   "/@alice/123",
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.target, nil)
			req.Header.Set("Accept", "text/html")
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)
			if rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != tt.want {
				t.Fatalf("status = %d location = %q", rec.Code, rec.Header().Get("Location"))
			}
		})
	}
}

func TestActivityPubCollectionsRespectSignedAccountBlocks(t *testing.T) {
	src, err := os.ReadFile("activitypub.go")
	if err != nil {
		t.Fatal(err)
	}
	checks := map[string][]string{
		"activityPubCollection": {
			`s.activityPubSignatureAccountIfAuthorized(c)`,
			`s.activityPubCollectionHiddenForSignedAccount(*account, signedAccount)`,
			`hiddenForSignedAccount`,
			`"@context": activityPubActivityStreamsContext()`,
			`"orderedItems": []any{}`,
			`"items": []any{}`,
			`context := any(activityPubActivityStreamsContext())`,
			`if len(items) > 0 {`,
			`context = activityContext()`,
			`activityPubFeaturedTagObject(s, tag)`,
			`activityPubHashtagContext()`,
		},
		"activityPubCollectionHiddenForSignedAccount": {
			`models.Block{}`,
			`account_id = ? AND target_account_id = ?`,
			`s.accountDomainBlocking(owner.ID, signed.Domain.String)`,
		},
	}
	for fn, wants := range checks {
		for _, want := range wants {
			if !functionBodyContains(t, src, fn, want) {
				t.Fatalf("activitypub.go:%s missing %q", fn, want)
			}
		}
	}
	if functionBodyContains(t, src, "activityPubCollection", `Order("statuses_count DESC")`) {
		t.Fatal("ActivityPub featured tags collection must keep Rails association ordering")
	}
	if functionBodyContains(t, src, "activityPubCollectionHiddenForSignedAccount", `strings.TrimSpace(signed.Domain.String) != ""`) {
		t.Fatal("ActivityPub collection signed-account domain block check must match Rails nil-only domain guard and not skip blank raw domains")
	}
}

func TestActivityPubFollowCollectionObjectHidesFirstPageWhenCollectionsHidden(t *testing.T) {
	out := activityPubFollowCollectionObject("https://example.com/users/alice/followers", 12, true)
	if out["id"] != "https://example.com/users/alice/followers" || out["type"] != "OrderedCollection" || out["totalItems"] != int64(12) {
		t.Fatalf("collection = %#v", out)
	}
	if out["@context"] != "https://www.w3.org/ns/activitystreams" {
		t.Fatalf("context should match Rails follow collection presenter: %#v", out["@context"])
	}
	if _, ok := out["first"]; ok {
		t.Fatalf("first should be hidden: %#v", out)
	}

	visible := activityPubFollowCollectionObject("https://example.com/users/alice/followers", 12, false)
	if visible["@context"] != "https://www.w3.org/ns/activitystreams" {
		t.Fatalf("visible context should match Rails follow collection presenter: %#v", visible["@context"])
	}
	if visible["first"] != "https://example.com/users/alice/followers?page=1" {
		t.Fatalf("first = %#v", visible["first"])
	}
}

func TestActivityPubHandlersNormalizeRailsJSONFormatParams(t *testing.T) {
	src, err := os.ReadFile("activitypub.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		fn   string
		want string
	}{
		{fn: "activityPubActor", want: `s.findAccountByAcct(activityPubFormatParam(c, "username"))`},
		{fn: "activityPubCollection", want: `collectionID := activityPubFormatParam(c, "id")`},
		{fn: "localActivityPubAccountWithSuspensionMode", want: `s.findAccountByAcct(activityPubFormatParam(c, "username"))`},
		{fn: "findActivityPubStatus", want: `s.findStatus(activityPubFormatParam(c, "id"))`},
	} {
		if !functionBodyContains(t, src, tc.fn, tc.want) {
			t.Fatalf("%s missing Rails .json format param normalization %q", tc.fn, tc.want)
		}
	}
}

func TestActivityPubOutboxVisibilityUsesSignedRequester(t *testing.T) {
	src, err := os.ReadFile("activitypub.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`signed == nil`,
		`statuses.visibility IN ?`,
		`signed.ID == account.ID`,
		`s.activityPubCollectionHiddenForSignedAccount(account, signed)`,
		`models.Follow{}`,
		`account_id = ? AND target_account_id = ?`,
		`visible = []int{0, 1, 2}`,
		`LEFT JOIN mentions AS activitypub_outbox_mentions`,
		`activitypub_outbox_mentions.id IS NOT NULL`,
		`Group("statuses.id")`,
		`s.applyActivityPubOutboxReblogExclusions(query, signed)`,
	} {
		if !functionBodyContains(t, src, "applyActivityPubOutboxVisibility", want) {
			t.Fatalf("applyActivityPubOutboxVisibility missing %q", want)
		}
	}
	if functionBodyContains(t, src, "activityPubStatuses", `reblog_of_id IS NULL`) {
		t.Fatal("activityPubStatuses must match Rails AccountStatusesFilter and include boosts in outbox")
	}
}

func TestActivityPubOutboxActivitySerializesBoostsAsAnnounce(t *testing.T) {
	server := &Server{cfg: config.Config{Scheme: "https", WebDomain: "example.com", LocalDomain: "example.com"}}
	status := models.Status{
		ID:         200,
		AccountID:  42,
		Text:       "boost",
		Visibility: 0,
		ReblogOfID: sql.NullInt64{Int64: 100, Valid: true},
		CreatedAt:  time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC),
		Account:    models.Account{ID: 42, Username: "alice"},
		Reblog: &models.Status{
			ID:        100,
			URI:       sql.NullString{String: "https://remote.example/statuses/100", Valid: true},
			AccountID: 50,
			Account:   models.Account{ID: 50, Username: "bob", Domain: sql.NullString{String: "remote.example", Valid: true}},
		},
	}

	announce := activityPubOutboxActivity(server, status)
	if announce["type"] != "Announce" || announce["object"] != "https://remote.example/statuses/100" {
		t.Fatalf("outbox boost activity = %#v", announce)
	}

	status.ReblogOfID = sql.NullInt64{}
	status.Reblog = nil
	create := activityPubOutboxActivity(server, status)
	if create["type"] != "Create" {
		t.Fatalf("outbox non-boost activity = %#v", create)
	}
}

func TestActivityPubStatusVisibilityMatchesCoreStatusPolicyCases(t *testing.T) {
	server := &Server{}
	author := models.Account{ID: 10}
	signedAuthor := &models.Account{ID: 10}
	remote := &models.Account{ID: 20}

	cases := []struct {
		name   string
		status models.Status
		signed *models.Account
		want   bool
	}{
		{name: "anonymousPublic", status: models.Status{Visibility: 0}, want: true},
		{name: "anonymousUnlisted", status: models.Status{Visibility: 1}, want: true},
		{name: "anonymousPrivate", status: models.Status{Visibility: 2}, want: false},
		{name: "authorDirect", status: models.Status{Visibility: 3}, signed: signedAuthor, want: true},
		{name: "authorLimited", status: models.Status{Visibility: 4}, signed: signedAuthor, want: true},
		{name: "remotePublic", status: models.Status{Visibility: 0}, signed: remote, want: true},
		{name: "remoteUnlisted", status: models.Status{Visibility: 1}, signed: remote, want: true},
		{name: "remoteDirectUnmentioned", status: models.Status{Visibility: 3}, signed: remote, want: false},
		{name: "remoteLimitedUnmentioned", status: models.Status{Visibility: 4}, signed: remote, want: false},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got, err := server.activityPubStatusVisible(tt.status, author, tt.signed)
			if err != nil {
				t.Fatalf("activityPubStatusVisible error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("visible = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestActivityPubAcceptDetection(t *testing.T) {
	if !acceptsActivityPub(`application/ld+json; profile="https://www.w3.org/ns/activitystreams"`) {
		t.Fatal("ld+json activitystreams accept was not detected")
	}
	if !acceptsActivityPub("application/activity+json") {
		t.Fatal("activity+json accept was not detected")
	}
	if acceptsActivityPub("text/html, */*") {
		t.Fatal("html accept was treated as ActivityPub")
	}
}

func TestActivityPubActorMissingAccountDoesNotFallbackToHTMLForActivityAccept(t *testing.T) {
	e := echo.New()
	e.HTTPErrorHandler = handleAPIError
	s := &Server{}
	e.GET("/users/:username", s.activityPubActor)
	req := httptest.NewRequest(http.MethodGet, "/users/missing", nil)
	req.Header.Set("Accept", "application/activity+json")
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "<!DOCTYPE html>") {
		t.Fatalf("unexpected html fallback: %s", rec.Body.String())
	}
}

func TestInstanceActorResourceMatchesLocalAcct(t *testing.T) {
	if !instanceActorResource("mastodon.internal@example.com", "example.com") {
		t.Fatal("local instance actor acct did not match")
	}
	if !instanceActorResource("mastodon.internal", "example.com") {
		t.Fatal("bare instance actor acct did not match")
	}
	if instanceActorResource("mastodon.internal@remote.example", "example.com") {
		t.Fatal("remote instance actor acct matched")
	}
}

func TestActivityPubPinnedStatusPayloads(t *testing.T) {
	server := &Server{cfg: config.Config{Scheme: "https", WebDomain: "example.com", LocalDomain: "example.com"}}
	status := models.Status{
		ID:         123,
		AccountID:  42,
		Visibility: 0,
		Account:    models.Account{ID: 42, Username: "alice"},
	}

	add := activityPubAddPinnedStatus(server, status)
	if add["type"] != "Add" {
		t.Fatalf("add = %#v", add)
	}
	if add["actor"] != "https://example.com/users/alice" || add["object"] != "https://example.com/users/alice/statuses/123" || add["target"] != "https://example.com/users/alice/collections/featured" {
		t.Fatalf("add target = %#v", add)
	}
	if add["@context"] != "https://www.w3.org/ns/activitystreams" {
		t.Fatalf("add context should match Rails AddSerializer for Status: %#v", add["@context"])
	}
	if add["id"] != nil || add["to"] != nil || add["cc"] != nil {
		t.Fatalf("add should match Rails AddSerializer fields: %#v", add)
	}

	remove := activityPubRemovePinnedStatus(server, status)
	if remove["type"] != "Remove" {
		t.Fatalf("remove = %#v", remove)
	}
	if remove["actor"] != "https://example.com/users/alice" || remove["object"] != "https://example.com/users/alice/statuses/123" || remove["target"] != "https://example.com/users/alice/collections/featured" {
		t.Fatalf("remove target = %#v", remove)
	}
	if remove["@context"] != "https://www.w3.org/ns/activitystreams" {
		t.Fatalf("remove context should match Rails RemoveSerializer for Status: %#v", remove["@context"])
	}
	if remove["id"] != nil || remove["to"] != nil || remove["cc"] != nil {
		t.Fatalf("remove should match Rails RemoveSerializer fields: %#v", remove)
	}
}

func TestActivityPubLikeAndUndoPayloads(t *testing.T) {
	server := &Server{cfg: config.Config{Scheme: "https", WebDomain: "example.com", LocalDomain: "example.com"}}
	account := models.Account{ID: 42, Username: "alice"}
	status := models.Status{
		ID:  123,
		URI: sql.NullString{String: "https://remote.example/statuses/123", Valid: true},
		Account: models.Account{
			Username: "bob",
			Domain:   sql.NullString{String: "remote.example", Valid: true},
			URI:      "https://remote.example/users/bob",
		},
	}

	like := activityPubLike(server, account, status, 55)
	if like["id"] != "https://example.com/users/alice#likes/55" || like["type"] != "Like" {
		t.Fatalf("like = %#v", like)
	}
	if like["@context"] != "https://www.w3.org/ns/activitystreams" {
		t.Fatalf("like context should match Rails LikeSerializer: %#v", like["@context"])
	}
	if like["actor"] != "https://example.com/users/alice" || like["object"] != "https://remote.example/statuses/123" {
		t.Fatalf("like actor/object = %#v", like)
	}

	undo := activityPubUndoLike(server, account, status, 55)
	if undo["id"] != "https://example.com/users/alice#likes/55/undo" || undo["type"] != "Undo" {
		t.Fatalf("undo = %#v", undo)
	}
	if undo["@context"] != "https://www.w3.org/ns/activitystreams" {
		t.Fatalf("undo like context should match Rails UndoLikeSerializer: %#v", undo["@context"])
	}
	object := undo["object"].(map[string]any)
	if _, ok := object["@context"]; ok {
		t.Fatalf("undo like nested object should match Rails LikeSerializer without top-level context: %#v", object)
	}
	if object["type"] != "Like" || object["id"] != "https://example.com/users/alice#likes/55" || object["object"] != "https://remote.example/statuses/123" {
		t.Fatalf("undo object = %#v", object)
	}
}

func TestStatusMentionRefs(t *testing.T) {
	refs := statusMentionRefs("hi @Bob and @alice@example.com and again @bob @carol@remote.example @first.last@remote.example @name-with-dash")
	if len(refs) != 5 {
		t.Fatalf("refs = %#v", refs)
	}
	if refs[0] != (statusMentionRef{Username: "bob"}) {
		t.Fatalf("refs[0] = %#v", refs[0])
	}
	if refs[1] != (statusMentionRef{Username: "alice", Domain: "example.com"}) {
		t.Fatalf("refs[1] = %#v", refs[1])
	}
	if refs[2] != (statusMentionRef{Username: "carol", Domain: "remote.example"}) {
		t.Fatalf("refs[2] = %#v", refs[2])
	}
	if refs[3] != (statusMentionRef{Username: "first.last", Domain: "remote.example"}) {
		t.Fatalf("refs[3] = %#v", refs[3])
	}
	if refs[4] != (statusMentionRef{Username: "name-with-dash"}) {
		t.Fatalf("refs[4] = %#v", refs[4])
	}
}

func TestRemoteMentionAcct(t *testing.T) {
	server := &Server{cfg: config.Config{LocalDomain: "example.com", WebDomain: "web.example.com"}}
	if acct, ok := server.remoteMentionAcct(statusMentionRef{Username: "alice", Domain: "remote.example"}); !ok || acct != "alice@remote.example" {
		t.Fatalf("remote mention acct = %q ok=%v", acct, ok)
	}
	for _, ref := range []statusMentionRef{
		{Username: "alice"},
		{Username: "alice", Domain: "example.com"},
		{Username: "alice", Domain: "web.example.com"},
	} {
		if acct, ok := server.remoteMentionAcct(ref); ok || acct != "" {
			t.Fatalf("local mention should not fetch: ref=%#v acct=%q ok=%v", ref, acct, ok)
		}
	}
}

func TestAccountMatchesMentionRef(t *testing.T) {
	account := &models.Account{Username: "Alice", Domain: sql.NullString{String: "Remote.Example", Valid: true}}
	if !accountMatchesMentionRef(account, statusMentionRef{Username: "alice", Domain: "remote.example"}) {
		t.Fatalf("expected account to match mention")
	}
	if accountMatchesMentionRef(account, statusMentionRef{Username: "bob", Domain: "remote.example"}) {
		t.Fatalf("expected username mismatch")
	}
	if accountMatchesMentionRef(&models.Account{Username: "alice"}, statusMentionRef{Username: "alice", Domain: "remote.example"}) {
		t.Fatalf("expected local account not to match remote mention")
	}
}

func TestStatusHashtagRefs(t *testing.T) {
	refs := statusHashtagRefs("hello #GoLang and #golang plus #日本語 and #bad-tag")
	if len(refs) != 3 {
		t.Fatalf("refs = %#v", refs)
	}
	if refs[0] != (statusHashtagRef{Normalized: "golang", Display: "GoLang"}) {
		t.Fatalf("refs[0] = %#v", refs[0])
	}
	if refs[1] != (statusHashtagRef{Normalized: "日本語", Display: "日本語"}) {
		t.Fatalf("refs[1] = %#v", refs[1])
	}
	if refs[2] != (statusHashtagRef{Normalized: "bad", Display: "bad"}) {
		t.Fatalf("refs[2] = %#v", refs[2])
	}
}

func TestActivityPubStatusRecipientInboxesForDirectMentions(t *testing.T) {
	server := &Server{}
	status := models.Status{
		Visibility: 3,
		Mentions: []models.Mention{
			{Account: models.Account{ID: 1, Username: "local"}},
			{Account: models.Account{
				ID:             2,
				Username:       "alice",
				Domain:         sql.NullString{String: "remote.example", Valid: true},
				InboxURL:       "https://remote.example/users/alice/inbox",
				SharedInboxURL: "https://remote.example/inbox",
			}},
			{Account: models.Account{
				ID:             3,
				Username:       "bob",
				Domain:         sql.NullString{String: "remote.example", Valid: true},
				SharedInboxURL: "https://remote.example/inbox",
			}},
		},
	}
	inboxes, err := server.activityPubStatusRecipientInboxes(status)
	if err != nil {
		t.Fatalf("activityPubStatusRecipientInboxes error = %v", err)
	}
	if len(inboxes) != 1 || inboxes[0] != "https://remote.example/inbox" {
		t.Fatalf("inboxes = %#v", inboxes)
	}
}

func TestActivityPubPollUpdateMentionInboxesUsesRemoteActivityPubMentions(t *testing.T) {
	server := &Server{}
	status := models.Status{
		Visibility: 3,
		Mentions: []models.Mention{
			{Account: models.Account{ID: 1, Username: "local"}},
			{Account: models.Account{
				ID:             2,
				Username:       "alice",
				Domain:         sql.NullString{String: "remote.example", Valid: true},
				InboxURL:       "https://remote.example/users/alice/inbox",
				SharedInboxURL: "https://remote.example/inbox",
			}},
			{Account: models.Account{
				ID:             3,
				Username:       "bob",
				Domain:         sql.NullString{String: "remote.example", Valid: true},
				SharedInboxURL: "https://remote.example/inbox",
			}},
			{Account: models.Account{
				ID:          4,
				Username:    "suspended",
				Domain:      sql.NullString{String: "remote.example", Valid: true},
				InboxURL:    "https://remote.example/users/suspended/inbox",
				SuspendedAt: sql.NullTime{Time: time.Now(), Valid: true},
			}},
		},
	}
	inboxes, err := server.activityPubPollUpdateMentionInboxes(status)
	if err != nil {
		t.Fatalf("activityPubPollUpdateMentionInboxes error = %v", err)
	}
	if len(inboxes) != 1 || inboxes[0] != "https://remote.example/inbox" {
		t.Fatalf("inboxes = %#v", inboxes)
	}
}

func TestActivityPubPrivateStatusDeliverySynchronizesFollowersLikeRails(t *testing.T) {
	if !activityPubStatusDeliverySynchronizeFollowers(models.Status{Visibility: 2}, map[string]any{"type": "Create"}) {
		t.Fatal("private Create should synchronize followers")
	}
	if !activityPubStatusDeliverySynchronizeFollowers(models.Status{Visibility: 2}, map[string]any{"type": "Update"}) {
		t.Fatal("private Update should synchronize followers")
	}
	if activityPubStatusDeliverySynchronizeFollowers(models.Status{Visibility: 2}, map[string]any{"type": "Delete"}) {
		t.Fatal("private Delete should not use Rails DistributionWorker synchronization option")
	}
	if activityPubStatusDeliverySynchronizeFollowers(models.Status{Visibility: 0}, map[string]any{"type": "Create"}) {
		t.Fatal("public Create should not synchronize followers")
	}
	if got := activityPubInboxOrigin("https://Remote.Example/inbox"); got != "https://remote.example" {
		t.Fatalf("origin = %q", got)
	}
	if got := activityPubInboxOrigin("https://bücher.example:8443/inbox"); got != "https://bücher.example:8443" {
		t.Fatalf("IDN origin = %q", got)
	}
	if got := activityPubInboxOrigin("ftp://remote.example/inbox"); got != "" {
		t.Fatalf("non-http origin = %q", got)
	}
}

func TestActivityPubInboxSkipsUnknownActorDelete(t *testing.T) {
	server := &Server{}
	e := echo.New()
	body := `{"type":"Delete","actor":"https://remote.example/users/alice","object":"https://remote.example/users/alice"}`
	req := httptest.NewRequest(http.MethodPost, "/inbox", strings.NewReader(body))
	rec := httptest.NewRecorder()
	ctx := e.NewContext(req, rec)

	if err := server.activityPubInbox(ctx); err != nil {
		t.Fatalf("activityPubInbox error = %v", err)
	}
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Fatalf("Cache-Control = %q, want private, no-store", got)
	}
}

func TestActivityPubInboxRequiresSignatureForExpandedUnknownActorDeleteLikeRails(t *testing.T) {
	server := &Server{}
	e := echo.New()
	body := `{
		"@context":"https://www.w3.org/ns/activitystreams",
		"@type":"https://www.w3.org/ns/activitystreams#Delete",
		"https://www.w3.org/ns/activitystreams#actor":[{"@id":"https://remote.example/users/alice"}],
		"https://www.w3.org/ns/activitystreams#object":[{"@id":"https://remote.example/users/alice"}]
	}`
	req := httptest.NewRequest(http.MethodPost, "/inbox", strings.NewReader(body))
	rec := httptest.NewRecorder()
	ctx := e.NewContext(req, rec)

	err := server.activityPubInbox(ctx)
	if err == nil || !strings.Contains(err.Error(), "request not signed") {
		t.Fatalf("expanded unknown actor Delete must still require a valid actor signature like Rails, err=%v status=%d", err, rec.Code)
	}
	if rec.Code == http.StatusAccepted {
		t.Fatal("expanded unknown actor Delete must not be accepted before signature verification")
	}
}

func TestDeliverActivityPubAccountDeleteDeduplicatesInboxes(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("GenerateKey error = %v", err)
	}
	privatePEM := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	requests := []string{}
	oldClient := activityHTTPClient
	defer func() { activityHTTPClient = oldClient }()
	activityHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests = append(requests, r.URL.String())
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"type":"Delete"`) ||
			!strings.Contains(string(body), `"id":"https://example.com/users/alice#delete"`) ||
			!strings.Contains(string(body), `"object":"https://example.com/users/alice"`) {
			t.Fatalf("body = %s", body)
		}
		return &http.Response{StatusCode: http.StatusAccepted, Body: io.NopCloser(strings.NewReader(""))}, nil
	})}

	server := &Server{cfg: config.Config{Scheme: "https", WebDomain: "example.com", LocalDomain: "example.com"}}
	account := models.Account{Username: "alice", PrivateKey: sql.NullString{String: privatePEM, Valid: true}}
	inboxes := []string{" https://remote.example/inbox ", "https://remote.example/inbox", "https://relay.example/inbox", ""}
	if err := server.deliverActivityPubAccountDeleteToInboxes(account, inboxes); err != nil {
		t.Fatalf("deliverActivityPubAccountDeleteToInboxes error = %v", err)
	}
	if len(requests) != 2 {
		t.Fatalf("requests = %#v", requests)
	}
	if requests[0] != "https://remote.example/inbox" || requests[1] != "https://relay.example/inbox" {
		t.Fatalf("requests = %#v", requests)
	}
}

func TestActivityPubDeliveryAvailabilityDefaultsToAvailableWithoutDatabase(t *testing.T) {
	server := &Server{}
	if !server.activityPubDeliveryAvailable("remote.example") {
		t.Fatal("nil database should not block delivery")
	}
	if !server.activityPubDeliveryAvailable("https://remote.example/inbox") {
		t.Fatal("delivery availability should accept inbox URLs like Rails DeliveryFailureTracker")
	}
	server.trackActivityPubDeliveryFailure("remote.example")
	server.trackActivityPubDeliverySuccess("remote.example")
	server.trackActivityPubDeliverySuccess("https://remote.example/inbox")
}

func TestActivityPubDeliveryResponseDispositionMatchesRailsTracking(t *testing.T) {
	tests := []struct {
		name                       string
		status                     int
		sourcePermanentlySuspended bool
		want                       activityPubDeliveryResponseDisposition
	}{
		{name: "accepted", status: http.StatusAccepted, want: activityPubDeliveryResponseSucceeded},
		{name: "method not allowed", status: http.StatusMethodNotAllowed, want: activityPubDeliveryResponseDiscarded},
		{name: "gone", status: http.StatusGone, want: activityPubDeliveryResponseDiscarded},
		{name: "not implemented", status: http.StatusNotImplemented, want: activityPubDeliveryResponseDiscarded},
		{name: "unauthorized active source", status: http.StatusUnauthorized, want: activityPubDeliveryResponseRetry},
		{name: "unauthorized permanently suspended source", status: http.StatusUnauthorized, sourcePermanentlySuspended: true, want: activityPubDeliveryResponseDiscarded},
		{name: "request timeout", status: http.StatusRequestTimeout, want: activityPubDeliveryResponseRetry},
		{name: "too many requests", status: http.StatusTooManyRequests, want: activityPubDeliveryResponseRetry},
		{name: "internal server error", status: http.StatusInternalServerError, want: activityPubDeliveryResponseRetry},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := activityPubDeliveryResponseDispositionFor(tt.status, tt.sourcePermanentlySuspended)
			if got != tt.want {
				t.Fatalf("disposition for status %d suspended=%t = %d, want %d", tt.status, tt.sourcePermanentlySuspended, got, tt.want)
			}
		})
	}
}

func TestActivityPubDeliveryUnsalvageableResponseTracksFailureWithoutRetrying(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("GenerateKey error = %v", err)
	}
	privatePEM := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	local := models.Account{ID: 1, PrivateKey: sql.NullString{String: privatePEM, Valid: true}}

	for _, tt := range []struct {
		name       string
		status     int
		wantResult string
	}{
		{name: "accepted", status: http.StatusAccepted, wantResult: "success"},
		{name: "method not allowed", status: http.StatusMethodNotAllowed, wantResult: "failure"},
		{name: "gone", status: http.StatusGone, wantResult: "failure"},
		{name: "not implemented", status: http.StatusNotImplemented, wantResult: "failure"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatalf("Listen error = %v", err)
			}
			defer listener.Close()
			host, port, err := net.SplitHostPort(listener.Addr().String())
			if err != nil {
				t.Fatalf("SplitHostPort error = %v", err)
			}
			recordedKey := make(chan string, 1)
			go func() {
				if tcpListener, ok := listener.(*net.TCPListener); ok {
					_ = tcpListener.SetDeadline(time.Now().Add(time.Second))
				}
				conn, acceptErr := listener.Accept()
				if acceptErr != nil {
					recordedKey <- ""
					return
				}
				defer conn.Close()
				_ = conn.SetReadDeadline(time.Now().Add(time.Second))
				buffer := make([]byte, 4096)
				n, _ := conn.Read(buffer)
				_, _ = conn.Write([]byte(":1\r\n"))
				recordedKey <- string(buffer[:n])
			}()

			previousClient := activityHTTPClient
			activityHTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: tt.status,
					Body:       io.NopCloser(strings.NewReader("response")),
					Header:     make(http.Header),
				}, nil
			})}
			t.Cleanup(func() { activityHTTPClient = previousClient })

			server := &Server{cfg: config.Config{RedisHost: host, RedisPort: port, RedisNamespace: "mastodon:"}}
			if err := server.deliverActivityPubOnce(local, "https://remote.example/inbox", []byte(`{}`), "remote.example", false); err != nil {
				t.Fatalf("deliverActivityPubOnce error = %v", err)
			}
			command := <-recordedKey
			if !strings.Contains(command, ":delivery_stats:remote.example:"+tt.wantResult+":") {
				t.Fatalf("delivery stats command %q does not record %s", command, tt.wantResult)
			}
		})
	}
}

func TestActivityPubFollowAndUndoPayloads(t *testing.T) {
	server := &Server{cfg: config.Config{Scheme: "https", WebDomain: "example.com", LocalDomain: "example.com"}}
	local := models.Account{Username: "bob"}
	remote := models.Account{Username: "alice", Domain: sql.NullString{String: "remote.example", Valid: true}, URI: "https://remote.example/users/alice"}

	follow := activityPubFollowPayload(server, local, remote, 44, "")
	if follow["id"] != "https://example.com/users/bob#follows/44" || follow["type"] != "Follow" {
		t.Fatalf("follow = %#v", follow)
	}
	if follow["@context"] != "https://www.w3.org/ns/activitystreams" {
		t.Fatalf("follow context should match Rails FollowSerializer: %#v", follow["@context"])
	}
	if follow["actor"] != "https://example.com/users/bob" || follow["object"] != "https://remote.example/users/alice" {
		t.Fatalf("follow actor/object = %#v", follow)
	}

	undo := activityPubUndoFollowPayload(server, local, remote, 44, follow["id"].(string))
	if undo["type"] != "Undo" || undo["actor"] != "https://example.com/users/bob" {
		t.Fatalf("undo = %#v", undo)
	}
	if undo["@context"] != "https://www.w3.org/ns/activitystreams" {
		t.Fatalf("undo follow context should match Rails UndoFollowSerializer: %#v", undo["@context"])
	}
	object := undo["object"].(map[string]any)
	if object["id"] != follow["id"] || object["type"] != "Follow" || object["actor"] != "https://example.com/users/bob" || object["object"] != "https://remote.example/users/alice" {
		t.Fatalf("undo object = %#v", object)
	}
}

func TestActivityPubBlockPayloadMatchesRailsShape(t *testing.T) {
	server := &Server{cfg: config.Config{Scheme: "https", WebDomain: "example.com", LocalDomain: "example.com"}}
	local := models.Account{Username: "bob"}
	remote := models.Account{Username: "alice", Domain: sql.NullString{String: "remote.example", Valid: true}, URI: "https://remote.example/users/alice"}

	payload := activityPubBlockPayload(server, local, remote, 55, "")
	if payload["id"] != "https://example.com/users/bob#blocks/55" || payload["type"] != "Block" {
		t.Fatalf("block = %#v", payload)
	}
	if payload["@context"] != "https://www.w3.org/ns/activitystreams" {
		t.Fatalf("block context should match Rails BlockSerializer: %#v", payload["@context"])
	}
	if payload["actor"] != "https://example.com/users/bob" || payload["object"] != "https://remote.example/users/alice" {
		t.Fatalf("block actor/object = %#v", payload)
	}

	undo := activityPubUndoBlockPayload(server, local, remote, 55, "https://example.com/users/bob#blocks/custom")
	if undo["id"] != "https://example.com/users/bob#blocks/55/undo" || undo["type"] != "Undo" || undo["actor"] != "https://example.com/users/bob" {
		t.Fatalf("undo block = %#v", undo)
	}
	if undo["@context"] != "https://www.w3.org/ns/activitystreams" {
		t.Fatalf("undo block context should match Rails UndoBlockSerializer: %#v", undo["@context"])
	}
	object := undo["object"].(map[string]any)
	if _, ok := object["@context"]; ok {
		t.Fatalf("undo block nested object should match Rails BlockSerializer without top-level context: %#v", object)
	}
	if object["id"] != "https://example.com/users/bob#blocks/custom" || object["type"] != "Block" || object["actor"] != "https://example.com/users/bob" || object["object"] != "https://remote.example/users/alice" {
		t.Fatalf("undo block object = %#v", object)
	}
}

func TestDeliverActivityPubFollowResponseUsesSharedInboxAndStoredURI(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("GenerateKey error = %v", err)
	}
	privatePEM := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	oldClient := activityHTTPClient
	defer func() { activityHTTPClient = oldClient }()
	activityHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://remote.example/shared-inbox" {
			t.Fatalf("url = %s", r.URL.String())
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"type":"Reject"`) || !strings.Contains(string(body), `"id":"https://remote.example/activities/follow"`) {
			t.Fatalf("body = %s", body)
		}
		if !strings.Contains(string(body), `"@context":"https://www.w3.org/ns/activitystreams"`) {
			t.Fatalf("follow response context should match Rails Accept/Reject serializer: %s", body)
		}
		return &http.Response{StatusCode: http.StatusAccepted, Body: io.NopCloser(strings.NewReader(""))}, nil
	})}

	server := &Server{cfg: config.Config{Scheme: "https", WebDomain: "example.com", LocalDomain: "example.com"}}
	local := models.Account{Username: "bob", PrivateKey: sql.NullString{String: privatePEM, Valid: true}}
	remote := models.Account{
		Username:       "alice",
		Domain:         sql.NullString{String: "remote.example", Valid: true},
		URI:            "https://remote.example/users/alice",
		SharedInboxURL: "https://remote.example/shared-inbox",
	}
	if err := server.deliverActivityPubFollowResponse("Reject", local, remote, 0, "https://remote.example/activities/follow"); err != nil {
		t.Fatalf("deliverActivityPubFollowResponse error = %v", err)
	}
}

func mustMarshalPublicKey(t *testing.T, key *rsa.PublicKey) []byte {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(key)
	if err != nil {
		t.Fatalf("MarshalPKIXPublicKey error = %v", err)
	}
	return der
}

func TestFetchActivityActor(t *testing.T) {
	oldClient := activityHTTPClient
	defer func() { activityHTTPClient = oldClient }()
	activityHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if !strings.Contains(r.Header.Get("Accept"), "application/activity+json") {
			t.Fatalf("Accept = %q", r.Header.Get("Accept"))
		}
		body := activityTestJSON(`{
			"id":"https://remote.example/users/alice",
			"type":["https://www.w3.org/ns/activitystreams#Object","Person"],
			"preferredUsername":"alice",
			"name":"Alice",
			"summary":"Bio",
			"url":[{"type":"Link","href":"https://remote.example/@alice"}],
			"inbox":"https://remote.example/users/alice/inbox",
			"outbox":"https://remote.example/users/alice/outbox",
			"followers":"https://remote.example/users/alice/followers",
			"featured":"https://remote.example/users/alice/collections/featured",
			"manuallyApprovesFollowers":true,
			"discoverable":true,
			"indexable":true,
			"memorial":true,
			"attachment":[{"type":"PropertyValue","name":"Site","value":"https://example.com"}],
			"endpoints":{"sharedInbox":"https://remote.example/inbox"},
			"publicKey":{"id":"https://remote.example/users/alice#main-key","owner":"https://remote.example/users/alice","publicKeyPem":"PEM"}
		}`)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/activity+json"}},
			Body:       io.NopCloser(strings.NewReader(body)),
		}, nil
	})}

	actor, err := fetchActivityActor("https://remote.example/users/alice")
	if err != nil {
		t.Fatalf("fetchActivityActor error = %v", err)
	}
	if actor.PreferredUsername != "alice" || actor.PublicKey.PublicKeyPem != "PEM" || actor.SharedInbox() != "https://remote.example/inbox" ||
		!actor.ManuallyApprovesFollowers || !actor.Discoverable || !actor.Indexable || !actor.Memorial {
		t.Fatalf("actor = %#v", actor)
	}
	fields := activityProfileFields(actor.Attachment)
	if len(fields) != 1 || fields[0].Name != "Site" || fields[0].Value != "https://example.com" {
		t.Fatalf("actor attachment fields = %#v", fields)
	}
	if got := firstActivityActorURL(actor.URL, actor.ID); got != "https://remote.example/@alice" {
		t.Fatalf("actor url = %q", got)
	}
}

func TestParseRemoteActivityActorAcceptsExpandedJSONLDLikeInboxActorUpdate(t *testing.T) {
	actor, err := parseRemoteActivityActor([]byte(`{
		"@context":["https://www.w3.org/ns/activitystreams","https://w3id.org/security/v1"],
		"@id":"https://remote.example/users/alice",
		"@type":"https://www.w3.org/ns/activitystreams#Person",
		"https://www.w3.org/ns/activitystreams#preferredUsername":[{"@value":"alice"}],
		"https://www.w3.org/ns/activitystreams#name":[{"@value":"Alice"}],
		"https://www.w3.org/ns/activitystreams#summary":[{"@value":"Bio"}],
		"https://www.w3.org/ns/activitystreams#published":[{"@value":"2026-06-22T00:00:00Z"}],
		"https://www.w3.org/ns/activitystreams#url":[{"@type":"https://www.w3.org/ns/activitystreams#Link","https://www.w3.org/ns/activitystreams#href":[{"@value":"https://remote.example/@alice"}],"https://www.w3.org/ns/activitystreams#mediaType":[{"@value":"text/html"}]}],
		"https://www.w3.org/ns/activitystreams#inbox":[{"@id":"https://remote.example/users/alice/inbox"}],
		"https://www.w3.org/ns/activitystreams#outbox":[{"@id":"https://remote.example/users/alice/outbox"}],
		"https://www.w3.org/ns/activitystreams#following":[{"@id":"https://remote.example/users/alice/following"}],
		"https://www.w3.org/ns/activitystreams#followers":[{"@id":"https://remote.example/users/alice/followers"}],
			"https://joinmastodon.org/ns#featured":[{"@id":"https://remote.example/users/alice/collections/featured"}],
			"https://joinmastodon.org/ns#featuredTags":[{"@id":"https://remote.example/users/alice/collections/tags"}],
			"https://joinmastodon.org/ns#devices":[{"@id":"https://remote.example/users/alice/collections/devices"}],
		"https://www.w3.org/ns/activitystreams#endpoints":[{"@list":[{"https://www.w3.org/ns/activitystreams#sharedInbox":[{"@id":"https://remote.example/inbox"}]}]}],
		"https://www.w3.org/ns/activitystreams#manuallyApprovesFollowers":[{"@list":[{"@value":true}]}],
			"https://joinmastodon.org/ns#discoverable":[{"@list":[{"@value":true}]}],
			"https://joinmastodon.org/ns#indexable":[{"@list":[{"@value":true}]}],
			"https://joinmastodon.org/ns#memorial":[{"@list":[{"@value":true}]}],
		"https://www.w3.org/ns/activitystreams#alsoKnownAs":[{"@id":"https://old.example/users/alice"}],
		"https://www.w3.org/ns/activitystreams#icon":[{"@type":"https://www.w3.org/ns/activitystreams#Image","https://www.w3.org/ns/activitystreams#url":[{"@id":"https://remote.example/avatar.png"}]}],
		"https://www.w3.org/ns/activitystreams#image":[{"https://www.w3.org/ns/activitystreams#href":[{"@value":"https://remote.example/header.png"}]}],
			"https://www.w3.org/ns/activitystreams#tag":[{"@type":"https://joinmastodon.org/ns#Emoji","@id":"https://remote.example/emoji/party","https://www.w3.org/ns/activitystreams#name":[{"@value":":party:"}],"https://www.w3.org/ns/activitystreams#icon":[{"@type":"https://www.w3.org/ns/activitystreams#Image","https://www.w3.org/ns/activitystreams#url":[{"@id":"https://remote.example/emoji/party.png"}]}]}],
		"https://www.w3.org/ns/activitystreams#attachment":[{"@type":"http://schema.org#PropertyValue","https://www.w3.org/ns/activitystreams#name":[{"@value":"Site"}],"http://schema.org#value":[{"@value":"https://example.com"}]}],
		"https://w3id.org/security#publicKey":[{
			"@id":"https://remote.example/users/alice#main-key",
			"https://w3id.org/security#owner":[{"@id":"https://remote.example/users/alice"}],
			"https://w3id.org/security#publicKeyPem":[{"@value":"PEM"}]
		}]
	}`))
	if err != nil {
		t.Fatalf("parseRemoteActivityActor error = %v", err)
	}
	if actor.ID != "https://remote.example/users/alice" || actor.Type != "Person" || actor.PreferredUsername != "alice" || actor.Name != "Alice" || actor.Summary != "Bio" {
		t.Fatalf("expanded actor identity = %#v", actor)
	}
	if actor.Inbox != "https://remote.example/users/alice/inbox" || actor.Outbox != "https://remote.example/users/alice/outbox" ||
		actor.Following != "https://remote.example/users/alice/following" || actor.Followers != "https://remote.example/users/alice/followers" ||
		actor.Featured != "https://remote.example/users/alice/collections/featured" || actor.FeaturedTags != "https://remote.example/users/alice/collections/tags" ||
		actor.SharedInbox() != "https://remote.example/inbox" {
		t.Fatalf("expanded actor collections = %#v", actor)
	}
	if actor.Devices == "https://remote.example/users/alice/collections/devices" || !strings.Contains(actor.Devices, `"@id"=>"https://remote.example/users/alice/collections/devices"`) {
		t.Fatalf("expanded actor devices must use Rails raw cast instead of value_or_id, got %q", actor.Devices)
	}
	src, err := os.ReadFile("activitypub_signature.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, src, "parseRemoteActivityActorWithImageFetcher", `Devices:                   activityRailsActorDevicesURL(activityJSONLDValue(raw, "devices"))`) {
		t.Fatal("remote actor fetch parser must store devices_url through Rails raw string-cast helper, not value_or_id")
	}
	inlineFeaturedActor, err := parseRemoteActivityActor([]byte(activityTestJSON(`{
		"id":"https://remote.example/users/alice",
		"type":"Person",
		"preferredUsername":"alice",
		"inbox":"https://remote.example/users/alice/inbox",
		"featured":{
			"type":"Collection",
			"items":[
				"https://remote.example/users/alice/statuses/pinned",
				{"type":"Hashtag","name":"#Go"}
			]
		},
		"publicKey":{"publicKeyPem":"PEM"}
	}`)))
	if err != nil {
		t.Fatalf("parse inline featured actor error = %v", err)
	}
	if inlineFeaturedActor.Featured != "" || inlineFeaturedActor.FeaturedCollection == nil {
		t.Fatalf("inline featured collection should be preserved separately from featured URI, got %#v", inlineFeaturedActor)
	}
	if got := inlineFeaturedActor.FeaturedCollection.ItemURIs(); !reflect.DeepEqual(got, []string{"https://remote.example/users/alice/statuses/pinned"}) {
		t.Fatalf("inline featured collection item uris = %#v", got)
	}
	if got := activityPubFeaturedTagNamesFromTags(inlineFeaturedActor.FeaturedCollection.Tags); !reflect.DeepEqual(got, []activityPubFeaturedTagName{{Normalized: "go", Display: "go"}}) {
		t.Fatalf("inline featured collection tags = %#v", got)
	}
	bearcapDevices := "bear:?u=https%3A%2F%2Fremote.example%2Fusers%2Falice%2Fcollections%2Fdevices"
	bearcapActor, err := parseRemoteActivityActor([]byte(activityTestJSON(`{
		"id":"https://remote.example/users/alice",
		"type":"Person",
		"preferredUsername":"alice",
		"inbox":"https://remote.example/users/alice/inbox",
		"devices":"` + bearcapDevices + `",
		"publicKey":{"publicKeyPem":"PEM"}
	}`)))
	if err != nil {
		t.Fatalf("parse bearcap devices actor error = %v", err)
	}
	if bearcapActor.Devices != bearcapDevices {
		t.Fatalf("remote actor devices should preserve Rails raw value, got %#v", bearcapActor)
	}
	rawDevicesActor, err := parseRemoteActivityActor([]byte(activityTestJSON(`{
		"id":"https://remote.example/users/alice",
		"type":"Person",
		"preferredUsername":"alice",
		"inbox":"https://remote.example/users/alice/inbox",
		"devices":{"type":"Collection","id":"https://remote.example/users/alice/collections/devices"},
		"publicKey":{"publicKeyPem":"PEM"}
	}`)))
	if err != nil {
		t.Fatalf("parse raw devices actor error = %v", err)
	}
	if rawDevicesActor.Devices == "https://remote.example/users/alice/collections/devices" || !strings.Contains(rawDevicesActor.Devices, `"id"=>"https://remote.example/users/alice/collections/devices"`) {
		t.Fatalf("remote actor devices Link object must use Rails raw cast instead of value_or_id, got %q", rawDevicesActor.Devices)
	}
	if !actor.ManuallyApprovesFollowers || !actor.Discoverable || !actor.Indexable || !actor.Memorial {
		t.Fatalf("expanded actor flags = %#v", actor)
	}
	if actor.PublicKey.ID != "https://remote.example/users/alice#main-key" || actor.PublicKey.Owner != "https://remote.example/users/alice" || actor.PublicKey.PublicKeyPem != "PEM" {
		t.Fatalf("expanded public key = %#v", actor.PublicKey)
	}
	if actor.AvatarRemoteURL != "https://remote.example/avatar.png" || actor.HeaderRemoteURL != "https://remote.example/header.png" {
		t.Fatalf("expanded actor media = avatar:%q header:%q", actor.AvatarRemoteURL, actor.HeaderRemoteURL)
	}
	if got := firstActivityActorURL(actor.URL, actor.ID); got != "https://remote.example/@alice" {
		t.Fatalf("expanded actor URL = %q", got)
	}
	fields := activityProfileFields(actor.Attachment)
	if len(fields) != 1 || fields[0].Name != "Site" || fields[0].Value != "https://example.com" {
		t.Fatalf("expanded actor fields = %#v", fields)
	}
	if len(actor.Tags) != 1 || actor.Tags[0].Type != "Emoji" || actor.Tags[0].Name != ":party:" || actor.Tags[0].IconURL != "https://remote.example/emoji/party.png" {
		t.Fatalf("expanded actor tags = %#v", actor.Tags)
	}
	actor.Endpoints = map[string]any{
		"https://www.w3.org/ns/activitystreams#sharedInbox": []any{map[string]any{"@id": "https://remote.example/inbox-from-endpoints"}},
	}
	if actor.SharedInbox() != "https://remote.example/inbox-from-endpoints" {
		t.Fatalf("expanded actor endpoints shared inbox = %q", actor.SharedInbox())
	}
}

func TestFetchActivityActorForPublicKeyDocumentRejectsUnconfirmedOwnerLikeRails(t *testing.T) {
	oldClient := activityHTTPClient
	defer func() { activityHTTPClient = oldClient }()
	keyID := "https://remote.example/keys/alice"
	ownerURI := "https://remote.example/users/alice"
	activityHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.String() {
		case keyID:
			return textResponse(http.StatusOK, "application/activity+json", activityTestJSON(`{
					"id":"https://remote.example/keys/alice",
					"owner":"https://remote.example/users/alice",
					"publicKeyPem":"PEM-FROM-KEY"
				}`)), nil
		case ownerURI:
			return textResponse(http.StatusOK, "application/activity+json", activityTestJSON(`{
					"id":"https://remote.example/users/alice",
					"type":"Person",
					"preferredUsername":"alice",
					"inbox":"https://remote.example/users/alice/inbox",
					"publicKey":{"publicKeyPem":"PEM-FROM-OWNER"}
				}`)), nil
		default:
			t.Fatalf("unexpected actor/key fetch URL %q", r.URL.String())
			return nil, nil
		}
	})}

	if _, err := fetchActivityActorForPublicKeyDocument(keyID); err == nil || !strings.Contains(err.Error(), "public key not found") {
		t.Fatalf("expected unconfirmed owner error, got %v", err)
	}
}

func TestFetchActivityActorForPublicKeyDocumentKeepsRawOwnerLikeRails(t *testing.T) {
	oldClient := activityHTTPClient
	defer func() { activityHTTPClient = oldClient }()
	keyID := "https://remote.example/keys/alice"
	activityHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.String() {
		case keyID:
			return textResponse(http.StatusOK, "application/activity+json", activityTestJSON(`{
				"id":"https://remote.example/keys/alice",
				"owner":" https://remote.example/users/alice ",
				"publicKeyPem":"PEM-FROM-PADDED-OWNER"
			}`)), nil
		default:
			t.Fatalf("unexpected actor/key fetch URL %q", r.URL.String())
			return nil, nil
		}
	})}

	if _, err := fetchActivityActorForPublicKeyDocument(keyID); err == nil {
		t.Fatal("padded publicKey owner must be fetched raw and fail like Rails fetch_resource(owner_uri)")
	}
}

func TestFetchActivityActorForPublicKeyDocumentRejectsBearcapConfirmedOwnerLikeRails(t *testing.T) {
	oldClient := activityHTTPClient
	defer func() { activityHTTPClient = oldClient }()
	keyID := "https://remote.example/keys/alice"
	ownerURI := "https://remote.example/users/alice"
	bearcapKeyID := "bear:?u=https%3A%2F%2Fremote.example%2Fkeys%2Falice"
	activityHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.String() {
		case keyID:
			return textResponse(http.StatusOK, "application/activity+json", activityTestJSON(`{
				"id":"https://remote.example/keys/alice",
				"owner":"https://remote.example/users/alice",
				"publicKeyPem":"PEM-FROM-KEY"
			}`)), nil
		case ownerURI:
			return textResponse(http.StatusOK, "application/activity+json", activityTestJSON(`{
				"id":"https://remote.example/users/alice",
				"type":"Person",
				"preferredUsername":"alice",
				"inbox":"https://remote.example/users/alice/inbox",
				"publicKey":"`+bearcapKeyID+`"
			}`)), nil
		default:
			t.Fatalf("unexpected actor/key fetch URL %q", r.URL.String())
			return nil, nil
		}
	})}

	if _, err := fetchActivityActorForPublicKeyDocument(keyID); err == nil || !strings.Contains(err.Error(), "public key not found") {
		t.Fatalf("bearcap publicKey must not confirm owner like Rails value_or_id comparison, got %v", err)
	}
}

func TestFetchActivityActorDoesNotFollowHTMLAlternateLinkLikeRailsFetchRemoteActor(t *testing.T) {
	oldClient := activityHTTPClient
	defer func() { activityHTTPClient = oldClient }()
	requests := []string{}
	activityHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests = append(requests, r.URL.String())
		switch r.URL.String() {
		case "https://remote.example/@alice":
			return textResponse(http.StatusOK, "text/html; charset=utf-8", `<link rel="alternate" type="application/activity+json" href="/users/alice">`), nil
		case "https://remote.example/users/alice":
			return textResponse(http.StatusOK, "application/activity+json", activityTestJSON(`{
				"id":"https://remote.example/users/alice",
				"type":"Person",
				"preferredUsername":"alice",
				"inbox":"https://remote.example/users/alice/inbox",
				"publicKey":{"publicKeyPem":"PEM"}
			}`)), nil
		default:
			t.Fatalf("unexpected actor fetch URL %q", r.URL.String())
			return nil, nil
		}
	})}

	if _, err := fetchActivityActor("https://remote.example/@alice"); err == nil {
		t.Fatal("FetchRemoteActorService must not follow HTML alternate like FetchResourceService")
	}
	if strings.Join(requests, ",") != "https://remote.example/@alice" {
		t.Fatalf("requests = %#v", requests)
	}
}

func TestFetchActivityActorURLFromWebFingerRejectsOversizedBodiesLikeRails(t *testing.T) {
	oldClient := activityHTTPClient
	defer func() { activityHTTPClient = oldClient }()

	activityHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/.well-known/webfinger" {
			t.Fatalf("unexpected request: %s", r.URL.String())
		}
		resp := &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{}`)),
			Request:    r,
		}
		resp.ContentLength = maxActivityResourceBodySize + 1
		return resp, nil
	})}
	if _, err := fetchActivityActorURLFromWebFinger("alice@remote.example"); err == nil {
		t.Fatal("WebFinger Content-Length above Rails body_with_limit should be rejected")
	}

	activityHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.String() {
		case "https://remote.example/.well-known/webfinger?resource=acct%3Aalice%40remote.example":
			return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
		case "https://remote.example/.well-known/host-meta":
			return &http.Response{
				StatusCode:    http.StatusOK,
				Body:          io.NopCloser(strings.NewReader(strings.Repeat("x", maxActivityResourceBodySize+1))),
				ContentLength: -1,
				Request:       r,
			}, nil
		default:
			t.Fatalf("unexpected request: %s", r.URL.String())
			return nil, nil
		}
	})}
	if _, err := fetchActivityActorURLFromWebFinger("alice@remote.example"); err == nil {
		t.Fatal("host-meta body above Rails body_with_limit should be rejected")
	}
}

func TestFetchActivityActorURLFromWebFingerUsesRailsExactSelfLinkTypes(t *testing.T) {
	oldClient := activityHTTPClient
	defer func() { activityHTTPClient = oldClient }()
	activityHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := `{"subject":"acct:alice@remote.example","links":[` +
			`{"rel":"self","type":"application/activity+xml","href":"https://remote.example/users/wrong"},` +
			`{"rel":"self","type":"application/ld+json; profile=\"https://www.w3.org/ns/activitystreams\"","href":"https://remote.example/users/alice"}` +
			`]}`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}

	actorURL, err := fetchActivityActorURLFromWebFinger("alice@remote.example")
	if err != nil {
		t.Fatalf("fetchActivityActorURLFromWebFinger error = %v", err)
	}
	if actorURL != "https://remote.example/users/alice" {
		t.Fatalf("actorURL = %q", actorURL)
	}
}

func TestFetchActivityActorURLFromWebFingerFallsBackToHostMetaLikeRails(t *testing.T) {
	oldClient := activityHTTPClient
	defer func() { activityHTTPClient = oldClient }()
	requests := []string{}
	activityHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests = append(requests, r.URL.String())
		switch r.URL.String() {
		case "https://remote.example/.well-known/webfinger?resource=acct%3Aalice%40remote.example":
			return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader(""))}, nil
		case "https://remote.example/.well-known/host-meta":
			body := `<XRD xmlns="http://docs.oasis-open.org/ns/xri/xrd-1.0"><Link rel="lrdd" template="https://fallback.example/webfinger?resource={uri}" /></XRD>`
			return textResponse(http.StatusOK, "application/xrd+xml", body), nil
		case "https://fallback.example/webfinger?resource=acct:alice@remote.example":
			body := `{"subject":"acct:alice@remote.example","links":[{"rel":"self","type":"application/activity+json","href":"https://remote.example/users/alice"}]}`
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
		default:
			t.Fatalf("unexpected webfinger/host-meta request: %s", r.URL.String())
			return nil, nil
		}
	})}

	actorURL, err := fetchActivityActorURLFromWebFinger("alice@remote.example")
	if err != nil {
		t.Fatalf("fetchActivityActorURLFromWebFinger error = %v", err)
	}
	if actorURL != "https://remote.example/users/alice" {
		t.Fatalf("actorURL = %q", actorURL)
	}
	want := []string{
		"https://remote.example/.well-known/webfinger?resource=acct%3Aalice%40remote.example",
		"https://remote.example/.well-known/host-meta",
		"https://fallback.example/webfinger?resource=acct:alice@remote.example",
	}
	if strings.Join(requests, "\n") != strings.Join(want, "\n") {
		t.Fatalf("requests = %#v", requests)
	}
}

func TestActivityWebFingerHostMetaTemplateRequiresXRDNamespaceLikeRails(t *testing.T) {
	if got, err := activityWebFingerHostMetaTemplate([]byte(`<XRD xmlns="http://docs.oasis-open.org/ns/xri/xrd-1.0"><Link rel="lrdd" template="https://remote.example/webfinger?resource={uri}" /></XRD>`)); err != nil || got != "https://remote.example/webfinger?resource={uri}" {
		t.Fatalf("namespaced host-meta template = %q err=%v", got, err)
	}
	if got, err := activityWebFingerHostMetaTemplate([]byte(`<XRD><Link rel="lrdd" template="https://remote.example/webfinger?resource={uri}" /></XRD>`)); err == nil {
		t.Fatalf("unnamespaced host-meta should be rejected like Rails xmlns XPath, got %q", got)
	}
	if got, err := activityWebFingerHostMetaTemplate([]byte(`<XRD xmlns="https://example.com/not-xrd"><Link rel="lrdd" template="https://remote.example/webfinger?resource={uri}" /></XRD>`)); err == nil {
		t.Fatalf("wrong-namespace host-meta should be rejected like Rails xmlns XPath, got %q", got)
	}
}

func TestFetchActivityActorURLFromWebFingerNormalizesIDNDomain(t *testing.T) {
	oldClient := activityHTTPClient
	defer func() { activityHTTPClient = oldClient }()
	activityHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://xn--bcher-kva.example/.well-known/webfinger?resource=acct%3Aalice%40xn--bcher-kva.example" {
			t.Fatalf("unexpected webfinger request: %s", r.URL.String())
		}
		body := `{"subject":"acct:alice@bücher.example","links":[{"rel":"self","type":"application/activity+json","href":"https://xn--bcher-kva.example/users/alice"}]}`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}

	actorURL, err := fetchActivityActorURLFromWebFinger("alice@bücher.example")
	if err != nil {
		t.Fatalf("fetchActivityActorURLFromWebFinger error = %v", err)
	}
	if actorURL != "https://xn--bcher-kva.example/users/alice" {
		t.Fatalf("actorURL = %q", actorURL)
	}
}

func TestFetchActivityActorURLFromWebFingerFollowsOneSubjectRedirect(t *testing.T) {
	oldClient := activityHTTPClient
	defer func() { activityHTTPClient = oldClient }()
	requests := []string{}
	activityHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		resource := r.URL.Query().Get("resource")
		requests = append(requests, resource)
		switch resource {
		case "acct:alice@remote.example":
			body := `{"subject":"acct:alice@canonical.example","links":[{"rel":"self","type":"application/activity+json","href":"https://remote.example/users/wrong"}]}`
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
		case "acct:alice@canonical.example":
			body := `{"subject":"acct:alice@canonical.example","links":[{"rel":"self","type":"application/activity+json","href":"https://canonical.example/users/alice"}]}`
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
		default:
			t.Fatalf("unexpected webfinger resource %q", resource)
			return nil, nil
		}
	})}

	actorURL, err := fetchActivityActorURLFromWebFinger("alice@remote.example")
	if err != nil {
		t.Fatalf("fetchActivityActorURLFromWebFinger error = %v", err)
	}
	if actorURL != "https://canonical.example/users/alice" {
		t.Fatalf("actorURL = %q", actorURL)
	}
	if strings.Join(requests, ",") != "acct:alice@remote.example,acct:alice@canonical.example" {
		t.Fatalf("requests = %#v", requests)
	}
}

func TestFetchAndStoreActivityActorForAcctRejectsWebFingerLoopbackMismatch(t *testing.T) {
	oldClient := activityHTTPClient
	defer func() { activityHTTPClient = oldClient }()
	activityHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.String() {
		case "https://remote.example/.well-known/webfinger?resource=acct%3Aalice%40remote.example":
			body := `{"subject":"acct:alice@remote.example","links":[{"rel":"self","type":"application/activity+json","href":"https://remote.example/@alice"}]}`
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
		case "https://remote.example/@alice":
			return textResponse(http.StatusOK, "application/activity+json", activityTestJSON(`{
				"id":"https://remote.example/users/alice",
				"type":"Person",
				"preferredUsername":"alice",
				"inbox":"https://remote.example/users/alice/inbox",
				"publicKey":{"publicKeyPem":"PEM"}
			}`)), nil
		default:
			t.Fatalf("unexpected actor fetch URL %q", r.URL.String())
			return nil, nil
		}
	})}

	server := &Server{}
	if _, err := server.fetchAndStoreActivityActorForAcctDB(nil, "alice@remote.example"); err == nil || !strings.Contains(err.Error(), "loop back") {
		t.Fatalf("expected loopback error, got %v", err)
	}
}

func TestFetchActivityActorURLFromWebFingerPreservesGoneStatus(t *testing.T) {
	oldClient := activityHTTPClient
	defer func() { activityHTTPClient = oldClient }()
	activityHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusGone, Body: io.NopCloser(strings.NewReader(""))}, nil
	})}

	_, err := fetchActivityActorURLFromWebFinger("alice@remote.example")
	if !activityFetchGone(err) {
		t.Fatalf("expected 410 Gone fetch error, got %v", err)
	}
	if status, ok := activityFetchStatus(err); !ok || status != http.StatusGone {
		t.Fatalf("status = %d, %v", status, ok)
	}
}

func TestFetchActivityResourcePreservesHTTPStatus(t *testing.T) {
	oldClient := activityHTTPClient
	defer func() { activityHTTPClient = oldClient }()
	activityHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader(""))}, nil
	})}

	_, err := fetchActivityResource("https://remote.example/users/missing")
	if err == nil {
		t.Fatal("expected fetch error")
	}
	if activityFetchGone(err) {
		t.Fatalf("404 must not be classified as Gone: %v", err)
	}
	if status, ok := activityFetchStatus(err); !ok || status != http.StatusNotFound {
		t.Fatalf("status = %d, %v", status, ok)
	}
}

func TestActivityFetchUnsalvageableMatchesRails(t *testing.T) {
	tests := []struct {
		status int
		want   bool
	}{
		{http.StatusBadRequest, true},
		{http.StatusUnauthorized, false},
		{http.StatusForbidden, true},
		{http.StatusNotFound, true},
		{http.StatusRequestTimeout, false},
		{http.StatusGone, true},
		{http.StatusUnprocessableEntity, true},
		{http.StatusTooManyRequests, false},
		{http.StatusInternalServerError, false},
		{http.StatusNotImplemented, true},
		{http.StatusServiceUnavailable, false},
	}
	for _, tt := range tests {
		err := activityFetchHTTPError{StatusCode: tt.status, URL: "https://remote.example/statuses/1/replies"}
		if got := activityFetchUnsalvageable(err); got != tt.want {
			t.Errorf("status %d: activityFetchUnsalvageable() = %v, want %v", tt.status, got, tt.want)
		}
	}
	if activityFetchUnsalvageable(errors.New("network failure")) {
		t.Fatal("network errors must remain retryable")
	}
}

func TestActivityFetchPrivateIPRejected(t *testing.T) {
	old := activityPrivateAddressExceptions
	activityPrivateAddressExceptions = nil
	t.Cleanup(func() { activityPrivateAddressExceptions = old })

	for _, raw := range []string{
		"127.0.0.1",
		"0.0.0.1",
		"169.254.1.1",
		"10.0.0.1",
		"100.64.0.1",
		"172.16.0.1",
		"192.0.0.1",
		"192.0.2.1",
		"192.88.99.1",
		"192.168.1.10",
		"198.18.0.1",
		"198.51.100.1",
		"203.0.113.1",
		"224.0.0.1",
		"240.0.0.1",
		"255.255.255.255",
		"::",
		"::1",
		"::ffff:0.0.0.1",
		"64:ff9b::1",
		"100::1",
		"2001::1",
		"2001:10::1",
		"2001:20::1",
		"2001:db8::1",
		"2002::1",
		"fc00::1",
		"fe80::1",
		"ff00::1",
	} {
		ip := net.ParseIP(raw)
		if activityIPAllowed(ip) {
			t.Fatalf("expected %s to be rejected", raw)
		}
	}
	for _, raw := range []string{"8.8.8.8", "2001:4860:4860::8888"} {
		if !activityIPAllowed(net.ParseIP(raw)) {
			t.Fatalf("expected public IP %s to be allowed", raw)
		}
	}
}

func TestActivityFetchPrivateIPExceptionsTakePrecedence(t *testing.T) {
	old := activityPrivateAddressExceptions
	activityPrivateAddressExceptions = parseActivityPrivateAddressExceptions("0.0.0.0/8, 100::/64")
	t.Cleanup(func() { activityPrivateAddressExceptions = old })

	for _, raw := range []string{"0.0.0.1", "::ffff:0.0.0.1", "100::1"} {
		if !activityIPAllowed(net.ParseIP(raw)) {
			t.Fatalf("expected configured private address exception %s to be allowed", raw)
		}
	}
	if activityIPAllowed(net.ParseIP("127.0.0.1")) {
		t.Fatal("an unrelated private address must remain rejected")
	}
}

func TestActivityHTTPDialControlRejectsResolvedPrivateAddresses(t *testing.T) {
	old := activityPrivateAddressExceptions
	activityPrivateAddressExceptions = nil
	t.Cleanup(func() { activityPrivateAddressExceptions = old })

	control := activityHTTPDialControl("remote.example:443", nil)
	if control == nil {
		t.Fatal("direct connections must install a dial-time address check")
	}
	for _, address := range []string{"0.0.0.1:443", "[::ffff:0.0.0.1]:443", "[64:ff9b::1]:443"} {
		err := control(context.Background(), "tcp", address, nil)
		if !errors.Is(err, errActivityPrivateNetworkAddress) {
			t.Fatalf("resolved address %s error = %v, want private-network rejection", address, err)
		}
	}
	for _, address := range []string{"8.8.8.8:443", "[2001:4860:4860::8888]:443"} {
		if err := control(context.Background(), "tcp", address, nil); err != nil {
			t.Fatalf("public resolved address %s error = %v", address, err)
		}
	}
}

func TestActivityHTTPDialControlPreservesConfiguredProxyConnections(t *testing.T) {
	proxyAddresses := activityHTTPProxyDialAddresses(config.Config{
		HTTPProxyURL:       "http://127.0.0.1:8080",
		HTTPHiddenProxyURL: "https://proxy.example",
	})
	for _, address := range []string{"127.0.0.1:8080", "proxy.example:443"} {
		if control := activityHTTPDialControl(address, proxyAddresses); control != nil {
			t.Fatalf("configured proxy address %s must bypass direct-destination filtering", address)
		}
	}
	if control := activityHTTPDialControl("remote.example:443", proxyAddresses); control == nil {
		t.Fatal("non-proxy destinations must retain dial-time filtering")
	}
}

func TestActivityHTTPTransportUsesOnlyConfiguredProxySettings(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:8080")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:8443")

	transport := activityHTTPTransportFromConfig(config.Config{})
	if transport.Proxy != nil {
		t.Fatal("unconfigured process-wide proxy settings must not bypass Paon's proxy guard")
	}
}

func TestActivityHTTPClientChecksActualDialedAddress(t *testing.T) {
	old := activityPrivateAddressExceptions
	activityPrivateAddressExceptions = nil
	t.Cleanup(func() { activityPrivateAddressExceptions = old })

	requests := 0
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(origin.Close)

	client := activityHTTPClientFromConfig(config.Config{})
	t.Cleanup(client.CloseIdleConnections)
	if _, err := client.Get(origin.URL); !errors.Is(err, errActivityPrivateNetworkAddress) {
		t.Fatalf("actual loopback dial error = %v, want private-network rejection", err)
	}
	if requests != 0 {
		t.Fatalf("blocked private-address request reached local server %d times", requests)
	}

	activityPrivateAddressExceptions = parseActivityPrivateAddressExceptions("127.0.0.1")
	resp, err := client.Get(origin.URL)
	if err != nil {
		t.Fatalf("configured private-address exception did not permit actual dial: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("allowed private-address response = %d, want %d", resp.StatusCode, http.StatusNoContent)
	}
	if requests != 1 {
		t.Fatalf("allowed private-address request count = %d, want 1", requests)
	}
}

func TestActivityHTTPClientChecksActualRedirectTarget(t *testing.T) {
	old := activityPrivateAddressExceptions
	activityPrivateAddressExceptions = parseActivityPrivateAddressExceptions("127.0.0.1")
	t.Cleanup(func() { activityPrivateAddressExceptions = old })

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://[::ffff:0.0.0.1]/metadata", http.StatusFound)
	}))
	t.Cleanup(origin.Close)

	_, err := activityHTTPClientFromConfig(config.Config{}).Get(origin.URL)
	if err == nil {
		t.Fatal("actual redirect to an IPv4-mapped forbidden address was allowed")
	}
}

func TestDefaultActivityHTTPClientRejectsPrivateRedirects(t *testing.T) {
	oldProxyConfigured := activityHTTPProxyConfigured
	activityHTTPProxyConfigured = false
	t.Cleanup(func() { activityHTTPProxyConfigured = oldProxyConfigured })

	client := activityHTTPClientFromConfig(config.Config{})
	if client.CheckRedirect == nil {
		t.Fatal("default activity HTTP client must install a redirect guard")
	}
	previous, err := http.NewRequest(http.MethodGet, "https://remote.example/start", nil)
	if err != nil {
		t.Fatal(err)
	}
	redirect, err := http.NewRequest(http.MethodGet, "http://[::ffff:0.0.0.1]/metadata", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.CheckRedirect(redirect, []*http.Request{previous}); err == nil {
		t.Fatal("default activity HTTP client allowed a redirect to a forbidden address")
	}
	if err := client.CheckRedirect(nil, make([]*http.Request, 3)); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatalf("redirect limit error = %v, want http.ErrUseLastResponse", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestParseActivityPayloadFollowAndUndo(t *testing.T) {
	follow, err := parseActivityPayload([]byte(`{"id":"https://remote.example/activities/1","type":"Follow","actor":"https://remote.example/users/alice","object":"https://example.com/users/bob"}`))
	if err != nil {
		t.Fatalf("parse follow error = %v", err)
	}
	if follow.Type != "Follow" || follow.Actor != "https://remote.example/users/alice" || follow.Object.ID != "https://example.com/users/bob" {
		t.Fatalf("follow = %#v", follow)
	}

	undo, err := parseActivityPayload([]byte(`{"type":"Undo","actor":"https://remote.example/users/alice","object":{"id":"https://remote.example/activities/1","type":"Follow","actor":"https://remote.example/users/alice","object":"https://example.com/users/bob"}}`))
	if err != nil {
		t.Fatalf("parse undo error = %v", err)
	}
	if undo.Type != "Undo" || undo.Object.Type != "Follow" || undo.Object.TypeExact != "Follow" || undo.Object.ObjectID != "https://example.com/users/bob" {
		t.Fatalf("undo = %#v", undo)
	}

	undoAccept, err := parseActivityPayload([]byte(`{"type":"Undo","actor":"https://remote.example/users/alice","object":{"id":"https://remote.example/accepts/1","type":"Accept","actor":"https://remote.example/users/alice","object":"https://example.com/users/bob#follows/1"}}`))
	if err != nil {
		t.Fatalf("parse undo accept error = %v", err)
	}
	if undoAccept.Type != "Undo" || undoAccept.Object.Type != "Accept" || undoAccept.Object.TypeExact != "Accept" || undoAccept.Object.ObjectID != "https://example.com/users/bob#follows/1" {
		t.Fatalf("undo accept = %#v", undoAccept)
	}
	arrayUndo, err := parseActivityPayload([]byte(`{"type":"Undo","actor":"https://remote.example/users/alice","object":{"id":"https://remote.example/activities/array","type":["Follow"],"actor":"https://remote.example/users/alice","object":"https://example.com/users/bob"}}`))
	if err != nil {
		t.Fatalf("parse array undo error = %v", err)
	}
	if arrayUndo.Object.Type != "Follow" || arrayUndo.Object.TypeExact != "" || !arrayUndo.Object.TypePresent {
		t.Fatalf("array undo object type should not exact-dispatch like Rails Undo#perform case: %#v", arrayUndo.Object)
	}
}

func TestParseActivityPayloadLikeAndUndo(t *testing.T) {
	like, err := parseActivityPayload([]byte(`{"id":"https://remote.example/likes/1","type":"Like","actor":"https://remote.example/users/alice","object":"https://example.com/users/bob/statuses/123"}`))
	if err != nil {
		t.Fatalf("parse like error = %v", err)
	}
	if like.Type != "Like" || like.Actor != "https://remote.example/users/alice" || like.Object.ID != "https://example.com/users/bob/statuses/123" {
		t.Fatalf("like = %#v", like)
	}

	undo, err := parseActivityPayload([]byte(`{"type":"Undo","actor":"https://remote.example/users/alice","object":{"id":"https://remote.example/likes/1","type":"Like","actor":"https://remote.example/users/alice","object":"https://example.com/users/bob/statuses/123"}}`))
	if err != nil {
		t.Fatalf("parse undo like error = %v", err)
	}
	if undo.Type != "Undo" || undo.Object.Type != "Like" || undo.Object.TypeExact != "Like" || undo.Object.ObjectID != "https://example.com/users/bob/statuses/123" {
		t.Fatalf("undo like = %#v", undo)
	}
}

func TestParseActivityPayloadAnnounceAndUndo(t *testing.T) {
	announce, err := parseActivityPayload([]byte(`{"id":"https://remote.example/announces/1","type":"Announce","actor":"https://remote.example/users/alice","object":"https://example.com/users/bob/statuses/123"}`))
	if err != nil {
		t.Fatalf("parse announce error = %v", err)
	}
	if announce.Type != "Announce" || announce.Actor != "https://remote.example/users/alice" || announce.Object.ID != "https://example.com/users/bob/statuses/123" {
		t.Fatalf("announce = %#v", announce)
	}

	undo, err := parseActivityPayload([]byte(`{"type":"Undo","actor":"https://remote.example/users/alice","object":{"id":"https://remote.example/announces/1","type":"Announce","actor":"https://remote.example/users/alice","object":"https://example.com/users/bob/statuses/123"}}`))
	if err != nil {
		t.Fatalf("parse undo announce error = %v", err)
	}
	if undo.Type != "Undo" || undo.Object.Type != "Announce" || undo.Object.TypeExact != "Announce" || undo.Object.ObjectID != "https://example.com/users/bob/statuses/123" {
		t.Fatalf("undo announce = %#v", undo)
	}

	referenceUndo, err := parseActivityPayload([]byte(`{"type":"Undo","actor":"https://remote.example/users/alice","object":"https://remote.example/announces/1"}`))
	if err != nil {
		t.Fatalf("parse reference undo error = %v", err)
	}
	if referenceUndo.Type != "Undo" || referenceUndo.Object.Type != "" || referenceUndo.Object.TypeExact != "" || referenceUndo.Object.TypePresent || referenceUndo.Object.ID != "https://remote.example/announces/1" {
		t.Fatalf("reference undo = %#v", referenceUndo)
	}
}

func TestParseActivityPayloadCreateNote(t *testing.T) {
	payload, err := parseActivityPayload([]byte(`{
		"id":"https://remote.example/activities/2",
		"type":"Create",
		"actor":"https://remote.example/users/alice",
		"object":{
			"id":"https://remote.example/statuses/10",
			"type":"Note",
			"attributedTo":"https://remote.example/users/alice",
			"url":[{"type":"Link","href":"https://remote.example/@alice/10"}],
			"content":"<p>Hello<br>world</p>",
			"summary":"cw",
			"inReplyTo":"https://example.com/users/bob/statuses/1",
			"quoteUrl":"https://remote.example/statuses/quoted",
			"published":"2026-06-18T12:00:00Z",
			"updated":"2026-06-18T12:30:00Z",
			"language":"en",
			"sensitive":true,
			"to":["https://www.w3.org/ns/activitystreams#Public"],
			"cc":["https://remote.example/users/alice/followers"],
			"tag":[
				{"type":"Hashtag","name":"#GoLang","href":"https://remote.example/tags/GoLang"},
				{"type":"Mention","name":"@bob@example.com","href":"https://example.com/users/bob"}
			],
			"attachment":[
				{"type":"Document","mediaType":"image/png","url":"https://remote.example/media/1.png","name":"Alt text","blurhash":"hash","icon":{"type":"Image","url":"https://remote.example/media/1-small.png"}}
			]
		}
	}`))
	if err != nil {
		t.Fatalf("parse create error = %v", err)
	}
	if payload.Type != "Create" || payload.Object.Type != "Note" || payload.Object.ID != "https://remote.example/statuses/10" {
		t.Fatalf("payload = %#v", payload)
	}
	if payload.Object.AttributedTo != payload.Actor || payload.Object.URL != "https://remote.example/@alice/10" {
		t.Fatalf("object = %#v", payload.Object)
	}
	if payload.Object.Content != "<p>Hello<br>world</p>" || !payload.Object.Sensitive || len(payload.Object.To) != 1 {
		t.Fatalf("object = %#v", payload.Object)
	}
	if payload.Object.Updated != "2026-06-18T12:30:00Z" {
		t.Fatalf("updated = %q", payload.Object.Updated)
	}
	if payload.Object.QuoteURL != "https://remote.example/statuses/quoted" || activityPubQuoteURL(payload.Object) != "https://remote.example/statuses/quoted" {
		t.Fatalf("quote = %#v", payload.Object)
	}
	if len(payload.Object.Tags) != 2 || payload.Object.Tags[0].Type != "Hashtag" || payload.Object.Tags[1].Name != "@bob@example.com" {
		t.Fatalf("tags = %#v", payload.Object.Tags)
	}
	if len(payload.Object.Attachments) != 1 || payload.Object.Attachments[0].URL != "https://remote.example/media/1.png" || payload.Object.Attachments[0].IconURL != "https://remote.example/media/1-small.png" {
		t.Fatalf("attachments = %#v", payload.Object.Attachments)
	}
}

func TestActivityPubQuoteURLPriorityMatchesRailsInbound(t *testing.T) {
	note := activityObject{
		QuoteURI:        "https://remote.example/quote-uri",
		QuoteURL:        "https://remote.example/quote-url",
		MisskeyQuote:    "https://remote.example/misskey",
		QuoteURISet:     true,
		QuoteURLSet:     true,
		MisskeyQuoteSet: true,
	}
	if got := activityPubQuoteURL(note); got != "https://remote.example/quote-uri" {
		t.Fatalf("quote URL = %q", got)
	}
	note.QuoteURI = ""
	note.QuoteURISet = false
	if got := activityPubQuoteURL(note); got != "https://remote.example/quote-url" {
		t.Fatalf("quote URL = %q", got)
	}
	note.QuoteURL = ""
	note.QuoteURLSet = false
	if got := activityPubQuoteURL(note); got != "https://remote.example/misskey" {
		t.Fatalf("quote URL = %q", got)
	}
	bearcap := "bear:?u=https%3A%2F%2Fremote.example%2Fstatuses%2Fbear-quote&t=secret-token"
	payloadWithBearcap, err := parseActivityPayload([]byte(activityTestJSON(`{
		"type":"Create",
		"actor":"https://remote.example/users/alice",
		"object":{
			"id":"https://remote.example/statuses/bear-quote-wrapper",
			"type":"Note",
			"attributedTo":"https://remote.example/users/alice",
			"quoteUri":"` + bearcap + `"
		}
	}`)))
	if err != nil {
		t.Fatal(err)
	}
	if payloadWithBearcap.Object.QuoteURI != "https://remote.example/statuses/bear-quote" || payloadWithBearcap.Object.QuoteURIRaw != bearcap {
		t.Fatalf("bearcap quote fields = %#v", payloadWithBearcap.Object)
	}
	if got := activityPubQuoteURL(payloadWithBearcap.Object); got != bearcap {
		t.Fatalf("bearcap quote URL = %q", got)
	}
	payload, err := parseActivityPayload([]byte(activityTestJSON(`{
		"type":"Create",
		"actor":"https://remote.example/users/alice",
		"object":{
			"id":"https://remote.example/statuses/expanded-misskey-quote",
			"type":"Note",
			"attributedTo":"https://remote.example/users/alice",
			"https://misskey-hub.net/ns#_misskey_quote":[{"@id":"https://remote.example/statuses/misskey-expanded"}]
		}
	}`)))
	if err != nil {
		t.Fatal(err)
	}
	if payload.Object.MisskeyQuote != "https://remote.example/statuses/misskey-expanded" || activityPubQuoteURL(payload.Object) != "https://remote.example/statuses/misskey-expanded" {
		t.Fatalf("expanded misskey quote = %#v", payload.Object)
	}
	expandedTootQuote := parseActivityObject(map[string]any{
		"@id":   "https://remote.example/statuses/expanded-quote-uri",
		"@type": "https://www.w3.org/ns/activitystreams#Note",
		"http://joinmastodon.org/ns#quoteUri": []any{
			map[string]any{"@id": "https://remote.example/statuses/toot-quote-uri"},
		},
	})
	if expandedTootQuote.QuoteURI != "https://remote.example/statuses/toot-quote-uri" || activityPubQuoteURL(expandedTootQuote) != "https://remote.example/statuses/toot-quote-uri" {
		t.Fatalf("expanded toot quoteUri = %#v", expandedTootQuote)
	}
	expandedFEPQuote := parseActivityObject(map[string]any{
		"@id":   "https://remote.example/statuses/expanded-fep-quote",
		"@type": "https://www.w3.org/ns/activitystreams#Note",
		"https://w3id.org/fep/044f#quote": []any{
			map[string]any{"@id": "https://remote.example/statuses/fep-quote"},
		},
	})
	if expandedFEPQuote.QuoteURI != "https://remote.example/statuses/fep-quote" || activityPubQuoteURL(expandedFEPQuote) != "https://remote.example/statuses/fep-quote" {
		t.Fatalf("expanded FEP quote = %#v", expandedFEPQuote)
	}
	compactFEPQuote, err := parseActivityPayload([]byte(activityTestJSON(`{
		"type":"Create",
		"actor":"https://remote.example/users/alice",
		"object":{
			"id":"https://remote.example/statuses/compact-fep-quote",
			"type":"Note",
			"attributedTo":"https://remote.example/users/alice",
			"quote":"https://remote.example/statuses/fep-compact-quote"
		}
	}`)))
	if err != nil {
		t.Fatal(err)
	}
	if compactFEPQuote.Object.QuoteURI != "https://remote.example/statuses/fep-compact-quote" || activityPubQuoteURL(compactFEPQuote.Object) != "https://remote.example/statuses/fep-compact-quote" {
		t.Fatalf("compact FEP quote = %#v", compactFEPQuote.Object)
	}
}

func TestParseActivityPayloadEncryptedMessage(t *testing.T) {
	payload, err := parseActivityPayload([]byte(`{
		"type":"Create",
		"actor":"https://remote.example/users/alice",
		"object":{
			"type":"EncryptedMessage",
			"attributedTo":{"type":"Device","deviceId":"source-device"},
			"to":{"type":"Device","deviceId":"target-device"},
			"messageType":1,
			"cipherText":"ciphertext",
			"messageFranking":"remote-franking",
			"digest":{"type":"Digest","digestValue":"digest-value"}
		}
	}`))
	if err != nil {
		t.Fatalf("parse encrypted message error = %v", err)
	}
	object := payload.Object
	if payload.Type != "Create" || object.Type != "EncryptedMessage" || object.TypeExact != "EncryptedMessage" {
		t.Fatalf("payload = %#v", payload)
	}
	if object.SourceDeviceID != "source-device" || object.TargetDeviceID != "target-device" {
		t.Fatalf("device ids = %#v", object)
	}
	if object.MessageType != 1 || object.CipherText != "ciphertext" || object.MessageFranking != "remote-franking" || object.DigestValue != "digest-value" {
		t.Fatalf("encrypted fields = %#v", object)
	}

	expanded, err := parseActivityPayload([]byte(`{
		"@context":["https://www.w3.org/ns/activitystreams",{"toot":"http://joinmastodon.org/ns#"}],
		"@type":"https://www.w3.org/ns/activitystreams#Create",
		"https://www.w3.org/ns/activitystreams#actor":[{"@id":"https://remote.example/users/alice"}],
		"https://www.w3.org/ns/activitystreams#object":[{"@list":[{
			"@type":"http://joinmastodon.org/ns#EncryptedMessage",
			"https://www.w3.org/ns/activitystreams#attributedTo":[{"@list":[{
				"@type":"http://joinmastodon.org/ns#Device",
				"http://joinmastodon.org/ns#deviceId":[{"@list":[{"@value":"source-device"}]}]
			}]}],
			"https://www.w3.org/ns/activitystreams#to":[{"@list":[{
				"@type":"http://joinmastodon.org/ns#Device",
				"http://joinmastodon.org/ns#deviceId":[{"@list":[{"@value":"target-device"}]}]
			}]}],
			"http://joinmastodon.org/ns#messageType":[{"@list":[{"@value":1}]}],
			"http://joinmastodon.org/ns#cipherText":[{"@value":"ciphertext"}],
			"http://joinmastodon.org/ns#messageFranking":[{"@value":"remote-franking"}],
			"https://www.w3.org/ns/activitystreams#digest":[{"@list":[{
				"https://www.w3.org/ns/activitystreams#digestValue":[{"@list":[{"@value":"digest-value"}]}]
			}]}]
		}]}]
	}`))
	if err != nil {
		t.Fatalf("parse expanded encrypted message error = %v", err)
	}
	if expanded.Type != "Create" || expanded.Object.Type != "EncryptedMessage" || expanded.Object.TypeExact != "EncryptedMessage" ||
		expanded.Object.SourceDeviceID != "source-device" || expanded.Object.TargetDeviceID != "target-device" ||
		expanded.Object.MessageType != 1 || expanded.Object.CipherText != "ciphertext" ||
		expanded.Object.MessageFranking != "remote-franking" || expanded.Object.DigestValue != "digest-value" {
		t.Fatalf("expanded encrypted object = %#v", expanded.Object)
	}
	arrayType, err := parseActivityPayload([]byte(`{
		"type":"Create",
		"actor":"https://remote.example/users/alice",
		"object":{
			"type":["EncryptedMessage"],
			"to":{"type":"Device","deviceId":"target-device"}
		}
	}`))
	if err != nil {
		t.Fatalf("parse encrypted array type error = %v", err)
	}
	if arrayType.Object.Type != "EncryptedMessage" || arrayType.Object.TypeExact != "" || !arrayType.Object.TypePresent {
		t.Fatalf("encrypted array type should not exact-dispatch like Rails Create#perform case: %#v", arrayType.Object)
	}
}

func TestParseActivityPayloadUpdateActorAndDelete(t *testing.T) {
	update, err := parseActivityPayload([]byte(`{"type":"Update","actor":"https://remote.example/users/alice","object":{"id":"https://remote.example/users/alice","type":"Person","name":"Alice","summary":"Bio","url":"https://remote.example/@alice","published":"2026-06-18T12:00:00Z","inbox":"https://remote.example/users/alice/inbox","outbox":"https://remote.example/users/alice/outbox","following":"https://remote.example/users/alice/following","followers":"https://remote.example/users/alice/followers","endpoints":{"sharedInbox":"https://remote.example/inbox"},"featured":"https://remote.example/users/alice/collections/featured","featuredTags":"https://remote.example/users/alice/collections/tags","devices":"https://remote.example/users/alice/collections/devices","manuallyApprovesFollowers":true,"discoverable":true,"indexable":true,"memorial":true,"suspended":true,"movedTo":"https://remote.example/users/newalice","alsoKnownAs":["https://old.example/users/alice"," ",{"id":"https://old.example/users/alice"}],"icon":{"type":"Image","url":"https://remote.example/avatar.png"},"image":{"href":"https://remote.example/header.png"},"tag":[{"type":"Emoji","id":"https://remote.example/emoji/party","name":":party:","icon":{"type":"Image","url":"https://remote.example/emoji/party.png"},"updated":"2026-06-22T00:00:00Z"}],"attachment":[{"type":"PropertyValue","name":"Site","value":"https://example.com"},{"type":"Document","name":"ignored"}],"publicKey":{"https://w3id.org/security#publicKeyPem":[{"@value":"PEM"}]}}}`))
	if err != nil {
		t.Fatalf("parse update error = %v", err)
	}
	if update.Type != "Update" || update.Object.Type != "Person" || update.Object.Name != "Alice" || update.Object.PublicKey != "PEM" ||
		update.Object.Featured != "https://remote.example/users/alice/collections/featured" ||
		update.Object.Inbox != "https://remote.example/users/alice/inbox" ||
		update.Object.Outbox != "https://remote.example/users/alice/outbox" ||
		update.Object.Following != "https://remote.example/users/alice/following" ||
		update.Object.Followers != "https://remote.example/users/alice/followers" ||
		update.Object.SharedInbox != "https://remote.example/inbox" ||
		update.Object.Published != "2026-06-18T12:00:00Z" ||
		update.Object.AvatarRemoteURL != "https://remote.example/avatar.png" ||
		update.Object.HeaderRemoteURL != "https://remote.example/header.png" ||
		len(update.Object.Tags) != 1 || update.Object.Tags[0].IconURL != "https://remote.example/emoji/party.png" ||
		update.Object.FeaturedTags != "https://remote.example/users/alice/collections/tags" ||
		update.Object.Devices != "https://remote.example/users/alice/collections/devices" ||
		!update.Object.Locked || !update.Object.Discoverable || !update.Object.Indexable || !update.Object.Memorial || !update.Object.Suspended ||
		activityPubObjectID(update.Object.MovedTo) != "https://remote.example/users/newalice" ||
		!reflect.DeepEqual(activityRailsValueOrIDList(update.Object.AlsoKnownAs), []string{"https://old.example/users/alice", " ", "https://old.example/users/alice"}) ||
		!update.Object.ProfileFieldsSet || len(update.Object.ProfileFields) != 1 ||
		update.Object.ProfileFields[0].Name != "Site" || update.Object.ProfileFields[0].Value != "https://example.com" {
		t.Fatalf("update = %#v", update)
	}

	deleted, err := parseActivityPayload([]byte(`{"type":"Delete","actor":"https://remote.example/users/alice","object":{"id":"https://remote.example/statuses/10","type":"Tombstone","atomUri":"tag:remote.example,2026-06-21:objectId=10:objectType=Status"}}`))
	if err != nil {
		t.Fatalf("parse delete error = %v", err)
	}
	if deleted.Type != "Delete" || deleted.Object.ID != "https://remote.example/statuses/10" || deleted.Object.AtomURI != "tag:remote.example,2026-06-21:objectId=10:objectType=Status" {
		t.Fatalf("delete = %#v", deleted)
	}
}

func TestLocalUsernameFromActivityURI(t *testing.T) {
	server := &Server{cfg: config.Config{Scheme: "https", WebDomain: "example.com", LocalDomain: "example.com"}}
	if got := server.localUsernameFromActivityURI("https://example.com/users/alice"); got != "alice" {
		t.Fatalf("users URI = %q", got)
	}
	if got := server.localUsernameFromActivityURI("https://example.com/@bob"); got != "bob" {
		t.Fatalf("profile URI = %q", got)
	}
	if got := server.localUsernameFromActivityURI("https://remote.example/users/alice"); got != "" {
		t.Fatalf("remote URI = %q", got)
	}

	localOnly := &Server{cfg: config.Config{Scheme: "https", LocalDomain: "local.example"}}
	if got := localOnly.localUsernameFromActivityURI("https://local.example/users/carol"); got != "carol" {
		t.Fatalf("local-domain-only URI = %q", got)
	}
	if !localOnly.localActivityHost("LOCAL.EXAMPLE") {
		t.Fatal("local activity host should match case-insensitively")
	}
	idnLocal := &Server{cfg: config.Config{Scheme: "https", LocalDomain: "xn--bcher-kva.example"}}
	if got := idnLocal.localUsernameFromActivityURI("https://bücher.example/users/dora"); got != "dora" {
		t.Fatalf("IDN local URI = %q", got)
	}
	if idnLocal.localActivityHost("bücher.example:8443") {
		t.Fatal("local activity host should keep ports significant after IDN normalization")
	}
}

func TestActivityPubPlainText(t *testing.T) {
	got := activityPubPlainText("<p>Hello<br>world &amp; friends</p><script>ignored</script>")
	if got != "Hello\nworld & friends\nignored" {
		t.Fatalf("plain text = %q", got)
	}
}

func railsActivityPubSerializerKeys(t *testing.T, path string) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	keys := map[string]bool{}
	for _, block := range railsActivityPubAttributeBlocks(src) {
		for _, match := range railsActivityPubSymbolPattern.FindAllStringSubmatch(block, -1) {
			keys[activityPubJSONKey(match[1])] = true
		}
	}
	for _, pattern := range []*regexp.Regexp{
		railsActivityPubAttributePattern,
		railsActivityPubHasOnePattern,
		railsActivityPubHasManyPattern,
	} {
		for _, match := range pattern.FindAllStringSubmatch(src, -1) {
			key := firstNonEmpty(match[2], match[1])
			if key != "" {
				keys[activityPubJSONKey(key)] = true
			}
		}
	}
	return keys
}

func railsActivityPubAttributeBlocks(src string) []string {
	blocks := []string{}
	lines := strings.Split(src, "\n")
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(lines[i], "  attributes ") {
			continue
		}
		block := strings.TrimPrefix(line, "attributes ")
		for i+1 < len(lines) {
			nextRaw := lines[i+1]
			next := strings.TrimSpace(nextRaw)
			if next == "" || railsActivityPubTopLevelDirective(nextRaw) {
				break
			}
			i++
			block += " " + next
		}
		blocks = append(blocks, block)
	}
	return blocks
}

func railsActivityPubTopLevelDirective(line string) bool {
	if !strings.HasPrefix(line, "  ") || strings.HasPrefix(line, "    ") {
		return false
	}
	trimmed := strings.TrimSpace(line)
	for _, prefix := range []string{"attribute ", "has_one ", "has_many ", "belongs_to ", "delegate ", "def ", "class ", "private", "end"} {
		if strings.HasPrefix(trimmed, prefix) {
			return true
		}
	}
	return false
}

func activityPubJSONKey(key string) string {
	if key == "" || strings.HasPrefix(key, "_") || strings.ContainsAny(key, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") {
		return key
	}
	parts := strings.Split(key, "_")
	out := parts[0]
	for _, part := range parts[1:] {
		if part == "" {
			continue
		}
		out += strings.ToUpper(part[:1]) + part[1:]
	}
	return out
}

func activityPubObjectKeys(object map[string]any) map[string]bool {
	keys := map[string]bool{}
	addActivityPubObjectKeys(keys, object)
	return keys
}

func assertActivityPubPayloadCoversRailsSerializerKeys(t *testing.T, serializerPath string, payload map[string]any) {
	t.Helper()
	keys := map[string]bool{}
	addActivityPubRecursiveObjectKeys(keys, payload)
	for key := range railsActivityPubSerializerKeys(t, serializerPath) {
		if !keys[key] {
			t.Fatalf("%s missing Rails serializer key %q; got %#v", serializerPath, key, keys)
		}
	}
}

func activityPubResponseObject(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	out := map[string]any{}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func addActivityPubObjectKeys(keys map[string]bool, object map[string]any) {
	for key := range object {
		keys[key] = true
	}
}

func addActivityPubRecursiveObjectKeys(keys map[string]bool, value any) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			keys[key] = true
			addActivityPubRecursiveObjectKeys(keys, child)
		}
	case []any:
		for _, child := range typed {
			addActivityPubRecursiveObjectKeys(keys, child)
		}
	}
}
