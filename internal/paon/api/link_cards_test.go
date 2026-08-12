package api

import (
	"context"
	"database/sql"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestFetchPreviewCardRejectsOversizedHTMLLikeRailsBodyWithLimit(t *testing.T) {
	previous := activityHTTPClient
	t.Cleanup(func() { activityHTTPClient = previous })
	server := &Server{cfg: config.Config{Version: "6.0.2", MastodonVersion: "4.2.27", Scheme: "https", WebDomain: "example.com"}}

	activityHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusOK,
			Header:        http.Header{"Content-Type": []string{"text/html; charset=utf-8"}},
			Body:          io.NopCloser(strings.NewReader(`<html><head><title>ignored</title></head></html>`)),
			ContentLength: maxActivityResourceBodySize + 1,
			Request:       req,
		}, nil
	})}
	if _, ok := server.fetchPreviewCard(context.Background(), "https://news.example/article"); ok {
		t.Fatal("preview card should reject an advertised body larger than Rails body_with_limit")
	}

	activityHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusOK,
			Header:        http.Header{"Content-Type": []string{"text/html; charset=utf-8"}},
			Body:          io.NopCloser(strings.NewReader(strings.Repeat("x", maxActivityResourceBodySize+1))),
			ContentLength: -1,
			Request:       req,
		}, nil
	})}
	if _, ok := server.fetchPreviewCard(context.Background(), "https://news.example/article"); ok {
		t.Fatal("preview card should reject a streamed body larger than Rails body_with_limit")
	}
}

func TestFetchPreviewCardSendsServerDefaultLocale(t *testing.T) {
	previous := activityHTTPClient
	t.Cleanup(func() { activityHTTPClient = previous })
	header := make(chan string, 1)
	activityHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		header <- req.Header.Get("Accept-Language")
		return &http.Response{
			StatusCode:    http.StatusOK,
			Header:        http.Header{"Content-Type": []string{"text/html; charset=utf-8"}},
			Body:          io.NopCloser(strings.NewReader(`<html lang="ja"><head><title>記事</title></head></html>`)),
			ContentLength: -1,
			Request:       req,
		}, nil
	})}
	server := &Server{cfg: config.Config{DefaultLocale: "ja", LocalDomain: "social.example", WebDomain: "social.example"}}
	if _, ok := server.fetchPreviewCard(context.Background(), "https://news.example/article"); !ok {
		t.Fatal("preview card fetch failed")
	}
	if got := <-header; got != "ja, *;q=0.5" {
		t.Fatalf("Accept-Language = %q", got)
	}
}

func TestPreviewCardFromOEmbedPayloadRejectsRichCards(t *testing.T) {
	if _, ok := previewCardFromOEmbedPayload("https://rich.example/1", "https://rich.example/oembed", map[string]any{
		"version": "1.0",
		"type":    "rich",
		"html":    "<b>rich</b>",
	}, time.Now()); ok {
		t.Fatal("rich oEmbed card should not be stored as a preview card")
	}
}

func TestPreviewCardCanonicalURLAndStatusOriginalURLMatchMastodon43(t *testing.T) {
	now := time.Now().UTC()
	fetched, ok := previewCardFromHTML("https://news.example/original", `<html><head><title>Story</title><link rel="canonical alternate" href="/canonical"></head></html>`, now)
	if !ok || fetched.card.URL != "https://news.example/canonical" {
		t.Fatalf("canonical card = %#v, %v", fetched.card, ok)
	}
	undefined, ok := previewCardFromHTML("https://news.example/original", `<html><head><title>Story</title><meta property="og:url" content="undefined"></head></html>`, now)
	if !ok || undefined.card.URL != "https://news.example/original" {
		t.Fatalf("undefined canonical card = %#v, %v", undefined.card, ok)
	}
	crossOrigin, ok := previewCardFromHTML("https://news.example/original", `<html><head><title>Story</title><link rel="canonical" href="https://attacker.example/canonical"></head></html>`, now)
	if !ok || crossOrigin.card.URL != "https://news.example/original" {
		t.Fatalf("cross-origin canonical card = %#v, %v", crossOrigin.card, ok)
	}

	status := models.Status{
		PreviewCards:        []models.PreviewCard{{ID: 7, URL: "https://news.example/canonical"}},
		PreviewCardStatuses: []models.PreviewCardStatus{{PreviewCardID: 7, URL: sql.NullString{String: "https://news.example/original", Valid: true}}},
	}
	card, ok := status.FirstPreviewCard()
	if !ok || card.URL != "https://news.example/original" {
		t.Fatalf("status preview card = %#v, %v", card, ok)
	}
}

func TestPreviewCardExtractsCreatorAndNormalizedHTMLLanguage(t *testing.T) {
	fetched, ok := previewCardFromHTML("https://news.example/article", `<html lang="en-US"><head><title>Story</title><meta name="fediverse:creator" content="@alice@social.example"></head></html>`, time.Now().UTC())
	if !ok {
		t.Fatal("preview card was not extracted")
	}
	if fetched.creator != "@alice@social.example" {
		t.Fatalf("creator = %q", fetched.creator)
	}
	if !fetched.card.Language.Valid || fetched.card.Language.String != "en" {
		t.Fatalf("language = %#v", fetched.card.Language)
	}
}

func TestPreviewCardAccountAttributionAllowsConfiguredParentDomainOnly(t *testing.T) {
	account := models.Account{AttributionDomains: models.StringArray{"example.com"}}
	if !previewCardAccountAllowsAttribution(account, "news.example.com") {
		t.Fatal("configured parent domain did not authorize attribution")
	}
	if previewCardAccountAllowsAttribution(account, "example.com.attacker.test") {
		t.Fatal("suffix-confusion domain authorized attribution")
	}
	if previewCardAccountAllowsAttribution(account, "unrelated.test") {
		t.Fatal("unrelated domain authorized attribution")
	}
}

func TestPreviewCardFetchedUpdatesIncludesAuthorAccount(t *testing.T) {
	updates := previewCardFetchedUpdates(models.PreviewCard{AuthorAccountID: sql.NullInt64{Int64: 42, Valid: true}})
	if got := updates["author_account_id"]; got != (sql.NullInt64{Int64: 42, Valid: true}) {
		t.Fatalf("author_account_id = %#v", got)
	}
}

func TestPreviewCardURLSelectionSkipsLocalAndTrimsPunctuation(t *testing.T) {
	server := &Server{cfg: config.Config{LocalDomain: "social.example", WebDomain: "social.example"}}
	status := models.Status{Text: "local https://social.example/@alice/1 remote (https://remote.invalid/post)."}
	if got := server.previewCardURLFromStatus(status); got != "https://remote.invalid/post" {
		t.Fatalf("preview URL = %q", got)
	}
}

func TestRemotePreviewCardURLSelectionSkipsMicroformatAnchors(t *testing.T) {
	server := &Server{cfg: config.Config{LocalDomain: "social.example", WebDomain: "social.example", Scheme: "https"}}
	status := models.Status{
		Text: `<p><a href="https://remote.example/tags/go" rel="tag">#go</a>
<a href="https://remote.example/@alice" class="u-url mention">@alice</a>
<a href="https://article.example/post">article</a></p>`,
		Local: sql.NullBool{Bool: false, Valid: true},
		Mentions: []models.Mention{
			{Account: models.Account{ID: 7, Username: "alice", Domain: sql.NullString{String: "remote.example", Valid: true}, URI: "https://remote.example/users/alice", URL: sql.NullString{String: "https://remote.example/@alice", Valid: true}}},
		},
	}
	if got := server.previewCardURLFromStatus(status); got != "https://article.example/post" {
		t.Fatalf("preview URL = %q", got)
	}
}

func TestRemotePreviewCardURLSelectionSkipsSanitizedHashtagAnchors(t *testing.T) {
	server := &Server{cfg: config.Config{LocalDomain: "social.example", WebDomain: "social.example", Scheme: "https"}}
	status := models.Status{
		Text: `<p><a href="https://remote.example/tags/%E5%AE%9F%E6%B3%81" rel="nofollow noopener noreferrer">#<span>実況</span></a>
<a href="https://article.example/post">article</a></p>`,
		Local: sql.NullBool{Bool: false, Valid: true},
		Tags:  []models.Tag{{Name: "実況"}},
	}
	if got := server.previewCardURLFromStatus(status); got != "https://article.example/post" {
		t.Fatalf("preview URL = %q, want ordinary article after sanitized hashtag", got)
	}
}

func TestRemotePreviewCardURLSelectionSkipsHashtagPathWithoutStoredTag(t *testing.T) {
	server := &Server{cfg: config.Config{LocalDomain: "social.example", WebDomain: "social.example", Scheme: "https"}}
	status := models.Status{
		Text:  `<p><a href="https://remote.example/tags/%E5%AE%9F%E6%B3%81" rel="nofollow noopener noreferrer">#実況</a></p>`,
		Local: sql.NullBool{Bool: false, Valid: true},
	}
	if got := server.previewCardURLFromStatus(status); got != "" {
		t.Fatalf("preview URL = %q, want hashtag URL excluded", got)
	}
}

func TestRemotePreviewCardURLSelectionKeepsHashLabelledOrdinaryLink(t *testing.T) {
	server := &Server{cfg: config.Config{LocalDomain: "social.example", WebDomain: "social.example", Scheme: "https"}}
	status := models.Status{
		Text:  `<p><a href="https://article.example/post">#documentation</a></p>`,
		Local: sql.NullBool{Bool: false, Valid: true},
	}
	if got := server.previewCardURLFromStatus(status); got != "https://article.example/post" {
		t.Fatalf("preview URL = %q, want non-tag URL retained", got)
	}
}

func TestStatusSerializationSuppressesAlreadyAttachedHashtagCard(t *testing.T) {
	status := models.Status{
		Text: `<p><a href="https://remote.example/tags/%E5%AE%9F%E6%B3%81" rel="nofollow noopener noreferrer">#実況</a>
<a href="https://article.example/post">article</a></p>`,
		Local: sql.NullBool{Bool: false, Valid: true},
		Tags:  []models.Tag{{Name: "実況"}},
		PreviewCards: []models.PreviewCard{
			{ID: 1, URL: "https://remote.example/tags/%E5%AE%9F%E6%B3%81"},
			{ID: 2, URL: "https://article.example/post"},
		},
	}
	filtered := statusWithoutHashtagPreviewCards(status)
	if len(filtered.PreviewCards) != 1 || filtered.PreviewCards[0].ID != 2 {
		t.Fatalf("filtered cards = %#v, want only ordinary article", filtered.PreviewCards)
	}
	if len(status.PreviewCards) != 2 || status.PreviewCards[0].ID != 1 || status.PreviewCards[1].ID != 2 {
		t.Fatalf("source status was mutated: %#v", status.PreviewCards)
	}
}

func TestStatusSerializationSuppressesRedirectedHashtagCard(t *testing.T) {
	status := models.Status{
		Text:         `<p><a href="https://remote.example/tags/%E5%AE%9F%E6%B3%81" rel="nofollow noopener noreferrer">#実況</a></p>`,
		Local:        sql.NullBool{Bool: false, Valid: true},
		Tags:         []models.Tag{{Name: "実況"}},
		PreviewCards: []models.PreviewCard{{ID: 1, URL: "https://remote.example/redirected-tag-page"}},
	}
	if filtered := statusWithoutHashtagPreviewCards(status); len(filtered.PreviewCards) != 0 {
		t.Fatalf("redirected hashtag card was retained: %#v", filtered.PreviewCards)
	}
}

func TestPreviewCardImagePathUsesRailsCachePaperclipLayout(t *testing.T) {
	server := &Server{cfg: config.Config{PublicDir: "/srv/paon/public"}}
	got := server.previewCardImagePath(42, "cover.png")
	want := "/srv/paon/public/system/cache/preview_cards/images/000/000/042/original/cover.png"
	if got != want {
		t.Fatalf("path = %q, want %q", got, want)
	}

	server.cfg.PaperclipRootPath = "/mnt/mastodon/system"
	got = server.previewCardImagePath(42, "cover.png")
	want = "/mnt/mastodon/system/cache/preview_cards/images/000/000/042/original/cover.png"
	if got != want {
		t.Fatalf("custom root path = %q, want %q", got, want)
	}

	if got := previewCardImageObjectKey(42, "cover.png"); got != "cache/preview_cards/images/000/000/042/original/cover.png" {
		t.Fatalf("object key = %q", got)
	}
}

func TestStatusLifecycleStartsPreviewCardFetch(t *testing.T) {
	files := map[string][]string{
		"server.go": {
			`s.fetchLinkCardForStatusAsync(updated.ID)`,
		},
		"local_status_postcommit.go": {
			`s.fetchLinkCardForStatusAsync(created.ID)`,
		},
		"scheduled_status_publish.go": {
			`s.fetchLinkCardForStatusAsync(created.ID)`,
		},
		"activitypub_inbox.go": {
			`s.fetchLinkCardForStatusDelayed(createdStatusID)`,
			`s.fetchLinkCardForStatusDelayed(status.ID)`,
		},
	}
	for file, wants := range files {
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
}

func TestPreviewCardFetchAppliesRailsAttachSideEffects(t *testing.T) {
	src, err := os.ReadFile("link_cards.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`if err := s.attachPreviewCardToStatus(ctx, statusID, card.ID, rawURL); err != nil {`,
		`INSERT INTO preview_cards_statuses (status_id, preview_card_id, url) VALUES (?, ?, ?)`,
		`DELETE FROM preview_cards_statuses WHERE status_id = ? AND preview_card_id IN ?`,
		`s.uploadPaperclipObject(ctx, previewCardImageObjectKey(card.ID, download.filename), path, download.contentType)`,
		`s.invalidateStatusCache(ctx, statusID)`,
		`s.recordPreviewCardTrendUseForStatus(ctx, status.AccountID, status.ID, status.Visibility, time.Now().UTC())`,
	} {
		if !strings.Contains(string(src), want) {
			t.Fatalf("link_cards.go missing %q", want)
		}
	}
}
