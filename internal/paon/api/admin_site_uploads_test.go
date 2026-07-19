package api

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestDestroyAdminSiteUploadRequiresSession(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/admin/site_uploads/7", nil)
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	want := "/auth/sign_in?redirect_to=" + url.QueryEscape("/admin/site_uploads/7")
	if got := rec.Header().Get("Location"); got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}
}

func TestPostAdminSiteUploadDestroyMethodOverrideRequiresSession(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/admin/site_uploads/7", strings.NewReader("_method=delete"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	want := "/auth/sign_in?redirect_to=" + url.QueryEscape("/admin/site_uploads/7")
	if got := rec.Header().Get("Location"); got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}
}

func TestSiteUploadStyles(t *testing.T) {
	if got, want := siteUploadStyles("thumbnail"), []string{"original", "@1x", "@2x"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("thumbnail styles = %#v, want %#v", got, want)
	}
	if got, want := siteUploadStyles("mascot"), []string{"original"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("mascot styles = %#v, want %#v", got, want)
	}
}

func TestSiteUploadFilePathUsesPaperclipPartition(t *testing.T) {
	s := &Server{cfg: config.Config{PublicDir: "/srv/paon/public"}}
	got := s.siteUploadFilePath(42, "@1x", "hero.png")
	want := filepath.Join("/srv/paon/public", "system", "site_uploads", "files", "000", "000", "042", "@1x", "hero.png")
	if got != want {
		t.Fatalf("site upload path = %q, want %q", got, want)
	}
}

func TestSiteUploadCacheKeyMatchesRailsModel(t *testing.T) {
	if got := siteUploadCacheKey("thumbnail"); got != "site_uploads/thumbnail" {
		t.Fatalf("site upload cache key = %q", got)
	}
	if got := siteUploadCacheKey(" "); got != "" {
		t.Fatalf("blank site upload cache key = %q", got)
	}
}

func TestAdminSiteUploadWritesInvalidateRailsCache(t *testing.T) {
	src, err := os.ReadFile("admin_site_uploads.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		fn   string
		want string
	}{
		{"destroyAdminSiteUpload", `s.invalidateSiteUploadCache(c.Request().Context(), upload.Var)`},
		{"storeAdminSiteUploadFromForm", `s.invalidateSiteUploadCache(c.Request().Context(), name)`},
		{"invalidateSiteUploadCache", `railsCacheRedisKeyCandidates(s.cfg, key)`},
		{"invalidateSiteUploadCache", `s.cacheRedisCommand(cacheCtx, append([]string{"DEL"}, keys...)...)`},
	} {
		if !functionBodyContains(t, src, check.fn, check.want) {
			t.Fatalf("%s missing %q", check.fn, check.want)
		}
	}
}

func TestValidateAdminSiteUploadHeaderRejectsInvalidInputs(t *testing.T) {
	if _, _, _, err := validateAdminSiteUploadHeader(siteUploadTestFileHeader(t, "site.txt", []byte("not image"))); err == nil || err.Error() != "Site upload must be an image" {
		t.Fatalf("text upload error = %v, want image error", err)
	}
	if _, _, _, err := validateAdminSiteUploadHeader(siteUploadTestFileHeader(t, "site.png", []byte("not image"))); err == nil || err.Error() != "Site upload must be a readable image" {
		t.Fatalf("invalid png error = %v, want readable image error", err)
	}
	filename, contentType, meta, err := validateAdminSiteUploadHeader(siteUploadTestFileHeader(t, "site.png", siteUploadTestPNG(t, 12, 7)))
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{16}\.png$`).MatchString(filename) || contentType != "image/png" || !strings.Contains(string(meta), `"width":12`) || !strings.Contains(string(meta), `"height":7`) {
		t.Fatalf("validated upload = filename=%q contentType=%q meta=%s", filename, contentType, string(meta))
	}
}

func TestAdminSiteUploadMessagesResolveJapaneseLocale(t *testing.T) {
	for _, tc := range []struct {
		key       string
		forbidden string
		want      string
	}{
		{key: "admin.site_uploads.not_found", forbidden: "Site upload not found.", want: "見つかりません"},
		{key: "admin.site_uploads.not_permitted", forbidden: "You are not allowed to manage site uploads.", want: "権限"},
		{key: "admin.site_uploads.unsupported_action", forbidden: "Unsupported site upload action.", want: "サポート"},
	} {
		got := adminT("ja", tc.key, tc.forbidden)
		if got == tc.forbidden || !strings.Contains(got, tc.want) {
			t.Fatalf("%s Japanese message = %q", tc.key, got)
		}
	}
}

func TestStoreSiteUploadThumbnailStylesGeneratesRailsGeometry(t *testing.T) {
	root := t.TempDir()
	s := &Server{cfg: config.Config{PublicDir: root}}
	header := siteUploadTestFileHeader(t, "hero.jpg", siteUploadTestPNG(t, 1600, 900))
	if _, err := s.storeSiteUploadFileStyles(header, 42, "thumbnail", "hero.jpg"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		style  string
		width  int
		height int
	}{
		{style: "@1x", width: 1200, height: 630},
		{style: "@2x", width: 2400, height: 1260},
	} {
		path := s.siteUploadFilePath(42, tc.style, "hero.png")
		file, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		cfg, format, err := image.DecodeConfig(file)
		_ = file.Close()
		if err != nil {
			t.Fatal(err)
		}
		if format != "png" {
			t.Fatalf("%s format = %q", tc.style, format)
		}
		if cfg.Width != tc.width || cfg.Height != tc.height {
			t.Fatalf("%s dimensions = %dx%d, want %dx%d", tc.style, cfg.Width, cfg.Height, tc.width, tc.height)
		}
	}
	original, err := os.Open(s.siteUploadFilePath(42, "original", "hero.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	originalCfg, _, err := image.DecodeConfig(original)
	_ = original.Close()
	if err != nil {
		t.Fatal(err)
	}
	if originalCfg.Width != 1600 || originalCfg.Height != 900 {
		t.Fatalf("original dimensions = %dx%d", originalCfg.Width, originalCfg.Height)
	}
}

func TestStoreSiteUploadOriginalReencodesJPEGToStripMetadata(t *testing.T) {
	root := t.TempDir()
	s := &Server{cfg: config.Config{PublicDir: root}}
	input := siteUploadTestJPEGWithAPP1(t, 80, 40)
	header := siteUploadTestFileHeader(t, "hero.jpg", input)

	size, err := s.storeSiteUploadFileStyles(header, 42, "mascot", "hero.jpg")
	if err != nil {
		t.Fatal(err)
	}
	path := s.siteUploadFilePath(42, "original", "hero.jpg")
	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(stored, []byte("Exif")) || bytes.Contains(stored, []byte("paon-go-test-metadata")) {
		t.Fatalf("stored JPEG still contains metadata marker: %q", stored[:min(len(stored), 64)])
	}
	if int64(len(stored)) != size {
		t.Fatalf("reported size = %d, stored size = %d", size, len(stored))
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg, format, err := image.DecodeConfig(file)
	_ = file.Close()
	if err != nil {
		t.Fatal(err)
	}
	if format != "jpeg" || cfg.Width != 80 || cfg.Height != 40 {
		t.Fatalf("stored image = %s %dx%d", format, cfg.Width, cfg.Height)
	}
}

func TestResizeImageToFillCenterCrops(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 4, 2))
	for y := 0; y < 2; y++ {
		for x := 0; x < 4; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x * 50), G: uint8(y * 100), A: 255})
		}
	}
	var input bytes.Buffer
	if err := png.Encode(&input, img); err != nil {
		t.Fatal(err)
	}
	data, err := resizeVIPSBufferToFill(input.Bytes(), "image/png", 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	resized, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if resized.Bounds().Dx() != 2 || resized.Bounds().Dy() != 2 {
		t.Fatalf("bounds = %v", resized.Bounds())
	}
	left, _, _, _ := resized.At(0, 0).RGBA()
	right, _, _, _ := resized.At(1, 0).RGBA()
	if left == 0 || right <= left {
		t.Fatalf("unexpected crop sample left=%d right=%d", left>>8, right>>8)
	}
}

func TestSiteUploadBlurhashUsesThumbnailOneXStyle(t *testing.T) {
	root := t.TempDir()
	s := &Server{cfg: config.Config{PublicDir: root}}
	original := s.siteUploadFilePath(42, "original", "hero.png")
	if err := os.MkdirAll(filepath.Dir(original), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(original, []byte("not an image"), 0o644); err != nil {
		t.Fatal(err)
	}
	oneX := s.siteUploadFilePath(42, "@1x", "hero.png")
	if err := os.MkdirAll(filepath.Dir(oneX), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(oneX, siteUploadTestPNG(t, 12, 7), 0o644); err != nil {
		t.Fatal(err)
	}
	if hash := s.siteUploadBlurhash(42, "thumbnail", "hero.png"); len(hash) != 36 {
		t.Fatalf("blurhash = %q", hash)
	}
}

func TestSiteUploadBlurhashUsesOriginalForMascot(t *testing.T) {
	root := t.TempDir()
	s := &Server{cfg: config.Config{PublicDir: root}}
	original := s.siteUploadFilePath(42, "original", "mascot.png")
	if err := os.MkdirAll(filepath.Dir(original), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(original, siteUploadTestPNG(t, 9, 9), 0o644); err != nil {
		t.Fatal(err)
	}
	if hash := s.siteUploadBlurhash(42, "mascot", "mascot.png"); len(hash) != 36 {
		t.Fatalf("blurhash = %q", hash)
	}
}

func TestSiteUploadMetaFromHeaderUsesTopLevelDimensions(t *testing.T) {
	header := siteUploadTestFileHeader(t, "site.png", siteUploadTestPNG(t, 12, 7))
	raw, err := siteUploadMetaFromHeader(header)
	if err != nil {
		t.Fatal(err)
	}
	var meta map[string]int
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatal(err)
	}
	if meta["width"] != 12 || meta["height"] != 7 {
		t.Fatalf("meta = %#v", meta)
	}
}

func TestSiteUploadMetaFromHeaderRejectsInvalidImage(t *testing.T) {
	header := siteUploadTestFileHeader(t, "site.png", []byte("not an image"))
	if _, err := siteUploadMetaFromHeader(header); err == nil {
		t.Fatal("expected invalid image to be rejected")
	}
}

func TestSiteUploadMetaForStoredFileUsesTopLevelDimensions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "site.png")
	if err := os.WriteFile(path, siteUploadTestPNG(t, 12, 7), 0o644); err != nil {
		t.Fatal(err)
	}

	raw := siteUploadMetaForStoredFile(path)
	var meta map[string]int
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatal(err)
	}
	if meta["width"] != 12 || meta["height"] != 7 {
		t.Fatalf("meta = %#v", meta)
	}
}

func TestSiteUploadMetaForStoredFileReturnsNilForInvalidImage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "site.png")
	if err := os.WriteFile(path, []byte("not an image"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := siteUploadMetaForStoredFile(path); got != nil {
		t.Fatalf("meta = %s, want nil", string(got))
	}
}

func TestRemoveSiteUploadFilesRemovesPartitionDirectory(t *testing.T) {
	root := t.TempDir()
	s := &Server{cfg: config.Config{PublicDir: root}}
	target := s.siteUploadFilePath(42, "@1x", "hero.png")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("image"), 0o644); err != nil {
		t.Fatal(err)
	}
	original := s.siteUploadFilePath(42, "original", "hero.png")
	if err := os.MkdirAll(filepath.Dir(original), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(original, []byte("image"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := s.removeSiteUploadFiles(42); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(original)); !os.IsNotExist(err) {
		t.Fatalf("site upload directory still exists or stat failed unexpectedly: %v", err)
	}
}

func TestRemoveReplacedSiteUploadFilesDeletesOnlyPreviousFilename(t *testing.T) {
	root := t.TempDir()
	s := &Server{cfg: config.Config{PublicDir: root}}
	upload := models.SiteUpload{ID: 42, FileFileName: sql.NullString{String: "old.png", Valid: true}}
	for _, style := range siteUploadStyles("thumbnail") {
		oldPath := s.siteUploadFilePath(42, style, "old.png")
		if err := os.MkdirAll(filepath.Dir(oldPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(oldPath, []byte("old"), 0o644); err != nil {
			t.Fatal(err)
		}
		newPath := s.siteUploadFilePath(42, style, "new.png")
		if err := os.WriteFile(newPath, []byte("new"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if err := s.removeReplacedSiteUploadFiles(upload, "thumbnail", "new.png"); err != nil {
		t.Fatal(err)
	}
	for _, style := range siteUploadStyles("thumbnail") {
		if _, err := os.Stat(s.siteUploadFilePath(42, style, "old.png")); !os.IsNotExist(err) {
			t.Fatalf("%s old file still exists or stat failed unexpectedly: %v", style, err)
		}
		if _, err := os.Stat(s.siteUploadFilePath(42, style, "new.png")); err != nil {
			t.Fatalf("%s new file should remain: %v", style, err)
		}
	}
}

func TestRemoveReplacedSiteUploadFilesKeepsSameFilename(t *testing.T) {
	root := t.TempDir()
	s := &Server{cfg: config.Config{PublicDir: root}}
	upload := models.SiteUpload{ID: 42, FileFileName: sql.NullString{String: "same.png", Valid: true}}
	path := s.siteUploadFilePath(42, "original", "same.png")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("same"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := s.removeReplacedSiteUploadFiles(upload, "mascot", "same.png"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("same filename should remain: %v", err)
	}
}

func TestRemoveEmptyPaperclipParentsKeepsNonEmptyParents(t *testing.T) {
	root := t.TempDir()
	stop := filepath.Join(root, "files")
	empty := filepath.Join(stop, "000", "000")
	sibling := filepath.Join(stop, "000", "001")
	if err := os.MkdirAll(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sibling, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sibling, "keep"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := removeEmptyPaperclipParents(empty, stop); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(empty); !os.IsNotExist(err) {
		t.Fatalf("empty dir still exists or stat failed unexpectedly: %v", err)
	}
	if _, err := os.Stat(sibling); err != nil {
		t.Fatalf("sibling should remain: %v", err)
	}
}

func siteUploadTestPNG(t *testing.T, width int, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func siteUploadTestJPEGWithAPP1(t *testing.T, width int, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	img.Set(0, 0, color.RGBA{R: 255, G: 128, A: 255})
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	data := buf.Bytes()
	if len(data) < 2 || data[0] != 0xff || data[1] != 0xd8 {
		t.Fatal("test JPEG missing SOI marker")
	}
	payload := []byte("Exif\x00\x00paon-go-test-metadata")
	length := len(payload) + 2
	app1 := []byte{0xff, 0xe1, byte(length >> 8), byte(length)}
	app1 = append(app1, payload...)
	out := append([]byte{}, data[:2]...)
	out = append(out, app1...)
	out = append(out, data[2:]...)
	return out
}

func siteUploadTestFileHeader(t *testing.T, filename string, data []byte) *multipart.FileHeader {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	file, err := writer.CreateFormFile("form_admin_settings[thumbnail]", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/settings/branding", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	if err := req.ParseMultipartForm(1 << 20); err != nil {
		t.Fatal(err)
	}
	return req.MultipartForm.File["form_admin_settings[thumbnail]"][0]
}
