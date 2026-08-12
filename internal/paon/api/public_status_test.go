package api

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"github.com/mstdn-plusminus-io/paon/internal/paon/web"
)

func TestPublicStatusActivityPubRouteDoesNotServeHTMLShell(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/@alice/123", nil)
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

func TestPublicStatusLinkHeaderMatchesRailsAlternateLink(t *testing.T) {
	cfg := config.Config{Scheme: "https", WebDomain: "example.com", LocalDomain: "example.com"}
	status := models.Status{ID: 123, Account: models.Account{ID: 10, Username: "alice"}}

	got := publicStatusLinkHeader(cfg, status)
	want := `<https://example.com/users/alice/statuses/123>; rel="alternate"; type="application/activity+json"`
	if got != want {
		t.Fatalf("Link = %q, want %q", got, want)
	}
}

func TestPublicStatusPathStatusAllowedMatchesRailsSetStatusScope(t *testing.T) {
	account := models.Account{ID: 10, Username: "alice"}
	if !publicStatusPathStatusAllowed(models.Status{ID: 123, Account: account, Visibility: 0}, "alice") {
		t.Fatal("public local status should be allowed")
	}
	if !publicStatusPathStatusAllowed(models.Status{ID: 123, Account: account, Visibility: 1}, "alice") {
		t.Fatal("unlisted local status should be allowed")
	}
	for _, status := range []models.Status{
		{ID: 123, Account: account, Visibility: 2},
		{ID: 123, Account: account, Visibility: 3},
		{ID: 123, Account: models.Account{ID: 11, Username: "bob"}, Visibility: 0},
		{ID: 123, Account: models.Account{ID: 12, Username: "alice", Domain: sql.NullString{String: "remote.example", Valid: true}}, Visibility: 0},
	} {
		if publicStatusPathStatusAllowed(status, "alice") {
			t.Fatalf("status should not be allowed for Rails public HTML route: %#v", status)
		}
	}
}

func TestPublicStatusOriginalRedirectURLMatchesRailsReblogRedirect(t *testing.T) {
	s := &Server{cfg: config.Config{Scheme: "https", WebDomain: "example.com", LocalDomain: "example.com"}}
	status := models.Status{
		ID:         123,
		ReblogOfID: sql.NullInt64{Int64: 99, Valid: true},
		Reblog: &models.Status{
			ID:      99,
			Account: models.Account{ID: 20, Username: "bob"},
		},
	}
	if got := s.publicStatusOriginalRedirectURL(status); got != "https://example.com/@bob/99" {
		t.Fatalf("redirect URL = %q", got)
	}
	status.ReblogOfID = sql.NullInt64{}
	if got := s.publicStatusOriginalRedirectURL(status); got != "" {
		t.Fatalf("non-reblog redirect URL = %q", got)
	}
}

func TestApplyPublicStatusHeadMatchesRailsPublicAndUnlistedMetadata(t *testing.T) {
	s := &Server{cfg: config.Config{Scheme: "https", WebDomain: "web.example", LocalDomain: "example.com", Title: "Paon"}}
	createdAt := time.Date(2026, time.July, 21, 12, 34, 56, 0, time.FixedZone("JST", 9*60*60))
	for _, visibility := range []int{0, 1} {
		options := web.AppOptions{}
		s.applyPublicStatusHead(&options, models.Status{
			ID:         123,
			Text:       "Hello from Paon",
			Language:   sql.NullString{String: "ca", Valid: true},
			CreatedAt:  createdAt,
			Visibility: visibility,
			Account: models.Account{
				ID:          10,
				Username:    "alice",
				DisplayName: "Alice",
			},
		}, "en")

		if options.DocumentTitle != `Alice: "Hello from Paon"` {
			t.Fatalf("visibility %d document title = %q", visibility, options.DocumentTitle)
		}
		for property, want := range map[string]string{
			"og:site_name":      "Mastodon",
			"og:type":           "article",
			"og:title":          "Alice (@alice@example.com)",
			"og:url":            "https://web.example/@alice/123",
			"og:published_time": "2026-07-21T03:34:56Z",
			"profile:username":  "alice@example.com",
			"og:description":    "Hello from Paon",
			"og:locale":         "ca",
			"twitter:card":      "summary",
		} {
			if got := publicStatusMetaProperty(options.HeadMeta, property); got != want {
				t.Fatalf("visibility %d %s = %q, want %q", visibility, property, got, want)
			}
		}
		if got := publicStatusMetaName(options.HeadMeta, "description"); got != "Hello from Paon" {
			t.Fatalf("visibility %d description = %q", visibility, got)
		}
		if len(options.HeadLinks) != 2 || options.HeadLinks[0].Type != "application/json+oembed" || options.HeadLinks[1].Href != "https://web.example/users/alice/statuses/123" {
			t.Fatalf("visibility %d links = %#v", visibility, options.HeadLinks)
		}
	}
}

func TestApplyPublicStatusHeadRejectsPrivateStatus(t *testing.T) {
	s := &Server{cfg: config.Config{Scheme: "https", WebDomain: "example.com", LocalDomain: "example.com", Title: "Paon"}}
	options := web.AppOptions{DocumentTitle: "unchanged"}
	s.applyPublicStatusHead(&options, models.Status{
		ID:         123,
		Text:       "private",
		Visibility: 2,
		Account:    models.Account{ID: 10, Username: "alice"},
	}, "en")
	if options.DocumentTitle != "unchanged" || len(options.HeadMeta) != 0 || len(options.HeadLinks) != 0 {
		t.Fatalf("private status changed app options: %#v", options)
	}
}

func TestApplyPublicStatusHeadOmitsLocaleWhenStatusLanguageIsMissing(t *testing.T) {
	s := &Server{cfg: config.Config{Scheme: "https", WebDomain: "example.com", LocalDomain: "example.com", Title: "Paon"}}
	options := web.AppOptions{}
	s.applyPublicStatusHead(&options, models.Status{
		ID:         123,
		Text:       "No language metadata",
		Visibility: 0,
		Account:    models.Account{ID: 10, Username: "alice"},
	}, "en")
	if got := publicStatusMetaProperty(options.HeadMeta, "og:locale"); got != "" {
		t.Fatalf("missing status language produced og:locale %q", got)
	}
}

func TestPublicStatusDescriptionMatchesRailsCWMediaAndPollRules(t *testing.T) {
	status := models.Status{
		Text: "Question",
		MediaAttachments: []models.MediaAttachment{
			{ID: 1, Type: 0},
			{ID: 2, Type: 2},
			{ID: 3, Type: 4},
		},
		Poll: &models.Poll{Options: models.StringArray{"One", "Two"}},
	}
	if got := publicStatusDescription(status, "en"); got != "Attached: 1 image · 1 video · 1 audio\n\nQuestion\n\n[ ] One\n[ ] Two" {
		t.Fatalf("description = %q", got)
	}
	status.SpoilerText = "Spoilers"
	if got := publicStatusDescription(status, "en"); got != "Attached: 1 image · 1 video · 1 audio · Content warning: Spoilers" {
		t.Fatalf("CW description = %q", got)
	}
}

func TestPublicStatusMediaMetaMatchesRailsImagesVideoAndSensitivePreview(t *testing.T) {
	s := &Server{cfg: config.Config{Scheme: "https", WebDomain: "example.com", LocalDomain: "example.com", PaperclipRootURL: "/system"}}
	image := models.MediaAttachment{
		ID:              1,
		Type:            0,
		FileFileName:    sql.NullString{String: "photo.png", Valid: true},
		FileContentType: sql.NullString{String: "image/png", Valid: true},
		Description:     sql.NullString{String: `alt "text"`, Valid: true},
		FileMeta:        []byte(`{"original":{"width":640,"height":480}}`),
	}
	video := models.MediaAttachment{
		ID:                2,
		Type:              2,
		FileFileName:      sql.NullString{String: "clip.mp4", Valid: true},
		FileContentType:   sql.NullString{String: "video/mp4", Valid: true},
		ThumbnailFileName: sql.NullString{String: "clip.png", Valid: true},
		FileMeta:          []byte(`{"original":{"width":1280,"height":720},"small":{"width":400,"height":225}}`),
	}
	status := models.Status{ID: 123, Account: models.Account{ID: 10, Username: "alice"}, MediaAttachments: []models.MediaAttachment{image, video}}
	meta := s.publicStatusMediaMeta(status)
	for property, want := range map[string]string{
		"og:image":              "https://example.com/system/media_attachments/files/000/000/001/original/photo.png",
		"og:image:type":         "image/png",
		"og:image:width":        "640",
		"og:image:height":       "480",
		"og:image:alt":          `alt "text"`,
		"og:video":              "https://example.com/system/media_attachments/files/000/000/002/original/clip.mp4",
		"og:video:width":        "1280",
		"twitter:player":        "https://example.com/media/2/player",
		"twitter:player:height": "720",
		"twitter:card":          "player",
	} {
		if got := publicStatusMetaProperty(meta, property); got != want {
			t.Fatalf("%s = %q, want %q", property, got, want)
		}
	}
	status.Sensitive = true
	meta = s.publicStatusMediaMeta(status)
	if len(meta) != 1 || meta[0].Property != "twitter:card" || meta[0].Content != "summary" {
		t.Fatalf("sensitive metadata = %#v", meta)
	}
}

func publicStatusMetaProperty(meta []web.HeadMeta, property string) string {
	for _, item := range meta {
		if item.Property == property {
			return item.Content
		}
	}
	return ""
}

func publicStatusMetaName(meta []web.HeadMeta, name string) string {
	for _, item := range meta {
		if item.Name == name {
			return item.Content
		}
	}
	return ""
}
