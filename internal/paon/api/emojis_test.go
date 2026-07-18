package api

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestActivityPubEmojiShape(t *testing.T) {
	s := &Server{cfg: config.Config{Scheme: "https", WebDomain: "example.com", LocalDomain: "example.com"}}
	updated := time.Date(2026, 6, 19, 12, 30, 0, 0, time.UTC)
	out := activityPubEmoji(s, models.CustomEmoji{
		ID:               42,
		Shortcode:        "party",
		ImageFileName:    sql.NullString{String: "party.gif", Valid: true},
		ImageContentType: sql.NullString{String: "image/gif", Valid: true},
		UpdatedAt:        updated,
	})

	if out["id"] != "https://example.com/emojis/42" || out["type"] != "Emoji" || out["name"] != ":party:" {
		t.Fatalf("emoji = %#v", out)
	}
	contexts := out["@context"].([]any)
	if len(contexts) != 2 || contexts[0] != "https://www.w3.org/ns/activitystreams" {
		t.Fatalf("context should start with ActivityStreams: %#v", contexts)
	}
	extensions, ok := contexts[1].(map[string]any)
	if !ok || extensions["Emoji"] != "toot:Emoji" || extensions["toot"] != "http://joinmastodon.org/ns#" {
		t.Fatalf("emoji context should match Rails EmojiSerializer: %#v", contexts[1])
	}
	focalPoint, ok := extensions["focalPoint"].(map[string]any)
	if !ok || focalPoint["@container"] != "@list" || focalPoint["@id"] != "toot:focalPoint" {
		t.Fatalf("focalPoint context should match nested Rails ImageSerializer: %#v", extensions["focalPoint"])
	}
	if out["updated"] != "2026-06-19T12:30:00Z" {
		t.Fatalf("updated = %#v", out["updated"])
	}
	icon := out["icon"].(map[string]any)
	if icon["type"] != "Image" || icon["mediaType"] != "image/gif" {
		t.Fatalf("icon = %#v", icon)
	}
	if icon["url"] != "https://example.com/system/custom_emojis/images/000/000/042/original/party.gif" {
		t.Fatalf("icon url = %#v", icon["url"])
	}
}

func TestPublicEmojiVariesBySignatureWhenAuthorizedFetchMode(t *testing.T) {
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

	req := httptest.NewRequest(http.MethodGet, "/emojis/42", nil)
	req.Header.Set("Accept", "application/activity+json")
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Vary"); got != "Signature" {
		t.Fatalf("Vary = %q, want Signature", got)
	}
}

func TestPublicEmojiDoesNotVaryBySignatureWhenAuthorizedFetchDisabled(t *testing.T) {
	s, err := NewServer(config.Config{
		Title:       "Paon",
		LocalDomain: "example.com",
		WebDomain:   "example.com",
		Scheme:      "https",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/emojis/42", nil)
	req.Header.Set("Accept", "application/activity+json")
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Vary"); got != "" {
		t.Fatalf("Vary = %q, want empty", got)
	}
}
