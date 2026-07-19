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

func TestPreviewCardFromOEmbedPayloadRejectsRichCards(t *testing.T) {
	if _, ok := previewCardFromOEmbedPayload("https://rich.example/1", "https://rich.example/oembed", map[string]any{
		"version": "1.0",
		"type":    "rich",
		"html":    "<b>rich</b>",
	}, time.Now()); ok {
		t.Fatal("rich oEmbed card should not be stored as a preview card")
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
			`s.fetchLinkCardForStatusAsync(created.ID)`,
			`s.fetchLinkCardForStatusAsync(updated.ID)`,
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
		`if err := s.attachPreviewCardToStatus(ctx, statusID, card.ID); err != nil {`,
		`s.uploadPaperclipObject(ctx, previewCardImageObjectKey(card.ID, download.filename), path, download.contentType)`,
		`s.invalidateStatusCache(ctx, statusID)`,
		`s.recordPreviewCardTrendUseForStatus(ctx, status.AccountID, status.ID, status.Visibility, time.Now().UTC())`,
	} {
		if !strings.Contains(string(src), want) {
			t.Fatalf("link_cards.go missing %q", want)
		}
	}
}
