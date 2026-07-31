package api

import (
	"database/sql"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestRemoveMediaAttachmentLocalFilesRemovesPaperclipDirectories(t *testing.T) {
	root := t.TempDir()
	s := &Server{cfg: config.Config{PublicDir: root}}
	attachment := models.MediaAttachment{ID: 42}
	original := s.mediaFilePath(42, "photo.png")
	thumbnail := s.mediaThumbnailPath(42, "photo.png")
	cachedOriginal := filepath.Join(root, "system", "cache", "media_attachments", "files", "000", "000", "042", "original", "photo.png")
	cachedThumbnail := filepath.Join(root, "system", "cache", "media_attachments", "thumbnails", "000", "000", "042", "original", "photo.png")
	for _, path := range []string{original, thumbnail, cachedOriginal, cachedThumbnail} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("media"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	s.removeMediaAttachmentLocalFiles(attachment)

	if _, err := os.Stat(filepath.Join(root, "system", "media_attachments", "files", "000", "000", "042")); !os.IsNotExist(err) {
		t.Fatalf("files directory still exists err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "system", "media_attachments", "thumbnails", "000", "000", "042")); !os.IsNotExist(err) {
		t.Fatalf("thumbnails directory still exists err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "system", "cache", "media_attachments", "files", "000", "000", "042")); !os.IsNotExist(err) {
		t.Fatalf("cached files directory still exists err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "system", "cache", "media_attachments", "thumbnails", "000", "000", "042")); !os.IsNotExist(err) {
		t.Fatalf("cached thumbnails directory still exists err=%v", err)
	}
}

func TestClearMediaAttachmentFileUpdatesNullsLocalFileColumns(t *testing.T) {
	now := time.Date(2026, 6, 19, 7, 30, 0, 0, time.UTC)
	updates := clearMediaAttachmentFileUpdates(now)
	if updates["file_file_name"] != (sql.NullString{}) || updates["thumbnail_file_name"] != (sql.NullString{}) {
		t.Fatalf("file names not nulled: %#v", updates)
	}
	if updates["file_storage_schema_version"] != (sql.NullInt64{}) {
		t.Fatalf("storage schema not nulled: %#v", updates["file_storage_schema_version"])
	}
	if updates["file_meta"] != nil {
		t.Fatalf("file_meta = %#v", updates["file_meta"])
	}
	if updates["blurhash"] != (sql.NullString{}) {
		t.Fatalf("blurhash = %#v", updates["blurhash"])
	}
	if updates["updated_at"] != now {
		t.Fatalf("updated_at = %#v", updates["updated_at"])
	}
}

func TestRemoveMediaAttachmentLocalFilesBustsRailsAssetURLs(t *testing.T) {
	requests := make(chan *http.Request, 3)
	previous := cacheBusterHTTPClient
	cacheBusterHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests <- req.Clone(req.Context())
		return &http.Response{StatusCode: http.StatusNoContent, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{}}, nil
	})}
	defer func() { cacheBusterHTTPClient = previous }()

	s := &Server{cfg: config.Config{
		CacheBusterEnabled:      true,
		CacheBusterHTTPMethod:   "PURGE",
		CacheBusterSecretHeader: "X-Bust-Secret",
		CacheBusterSecret:       "secret",
		CDNHost:                 "https://cdn.example",
		WebDomain:               "social.example",
		Scheme:                  "https",
		PublicDir:               t.TempDir(),
	}}
	s.removeMediaAttachmentLocalFiles(models.MediaAttachment{
		ID:                42,
		FileFileName:      sql.NullString{String: "photo one.png", Valid: true},
		ThumbnailFileName: sql.NullString{String: "thumb one.png", Valid: true},
		FileMeta:          []byte(`{"original":{"width":640,"height":480},"small":{"width":320,"height":240}}`),
	})

	gotPaths := make([]string, 0, 3)
	for i := 0; i < 3; i++ {
		select {
		case req := <-requests:
			if req.Method != "PURGE" {
				t.Fatalf("method = %q", req.Method)
			}
			if req.Header.Get("X-Bust-Secret") != "secret" {
				t.Fatalf("secret header = %q", req.Header.Get("X-Bust-Secret"))
			}
			gotPaths = append(gotPaths, req.URL.EscapedPath())
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for cache buster request")
		}
	}
	sort.Strings(gotPaths)
	want := []string{
		"/system/media_attachments/files/000/000/042/original/photo%20one.png",
		"/system/media_attachments/files/000/000/042/small/photo%20one.png",
		"/system/media_attachments/thumbnails/000/000/042/original/thumb%20one.png",
	}
	if !reflect.DeepEqual(gotPaths, want) {
		t.Fatalf("cache buster paths = %#v, want %#v", gotPaths, want)
	}
}

func TestCacheBusterAssetURLUsesStorageHostBeforeCDN(t *testing.T) {
	s := &Server{cfg: config.Config{
		StorageHost: "https://media.example",
		CDNHost:     "https://cdn.example",
		WebDomain:   "social.example",
		Scheme:      "https",
	}}
	got := s.cacheBusterMediaAttachmentURL(42, "files", "original", "photo one.png")
	want := "https://media.example/media_attachments/files/000/000/042/original/photo%20one.png"
	if got != want {
		t.Fatalf("cache buster URL = %q, want %q", got, want)
	}
}

func TestRemoveAccountImageObjectsBustsAvatarAndHeaderRailsAssetURLs(t *testing.T) {
	requests := make(chan *http.Request, 4)
	previous := cacheBusterHTTPClient
	cacheBusterHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests <- req.Clone(req.Context())
		return &http.Response{StatusCode: http.StatusNoContent, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{}}, nil
	})}
	defer func() { cacheBusterHTTPClient = previous }()

	s := &Server{cfg: config.Config{
		CacheBusterEnabled:    true,
		CacheBusterHTTPMethod: "PURGE",
		CDNHost:               "https://cdn.example",
		WebDomain:             "social.example",
		Scheme:                "https",
	}}
	s.removeAccountImageObjects(models.Account{
		ID:                42,
		AvatarFileName:    sql.NullString{String: "avatar one.jpg", Valid: true},
		AvatarContentType: sql.NullString{String: "image/jpeg", Valid: true},
		HeaderFileName:    sql.NullString{String: "header one.png", Valid: true},
		HeaderContentType: sql.NullString{String: "image/png", Valid: true},
	})

	gotPaths := make([]string, 0, 4)
	for i := 0; i < 4; i++ {
		select {
		case req := <-requests:
			if req.Method != "PURGE" {
				t.Fatalf("method = %q", req.Method)
			}
			gotPaths = append(gotPaths, req.URL.EscapedPath())
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for account image cache buster request")
		}
	}
	sort.Strings(gotPaths)
	want := []string{
		"/system/accounts/avatars/000/000/042/original/avatar%20one.jpg",
		"/system/accounts/avatars/000/000/042/static/avatar%20one.jpg",
		"/system/accounts/headers/000/000/042/original/header%20one.png",
		"/system/accounts/headers/000/000/042/static/header%20one.png",
	}
	if !reflect.DeepEqual(gotPaths, want) {
		t.Fatalf("cache buster paths = %#v, want %#v", gotPaths, want)
	}
}

func TestRemoveAccountLocalImageFilesRemovesAvatarAndHeaderDirectories(t *testing.T) {
	root := t.TempDir()
	s := &Server{cfg: config.Config{PublicDir: root}}
	avatar := s.accountImagePath(42, "avatar", "avatar.png")
	header := s.accountImagePath(42, "header", "header.png")
	if err := os.MkdirAll(filepath.Dir(avatar), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(header), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(avatar, []byte("avatar"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(header, []byte("header"), 0o644); err != nil {
		t.Fatal(err)
	}

	s.removeAccountLocalImageFiles(42)

	if _, err := os.Stat(filepath.Join(root, "system", "accounts", "avatars", "000", "000", "042")); !os.IsNotExist(err) {
		t.Fatalf("avatar directory still exists err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "system", "accounts", "headers", "000", "000", "042")); !os.IsNotExist(err) {
		t.Fatalf("header directory still exists err=%v", err)
	}
}

func TestRemoveAccountLocalImageFilesForKindRemovesOnlyRequestedDirectory(t *testing.T) {
	root := t.TempDir()
	s := &Server{cfg: config.Config{PublicDir: root}}
	avatar := s.accountImagePath(42, "avatar", "avatar.png")
	header := s.accountImagePath(42, "header", "header.png")
	for _, target := range []string{avatar, header} {
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte("image"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	s.removeAccountLocalImageFilesForKind(42, "avatar")

	if _, err := os.Stat(filepath.Join(root, "system", "accounts", "avatars", "000", "000", "042")); !os.IsNotExist(err) {
		t.Fatalf("avatar directory still exists err=%v", err)
	}
	if _, err := os.Stat(header); err != nil {
		t.Fatalf("header should remain: %v", err)
	}
}

func TestRemoveCustomEmojiLocalFilesRemovesCacheAndOriginalDirectories(t *testing.T) {
	root := t.TempDir()
	s := &Server{cfg: config.Config{PublicDir: root}}
	emoji := models.CustomEmoji{ID: 42}
	original := filepath.Join(root, "system", "custom_emojis", "images", "000", "000", "042", "original", "party.gif")
	cache := filepath.Join(root, "system", "cache", "custom_emojis", "images", "000", "000", "042", "static", "party.png")
	if err := os.MkdirAll(filepath.Dir(original), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(cache), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(original, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cache, []byte("cache"), 0o644); err != nil {
		t.Fatal(err)
	}

	s.removeCustomEmojiLocalFiles(emoji)

	if _, err := os.Stat(filepath.Join(root, "system", "custom_emojis", "images", "000", "000", "042")); !os.IsNotExist(err) {
		t.Fatalf("custom emoji directory still exists err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "system", "cache", "custom_emojis", "images", "000", "000", "042")); !os.IsNotExist(err) {
		t.Fatalf("cache custom emoji directory still exists err=%v", err)
	}
}

func TestRemoveCustomEmojiLocalFilesInvalidatesRailsEntityCache(t *testing.T) {
	src, err := os.ReadFile("domain_media_cleanup.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, src, "removeCustomEmojiLocalFiles", `s.invalidateCustomEmojiEntityCaches(context.Background(), []models.CustomEmoji{emoji})`) {
		t.Fatal("removeCustomEmojiLocalFiles must invalidate Rails EntityCache custom emoji keys")
	}
}

func TestClearAccountImageFileUpdatesNullsLocalFileColumns(t *testing.T) {
	now := time.Date(2026, 6, 19, 8, 15, 0, 0, time.UTC)
	updates := clearAccountImageFileUpdates(now)
	if updates["avatar_file_name"] != (sql.NullString{}) || updates["header_file_name"] != (sql.NullString{}) {
		t.Fatalf("file names not nulled: %#v", updates)
	}
	if updates["avatar_storage_schema_version"] != (sql.NullInt64{}) {
		t.Fatalf("avatar storage schema = %#v", updates["avatar_storage_schema_version"])
	}
	if updates["header_storage_schema_version"] != (sql.NullInt64{}) {
		t.Fatalf("header storage schema = %#v", updates["header_storage_schema_version"])
	}
	if updates["updated_at"] != now {
		t.Fatalf("updated_at = %#v", updates["updated_at"])
	}
}

func TestDomainAndSubdomainsSQLUsesExactAndChildDomainMatch(t *testing.T) {
	got := domainAndSubdomainsSQL("accounts.domain")
	want := "lower(accounts.domain) = ? OR lower(accounts.domain) LIKE ?"
	if got != want {
		t.Fatalf("domainAndSubdomainsSQL() = %q, want %q", got, want)
	}
}
