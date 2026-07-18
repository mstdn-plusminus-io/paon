package api

import (
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestEmbedHTMLEscapesURLs(t *testing.T) {
	height := 0
	html := embedHTML(`https://example.test/@alice/1?x=<bad>`, `https://example.test/embed.js?x=<bad>`, 400, &height)
	if strings.Contains(html, "<bad>") {
		t.Fatalf("html was not escaped: %s", html)
	}
	if !strings.Contains(html, `class="mastodon-embed"`) || !strings.Contains(html, `width="400"`) || !strings.Contains(html, `height="0"`) {
		t.Fatalf("html = %s", html)
	}
	if !strings.Contains(html, `async="async"`) {
		t.Fatalf("script missing async: %s", html)
	}
}

func TestEmbedScriptRouteServesMastodonHeightResizer(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com", PublicDir: testPublicDir(t)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/embed.js", nil)
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		`iframe.mastodon-embed`,
		`data.type !== 'setHeight'`,
		`iframe.contentWindow.postMessage`,
		`iframe.height = data.height`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("/embed.js missing %q: %s", want, body)
		}
	}
}

func TestOEmbedMatchesRailsPrivateNoStoreCache(t *testing.T) {
	src, err := os.ReadFile("embeds.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, src, "oEmbed", `c.Response().Header().Set("Cache-Control", "private, no-store")`) {
		t.Fatal("oEmbed must match Rails Api::OEmbedController private/no-store cache header")
	}
}

func TestStatusEmbedUsesRailsSiteTitleSetting(t *testing.T) {
	src, err := os.ReadFile("embeds.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, src, "statusEmbed", `statusEmbedHTMLWithConfig(s.settingStringValue("site_title", s.cfg.Title), s.cfg`) {
		t.Fatal("statusEmbed must use Rails site_title setting for the embed document title")
	}
}

func TestStaticAssetDirectoriesServeFromAbsolutePublicDir(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com", PublicDir: testPublicDir(t)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path string
		want string
	}{
		{path: "/packs/css/common-5baff5e5.css", want: "body"},
		{path: "/assets/500.html", want: "<html"},
		{path: "/emoji/2198.svg", want: "<svg"},
		{path: "/avatars/original/missing.png"},
		{path: "/headers/original/missing.png"},
		{path: "/system/media_attachments/files/109/915/428/643/912/138/original/c19f9af2fe59d814.jpg"},
	} {
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		rec := httptest.NewRecorder()
		s.echo.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d, body = %s", tc.path, rec.Code, rec.Body.String())
		}
		if rec.Body.Len() == 0 {
			t.Fatalf("%s body was empty", tc.path)
		}
		if tc.want != "" && !strings.Contains(rec.Body.String(), tc.want) {
			t.Fatalf("%s missing %q", tc.path, tc.want)
		}
		if strings.HasPrefix(tc.path, "/system/") {
			if got := rec.Header().Get("Cache-Control"); got != "public, max-age=2419200, immutable" {
				t.Fatalf("%s Cache-Control = %q", tc.path, got)
			}
			if got := rec.Header().Get("Content-Security-Policy"); got != "default-src 'none'; form-action 'none'" {
				t.Fatalf("%s Content-Security-Policy = %q", tc.path, got)
			}
		} else if got := rec.Header().Get("Cache-Control"); got != "public, max-age=2419200, must-revalidate" {
			t.Fatalf("%s Cache-Control = %q", tc.path, got)
		}
		if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
			t.Fatalf("%s X-Content-Type-Options = %q", tc.path, got)
		}
	}
}

func TestMissingStaticAssetReturnsUncacheableNotFound(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com", PublicDir: testPublicDir(t)}, nil)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/packs/js/missing-during-watch.js", nil)
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	if got := rec.Header().Get("ETag"); got != "" {
		t.Fatalf("ETag = %q, want empty", got)
	}
	if got := rec.Header().Get("Last-Modified"); got != "" {
		t.Fatalf("Last-Modified = %q, want empty", got)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Fatalf("Content-Type = %q", got)
	}
	if !strings.Contains(rec.Body.String(), `"error":"Not Found"`) {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func testPublicDir(t *testing.T) string {
	t.Helper()
	for _, dir := range []string{"public", "../../../public"} {
		if _, err := os.Stat(dir + "/embed.js"); err == nil {
			abs, err := filepath.Abs(dir)
			if err != nil {
				t.Fatal(err)
			}
			return abs
		}
	}
	t.Fatal("public/embed.js was not found")
	return ""
}

func TestStatusIDFromLocalURLAcceptsOnlyLocalStatusURLs(t *testing.T) {
	base := "https://social.example"
	if got := statusIDFromLocalURL(base, "https://social.example/@alice/123"); got != "123" {
		t.Fatalf("id = %q", got)
	}
	if got := statusIDFromLocalURL(base, "https://social.example/users/alice/statuses/456"); got != "456" {
		t.Fatalf("activitypub id = %q", got)
	}
	if got := statusIDFromLocalURL(base, "https://remote.example/@alice/123"); got != "" {
		t.Fatalf("remote id = %q", got)
	}
	if got := statusIDFromLocalURL(base, "https://social.example/users/alice/456"); got != "" {
		t.Fatalf("bad activitypub path id = %q", got)
	}
	if got := statusIDFromLocalURL(base, "https://social.example/@alice/not-id"); got != "" {
		t.Fatalf("bad id = %q", got)
	}
}

func TestStatusEmbedHTMLIsEscaped(t *testing.T) {
	html := statusEmbedHTML("Paon <test>", "https://social.example", models.Status{
		ID:        123,
		Text:      "hello <script>\nworld",
		CreatedAt: time.Date(2026, 6, 19, 1, 2, 0, 0, time.UTC),
		Language:  sql.NullString{String: "en", Valid: true},
		Account: models.Account{
			Username:    "alice",
			DisplayName: "Alice <Admin>",
		},
	})
	if strings.Contains(html, "hello <script>") || strings.Contains(html, "Alice <Admin>") || strings.Contains(html, "Paon <test>") {
		t.Fatalf("embed html was not escaped: %s", html)
	}
	if !strings.Contains(html, "hello &lt;script&gt;<br>world") || !strings.Contains(html, `href="https://social.example/@alice/123"`) {
		t.Fatalf("embed html missing content or permalink: %s", html)
	}
	if !strings.Contains(html, `<div class="content e-content" lang="en">`) {
		t.Fatalf("embed html missing e-content lang: %s", html)
	}
}

func TestStatusEmbedHTMLRespondsToMastodonEmbedHeightHandshake(t *testing.T) {
	html := statusEmbedHTML("Paon", "https://social.example", models.Status{
		ID:        123,
		Text:      "hello",
		CreatedAt: time.Date(2026, 6, 19, 1, 2, 0, 0, time.UTC),
		Account: models.Account{
			Username:    "alice",
			DisplayName: "Alice",
		},
	})
	for _, want := range []string{
		`window.addEventListener('message'`,
		`data.type !== 'setHeight'`,
		`window.parent.postMessage`,
		`height: document.documentElement.scrollHeight`,
		`document.addEventListener('toggle', sendHeight, true)`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("embed html missing height responder %q: %s", want, html)
		}
	}
}

func TestStatusEmbedCustomEmojisUseSharedCaseInsensitiveDomainLookup(t *testing.T) {
	src, err := os.ReadFile("embeds.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, src, "statusEmbedCustomEmojis", `query := customEmojiDomainQuery(s.db.Where("shortcode IN ? AND disabled = false", shortcodes), status.Account.Domain)`) {
		t.Fatal("status embed emoji lookup must use shared custom emoji domain query")
	}
	if functionBodyContains(t, src, "statusEmbedCustomEmojis", `query = query.Where("domain = ?", strings.ToLower(strings.TrimSpace(status.Account.Domain.String)))`) {
		t.Fatal("status embed emoji lookup must not use exact domain comparison")
	}
}

func TestStatusEmbedHTMLIncludesAccountAvatar(t *testing.T) {
	html := statusEmbedHTML("Paon", "https://social.example", models.Status{
		ID:        123,
		Text:      "hello",
		CreatedAt: time.Date(2026, 6, 19, 1, 2, 0, 0, time.UTC),
		Account: models.Account{
			ID:             10,
			Username:       "alice",
			DisplayName:    "Alice",
			AvatarFileName: sql.NullString{String: "avatar image.png", Valid: true},
		},
	})
	for _, want := range []string{
		`<article class="entry status detailed-status detailed-status--flex detailed-status-public">`,
		`<span class="p-author h-card"><a class="account u-url"`,
		`<img class="avatar u-photo"`,
		`src="https://social.example/system/accounts/avatars/000/000/010/original/avatar%20image.png"`,
		`alt=""`,
		`<span class="account-name"><strong class="p-name">Alice</strong><span class="acct">@alice</span></span>`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("embed avatar missing %q: %s", want, html)
		}
	}
}

func TestStatusEmbedHTMLRendersAccountDisplayNameEmojiAndLock(t *testing.T) {
	html := statusEmbedHTMLWithCustomEmojis("Paon", "https://social.example", models.Status{
		ID:        123,
		Text:      "hello",
		CreatedAt: time.Date(2026, 6, 19, 1, 2, 0, 0, time.UTC),
		Account: models.Account{
			Username:    "alice",
			DisplayName: "Alice :party:",
			Locked:      true,
		},
	}, []models.CustomEmoji{{
		ID:            42,
		Shortcode:     "party",
		ImageFileName: sql.NullString{String: "party.gif", Valid: true},
	}})
	for _, want := range []string{
		`<strong class="p-name">Alice <img rel="emoji"`,
		`alt=":party:"`,
		`<span class="acct">@alice <span title="Locked">[locked]</span></span>`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("account display name missing %q: %s", want, html)
		}
	}
}

func TestStatusEmbedHTMLIncludesMetaCountsAndEditedAt(t *testing.T) {
	html := statusEmbedHTML("Paon", "https://social.example", models.Status{
		ID:         123,
		Text:       "hello",
		CreatedAt:  time.Date(2026, 6, 19, 1, 2, 0, 0, time.UTC),
		EditedAt:   sql.NullTime{Time: time.Date(2026, 6, 19, 2, 3, 0, 0, time.UTC), Valid: true},
		Visibility: 1,
		Account: models.Account{
			Username: "alice",
			User:     models.User{ID: 7},
		},
		Application: &models.OAuthApplication{
			Name:    "Paon App <bad>",
			Website: "https://app.example/?x=<bad>",
		},
		StatusStat: models.StatusStat{
			RepliesCount:    1,
			ReblogsCount:    2,
			FavouritesCount: 3,
		},
	})
	for _, want := range []string{
		`detailed-status-unlisted`,
		`<div class="meta">`,
		`<data class="dt-published" value="2026-06-19T01:02:00Z"></data>`,
		`<data class="dt-updated" value="2026-06-19T02:03:00Z"></data>`,
		`class="u-url u-uid" target="_blank" rel="noopener noreferrer" href="https://social.example/@alice/123"`,
		`<time class="formatted" datetime="2026-06-19T01:02:00Z">2026-06-19 01:02 UTC</time>`,
		`Edited <time class="formatted" datetime="2026-06-19T02:03:00Z">2026-06-19 02:03 UTC</time>`,
		`title="Visibility">Unlisted</span>`,
		`class="application" target="_blank" rel="noopener noreferrer" href="https://app.example/?x=&lt;bad&gt;">Paon App &lt;bad&gt;</a>`,
		`title="Replies">Replies 1</span>`,
		`title="Reblogs">Reblogs 2</span>`,
		`title="Favorites">Favorites 3</span>`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("embed meta missing %q: %s", want, html)
		}
	}
}

func TestStatusEmbedApplicationHonorsAuthorSetting(t *testing.T) {
	status := models.Status{
		Account: models.Account{
			User: models.User{
				ID:       7,
				Settings: sql.NullString{String: `{"show_application":false}`, Valid: true},
			},
		},
		Application: &models.OAuthApplication{Name: "Hidden app"},
	}
	if got := statusEmbedApplicationHTML(status); got != "" {
		t.Fatalf("application should be hidden: %s", got)
	}
}

func TestStatusEmbedAccountAvatarURLSupportsRemoteAndMissing(t *testing.T) {
	remote := statusEmbedAccountAvatarURL("https://social.example", models.Account{
		AvatarRemoteURL: sql.NullString{String: "https://remote.example/avatar.png", Valid: true},
	})
	if remote != "https://remote.example/avatar.png" {
		t.Fatalf("remote avatar URL = %q", remote)
	}

	missing := statusEmbedAccountAvatarURL("https://social.example", models.Account{})
	if missing != "https://social.example/avatars/original/missing.png" {
		t.Fatalf("missing avatar URL = %q", missing)
	}
}

func TestStatusEmbedHTMLRendersCustomEmojis(t *testing.T) {
	html := statusEmbedHTMLWithCustomEmojis("Paon", "https://social.example", models.Status{
		ID:        123,
		Text:      "hello :party: mid:party: :missing:",
		CreatedAt: time.Date(2026, 6, 19, 1, 2, 0, 0, time.UTC),
		Account:   models.Account{Username: "alice"},
		Poll: &models.Poll{
			Options:   models.StringArray{":party: yes"},
			ExpiresAt: sql.NullTime{Time: time.Now().UTC().Add(time.Hour), Valid: true},
		},
	}, []models.CustomEmoji{{
		ID:            42,
		Shortcode:     "party",
		ImageFileName: sql.NullString{String: "party.gif", Valid: true},
	}})
	for _, want := range []string{
		`class="emojione custom-emoji"`,
		`alt=":party:"`,
		`src="https://social.example/system/custom_emojis/images/000/000/042/static/party.png"`,
		`data-original="https://social.example/system/custom_emojis/images/000/000/042/original/party.gif"`,
		`mid:party:`,
		`:missing:`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("custom emoji embed missing %q: %s", want, html)
		}
	}
	if count := strings.Count(html, `alt=":party:"`); count != 2 {
		t.Fatalf("custom emoji render count = %d html = %s", count, html)
	}
}

func TestStatusEmbedHTMLUsesConfiguredLocale(t *testing.T) {
	html := statusEmbedHTMLWithCustomEmojis("Paon", "https://social.example", models.Status{
		ID:        10,
		Text:      "hello",
		CreatedAt: time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC),
		Account:   models.Account{Username: "alice"},
	}, nil, "en")
	if !strings.Contains(html, `<html lang="en">`) {
		t.Fatalf("embed html missing configured lang: %s", html)
	}
}

func TestStatusEmbedHTMLLinkifiesStatusBodyOnly(t *testing.T) {
	html := statusEmbedHTML("Paon", "https://social.example", models.Status{
		ID:          123,
		Text:        "hello https://remote.example/a?x=1&y=2 #Go #123 foo=#bad /path/#also_bad testé#nonascii @bob@remote.example user=@bad /@also_bad @carol.",
		SpoilerText: "CW #notlink",
		CreatedAt:   time.Date(2026, 6, 19, 1, 2, 0, 0, time.UTC),
		Account: models.Account{
			Username:    "alice",
			DisplayName: "Alice #notlink",
		},
		Mentions: []models.Mention{{
			Account: models.Account{
				ID:       9,
				Username: "bob",
				Domain:   sql.NullString{String: "remote.example", Valid: true},
				URL:      sql.NullString{String: "https://remote.example/@bob", Valid: true},
			},
		}},
		Tags: []models.Tag{{
			Name:        "golang",
			DisplayName: sql.NullString{String: "Go", Valid: true},
		}},
	})
	for _, want := range []string{
		`href="https://remote.example/a?x=1&amp;y=2"`,
		`<a href="https://social.example/tags/golang" class="mention hashtag" rel="tag">#<span>Go</span></a>`,
		`#123`,
		`foo=#bad`,
		`/path/#also_bad`,
		`testé#nonascii`,
		`user=@bad`,
		`/@also_bad`,
		`<span class="h-card" translate="no"><a href="https://remote.example/@bob" class="u-url mention">@<span>bob</span></a></span>`,
		`@carol.`,
		`CW #notlink`,
		`Alice #notlink`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("linkified embed missing %q: %s", want, html)
		}
	}
	if strings.Contains(html, `href="https://social.example/@carol"`) {
		t.Fatalf("unresolved mention should remain plain text like Rails TextFormatter: %s", html)
	}
	if strings.Contains(html, `/tags/123`) || strings.Contains(html, `#<span>123</span>`) {
		t.Fatalf("numeric-only hashtag should remain plain text like Rails Tag::HASHTAG_RE: %s", html)
	}
	for _, unwanted := range []string{`/tags/bad`, `/tags/also_bad`, `/tags/nonascii`, `href="https://social.example/@bad"`, `href="https://social.example/@also_bad"`} {
		if strings.Contains(html, unwanted) {
			t.Fatalf("embed token boundary mismatch kept %q: %s", unwanted, html)
		}
	}
	if strings.Contains(html, `href="https://social.example/tags/notlink"`) {
		t.Fatalf("spoiler or display name should not be hashtag-linkified: %s", html)
	}
}

func TestStatusEmbedLinkifiesRailsExtendedURIs(t *testing.T) {
	html := statusEmbedHTML("Paon", "https://social.example", models.Status{
		ID:        123,
		Text:      "chat xmpp:muc@instance.com?join gemini://capsule.example/page magnet:?xt=urn:btih:abcdef.",
		CreatedAt: time.Date(2026, 6, 19, 1, 2, 0, 0, time.UTC),
		Account:   models.Account{Username: "alice"},
	})
	for _, want := range []string{
		`href="xmpp:muc@instance.com?join"`,
		`<span class="invisible">xmpp:</span><span>muc@instance.com?join</span>`,
		`href="gemini://capsule.example/page"`,
		`href="magnet:?xt=urn:btih:abcdef"`,
		`abcdef</span><span class="invisible"></span></a>.`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("extended URI embed missing %q: %s", want, html)
		}
	}
}

func TestStatusEmbedHTMLUsesContentWarningRevealControls(t *testing.T) {
	html := statusEmbedHTMLWithCustomEmojis("Paon", "https://social.example", models.Status{
		ID:          123,
		Text:        "hidden body <bad>",
		SpoilerText: "CW :party:",
		CreatedAt:   time.Date(2026, 6, 19, 1, 2, 0, 0, time.UTC),
		Account:     models.Account{Username: "alice"},
		Poll: &models.Poll{
			Options:   models.StringArray{"yes <maybe>"},
			ExpiresAt: sql.NullTime{Time: time.Now().UTC().Add(time.Hour), Valid: true},
		},
	}, []models.CustomEmoji{{
		ID:            42,
		Shortcode:     "party",
		ImageFileName: sql.NullString{String: "party.gif", Valid: true},
	}})
	for _, want := range []string{
		`<details class="content content-warning">`,
		`<summary class="content-warning__summary">`,
		`<span class="content-warning__text p-summary">`,
		`CW <img rel="emoji"`,
		`<span class="content-warning__trigger">Show more</span>`,
		`<div class="content-warning__body e-content">hidden body &lt;bad&gt;`,
		`yes &lt;maybe&gt;`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("content warning embed missing %q: %s", want, html)
		}
	}
	detailsStart := strings.Index(html, `<details class="content content-warning">`)
	pollStart := strings.Index(html, `<div class="poll">`)
	detailsEnd := strings.Index(html, `</details>`)
	if detailsStart < 0 || pollStart < detailsStart || detailsEnd < pollStart {
		t.Fatalf("poll should be inside content warning details: %s", html)
	}
}

func TestStatusEmbedEmojiShortcodesUsesRailsBoundaries(t *testing.T) {
	got := strings.Join(statusEmbedEmojiShortcodes(statusEmbedEmojiText(models.Status{
		Text:        " :party: mid:party:",
		SpoilerText: ":ok_2:\n:party: :x:",
		Account:     models.Account{DisplayName: "Alice :display:"},
	})), ",")
	if got != "party,ok_2,display" {
		t.Fatalf("shortcodes = %q", got)
	}
}

func TestStatusEmbedCustomEmojiURLSupportsRemoteAndCache(t *testing.T) {
	remote := statusEmbedCustomEmojiURL("https://social.example", models.CustomEmoji{
		Shortcode:      "party",
		ImageRemoteURL: sql.NullString{String: "https://remote.example/party.png", Valid: true},
	}, "static")
	if remote != "https://remote.example/party.png" {
		t.Fatalf("remote emoji URL = %q", remote)
	}

	cached := statusEmbedCustomEmojiURL("https://social.example", models.CustomEmoji{
		ID:                        42,
		Shortcode:                 "party",
		Domain:                    sql.NullString{String: "remote.example", Valid: true},
		ImageFileName:             sql.NullString{String: "party.gif", Valid: true},
		ImageStorageSchemaVersion: sql.NullInt64{Int64: 1, Valid: true},
	}, "static")
	if cached != "https://social.example/system/cache/custom_emojis/images/000/000/042/static/party.png" {
		t.Fatalf("cached emoji URL = %q", cached)
	}
}

func TestStatusEmbedHTMLIncludesMediaAttachments(t *testing.T) {
	html := statusEmbedHTML("Paon", "https://social.example", models.Status{
		ID:        123,
		Text:      "hello",
		CreatedAt: time.Date(2026, 6, 19, 1, 2, 0, 0, time.UTC),
		Account:   models.Account{Username: "alice"},
		MediaAttachments: []models.MediaAttachment{{
			ID:           8,
			Type:         0,
			FileFileName: sql.NullString{String: "photo 1.png", Valid: true},
			Description:  sql.NullString{String: "A <photo>", Valid: true},
		}},
	})
	for _, want := range []string{
		`<div class="media">`,
		`src="https://social.example/system/media_attachments/files/000/000/008/original/photo%201.png"`,
		`alt="A &lt;photo&gt;"`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("embed html missing %q: %s", want, html)
		}
	}
}

func TestStatusEmbedHTMLWithConfigUsesStorageHostForPaperclipAssets(t *testing.T) {
	html := statusEmbedHTMLWithConfig("Paon", config.Config{
		WebDomain:   "social.example",
		Scheme:      "https",
		StorageHost: "https://media.example/",
	}, models.Status{
		ID:        123,
		Text:      ":party:",
		CreatedAt: time.Date(2026, 6, 19, 1, 2, 0, 0, time.UTC),
		Account: models.Account{
			ID:             10,
			Username:       "alice",
			DisplayName:    ":party:",
			AvatarFileName: sql.NullString{String: "avatar.png", Valid: true},
		},
		MediaAttachments: []models.MediaAttachment{{
			ID:                8,
			Type:              2,
			FileFileName:      sql.NullString{String: "video.mp4", Valid: true},
			ThumbnailFileName: sql.NullString{String: "thumb.png", Valid: true},
		}},
	}, []models.CustomEmoji{{
		ID:            42,
		Shortcode:     "party",
		ImageFileName: sql.NullString{String: "party.gif", Valid: true},
	}})
	for _, want := range []string{
		`src="https://media.example/accounts/avatars/000/000/010/original/avatar.png"`,
		`src="https://media.example/media_attachments/files/000/000/008/original/video.mp4"`,
		`poster="https://media.example/media_attachments/thumbnails/000/000/008/original/thumb.png"`,
		`src="https://media.example/custom_emojis/images/000/000/042/static/party.png"`,
		`data-original="https://media.example/custom_emojis/images/000/000/042/original/party.gif"`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("storage-host embed html missing %q: %s", want, html)
		}
	}
	if strings.Contains(html, `https://social.example/system/`) {
		t.Fatalf("embed html leaked local /system asset URL with StorageHost: %s", html)
	}
}

func TestStatusEmbedMediaUsesRemoteURLAndRevealsSensitiveMedia(t *testing.T) {
	remote := statusEmbedMediaHTML("https://social.example", models.Status{
		MediaAttachments: []models.MediaAttachment{{
			ID:          9,
			Type:        0,
			RemoteURL:   "https://remote.example/image.png",
			Description: sql.NullString{String: "remote", Valid: true},
		}},
	})
	if !strings.Contains(remote, `src="https://remote.example/image.png"`) {
		t.Fatalf("remote media missing: %s", remote)
	}

	sensitive := statusEmbedMediaHTML("https://social.example", models.Status{
		Sensitive: true,
		MediaAttachments: []models.MediaAttachment{{
			ID:        9,
			Type:      0,
			RemoteURL: "https://remote.example/image.png",
		}},
	})
	for _, want := range []string{
		`<details class="media sensitive-media">`,
		`<summary class="media-spoiler">`,
		`Sensitive content`,
		`Click to show`,
		`src="https://remote.example/image.png"`,
	} {
		if !strings.Contains(sensitive, want) {
			t.Fatalf("sensitive media missing %q: %s", want, sensitive)
		}
	}
}

func TestStatusEmbedHTMLIncludesPreviewCardWhenNoMedia(t *testing.T) {
	html := statusEmbedHTML("Paon", "https://social.example", models.Status{
		ID:        123,
		Text:      "hello",
		CreatedAt: time.Date(2026, 6, 19, 1, 2, 0, 0, time.UTC),
		Account:   models.Account{Username: "alice"},
		PreviewCards: []models.PreviewCard{{
			ID:                        12,
			URL:                       "https://remote.example/article?x=<bad>",
			Title:                     "A <card>",
			Description:               "Description <bad>",
			AuthorName:                "Reporter <Name>",
			ProviderName:              "Remote <News>",
			Width:                     640,
			Height:                    360,
			ImageFileName:             sql.NullString{String: "cover image.png", Valid: true},
			ImageStorageSchemaVersion: sql.NullInt64{Int64: 1, Valid: true},
			ImageDescription:          "Cover <alt>",
			Language:                  sql.NullString{String: "en", Valid: true},
		}},
	})
	for _, want := range []string{
		`class="status-card expanded"`,
		`class="status-card__image"`,
		`class="status-card__image-image"`,
		`class="status-card__content"`,
		`class="status-card__host"`,
		`class="status-card__title"`,
		`class="status-card__author"`,
		`href="https://remote.example/article?x=&lt;bad&gt;"`,
		`src="https://social.example/system/cache/preview_cards/images/000/000/012/original/cover%20image.png"`,
		`alt="Cover &lt;alt&gt;"`,
		`title="Cover &lt;alt&gt;"`,
		`lang="en"`,
		`A &lt;card&gt;`,
		`By <strong>Reporter &lt;Name&gt;</strong>`,
		`Remote &lt;News&gt;`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("embed html missing %q: %s", want, html)
		}
	}
	if strings.Contains(html, "Description <bad>") {
		t.Fatalf("preview card html was not escaped: %s", html)
	}
}

func TestStatusEmbedPreviewCardMarkupMirrorsReactCardClasses(t *testing.T) {
	reactCard, err := os.ReadFile("../../../app/javascript/mastodon/features/status/components/card.jsx")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`status-card`,
		`status-card__image`,
		`status-card__image-image`,
		`status-card__content`,
		`status-card__host`,
		`status-card__title`,
		`status-card__author`,
		`status-card__description`,
	} {
		if !strings.Contains(string(reactCard), want) {
			t.Fatalf("React status card component changed; missing %q", want)
		}
	}
	src, err := os.ReadFile("embeds.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`cardClass := "status-card"`,
		`class="status-card__image"`,
		`class="status-card__image-image"`,
		`class="status-card__content"`,
		`class="status-card__host"`,
		`class="status-card__title"`,
		`class="status-card__author"`,
		`class="status-card__description"`,
	} {
		if !strings.Contains(string(src), want) {
			t.Fatalf("Go status embed preview card missing React class %q", want)
		}
	}
}

func TestStatusEmbedPreviewCardUsesLocalizedAuthorLabel(t *testing.T) {
	html := statusEmbedHTMLWithConfig("Paon", config.Config{WebDomain: "social.example", Scheme: "https"}, models.Status{
		ID:        123,
		Text:      "hello",
		CreatedAt: time.Date(2026, 6, 19, 1, 2, 0, 0, time.UTC),
		Account:   models.Account{Username: "alice"},
		PreviewCards: []models.PreviewCard{{
			ID:         12,
			URL:        "https://remote.example/article",
			Title:      "A card",
			AuthorName: "著者",
		}},
	}, nil, "ja")
	if !strings.Contains(html, `<span class="status-card__author"><strong>著者</strong></span>`) {
		t.Fatalf("localized author label mismatch: %s", html)
	}
	if strings.Contains(html, "By ") {
		t.Fatalf("Japanese embed should not force English author label: %s", html)
	}
}

func TestStatusEmbedHTMLWithConfigUsesStorageHostForPreviewCard(t *testing.T) {
	html := statusEmbedHTMLWithConfig("Paon", config.Config{
		WebDomain:   "social.example",
		Scheme:      "https",
		StorageHost: "https://media.example",
	}, models.Status{
		ID:        123,
		Text:      "hello",
		CreatedAt: time.Date(2026, 6, 19, 1, 2, 0, 0, time.UTC),
		Account:   models.Account{Username: "alice"},
		PreviewCards: []models.PreviewCard{{
			ID:                        12,
			URL:                       "https://remote.example/article",
			ImageFileName:             sql.NullString{String: "cover.png", Valid: true},
			ImageStorageSchemaVersion: sql.NullInt64{Int64: 1, Valid: true},
		}},
	}, nil)
	if !strings.Contains(html, `src="https://media.example/cache/preview_cards/images/000/000/012/original/cover.png"`) {
		t.Fatalf("storage-host preview card image missing: %s", html)
	}
}

func TestStatusEmbedHTMLIncludesPollOptions(t *testing.T) {
	expiresAt := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	html := statusEmbedHTML("Paon", "https://social.example", models.Status{
		ID:        123,
		Text:      "hello",
		CreatedAt: time.Date(2026, 6, 19, 1, 2, 0, 0, time.UTC),
		Account:   models.Account{Username: "alice"},
		Poll: &models.Poll{
			Options:     models.StringArray{"yes", "no <maybe>"},
			Multiple:    true,
			VotesCount:  3,
			VotersCount: sql.NullInt64{Int64: 2, Valid: true},
			ExpiresAt:   sql.NullTime{Time: expiresAt, Valid: true},
		},
	})
	for _, want := range []string{
		`<div class="poll">`,
		`class="poll-input checkbox"`,
		`no &lt;maybe&gt;`,
		`2 people`,
		expiresAt.Format("2006-01-02 15:04 UTC"),
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("embed poll missing %q: %s", want, html)
		}
	}
	if strings.Contains(html, "<progress") {
		t.Fatalf("non-expired anonymous poll should not show results: %s", html)
	}
}

func TestStatusEmbedPollShowsExpiredResults(t *testing.T) {
	html := statusEmbedPollHTML(models.Status{
		Poll: &models.Poll{
			Options:       models.StringArray{"yes", "no"},
			CachedTallies: models.Int64Array{3, 1},
			VotesCount:    4,
			ExpiresAt:     sql.NullTime{Time: time.Now().UTC().Add(-time.Hour), Valid: true},
		},
	})
	for _, want := range []string{
		`<span class="poll-percent">75%</span>`,
		`<span class="poll-percent">25%</span>`,
		`<progress max="100" value="75"></progress>`,
		`4 votes`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("expired poll missing %q: %s", want, html)
		}
	}
}

func TestStatusEmbedPreviewCardIsSkippedWhenMediaExists(t *testing.T) {
	html := statusEmbedHTML("Paon", "https://social.example", models.Status{
		ID:        123,
		Text:      "hello",
		CreatedAt: time.Date(2026, 6, 19, 1, 2, 0, 0, time.UTC),
		Account:   models.Account{Username: "alice"},
		MediaAttachments: []models.MediaAttachment{{
			ID:           8,
			Type:         0,
			FileFileName: sql.NullString{String: "photo.png", Valid: true},
		}},
		PreviewCards: []models.PreviewCard{{
			URL:   "https://remote.example/article",
			Title: "A card",
		}},
	})
	if strings.Contains(html, `class="card"`) {
		t.Fatalf("preview card should be skipped when media exists: %s", html)
	}
	if !strings.Contains(html, `photo.png`) {
		t.Fatalf("media should still render: %s", html)
	}
}

func TestEmbeddableStatusRejectsHiddenAndReblog(t *testing.T) {
	if !embeddableStatus(&models.Status{Visibility: 0}) {
		t.Fatal("public status should be embeddable")
	}
	if !embeddableStatus(&models.Status{Visibility: 1}) {
		t.Fatal("unlisted status should be embeddable")
	}
	if embeddableStatus(&models.Status{Visibility: 2}) {
		t.Fatal("private status should not be embeddable")
	}
	if embeddableStatus(&models.Status{Visibility: 0, ReblogOfID: sql.NullInt64{Int64: 1, Valid: true}}) {
		t.Fatal("reblog should not be embeddable")
	}
}

func TestStatusEmbedRouteDoesNotFallbackToWebApp(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/@alice/123/embed", nil)
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `id="mastodon"`) {
		t.Fatalf("embed route fell back to web app: %s", rec.Body.String())
	}
}

func TestDiscoverRemoteOEmbedEndpointPrefersJSONAndResolvesRelative(t *testing.T) {
	endpoint, format := discoverRemoteOEmbedEndpointFromHTML("https://remote.example/statuses/1", `
<html><head>
  <link rel="alternate" type="text/xml+oembed" href="/oembed.xml?url=1">
  <link rel="alternate" type="application/json+oembed" href="/oembed.json?url=1&amp;format=json">
</head></html>`)
	if endpoint != "https://remote.example/oembed.json?url=1&format=json" {
		t.Fatalf("endpoint = %q", endpoint)
	}
	if format != remoteOEmbedJSON {
		t.Fatalf("format = %q", format)
	}
}

func TestParseRemoteOEmbedXMLAndValidate(t *testing.T) {
	embed, err := parseRemoteOEmbed([]byte(`<oembed><version>1.0</version><type>rich</type><html><![CDATA[<iframe src="https://remote.example/embed"></iframe>]]></html><width>640</width></oembed>`), remoteOEmbedXML)
	if err != nil {
		t.Fatal(err)
	}
	if !validRemoteOEmbed(embed) {
		t.Fatalf("expected valid oembed: %#v", embed)
	}
	if embed["width"] != 640 {
		t.Fatalf("width = %#v", embed["width"])
	}
}

func TestSanitizeRemoteOEmbedHTMLRemovesDangerousContent(t *testing.T) {
	got := sanitizeRemoteOEmbedHTML(`<div><iframe src="https://remote.example/embed" onload="alert(2)" allowfullscreen width="640"></iframe></div><script>alert(3)</script><b onclick=x>ok</b>`)
	lower := strings.ToLower(got)
	if strings.Contains(lower, "<script") || strings.Contains(lower, "</script") || strings.Contains(lower, "javascript:") || strings.Contains(lower, "onload") || strings.Contains(lower, "onclick") {
		t.Fatalf("dangerous oembed html remained: %s", got)
	}
	for _, want := range []string{`<iframe`, `src="https://remote.example/embed"`, `allowfullscreen`, `width="640"`, `sandbox="allow-scripts allow-same-origin allow-popups allow-popups-to-escape-sandbox allow-forms"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("sanitized oembed html missing %q: %s", want, got)
		}
	}
	if strings.Contains(got, "<div") || strings.Contains(got, "<b") {
		t.Fatalf("non-oembed tags remained: %s", got)
	}
}

func TestSanitizeRemoteOEmbedHTMLAllowsRailsOEmbedMediaTags(t *testing.T) {
	got := sanitizeRemoteOEmbedHTML(`<video controls height="360" width="640" poster="bad"><source src="https://cdn.example/video.mp4" type="video/mp4"><source src="javascript:alert(1)" type="video/mp4"></video><audio controls autoplay><source src="http://cdn.example/audio.mp3" type="audio/mpeg"></audio>`)
	for _, want := range []string{`<video`, `controls`, `height="360"`, `width="640"`, `<source`, `src="https://cdn.example/video.mp4"`, `type="video/mp4"`, `<audio`, `src="http://cdn.example/audio.mp3"`, `type="audio/mpeg"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("sanitized oembed html missing %q: %s", want, got)
		}
	}
	for _, unwanted := range []string{"poster", "autoplay", "javascript:"} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("sanitized oembed html kept %q: %s", unwanted, got)
		}
	}
}

func TestFetchRemoteOEmbedDiscoversAndFetchesJSON(t *testing.T) {
	previous := activityHTTPClient
	defer func() { activityHTTPClient = previous }()

	var htmlAccept string
	activityHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.String() {
		case "https://remote.test/statuses/1":
			htmlAccept = r.Header.Get("Accept")
			return textResponse(http.StatusOK, "text/html", `<link rel="alternate" type="application/json+oembed" href="https://remote.test/oembed?url=1">`), nil
		case "https://remote.test/oembed?url=1":
			return textResponse(http.StatusOK, "application/json", `{"version":"1.0","type":"rich","html":"<iframe src=\"https://remote.test/embed\"></iframe>"}`), nil
		default:
			t.Fatalf("unexpected request URL: %s", r.URL.String())
			return nil, nil
		}
	})}

	embed, err := fetchRemoteOEmbed("https://remote.test/statuses/1")
	if err != nil {
		t.Fatal(err)
	}
	if htmlAccept != "text/html" {
		t.Fatalf("discovery Accept = %q", htmlAccept)
	}
	if embed["type"] != "rich" || !strings.Contains(embed["html"].(string), "iframe") {
		t.Fatalf("embed = %#v", embed)
	}
}

func textResponse(status int, contentType string, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{contentType}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
