package api

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"image"
	"io"
	"net/http"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"github.com/mstdn-plusminus-io/paon/internal/paon/serializer"
)

func TestRemoteMediaFilenameAddsContentTypeExtension(t *testing.T) {
	if got := remoteMediaFilename("https://remote.example/media/abc", "image/png"); got != "abc.png" {
		t.Fatalf("filename = %q", got)
	}
	if got := remoteMediaFilename("https://remote.example/media/a bad.gif?token=1", "image/png"); got != "a_bad.gif" {
		t.Fatalf("sanitized filename = %q", got)
	}
}

func TestDomainBlockRuleCandidatesMatchRailsRuleForVariants(t *testing.T) {
	got := domainRuleVariants("Media.Images.Remote.Example.com")
	want := []string{"media.images.remote.example.com", "images.remote.example.com", "remote.example.com", "example.com", "com"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("candidates = %#v, want %#v", got, want)
	}
}

func TestFetchRemoteImageMediaUsesActivityHTTPClientAndLimitsSize(t *testing.T) {
	oldClient := activityHTTPClient
	defer func() { activityHTTPClient = oldClient }()
	body := siteUploadTestPNG(t, 8, 6)
	activityHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Header.Get("Accept") != "image/*" {
			t.Fatalf("Accept = %q", req.Header.Get("Accept"))
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"image/png"}},
			Body:       io.NopCloser(stringsReader(string(body))),
			Request:    req,
		}, nil
	})}

	download, err := fetchRemoteImageMedia(context.Background(), "https://remote.example/media/photo", 1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	if download.filename != "photo.png" || download.contentType != "image/png" || len(download.body) != len(body) {
		t.Fatalf("download = %#v body=%d", download, len(download.body))
	}
	if _, err := fetchRemoteImageMedia(context.Background(), "https://remote.example/media/photo", len(body)-1); !errors.Is(err, errRemoteMediaSizeInvalid) {
		t.Fatalf("oversized remote media error = %v", err)
	}
}

func TestFetchRemoteMediaSupportsVideoAndAudioLikeRailsRemoteDownloads(t *testing.T) {
	oldClient := activityHTTPClient
	defer func() { activityHTTPClient = oldClient }()
	requests := make([]string, 0, 2)
	activityHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests = append(requests, req.Header.Get("Accept"))
		contentType := "video/mp4"
		body := "video"
		if len(requests) == 2 {
			contentType = "audio/mpeg"
			body = "audio"
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{contentType}},
			Body:       io.NopCloser(stringsReader(body)),
			Request:    req,
		}, nil
	})}

	video, err := fetchRemoteMedia(context.Background(), "https://remote.example/media/movie", 1024, 2)
	if err != nil {
		t.Fatal(err)
	}
	audio, err := fetchRemoteMedia(context.Background(), "https://remote.example/media/sound", 1024, 4)
	if err != nil {
		t.Fatal(err)
	}
	if video.filename != "movie.mp4" || video.contentType != "video/mp4" {
		t.Fatalf("video download = %#v", video)
	}
	if audio.filename != "sound.mp3" || audio.contentType != "audio/mpeg" {
		t.Fatalf("audio download = %#v", audio)
	}
	if !reflect.DeepEqual(requests, []string{"video/*", "audio/*"}) {
		t.Fatalf("Accept headers = %#v", requests)
	}
}

func TestFetchRemoteMediaPreservesHTTPStatusForRailsUnsalvageableRetry(t *testing.T) {
	oldClient := activityHTTPClient
	defer func() { activityHTTPClient = oldClient }()
	statuses := []int{http.StatusNotFound, http.StatusTooManyRequests}
	activityHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		status := statuses[0]
		statuses = statuses[1:]
		return &http.Response{
			StatusCode: status,
			Header:     http.Header{"Content-Type": []string{"image/png"}},
			Body:       io.NopCloser(stringsReader("")),
			Request:    req,
		}, nil
	})}

	_, err := fetchRemoteMedia(context.Background(), "https://remote.example/media/missing.png", 1024, 0)
	if status, ok := activityFetchStatus(err); !ok || status != http.StatusNotFound {
		t.Fatalf("404 fetch status = %d, %v; err=%v", status, ok, err)
	}
	if !remoteMediaErrorUnsalvageable(err) {
		t.Fatal("404 remote media fetch should be consumed like Rails response_error_unsalvageable?")
	}
	_, err = fetchRemoteMedia(context.Background(), "https://remote.example/media/rate-limited.png", 1024, 0)
	if status, ok := activityFetchStatus(err); !ok || status != http.StatusTooManyRequests {
		t.Fatalf("429 fetch status = %d, %v; err=%v", status, ok, err)
	}
	for _, want := range []string{`target=remote`, `host="remote.example"`, `status=429`} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("429 fetch error %q missing %q", err, want)
		}
	}
	if remoteMediaErrorUnsalvageable(err) {
		t.Fatal("429 remote media fetch must remain retryable like Rails response_error_unsalvageable?")
	}
}

func TestRemoteMediaValidationErrorsAreUnsalvageableLikeRailsRemotable(t *testing.T) {
	for _, err := range []error{
		errRemoteMediaURLInvalid,
		errRemoteMediaHostNotAllowed,
		errRemoteMediaSizeInvalid,
		errRemoteMediaContentTypeUnsupported,
		errRemoteMediaNotImage,
		errRemoteMediaUnreadable,
		fmt.Errorf("wrapped: %w", errRemoteMediaSizeInvalid),
	} {
		if !remoteMediaErrorUnsalvageable(err) {
			t.Errorf("%v should not be retried", err)
		}
	}
	for _, err := range []error{context.DeadlineExceeded, errors.New("storage failure")} {
		if remoteMediaErrorUnsalvageable(err) {
			t.Errorf("%v must remain retryable", err)
		}
	}
}

func TestStoreRemoteMediaFilesDownloadsRemoteThumbnailToSeparateCacheAttachment(t *testing.T) {
	root := t.TempDir()
	s := &Server{cfg: config.Config{PublicDir: root}}
	now := time.Date(2026, 6, 19, 7, 0, 0, 0, time.UTC)
	thumb := siteUploadTestPNG(t, 10, 8)
	oldClient := activityHTTPClient
	t.Cleanup(func() { activityHTTPClient = oldClient })
	activityHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != "https://remote.example/thumb.png" {
			t.Fatalf("unexpected thumbnail fetch URL: %s", req.URL.String())
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"image/png"}},
			Body:       io.NopCloser(bytes.NewReader(thumb)),
			Request:    req,
		}, nil
	})}

	media := models.MediaAttachment{
		ID:                 43,
		Type:               0,
		RemoteURL:          "https://remote.example/photo.png",
		ThumbnailRemoteURL: sql.NullString{String: "https://remote.example/thumb.png", Valid: true},
	}
	download := remoteMediaDownload{filename: "photo.png", contentType: "image/png", body: siteUploadTestPNG(t, 16, 12)}

	if err := s.storeRemoteMediaFiles(&media, download, now); err != nil {
		t.Fatal(err)
	}
	if !media.ThumbnailFileName.Valid || media.ThumbnailFileName.String != "thumb.png" {
		t.Fatalf("thumbnail file name = %#v", media.ThumbnailFileName)
	}
	if _, err := os.Stat(s.mediaThumbnailPathWithCachePrefix(43, "thumb.png", true)); err != nil {
		t.Fatalf("remote thumbnail missing: %v", err)
	}
	if _, err := os.Stat(s.mediaFileStylePathWithCachePrefix(43, "small", "photo.png", true)); !os.IsNotExist(err) {
		t.Fatalf("remote thumbnail should not be generated as files/small fallback: %v", err)
	}
	if got := mediaAttachmentObjectKeyWithCachePrefix(43, "thumbnails", "original", "thumb.png", true); got != "cache/media_attachments/thumbnails/000/000/043/original/thumb.png" {
		t.Fatalf("thumbnail object key = %q", got)
	}
}

func TestStoreRemoteMediaFilesWritesVideoAndAudioOriginals(t *testing.T) {
	root := t.TempDir()
	s := &Server{cfg: config.Config{PublicDir: root}}
	now := time.Date(2026, 6, 19, 7, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name        string
		mediaType   int
		filename    string
		contentType string
		body        []byte
	}{
		{name: "video", mediaType: 2, filename: "movie.mp4", contentType: "video/mp4", body: []byte("video")},
		{name: "audio", mediaType: 4, filename: "sound.mp3", contentType: "audio/mpeg", body: []byte("audio")},
	} {
		media := models.MediaAttachment{ID: 100 + int64(test.mediaType), Type: test.mediaType, RemoteURL: "https://remote.example/" + test.filename}
		download := remoteMediaDownload{filename: test.filename, contentType: test.contentType, body: test.body}
		if err := s.storeRemoteMediaFiles(&media, download, now); err != nil {
			t.Fatalf("%s store failed: %v", test.name, err)
		}
		if _, err := os.Stat(s.mediaFilePathWithCachePrefix(media.ID, test.filename, true)); err != nil {
			t.Fatalf("%s original missing: %v", test.name, err)
		}
		if !media.FileFileName.Valid || media.FileFileName.String != test.filename || media.FileContentType.String != test.contentType {
			t.Fatalf("%s file attrs = %#v/%#v", test.name, media.FileFileName, media.FileContentType)
		}
	}
}

func TestStoreRemoteMediaFilesConvertsRailsConvertibleImagesWhenFFmpegAvailable(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed")
	}
	root := t.TempDir()
	s := &Server{cfg: config.Config{PublicDir: root}}
	now := time.Date(2026, 6, 19, 7, 0, 0, 0, time.UTC)
	body, err := os.ReadFile(testAVIFFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	media := models.MediaAttachment{
		ID:              142,
		Type:            0,
		RemoteURL:       "https://remote.example/600x400.avif",
		FileContentType: sql.NullString{String: "image/avif", Valid: true},
	}
	download := remoteMediaDownload{filename: "600x400.avif", contentType: "image/avif", body: body}
	if err := s.storeRemoteMediaFiles(&media, download, now); err != nil {
		t.Fatal(err)
	}
	if !media.FileFileName.Valid || media.FileFileName.String != "600x400.jpeg" || media.FileContentType.String != "image/jpeg" {
		t.Fatalf("converted remote file attrs = %#v/%#v", media.FileFileName, media.FileContentType)
	}
	if media.ThumbnailFileName.Valid || media.ThumbnailContentType.Valid {
		t.Fatalf("generated remote preview must not populate explicit thumbnail attrs = %#v/%#v", media.ThumbnailFileName, media.ThumbnailContentType)
	}
	for _, path := range []string{s.mediaFilePathWithCachePrefix(media.ID, "600x400.jpeg", true), s.mediaFileStylePathWithCachePrefix(media.ID, "small", "600x400.jpeg", true)} {
		file, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		cfg, format, err := image.DecodeConfig(file)
		_ = file.Close()
		if err != nil {
			t.Fatal(err)
		}
		if format != "jpeg" || cfg.Width <= 0 || cfg.Height <= 0 {
			t.Fatalf("converted remote image %s = %#v format=%q", path, cfg, format)
		}
	}
	if _, err := os.Stat(s.mediaFilePathWithCachePrefix(media.ID, "600x400.avif", true)); !os.IsNotExist(err) {
		t.Fatalf("remote source convertible file should be removed after conversion: %v", err)
	}
}

func TestRemoteMediaCacheableIncludesRailsVideoAndAudioTypes(t *testing.T) {
	for _, media := range []models.MediaAttachment{
		{Type: 2, FileContentType: sql.NullString{String: "video/mp4", Valid: true}},
		{Type: 4, FileContentType: sql.NullString{String: "audio/mpeg", Valid: true}},
	} {
		if !activityPubMediaAttachmentCacheable(media) {
			t.Fatalf("media should be cacheable: %#v", media)
		}
	}
	if activityPubMediaAttachmentCacheable(models.MediaAttachment{Type: 2, FileContentType: sql.NullString{String: "video/x-matroska", Valid: true}}) {
		t.Fatal("unsupported video MIME should not be cacheable")
	}
}

func TestCachedRemoteMediaKeepsRemoteURLButUsesLocalRESTURL(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	out := serializer.MediaAttachmentFromModel(cfg, models.MediaAttachment{
		ID:                       42,
		Type:                     0,
		RemoteURL:                "https://remote.example/photo.png",
		FileFileName:             sql.NullString{String: "photo.png", Valid: true},
		FileStorageSchemaVersion: sql.NullInt64{Int64: 1, Valid: true},
	})
	if out.URL != "https://example.test/system/cache/media_attachments/files/000/000/042/original/photo.png" {
		t.Fatalf("URL = %q", out.URL)
	}
	if out.RemoteURL != "https://remote.example/photo.png" {
		t.Fatalf("RemoteURL = %q", out.RemoteURL)
	}
}
