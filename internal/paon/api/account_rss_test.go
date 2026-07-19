package api

import (
	"database/sql"
	"encoding/json"
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

func TestAccountNameFromPublicPathDetectsRSS(t *testing.T) {
	name, rss := accountNameFromPublicPath("alice.rss")
	if name != "alice" || !rss {
		t.Fatalf("name = %q rss = %v", name, rss)
	}
	name, rss = accountNameFromPublicPath("alice@example.com")
	if name != "alice@example.com" || rss {
		t.Fatalf("name = %q rss = %v", name, rss)
	}
}

func TestSetPublicRSSCacheUsesRailsHeaderShape(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/@alice.rss", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())

	setPublicRSSCache(c, 60)

	if got := rec.Header().Get("Cache-Control"); got != "max-age=60, public" {
		t.Fatalf("Cache-Control = %q", got)
	}
}

func TestPublicRSSLimitMatchesRailsAccountsAndTagsControllers(t *testing.T) {
	e := echo.New()
	for _, tt := range []struct {
		target string
		want   int
	}{
		{"/@alice.rss", 20},
		{"/@alice.rss?limit=", 20},
		{"/@alice.rss?limit=+++%09", 20},
		{"/@alice.rss?limit=bad", 0},
		{"/@alice.rss?limit=12abc", 12},
		{"/@alice.rss?limit=500", 200},
		{"/@alice.rss?limit=-5", 0},
		{"/tags/go.rss?limit=150", 150},
		{"/tags/go.rss?limit=bad", 0},
	} {
		req := httptest.NewRequest(http.MethodGet, tt.target, nil)
		c := echo.NewContext(req, httptest.NewRecorder(), e)
		if got := publicRSSLimit(c); got != tt.want {
			t.Fatalf("publicRSSLimit(%s) = %d, want %d", tt.target, got, tt.want)
		}
	}
}

func TestAccountAndTagRSSHandlersSetRailsCacheHeaders(t *testing.T) {
	for _, check := range []struct {
		file string
		fn   string
		want string
	}{
		{"account_rss.go", "publicAccountRSS", `setPublicRSSCache(c, 60)`},
		{"tags.go", "publicTagRSS", `setPublicRSSCache(c, 0)`},
	} {
		src, err := os.ReadFile(check.file)
		if err != nil {
			t.Fatal(err)
		}
		if !functionBodyContains(t, src, check.fn, check.want) {
			t.Fatalf("%s:%s missing %q", check.file, check.fn, check.want)
		}
	}
}

func TestPublicAccountRSSHydratesCustomEmojisBeforeRendering(t *testing.T) {
	src, err := os.ReadFile("account_rss.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, src, "publicAccountRSS", `if err := s.hydrateStatusesCustomEmojis(statuses); err != nil`) {
		t.Fatal("account RSS must hydrate status custom emojis before rendering like Rails AccountStatusesFilter/RSS flow")
	}
	if !functionBodyContains(t, src, "renderAccountRSS", `Description:   statusRSSDescriptionWithConfig(s.cfg, status)`) {
		t.Fatal("account RSS must render descriptions with config-aware custom emoji URLs")
	}
}

func TestPublicAccountHiddenMatchesAccountOwnedConcernVisibility(t *testing.T) {
	now := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)
	s := &Server{}
	for _, tt := range []struct {
		name    string
		account *models.Account
		hidden  bool
	}{
		{name: "nil", hidden: true},
		{name: "confirmed", account: &models.Account{ID: 1, User: models.User{ID: 10, Approved: true, ConfirmedAt: sql.NullTime{Time: now, Valid: true}}}},
		{name: "pending", account: &models.Account{ID: 2, User: models.User{ID: 20, Approved: false, ConfirmedAt: sql.NullTime{Time: now, Valid: true}}}, hidden: true},
		{name: "unconfirmed", account: &models.Account{ID: 3, User: models.User{ID: 30, Approved: true}}, hidden: true},
		{name: "suspended", account: &models.Account{ID: 4, User: models.User{ID: 40, Approved: true, ConfirmedAt: sql.NullTime{Time: now, Valid: true}}, SuspendedAt: sql.NullTime{Time: now, Valid: true}}, hidden: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := s.publicAccountHidden(tt.account); got != tt.hidden {
				t.Fatalf("hidden = %v, want %v", got, tt.hidden)
			}
		})
	}
}

func TestPublicAccountHandlersApplyAccountOwnedConcernGuards(t *testing.T) {
	src, err := os.ReadFile("account_rss.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, fn := range []string{
		"publicAccountFollowCollection",
		"publicAccountTagged",
		"publicAccountMaybeRSS",
		"publicAccountActivityPub",
	} {
		if !functionBodyContains(t, src, fn, "s.requirePublicAccountVisible") {
			t.Fatalf("%s must apply AccountOwnedConcern visibility guards", fn)
		}
	}
	for _, fn := range []string{"publicAccountRSS", "setPublicAccountLinkHeader"} {
		if !functionBodyContains(t, src, fn, "s.publicAccountHidden(account)") {
			t.Fatalf("%s must not expose hidden local accounts", fn)
		}
	}
	activityPubSrc, err := os.ReadFile("activitypub.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, activityPubSrc, "localActivityPubAccountWithSuspensionMode", "s.activityPubAccountOwnedGuard(c, account, skipTemporarySuspension)") {
		t.Fatal("ActivityPub account collections must apply AccountOwnedConcern guards")
	}
}

func TestPublicAccountRSSStatusesMatchRailsAccountFilters(t *testing.T) {
	src, err := os.ReadFile("account_rss.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`statuses.visibility IN ?`,
		`statuses.reblog_of_id IS NULL`,
		`statuses.reply = false OR statuses.in_reply_to_account_id = statuses.account_id`,
		`account_rss_media.status_id = statuses.id`,
		`account_rss_media.account_id = ?`,
		`accountStatusTagQueryValue(opts.Tag)`,
		`query = query.Where(statusHasTagSQL(), tag)`,
	} {
		if !functionBodyContains(t, src, "publicAccountRSSStatuses", want) {
			t.Fatalf("publicAccountRSSStatuses missing Rails account RSS condition %q", want)
		}
	}
	if functionBodyContains(t, src, "publicAccountRSSStatuses", `statuses.in_reply_to_id IS NULL`) {
		t.Fatal("account RSS must use Rails Status.without_replies and keep self replies")
	}
}

func TestRenderAccountRSSIncludesAccountAndStatuses(t *testing.T) {
	s := &Server{cfg: config.Config{Scheme: "https", WebDomain: "example.com", LocalDomain: "example.com"}}
	account := models.Account{
		ID:          10,
		Username:    "alice",
		DisplayName: "Alice",
		AvatarFileName: sql.NullString{
			String: "avatar.png",
			Valid:  true,
		},
	}
	status := models.Status{
		ID:                        123,
		Text:                      "hello <world>",
		CreatedAt:                 time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC),
		Account:                   account,
		Sensitive:                 true,
		OrderedMediaAttachmentIDs: models.Int64Array{9, 8},
		Tags: []models.Tag{{
			Name:        "golang",
			DisplayName: sql.NullString{String: "GoLang", Valid: true},
		}},
		MediaAttachments: []models.MediaAttachment{
			{
				ID:              8,
				Type:            0,
				FileFileName:    sql.NullString{String: "photo.png", Valid: true},
				FileContentType: sql.NullString{String: "image/png", Valid: true},
				FileFileSize:    sql.NullInt64{Int64: 1234, Valid: true},
				Description:     sql.NullString{String: "alt text", Valid: true},
				ThumbnailFileName: sql.NullString{
					String: "thumb.png",
					Valid:  true,
				},
			},
			{
				ID:              9,
				Type:            4,
				FileFileName:    sql.NullString{String: "sound.mp3", Valid: true},
				FileContentType: sql.NullString{String: "audio/mpeg", Valid: true},
				FileFileSize:    sql.NullInt64{Int64: 4567, Valid: true},
			},
		},
	}

	body, err := s.renderAccountRSS(account, accountRSSOptions{}, []models.Status{status})
	if err != nil {
		t.Fatal(err)
	}
	xml := string(body)
	for _, want := range []string{
		`<title>Alice</title>`,
		`xmlns:webfeeds="http://webfeeds.org/rss/1.0"`,
		`<description>Public posts from @alice@example.com</description>`,
		`<generator>Mastodon v4.2.27</generator>`,
		`<url>https://example.com/system/accounts/avatars/000/000/010/original/avatar.png</url>`,
		`<webfeeds:icon>https://example.com/system/accounts/avatars/000/000/010/original/avatar.png</webfeeds:icon>`,
		`<link>https://example.com/@alice/123</link>`,
		`<description>&lt;p&gt;hello &amp;lt;world&amp;gt;&lt;/p&gt;</description>`,
		`xmlns:media="http://search.yahoo.com/mrss/"`,
		`<enclosure url="https://example.com/system/media_attachments/files/000/000/009/original/sound.mp3" length="4567" type="audio/mpeg"></enclosure>`,
		`<media:content url="https://example.com/system/media_attachments/files/000/000/008/original/photo.png" type="image/png" fileSize="1234" medium="image">`,
		`<media:rating scheme="urn:simple">adult</media:rating>`,
		`<media:description type="plain">alt text</media:description>`,
		`<media:thumbnail url="/system/media_attachments/thumbnails/000/000/008/original/thumb.png"></media:thumbnail>`,
		`<category>GoLang</category>`,
	} {
		if !strings.Contains(xml, want) {
			t.Fatalf("RSS body missing %q: %s", want, xml)
		}
	}
	if strings.Contains(xml, "<title>Status by ") {
		t.Fatalf("RSS item must not include Go-only status titles: %s", xml)
	}
	if strings.Contains(xml, "xmlns:atom=") {
		t.Fatalf("RSS root must not include Go-only atom namespace: %s", xml)
	}
	guidIndex := strings.Index(xml, `<guid isPermaLink="true">https://example.com/@alice/123</guid>`)
	linkIndex := strings.Index(xml, `<link>https://example.com/@alice/123</link>`)
	if guidIndex == -1 || linkIndex == -1 || guidIndex > linkIndex {
		t.Fatalf("RSS item must emit guid before link like Rails: %s", xml)
	}
}

func TestAccountRSSDescriptionUsesRailsLocalUsernameAndDomain(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.com", WebDomain: "web.example.com"}
	local := models.Account{Username: "alice"}
	if got := accountRSSLocalUsernameAndDomain(cfg, local); got != "alice@example.com" {
		t.Fatalf("local account description acct = %q", got)
	}

	remote := models.Account{
		Username: "bob",
		Domain:   sql.NullString{String: "remote.example", Valid: true},
	}
	if got := accountRSSLocalUsernameAndDomain(cfg, remote); got != "bob@remote.example" {
		t.Fatalf("remote account description acct = %q", got)
	}
}

func TestPublicAccountHTMLRouteStillServesShell(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/@alice", nil)
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

	req = httptest.NewRequest(http.MethodGet, "/@alice", nil)
	req.AddCookie(&http.Cookie{Name: railsSessionCookieName, Value: "session"})
	rec = httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)
	if got := rec.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Fatalf("session Cache-Control = %q, want private, no-store", got)
	}
}

func TestRemoteAcctPublicPathsServeReactShellLikeRailsRouteConstraint(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{
		"/@alice@example.com",
		"/@alice.example",
		"/@alice@example.com/followers",
		"/@alice@example.com/following",
		"/@alice@example.com/followers.json",
		"/@alice@example.com/following.json",
		"/@alice@example.com/followers.rss",
		"/@alice@example.com/following.rss",
		"/@alice@example.com/tagged/go",
		"/@alice@example.com.json",
		"/@alice@example.com.rss",
		"/@alice.example.json",
		"/@alice.example.rss",
		"/@alice@example.com/123",
		"/@alice@example.com/123.json",
	} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()
			s.echo.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
				t.Fatalf("content-type = %q", got)
			}
			if !strings.Contains(rec.Body.String(), `id="mastodon"`) {
				t.Fatalf("remote acct path did not render React shell: %s", rec.Body.String())
			}
		})
	}
}

func TestPublicAccountRoutesRequireAuthenticationInLimitedFederationMode(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com", WebDomain: "example.com", Scheme: "https", LimitedFederationMode: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/@alice", "/@alice.rss?limit=1", "/@alice/with_replies.rss", "/@alice/media.rss", "/@alice/tagged/go", "/@alice/followers", "/@alice/following"} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()
			s.echo.ServeHTTP(rec, req)
			want := "/auth/sign_in?redirect_to=" + url.QueryEscape(path)
			if rec.Code != http.StatusFound || rec.Header().Get("Location") != want {
				t.Fatalf("status = %d location = %q want %q", rec.Code, rec.Header().Get("Location"), want)
			}
		})
	}

	src, err := os.ReadFile("account_rss.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, fn := range []string{"publicAccountFollowCollection", "publicAccountTagged", "publicAccountMaybeRSS", "publicAccountRSS"} {
		if !functionBodyContains(t, src, fn, `s.requirePublicAccountAuthenticationIfLimited(c)`) {
			t.Fatalf("%s must match Rails AccountsController limited_federation require_functional! gate", fn)
		}
	}
}

func TestPublicAccountLinkHeaderMatchesRailsDiscoveryLinks(t *testing.T) {
	cfg := config.Config{Scheme: "https", WebDomain: "example.com", LocalDomain: "example.com"}
	account := models.Account{ID: 10, Username: "alice"}
	got := publicAccountLinkHeader(cfg, account)
	want := `<https://example.com/.well-known/webfinger?resource=acct%3Aalice%40example.com>; rel="lrdd"; type="application/jrd+json", <https://example.com/users/alice>; rel="alternate"; type="application/activity+json"`
	if got != want {
		t.Fatalf("Link = %q, want %q", got, want)
	}
}

func TestPublicAccountRSSRouteDoesNotServeHTMLShell(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{"/@alice.rss", "/@alice/with_replies.rss", "/@alice/media.rss"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		s.echo.ServeHTTP(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d body = %s", path, rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("Content-Type"); strings.HasPrefix(got, "text/html") {
			t.Fatalf("%s content-type = %q", path, got)
		}
	}
}

func TestPublicAccountActivityPubRouteDoesNotServeHTMLShell(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/@alice", nil)
	req.Header.Set("Accept", "application/activity+json")
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); strings.HasPrefix(got, "text/html") {
		t.Fatalf("content-type = %q", got)
	}
}

func TestPublicAccountFollowCollectionsNegotiateActivityPub(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{"/@alice/followers", "/@alice/following"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Accept", "application/activity+json")
		rec := httptest.NewRecorder()
		s.echo.ServeHTTP(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d body = %s", path, rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("Content-Type"); strings.HasPrefix(got, "text/html") {
			t.Fatalf("%s content-type = %q", path, got)
		}
	}
}

func TestPublicAccountFollowCollectionsHTMLStillServesShell(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{"/@alice/followers", "/@alice/following"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		s.echo.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d body = %s", path, rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
			t.Fatalf("%s content-type = %q", path, got)
		}
	}
}

func TestPublicAccountActivityPubActorObjectUsesCanonicalActorURL(t *testing.T) {
	s := &Server{cfg: config.Config{Scheme: "https", WebDomain: "example.com", LocalDomain: "example.com"}}
	actor := activityPubActorObject(s, models.Account{
		ID:          10,
		Username:    "alice",
		DisplayName: "Alice",
		PublicKey:   "PUBLIC KEY",
	})

	body, err := json.Marshal(actor)
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(body)
	for _, want := range []string{
		`"id":"https://example.com/users/alice"`,
		`"url":"https://example.com/@alice"`,
		`"preferredUsername":"alice"`,
		`"name":"Alice"`,
	} {
		if !strings.Contains(encoded, want) {
			t.Fatalf("actor missing %q: %s", want, encoded)
		}
	}
}
