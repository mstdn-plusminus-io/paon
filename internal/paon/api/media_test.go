package api

import (
	"database/sql"
	"encoding/json"
	"image"
	"image/color"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestSanitizeFilename(t *testing.T) {
	tests := map[string]string{
		"photo.png":          "photo.png",
		"../../secret.png":   "secret.png",
		"space name @ 1.jpg": "space_name_1.jpg",
		"..":                 "upload",
		"日本語.png":            "upload.png",
	}

	for input, want := range tests {
		if got := sanitizeFilename(input); got != want {
			t.Fatalf("sanitizeFilename(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestMediaContentTypeFallback(t *testing.T) {
	if got := mediaContentType("photo.png", "application/octet-stream"); got != "image/png" {
		t.Fatalf("mediaContentType png = %q", got)
	}
	if got := mediaContentType("photo.heic", "application/octet-stream"); got != "image/heic" {
		t.Fatalf("mediaContentType heic = %q", got)
	}
	if got := mediaContentType("clip.mov", "application/octet-stream"); got != "video/quicktime" {
		t.Fatalf("mediaContentType mov = %q", got)
	}
	if got := mediaContentType("audio.wma", "application/octet-stream"); got != "video/x-ms-asf" {
		t.Fatalf("mediaContentType wma = %q", got)
	}
	if got := mediaContentType("clip.mp4", "video/mp4; charset=binary"); got != "video/mp4" {
		t.Fatalf("mediaContentType header = %q", got)
	}
}

func TestMediaTypeFromContentType(t *testing.T) {
	tests := map[string]int{
		"image/png":      0,
		"image/gif":      1,
		"video/mp4":      2,
		"audio/mpeg":     4,
		"video/x-ms-asf": 4,
		"text/plain":     3,
	}
	for contentType, want := range tests {
		if got := mediaTypeFromContentType(contentType); got != want {
			t.Fatalf("mediaTypeFromContentType(%q) = %d, want %d", contentType, got, want)
		}
	}
}

func TestMediaContentTypeSupportedMatchesRailsMediaTypes(t *testing.T) {
	tests := []struct {
		contentType string
		mediaType   int
		want        bool
	}{
		{contentType: "image/heic", mediaType: 0, want: true},
		{contentType: "image/avif", mediaType: 0, want: true},
		{contentType: "video/quicktime", mediaType: 2, want: true},
		{contentType: "audio/x-m4a", mediaType: 4, want: true},
		{contentType: "video/x-ms-asf", mediaType: 4, want: true},
		{contentType: "image/svg+xml", mediaType: 0, want: false},
		{contentType: "video/x-matroska", mediaType: 2, want: false},
	}
	for _, test := range tests {
		if got := mediaContentTypeSupported(test.contentType, test.mediaType); got != test.want {
			t.Fatalf("mediaContentTypeSupported(%q, %d) = %v, want %v", test.contentType, test.mediaType, got, test.want)
		}
	}
}

func TestMediaOriginalThumbnailEligibility(t *testing.T) {
	if !mediaOriginalRequiresReadableImage("image/png", 0) || !mediaOriginalCanGenerateThumbnail("image/webp", 0) {
		t.Fatal("readable images should require validation and allow thumbnail generation")
	}
	if !mediaOriginalConvertibleImageContentType("image/heic", 0) || !mediaOriginalConvertibleImageContentType("image/heif", 0) || !mediaOriginalConvertibleImageContentType("image/avif", 0) {
		t.Fatal("Rails-compatible convertible images should use the converted image pipeline")
	}
	if mediaOriginalRequiresReadableImage("image/heic", 0) || mediaOriginalCanGenerateThumbnail("image/avif", 0) {
		t.Fatal("convertible images should skip Go decoder validation and PNG thumbnail generation")
	}
	if mediaOriginalRequiresReadableImage("video/mp4", 2) || mediaOriginalCanGenerateThumbnail("video/mp4", 2) {
		t.Fatal("video originals should not use image validation")
	}
}

func TestMediaAttachmentStatusCodeMatchesRailsProcessingState(t *testing.T) {
	tests := []struct {
		name        string
		processing  sql.NullInt64
		wantStatus  int
		wantPending bool
		wantFailed  bool
	}{
		{name: "unset", processing: sql.NullInt64{}, wantStatus: http.StatusOK},
		{name: "queued", processing: sql.NullInt64{Int64: 0, Valid: true}, wantStatus: http.StatusPartialContent, wantPending: true},
		{name: "in_progress", processing: sql.NullInt64{Int64: 1, Valid: true}, wantStatus: http.StatusPartialContent, wantPending: true},
		{name: "complete", processing: sql.NullInt64{Int64: 2, Valid: true}, wantStatus: http.StatusOK},
		{name: "failed", processing: sql.NullInt64{Int64: 3, Valid: true}, wantStatus: http.StatusPartialContent, wantPending: true, wantFailed: true},
	}
	for _, test := range tests {
		attachment := models.MediaAttachment{Processing: test.processing}
		if got := mediaAttachmentStatusCode(attachment); got != test.wantStatus {
			t.Fatalf("%s status = %d, want %d", test.name, got, test.wantStatus)
		}
		if got := mediaAttachmentNotProcessed(attachment); got != test.wantPending {
			t.Fatalf("%s notProcessed = %v, want %v", test.name, got, test.wantPending)
		}
		if got := mediaAttachmentProcessingFailed(attachment); got != test.wantFailed {
			t.Fatalf("%s failed = %v, want %v", test.name, got, test.wantFailed)
		}
	}
}

func TestUploadedThumbnailAllowedOnlyForRailsAudioOrVideoMedia(t *testing.T) {
	cases := map[int]bool{
		0: false,
		1: false,
		2: true,
		3: false,
		4: true,
	}
	for mediaType, want := range cases {
		if got := mediaAttachmentAllowsUploadedThumbnail(mediaType); got != want {
			t.Fatalf("mediaAttachmentAllowsUploadedThumbnail(%d) = %v, want %v", mediaType, got, want)
		}
	}
}

func TestMediaThumbnailUploadValidationMatchesRailsModel(t *testing.T) {
	src, err := os.ReadFile("media.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`if hasThumbnail && !mediaAttachmentAllowsUploadedThumbnail(mediaType)`,
		`if hasThumbnail && !mediaAttachmentAllowsUploadedThumbnail(attachment.Type)`,
		`return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Thumbnail must be blank")`,
	} {
		if !strings.Contains(string(src), want) {
			t.Fatalf("media thumbnail Rails validation missing %q", want)
		}
	}
}

func TestUpdateMediaPersistsNullableDescription(t *testing.T) {
	src, err := os.ReadFile("media.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, src, "updateMedia", `updates["description"] = sql.NullString{String: description, Valid: description != ""}`) {
		t.Fatal("updateMedia should persist JSON null descriptions as SQL NULL")
	}
	if !functionBodyContains(t, src, "updateMedia", `mediaUpdateJSONFields(c)`) {
		t.Fatal("updateMedia should use JSON field presence parsing instead of pointer binding")
	}
}

func TestMediaV2RouteUsesDelayProcessingHandler(t *testing.T) {
	serverSrc, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	mediaSrc, err := os.ReadFile("media.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(serverSrc) + string(mediaSrc)
	for _, want := range []string{
		`e.POST("/api/v1/media", s.createMedia)`,
		`e.POST("/api/v2/media", s.createMediaV2)`,
		`func (s *Server) createMediaV2(c *echo.Context) error`,
		`return s.createMediaWithOptions(c, true)`,
	} {
		if !strings.Contains(src, want) {
			t.Fatalf("media v2 compatibility missing %q", want)
		}
	}
}

func TestMediaThumbnailAttributesValidatesReadableImage(t *testing.T) {
	header := siteUploadTestFileHeader(t, "thumb.png", siteUploadTestPNG(t, 16, 9))
	got, err := mediaThumbnailAttributes(header)
	if err != nil {
		t.Fatal(err)
	}
	if got.filename != "thumb.png" || got.contentType != "image/png" || got.size <= 0 {
		t.Fatalf("thumbnail attrs = %#v", got)
	}
}

func TestImageConfigFromHeaderRejectsUnreadableImageOriginal(t *testing.T) {
	header := siteUploadTestFileHeader(t, "photo.png", []byte("not an image"))
	if _, err := imageConfigFromHeader(header); err == nil {
		t.Fatal("expected unreadable image original to be rejected")
	}
}

func TestMediaThumbnailAttributesRejectsUnreadableImage(t *testing.T) {
	header := siteUploadTestFileHeader(t, "thumb.png", []byte("not an image"))
	if _, err := mediaThumbnailAttributes(header); err == nil {
		t.Fatal("expected unreadable thumbnail to be rejected")
	}
}

func TestMediaLimitHelpersPreserveExplicitRailsToIValues(t *testing.T) {
	server := &Server{cfg: config.Config{
		ImageSizeLimit:    0,
		ImageSizeLimitSet: true,
		VideoSizeLimit:    -1,
		VideoSizeLimitSet: true,
		MatrixLimit:       0,
		MatrixLimitSet:    true,
	}}
	if got := server.imageSizeLimitBytes(); got != 0 {
		t.Fatalf("explicit zero IMAGE_LIMIT_MEGABYTES limit = %d, want 0", got)
	}
	if got := server.videoSizeLimitBytes(); got != -1 {
		t.Fatalf("explicit negative VIDEO_LIMIT_MEGABYTES limit = %d, want -1", got)
	}
	if got := server.mediaMatrixLimit(); got != 0 {
		t.Fatalf("explicit zero MAX_ATTACHMENT_MATRIX_LIMIT limit = %d, want 0", got)
	}
	if got := server.remoteMediaSizeLimit(2); got != -1 {
		t.Fatalf("remote video limit = %d, want -1", got)
	}
	if got := server.remoteMediaSizeLimit(0); got != 0 {
		t.Fatalf("remote image limit = %d, want 0", got)
	}
}

func TestUploadedVideoValidationUsesRailsFFProbeDimensions(t *testing.T) {
	script := filepath.Join(t.TempDir(), "ffprobe")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ncat \"$FFPROBE_JSON\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FFPROBE_BINARY", script)
	jsonPath := filepath.Join(t.TempDir(), "probe.json")
	t.Setenv("FFPROBE_JSON", jsonPath)
	server := &Server{cfg: config.Config{VideoSizeLimit: 1 << 20, MatrixLimit: 100}}
	header := siteUploadTestFileHeader(t, "clip.mp4", []byte("video bytes"))

	cases := []struct {
		name string
		raw  string
		want string
	}{
		{name: "no_video_stream", raw: `{"format":{},"streams":[{"codec_type":"audio"}]}`, want: "Video has no video stream"},
		{name: "matrix", raw: `{"format":{},"streams":[{"codec_type":"video","width":20,"height":20,"r_frame_rate":"30/1"}]}`, want: "20x20 videos are not supported"},
		{name: "frame_rate", raw: `{"format":{},"streams":[{"codec_type":"video","width":10,"height":10,"r_frame_rate":"121/1"}]}`, want: "121fps videos are not supported"},
		{name: "valid", raw: `{"format":{},"streams":[{"codec_type":"video","width":10,"height":10,"r_frame_rate":"120/1"}]}`, want: ""},
	}
	for _, tc := range cases {
		if err := os.WriteFile(jsonPath, []byte(tc.raw), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := server.validateUploadedMediaAttachment(header, 2); got != tc.want {
			t.Fatalf("%s validation = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestGeneratedMediaThumbnailFilenameUsesPNGExtension(t *testing.T) {
	if got := generatedMediaThumbnailFilename("photo.large.jpg"); got != "photo.large.png" {
		t.Fatalf("thumbnail filename = %q", got)
	}
	if got := convertedMediaImageFilename("600x400.avif"); got != "600x400.jpeg" {
		t.Fatalf("converted filename = %q", got)
	}
}

func TestThumbnailDimensionsMatchRailsSmallPixelLimit(t *testing.T) {
	width, height := thumbnailDimensions(2000, 1000, mediaThumbnailMaxPixels)
	if width*height > mediaThumbnailMaxPixels {
		t.Fatalf("thumbnail dimensions = %dx%d exceeds limit", width, height)
	}
	if width != 678 || height != 339 {
		t.Fatalf("thumbnail dimensions = %dx%d, want 678x339", width, height)
	}
	width, height = thumbnailDimensions(320, 240, mediaThumbnailMaxPixels)
	if width != 320 || height != 240 {
		t.Fatalf("small image dimensions = %dx%d", width, height)
	}
}

func TestResizeImageUsesInterpolatingScaler(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			if (x+y)%2 == 0 {
				img.Set(x, y, color.White)
			} else {
				img.Set(x, y, color.Black)
			}
		}
	}

	for name, resized := range map[string]image.Image{
		"fill":       resizeImageToFill(img, 4, 4),
		"max-pixels": resizeImageToMaxPixels(img, 16),
	} {
		if resized.Bounds().Dx() != 4 || resized.Bounds().Dy() != 4 {
			t.Fatalf("%s bounds = %v", name, resized.Bounds())
		}
		if !imageHasInterpolatedPixel(resized) {
			t.Fatalf("%s resize used only source colors; expected interpolated pixels", name)
		}
	}
}

func imageHasInterpolatedPixel(img image.Image) bool {
	bounds := img.Bounds()
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			r, g, b, _ := img.At(x, y).RGBA()
			if isIntermediateColor(r) || isIntermediateColor(g) || isIntermediateColor(b) {
				return true
			}
		}
	}
	return false
}

func isIntermediateColor(v uint32) bool {
	return v != 0 && v != 0xffff
}

func TestGenerateImageThumbnailFileWritesReadablePNG(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "original.png")
	target := filepath.Join(root, "thumbnails", "photo.png")
	if err := os.WriteFile(source, siteUploadTestPNG(t, 2000, 1000), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := generateImageThumbnailFile(source, target); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(target)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	cfg, format, err := image.DecodeConfig(file)
	if err != nil {
		t.Fatal(err)
	}
	if format != "png" {
		t.Fatalf("format = %q", format)
	}
	if cfg.Width*cfg.Height > mediaThumbnailMaxPixels {
		t.Fatalf("generated dimensions = %dx%d", cfg.Width, cfg.Height)
	}
}

func TestMediaMetaWithGeometryAddsSmallMetadata(t *testing.T) {
	root := t.TempDir()
	original := filepath.Join(root, "original.png")
	small := filepath.Join(root, "small.png")
	if err := os.WriteFile(original, siteUploadTestPNG(t, 640, 480), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(small, siteUploadTestPNG(t, 320, 240), 0o644); err != nil {
		t.Fatal(err)
	}
	raw := mediaMetaForStoredFile(original, 0)
	raw, ok := mediaMetaWithGeometry(raw, "small", small)
	if !ok {
		t.Fatal("small metadata was not added")
	}
	raw, ok = mediaMetaWithFocus(raw, "0.1,-0.2")
	if !ok {
		t.Fatal("focus metadata was not added")
	}
	var meta map[string]any
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatal(err)
	}
	originalMeta := meta["original"].(map[string]any)
	if originalMeta["width"].(float64) != 640 || originalMeta["height"].(float64) != 480 {
		t.Fatalf("original metadata = %#v", originalMeta)
	}
	smallMeta := meta["small"].(map[string]any)
	if smallMeta["width"].(float64) != 320 || smallMeta["height"].(float64) != 240 || smallMeta["size"] != "320x240" {
		t.Fatalf("small metadata = %#v", smallMeta)
	}
	focus := meta["focus"].(map[string]any)
	if focus["x"].(float64) != 0.1 || focus["y"].(float64) != -0.2 {
		t.Fatalf("focus = %#v", focus)
	}
}

func TestMediaMetaFromFFProbeJSONMatchesRailsVideoMetadata(t *testing.T) {
	raw := []byte(`{
		"format":{"duration":"12.345000","bit_rate":"345678"},
		"streams":[{
			"codec_type":"video",
			"width":1920,
			"height":1080,
			"avg_frame_rate":"0/0",
			"r_frame_rate":"30000/1001",
			"side_data_list":[{"rotation":90}]
		},{"codec_type":"audio"}]
	}`)
	meta := map[string]any{}
	if err := json.Unmarshal(mediaMetaFromFFProbeJSON(raw), &meta); err != nil {
		t.Fatal(err)
	}
	original := meta["original"].(map[string]any)
	if original["width"].(float64) != 1080 || original["height"].(float64) != 1920 || original["size"] != "1080x1920" {
		t.Fatalf("rotated dimensions = %#v", original)
	}
	if original["frame_rate"] != "30000/1001" || original["duration"].(float64) != 12.345 || original["bitrate"].(float64) != 345678 {
		t.Fatalf("ffprobe metadata = %#v", original)
	}
}

func TestMediaMetaFromFFProbeJSONMatchesRailsAudioMetadata(t *testing.T) {
	raw := []byte(`{
		"format":{"duration":"3.250000","bit_rate":"128000"},
		"streams":[{"codec_type":"audio"}]
	}`)
	meta := map[string]any{}
	if err := json.Unmarshal(mediaMetaFromFFProbeJSON(raw), &meta); err != nil {
		t.Fatal(err)
	}
	original := meta["original"].(map[string]any)
	if original["duration"].(float64) != 3.25 || original["bitrate"].(float64) != 128000 {
		t.Fatalf("audio metadata = %#v", original)
	}
	if _, ok := original["width"]; ok {
		t.Fatalf("audio metadata should not contain video dimensions: %#v", original)
	}
}

func TestMediaMetaForStoredVideoUsesFFProbeWhenAvailable(t *testing.T) {
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe is not installed")
	}
	raw := mediaMetaForStoredFile(testWebMFixture(t), 2)
	if len(raw) == 0 {
		t.Fatal("ffprobe did not return media metadata")
	}
	meta := map[string]any{}
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatal(err)
	}
	original := meta["original"].(map[string]any)
	if original["duration"].(float64) <= 0 {
		t.Fatalf("video duration missing: %#v", original)
	}
	if original["frame_rate"] == "" {
		t.Fatalf("video frame_rate missing: %#v", original)
	}
}

func TestCompactMediaIDsMatchesRailsArrayParams(t *testing.T) {
	got := compactMediaIDs([]string{"1", "2,3", "2", "", " 4 "})
	want := []string{"1", "2,3", "2", "4"}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %#v, want %#v", got, want)
		}
	}
}

func TestMediaPaperclipIDPartition(t *testing.T) {
	tests := map[int64]string{
		123:                "000/000/123",
		110902934819309161: "110/902/934/819/309/161",
	}
	for id, want := range tests {
		if got := mediaPaperclipIDPartition(id); got != want {
			t.Fatalf("mediaPaperclipIDPartition(%d) = %q, want %q", id, got, want)
		}
	}
}

func TestMediaMetaWithFocusPreservesExistingMetadata(t *testing.T) {
	got, ok := mediaMetaWithFocus([]byte(`{"original":{"width":640,"height":480}}`), "0.25,-0.5")
	if !ok {
		t.Fatal("mediaMetaWithFocus rejected valid focus")
	}
	var meta map[string]any
	if err := json.Unmarshal(got, &meta); err != nil {
		t.Fatal(err)
	}
	original := meta["original"].(map[string]any)
	if original["width"].(float64) != 640 || original["height"].(float64) != 480 {
		t.Fatalf("original metadata = %#v", original)
	}
	focus := meta["focus"].(map[string]any)
	if focus["x"].(float64) != 0.25 || focus["y"].(float64) != -0.5 {
		t.Fatalf("focus = %#v", focus)
	}
}

func TestMediaMetaWithFocusRejectsInvalidFocus(t *testing.T) {
	if _, ok := mediaMetaWithFocus(nil, "not-a-point"); ok {
		t.Fatal("mediaMetaWithFocus accepted invalid focus")
	}
}

func TestPublicMediaRedirectURLUsesOriginalPaperclipURL(t *testing.T) {
	s := &Server{cfg: config.Config{WebDomain: "example.test", Scheme: "https"}}
	got := s.publicMediaRedirectURL(models.MediaAttachment{
		ID:           123,
		FileFileName: sql.NullString{String: "photo one.png", Valid: true},
	})
	want := "https://example.test/system/media_attachments/files/000/000/123/original/photo%20one.png"
	if got != want {
		t.Fatalf("publicMediaRedirectURL = %q, want %q", got, want)
	}
}

func TestPublicMediaStatusVisible(t *testing.T) {
	s := &Server{}
	if visible, err := s.publicMediaStatusVisible(models.Status{ID: 1, AccountID: 10, Visibility: 0}, nil); err != nil || !visible {
		t.Fatal("public status should be visible")
	}
	if visible, err := s.publicMediaStatusVisible(models.Status{ID: 1, AccountID: 10, Visibility: 1}, nil); err != nil || !visible {
		t.Fatal("unlisted status should be visible")
	}
	if visible, err := s.publicMediaStatusVisible(models.Status{ID: 1, AccountID: 10, Visibility: 2}, nil); err != nil || visible {
		t.Fatal("private status should be hidden")
	}
	if visible, err := s.publicMediaStatusVisible(models.Status{ID: 1, AccountID: 10, Visibility: 2}, &models.Account{ID: 10}); err != nil || !visible {
		t.Fatal("private status media should be visible to the owner like Rails StatusPolicy#show?")
	}
	if visible, err := s.publicMediaStatusVisible(models.Status{ID: 1, AccountID: 10, Visibility: 3}, &models.Account{ID: 10}); err != nil || !visible {
		t.Fatal("direct status media should be visible to the owner like Rails StatusPolicy#show?")
	}
	if visible, err := s.publicMediaStatusVisible(models.Status{ID: 1, AccountID: 10, Visibility: 0, ReblogOfID: sql.NullInt64{Int64: 2, Valid: true}}, &models.Account{ID: 10}); err != nil || visible {
		t.Fatal("reblog media should be hidden")
	}
	if visible, err := s.publicMediaStatusVisible(models.Status{}, nil); err != nil || visible {
		t.Fatal("missing status should be hidden")
	}
}

func TestPublicMediaRoutesRequireAuthenticationInLimitedFederationMode(t *testing.T) {
	src, err := os.ReadFile("media.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, fn := range []string{"publicMedia", "publicMediaPlayer", "mediaProxy", "downloadProxy"} {
		if !functionBodyContains(t, src, fn, `s.requireMediaProxyAuthenticationIfLimited(c)`) {
			t.Fatalf("%s must match Rails limited federation authentication gate", fn)
		}
	}
}

func TestMediaPlayerHTMLEscapesSourceAndDescription(t *testing.T) {
	s := &Server{cfg: config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}}
	got := s.mediaPlayerHTML(`https://example.test/a.mp4?x=<bad>`, models.MediaAttachment{
		ID:                42,
		Type:              2,
		FileFileName:      sql.NullString{String: "a.mp4", Valid: true},
		Description:       sql.NullString{String: `<caption>`, Valid: true},
		Blurhash:          sql.NullString{String: "blur", Valid: true},
		Processing:        sql.NullInt64{Int64: 2, Valid: true},
		ThumbnailFileName: sql.NullString{String: "thumb.jpg", Valid: true},
		FileMeta:          []byte(`{"original":{"frame_rate":"30000/1001"}}`),
	})
	if !strings.Contains(got, `data-component="Video"`) || !strings.Contains(got, `data-props=`) {
		t.Fatalf("unexpected video player html: %s", got)
	}
	if !strings.Contains(got, `<video controls="controls"><source src="https://example.test/a.mp4?x=&lt;bad&gt;"></video>`) {
		t.Fatalf("unexpected video fallback html: %s", got)
	}
	if strings.Contains(got, `<caption>`) || strings.Contains(got, `<bad>`) {
		t.Fatalf("player html was not escaped: %s", got)
	}
	if !strings.Contains(got, `&#34;alt&#34;:&#34;\u003ccaption\u003e&#34;`) ||
		!strings.Contains(got, `&#34;frameRate&#34;:&#34;30000/1001&#34;`) ||
		!strings.Contains(got, `&#34;blurhash&#34;:&#34;blur&#34;`) ||
		!strings.Contains(got, `&#34;media&#34;:[`) ||
		!strings.Contains(got, `<script src="/packs/js/public.js" crossorigin="anonymous" defer></script>`) {
		t.Fatalf("video player props do not match Rails bootstrap shape: %s", got)
	}

	audio := s.mediaPlayerHTML("https://example.test/a.mp3", models.MediaAttachment{
		ID:           43,
		Type:         4,
		FileFileName: sql.NullString{String: "a.mp3", Valid: true},
		Processing:   sql.NullInt64{Int64: 2, Valid: true},
		FileMeta:     []byte(`{"original":{"duration":12.5},"colors":{"background":"#111111","foreground":"#eeeeee","accent":"#ffcc00"}}`),
	})
	if !strings.Contains(audio, `data-component="Audio"`) ||
		!strings.Contains(audio, `<audio controls="controls"><source src="https://example.test/a.mp3"></audio>`) ||
		!strings.Contains(audio, `&#34;backgroundColor&#34;:&#34;#111111&#34;`) ||
		!strings.Contains(audio, `&#34;duration&#34;:12.5`) {
		t.Fatalf("unexpected audio player html: %s", audio)
	}
	gifv := s.mediaPlayerHTML("https://example.test/a.mp4", models.MediaAttachment{
		ID:           44,
		Type:         1,
		FileFileName: sql.NullString{String: "a.mp4", Valid: true},
		Processing:   sql.NullInt64{Int64: 2, Valid: true},
	})
	if !strings.Contains(gifv, `data-component="MediaGallery"`) ||
		!strings.Contains(gifv, `<video autoplay="autoplay" muted="muted" loop="loop"><source src="https://example.test/a.mp4"></video>`) ||
		!strings.Contains(gifv, `&#34;standalone&#34;:true`) ||
		!strings.Contains(gifv, `&#34;autoplay&#34;:true`) {
		t.Fatalf("unexpected gifv player html: %s", gifv)
	}
}

func TestMediaProxyRedirectURLPrefersThumbnailForSmallVersion(t *testing.T) {
	s := &Server{cfg: config.Config{WebDomain: "example.test", Scheme: "https"}}
	attachment := models.MediaAttachment{
		ID:                123,
		FileFileName:      sql.NullString{String: "photo.png", Valid: true},
		ThumbnailFileName: sql.NullString{String: "thumb.png", Valid: true},
		RemoteURL:         "https://remote.example/photo.png",
	}
	got := s.mediaProxyRedirectURL(attachment, "small")
	want := "https://example.test/system/media_attachments/thumbnails/000/000/123/original/thumb.png"
	if got != want {
		t.Fatalf("small proxy url = %q, want %q", got, want)
	}
	if got := s.mediaProxyRedirectURL(attachment, "original"); got != "https://example.test/system/media_attachments/files/000/000/123/original/photo.png" {
		t.Fatalf("original proxy url = %q", got)
	}
}

func TestMediaProxyRedirectURLUsesStorageHostForPaperclipAssets(t *testing.T) {
	s := &Server{cfg: config.Config{WebDomain: "example.test", Scheme: "https", StorageHost: "https://media.example.test"}}
	attachment := models.MediaAttachment{
		ID:                123,
		FileFileName:      sql.NullString{String: "photo.png", Valid: true},
		ThumbnailFileName: sql.NullString{String: "thumb.png", Valid: true},
	}
	if got := s.mediaProxyRedirectURL(attachment, "small"); got != "https://media.example.test/media_attachments/thumbnails/000/000/123/original/thumb.png" {
		t.Fatalf("small storage-host proxy url = %q", got)
	}
	if got := s.mediaProxyRedirectURL(attachment, "original"); got != "https://media.example.test/media_attachments/files/000/000/123/original/photo.png" {
		t.Fatalf("original storage-host proxy url = %q", got)
	}
}

func TestMediaProxyRedirectURLUsesAzureAndSwiftExpiringURLs(t *testing.T) {
	attachment := models.MediaAttachment{
		ID:           123,
		FileFileName: sql.NullString{String: "photo one.png", Valid: true},
	}
	azure := &Server{cfg: config.Config{
		AzureEnabled:          true,
		AzureStorageAccount:   "acct",
		AzureStorageAccessKey: "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=",
		AzureContainerName:    "media",
	}}
	got := azure.mediaProxyRedirectURL(attachment, "original")
	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Host != "acct.blob.core.windows.net" || parsed.EscapedPath() != "/media/media_attachments/files/000/000/123/original/photo%20one.png" {
		t.Fatalf("azure media proxy URL = %s", got)
	}
	if parsed.Query().Get("sig") == "" || parsed.Query().Get("sp") != "r" {
		t.Fatalf("azure media proxy query = %s", parsed.RawQuery)
	}

	swift := &Server{cfg: config.Config{
		SwiftEnabled:    true,
		SwiftObjectURL:  "https://swift.example.test/v1/AUTH_project/container",
		SwiftTempURLKey: "temp-key",
	}}
	got = swift.mediaProxyRedirectURL(attachment, "original")
	parsed, err = url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Host != "swift.example.test" || parsed.EscapedPath() != "/v1/AUTH_project/container/media_attachments/files/000/000/123/original/photo%20one.png" {
		t.Fatalf("swift media proxy URL = %s", got)
	}
	if parsed.Query().Get("temp_url_sig") == "" || parsed.Query().Get("temp_url_expires") == "" {
		t.Fatalf("swift media proxy query = %s", parsed.RawQuery)
	}
}

func TestMediaProxyRedirectURLUsesLocalSmallStyleWhenNoThumbnail(t *testing.T) {
	s := &Server{cfg: config.Config{WebDomain: "example.test", Scheme: "https"}}
	for _, tc := range []struct {
		name string
		kind int
		file string
		want string
	}{
		{name: "image", kind: 0, file: "photo.png", want: "https://example.test/system/media_attachments/files/000/000/123/small/photo.png"},
		{name: "gifv", kind: 1, file: "clip.mp4", want: "https://example.test/system/media_attachments/files/000/000/123/small/clip.png"},
		{name: "video", kind: 2, file: "video.mp4", want: "https://example.test/system/media_attachments/files/000/000/123/small/video.png"},
		{name: "audio", kind: 4, file: "audio.mp3", want: "https://example.test/system/media_attachments/files/000/000/123/original/audio.mp3"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			attachment := models.MediaAttachment{
				ID:           123,
				Type:         tc.kind,
				FileFileName: sql.NullString{String: tc.file, Valid: true},
			}
			if got := s.mediaProxyRedirectURL(attachment, "small"); got != tc.want {
				t.Fatalf("small proxy url = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestMediaProxyRedirectURLDoesNotUseLocalSmallStyleWhileProcessing(t *testing.T) {
	s := &Server{cfg: config.Config{WebDomain: "example.test", Scheme: "https"}}
	attachment := models.MediaAttachment{
		ID:           123,
		Type:         2,
		FileFileName: sql.NullString{String: "video.mp4", Valid: true},
		Processing:   sql.NullInt64{Int64: 0, Valid: true},
	}
	if got := s.mediaProxyRedirectURL(attachment, "small"); got != "https://example.test/system/media_attachments/files/000/000/123/original/video.mp4" {
		t.Fatalf("processing small proxy url = %q", got)
	}
}

func TestMediaProxyRedirectURLFallsBackToRemoteURL(t *testing.T) {
	s := &Server{cfg: config.Config{WebDomain: "example.test", Scheme: "https"}}
	attachment := models.MediaAttachment{RemoteURL: "https://remote.example/media/video.mp4?token=1"}
	if got := s.mediaProxyRedirectURL(attachment, "original"); got != attachment.RemoteURL {
		t.Fatalf("remote proxy url = %q", got)
	}
}

func TestDownloadProxyLocalPathPrefersRequestedLocalFile(t *testing.T) {
	s := &Server{cfg: config.Config{PublicDir: "/srv/paon/public"}}
	attachment := models.MediaAttachment{
		ID:                123,
		FileFileName:      sql.NullString{String: "photo.png", Valid: true},
		ThumbnailFileName: sql.NullString{String: "thumb.png", Valid: true},
	}
	if got := s.downloadProxyLocalPath(attachment, "small"); got != "/srv/paon/public/system/media_attachments/thumbnails/000/000/123/original/thumb.png" {
		t.Fatalf("small local path = %q", got)
	}
	if got := s.downloadProxyLocalPath(attachment, "original"); got != "/srv/paon/public/system/media_attachments/files/000/000/123/original/photo.png" {
		t.Fatalf("original local path = %q", got)
	}
	attachment.FileFileName = sql.NullString{String: "../photo.png", Valid: true}
	if strings.Contains(s.downloadProxyLocalPath(attachment, "original"), "..") {
		t.Fatal("local path kept unsafe filename components")
	}

	s.cfg.PaperclipRootPath = "/mnt/mastodon/system"
	attachment.FileFileName = sql.NullString{String: "photo.png", Valid: true}
	if got := s.downloadProxyLocalPath(attachment, "original"); got != "/mnt/mastodon/system/media_attachments/files/000/000/123/original/photo.png" {
		t.Fatalf("custom root original local path = %q", got)
	}
}

func TestDownloadProxyLocalPathUsesLocalSmallStyleWhenNoThumbnail(t *testing.T) {
	s := &Server{cfg: config.Config{PublicDir: "/srv/paon/public"}}
	attachment := models.MediaAttachment{
		ID:           123,
		Type:         0,
		FileFileName: sql.NullString{String: "../photo.png", Valid: true},
	}
	if got := s.downloadProxyLocalPath(attachment, "small"); got != "/srv/paon/public/system/media_attachments/files/000/000/123/small/photo.png" {
		t.Fatalf("small local path = %q", got)
	}

	attachment.Type = 4
	attachment.FileFileName = sql.NullString{String: "audio.mp3", Valid: true}
	if got := s.downloadProxyLocalPath(attachment, "small"); got != "/srv/paon/public/system/media_attachments/files/000/000/123/original/audio.mp3" {
		t.Fatalf("audio local path = %q", got)
	}
}

func TestSetDownloadProxyHeadersMatchesRailsDownloadProxy(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/download_proxy/123/original", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)
	s := &Server{}

	s.setDownloadProxyHeaders(c, models.MediaAttachment{
		FileContentType: sql.NullString{String: "image/png", Valid: true},
	}, `https://remote.example/path/photo.png?token=1`)

	headers := rec.Header()
	if headers.Get("Access-Control-Allow-Origin") != "*" || headers.Get("Access-Control-Allow-Methods") != "GET, HEAD, OPTIONS" || headers.Get("Access-Control-Allow-Headers") != "Content-Type, Authorization" {
		t.Fatalf("cors headers = %#v", headers)
	}
	if headers.Get("Content-Type") != "image/png" {
		t.Fatalf("content type = %q", headers.Get("Content-Type"))
	}
	if headers.Get("Content-Disposition") != `attachment; filename="photo.png"` {
		t.Fatalf("content disposition = %q", headers.Get("Content-Disposition"))
	}
}

func TestStreamDownloadProxyRemoteCopiesResponseBody(t *testing.T) {
	previous := activityHTTPClient
	t.Cleanup(func() { activityHTTPClient = previous })
	activityHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != "https://remote.example/media/photo.png" {
			t.Fatalf("unexpected remote download URL: %s", req.URL.String())
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"image/png"}},
			Body:       io.NopCloser(strings.NewReader("image-bytes")),
		}, nil
	})}

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/download_proxy/123/original", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)
	s := &Server{}

	streamed, err := s.streamDownloadProxyRemote(c, "https://remote.example/media/photo.png")
	if err != nil {
		t.Fatal(err)
	}
	if !streamed {
		t.Fatal("remote media was not streamed")
	}
	if rec.Code != http.StatusOK || rec.Body.String() != "image-bytes" {
		t.Fatalf("status = %d body = %q", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("content type = %q", rec.Header().Get("Content-Type"))
	}
}

func TestDownloadProxyStreamsLocalFilesBeforeRedirecting(t *testing.T) {
	src, err := os.ReadFile("media.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`target := s.downloadProxyTargetURL(*attachment, version)`,
		`if localPath := s.downloadProxyLocalPath(*attachment, version); localPath != ""`,
		`return s.serveDownloadProxyLocalFile(c, localPath)`,
		`if remoteURL := downloadProxyRemoteURL(*attachment); remoteURL != ""`,
		`s.streamDownloadProxyRemote(c, remoteURL)`,
		`return apiError(c, http.StatusNotFound, "Record not found")`,
		`return c.Redirect(http.StatusFound, target)`,
	} {
		if !strings.Contains(string(src), want) {
			t.Fatalf("downloadProxy missing %q", want)
		}
	}
}

func TestDownloadProxyLocalFileStreamsWhenSendfileHeaderUnset(t *testing.T) {
	localPath := filepath.Join(t.TempDir(), "photo.txt")
	if err := os.WriteFile(localPath, []byte("image-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/download_proxy/123/original", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)
	s := &Server{}
	if err := s.serveDownloadProxyLocalFile(c, localPath); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK || rec.Body.String() != "image-bytes" {
		t.Fatalf("status = %d body = %q", rec.Code, rec.Body.String())
	}
}

func TestProxyRoutesReachHandlersWithoutDatabase(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com", WebDomain: "example.com", Scheme: "https"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/media_proxy/1/original", "/media_proxy/1/small", "/download_proxy/1/original"} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()
			s.echo.ServeHTTP(rec, req)
			if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "Record not found") {
				t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestProxyRoutesRequireAuthenticationInLimitedFederationMode(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com", WebDomain: "example.com", Scheme: "https", LimitedFederationMode: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/media_proxy/1/original", "/download_proxy/1/original"} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path+"?x=1", nil)
			rec := httptest.NewRecorder()
			s.echo.ServeHTTP(rec, req)
			want := "/auth/sign_in?redirect_to=" + url.QueryEscape(path+"?x=1")
			if rec.Code != http.StatusFound || rec.Header().Get("Location") != want {
				t.Fatalf("status = %d location = %q want %q", rec.Code, rec.Header().Get("Location"), want)
			}
		})
	}
}
