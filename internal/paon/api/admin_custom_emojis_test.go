package api

import (
	"database/sql"
	"image"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestAdminCustomEmojisRequireSession(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/custom_emojis?local=1", nil)
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	want := "/auth/sign_in?redirect_to=" + url.QueryEscape("/admin/custom_emojis?local=1")
	if got := rec.Header().Get("Location"); got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}
}

func TestParseAdminCustomEmojiIDs(t *testing.T) {
	form := url.Values{}
	form.Add("form_custom_emoji_batch[custom_emoji_ids][]", "4")
	form.Add("form_custom_emoji_batch[custom_emoji_ids][]", "bad")
	form.Add("form_custom_emoji_batch[custom_emoji_ids]", "5")

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/admin/custom_emojis/batch", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	got := parseAdminCustomEmojiIDs(c)
	if len(got) != 1 || got[0] != 4 {
		t.Fatalf("ids = %#v", got)
	}
}

func TestAdminCustomEmojiBatchAction(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/admin/custom_emojis/batch", strings.NewReader("disable=1"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	if got := adminCustomEmojiBatchAction(c); got != "disable" {
		t.Fatalf("action = %q", got)
	}

	req = httptest.NewRequest(http.MethodPost, "/admin/custom_emojis/batch", strings.NewReader("delete="))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c = echo.NewContext(req, httptest.NewRecorder(), e)
	if got := adminCustomEmojiBatchAction(c); got != "delete" {
		t.Fatalf("empty-valued submit button action = %q", got)
	}
}

func TestAdminCustomEmojiBatchRedirectURLPreservesFilters(t *testing.T) {
	form := url.Values{}
	form.Set("page", "3")
	form.Set("local", "1")
	form.Set("shortcode", "par")
	form.Set("by_domain", "remote.example")

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/admin/custom_emojis/batch?remote=1", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	got := adminCustomEmojiBatchRedirectURL(c, "notice", "Custom emoji batch action applied")
	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Path != "/admin/custom_emojis" {
		t.Fatalf("path = %q", parsed.Path)
	}
	values := parsed.Query()
	for key, want := range map[string]string{
		"page":      "3",
		"local":     "1",
		"remote":    "1",
		"shortcode": "par",
		"by_domain": "remote.example",
		"notice":    "Custom emoji batch action applied",
	} {
		if got := values.Get(key); got != want {
			t.Fatalf("%s = %q, want %q in %s", key, got, want, parsed.RawQuery)
		}
	}
}

func TestAdminCustomEmojiBatchRedirectURLWithoutMessageMatchesRailsSuccessRedirect(t *testing.T) {
	form := url.Values{}
	form.Set("page", "3")
	form.Set("local", "1")
	form.Set("shortcode", "par")
	form.Set("by_domain", "remote.example")

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/admin/custom_emojis/batch?remote=1", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	got := adminCustomEmojiBatchRedirectURL(c, "", "")
	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Path != "/admin/custom_emojis" {
		t.Fatalf("path = %q", parsed.Path)
	}
	values := parsed.Query()
	for key, want := range map[string]string{
		"page":      "3",
		"local":     "1",
		"remote":    "1",
		"shortcode": "par",
		"by_domain": "remote.example",
	} {
		if got := values.Get(key); got != want {
			t.Fatalf("%s = %q, want %q in %s", key, got, want, parsed.RawQuery)
		}
	}
	if values.Get("notice") != "" || values.Get("error") != "" {
		t.Fatalf("success redirect should not add flash query params: %s", parsed.RawQuery)
	}
}

func TestAdminCustomEmojiMessagesUseAdminLocaleKeys(t *testing.T) {
	tests := []struct {
		locale string
		key    string
		want   string
	}{
		{"en", "created_msg", "Emoji successfully created!"},
		{"en", "no_emoji_selected", "No emojis were changed as none were selected"},
		{"en", "errors.image_invalid", "Image is invalid"},
		{"ja", "created_msg", "絵文字の追加に成功しました！"},
		{"ja", "errors.image_invalid", "画像が不正です"},
		{"ja", "errors.shortcode_invalid", "ショートコードが不正です"},
	}
	for _, tt := range tests {
		if got := adminCustomEmojiMessage(tt.locale, tt.key, "fallback"); got != tt.want {
			t.Fatalf("adminCustomEmojiMessage(%q, %q) = %q, want %q", tt.locale, tt.key, got, tt.want)
		}
	}
}

func TestAdminCustomEmojiBatchHiddenFields(t *testing.T) {
	html := adminCustomEmojiBatchHiddenFields(adminCustomEmojiFilters{Page: "2", Local: "1", Shortcode: "par", ByDomain: "remote.example"})
	for _, want := range []string{
		`name="page" value="2"`,
		`name="local" value="1"`,
		`name="shortcode" value="par"`,
		`name="by_domain" value="remote.example"`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("hidden fields missing %q: %s", want, html)
		}
	}
	if strings.Contains(html, `name="remote"`) {
		t.Fatalf("blank remote filter should be omitted: %s", html)
	}
}

func TestAdminCustomEmojiModelsUseRailsKaminariPageSizeAndOffset(t *testing.T) {
	src, err := os.ReadFile("admin_custom_emojis.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Offset(adminRailsPageOffset(c))",
		"Limit(adminRailsDefaultPageSize)",
	} {
		if !functionBodyContains(t, src, "adminCustomEmojiModels", want) {
			t.Fatalf("adminCustomEmojiModels missing %q", want)
		}
	}
}

func TestAdminCustomEmojisHTMLIncludesRailsFields(t *testing.T) {
	html := adminCustomEmojisHTML([]models.CustomEmoji{{
		ID:              7,
		Shortcode:       "party",
		VisibleInPicker: false,
		Category:        models.CustomEmojiCategory{ID: 2, Name: models.CustomEmojiCategoryName("Reactions")},
	}}, []models.CustomEmojiCategory{{ID: 2, Name: models.CustomEmojiCategoryName("Reactions")}}, "saved", "", adminCustomEmojiFilters{Page: "3", Local: "1", Shortcode: "par"})
	for _, want := range []string{
		"Custom emojis",
		`href="/admin/custom_emojis/new"`,
		`action="/admin/custom_emojis"`,
		`action="/admin/custom_emojis/batch"`,
		`name="page" value="3"`,
		`name="local" value="1"`,
		`name="shortcode" value="par"`,
		`name="form_custom_emoji_batch[custom_emoji_ids][]" value="7"`,
		`name="form_custom_emoji_batch[category_id]"`,
		`name="form_custom_emoji_batch[category_name]"`,
		":party:",
		"Unlisted",
		"saved",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("custom emojis html missing %q: %s", want, html)
		}
	}
}

func TestAdminCustomEmojiFormHTMLIncludesMultipartFields(t *testing.T) {
	html := adminCustomEmojiFormHTML("bad", "en")
	for _, want := range []string{
		"Upload",
		`action="/admin/custom_emojis"`,
		`enctype="multipart/form-data"`,
		`name="custom_emoji[shortcode]"`,
		`name="custom_emoji[image]"`,
		`name="custom_emoji[visible_in_picker]"`,
		"bad",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("custom emoji form html missing %q: %s", want, html)
		}
	}
}

func TestAdminCustomEmojiRowHTMLRemoteDisabled(t *testing.T) {
	html := adminCustomEmojiRowHTML(models.CustomEmoji{
		ID:        8,
		Shortcode: "wave",
		Domain:    sql.NullString{String: "remote.example", Valid: true},
		Disabled:  true,
	})
	for _, want := range []string{`value="8"`, ":wave:", "remote.example", "Disabled", `class="batch-table__row"`} {
		if !strings.Contains(html, want) {
			t.Fatalf("row html missing %q: %s", want, html)
		}
	}
}

func TestCustomEmojiContentTypeAllowed(t *testing.T) {
	for _, contentType := range []string{"image/png", "image/gif", "image/webp"} {
		if !customEmojiContentTypeAllowed(contentType) {
			t.Fatalf("%s should be allowed", contentType)
		}
	}
	if customEmojiContentTypeAllowed("image/jpeg") {
		t.Fatal("image/jpeg should not be allowed")
	}
}

func TestStoreCustomEmojiFileWritesOriginalAndStaticPNG(t *testing.T) {
	root := t.TempDir()
	s := &Server{cfg: config.Config{PublicDir: root}}
	emoji := models.CustomEmoji{
		ID:            42,
		ImageFileName: sql.NullString{String: "party.gif", Valid: true},
	}
	header := siteUploadTestFileHeader(t, "party.gif", siteUploadTestPNG(t, 16, 12))
	if err := s.storeCustomEmojiFile(emoji, header); err != nil {
		t.Fatal(err)
	}
	original := s.customEmojiImagePath(42, "original", "party.gif")
	if _, err := os.Stat(original); err != nil {
		t.Fatalf("original missing: %v", err)
	}
	static := s.customEmojiImagePath(42, "static", "party.png")
	file, err := os.Open(static)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	cfg, format, err := image.DecodeConfig(file)
	if err != nil {
		t.Fatal(err)
	}
	if format != "png" {
		t.Fatalf("static format = %q", format)
	}
	if cfg.Width != 16 || cfg.Height != 12 {
		t.Fatalf("static dimensions = %dx%d", cfg.Width, cfg.Height)
	}
	wantStatic := filepath.Join(root, "system", "custom_emojis", "images", "000", "000", "042", "static", "party.png")
	if static != wantStatic {
		t.Fatalf("static path = %q, want %q", static, wantStatic)
	}
}

func TestCopyCustomEmojiFilesCopiesRemoteCacheStyles(t *testing.T) {
	root := t.TempDir()
	s := &Server{cfg: config.Config{PublicDir: root}}
	source := models.CustomEmoji{
		ID:                        7,
		Shortcode:                 "party",
		Domain:                    sql.NullString{String: "remote.example", Valid: true},
		ImageFileName:             sql.NullString{String: "party.gif", Valid: true},
		ImageStorageSchemaVersion: sql.NullInt64{Int64: 1, Valid: true},
	}
	target := models.CustomEmoji{
		ID:            42,
		Shortcode:     "party",
		ImageFileName: sql.NullString{String: "party.gif", Valid: true},
	}
	sourceOriginal := s.customEmojiImagePathFor(source, "original", "party.gif")
	sourceStatic := s.customEmojiImagePathFor(source, "static", "party.png")
	if err := os.MkdirAll(filepath.Dir(sourceOriginal), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(sourceStatic), 0o755); err != nil {
		t.Fatal(err)
	}
	originalBytes := siteUploadTestPNG(t, 12, 10)
	staticBytes := siteUploadTestPNG(t, 8, 8)
	if err := os.WriteFile(sourceOriginal, originalBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sourceStatic, staticBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := s.copyCustomEmojiFiles(source, target); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(s.customEmojiImagePathFor(target, "original", "party.gif")); err != nil || string(got) != string(originalBytes) {
		t.Fatalf("copied original = %d bytes err=%v", len(got), err)
	}
	if got, err := os.ReadFile(s.customEmojiImagePathFor(target, "static", "party.png")); err != nil || string(got) != string(staticBytes) {
		t.Fatalf("copied static = %d bytes err=%v", len(got), err)
	}
}

func TestCopyCustomEmojiFilesGeneratesStaticWhenMissing(t *testing.T) {
	root := t.TempDir()
	s := &Server{cfg: config.Config{PublicDir: root}}
	source := models.CustomEmoji{
		ID:                        7,
		Shortcode:                 "party",
		Domain:                    sql.NullString{String: "remote.example", Valid: true},
		ImageFileName:             sql.NullString{String: "party.gif", Valid: true},
		ImageStorageSchemaVersion: sql.NullInt64{Int64: 1, Valid: true},
	}
	target := models.CustomEmoji{
		ID:            42,
		Shortcode:     "party",
		ImageFileName: sql.NullString{String: "party.gif", Valid: true},
	}
	sourceOriginal := s.customEmojiImagePathFor(source, "original", "party.gif")
	if err := os.MkdirAll(filepath.Dir(sourceOriginal), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sourceOriginal, siteUploadTestPNG(t, 12, 10), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := s.copyCustomEmojiFiles(source, target); err != nil {
		t.Fatal(err)
	}
	static := s.customEmojiImagePathFor(target, "static", "party.png")
	file, err := os.Open(static)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	cfg, format, err := image.DecodeConfig(file)
	if err != nil {
		t.Fatal(err)
	}
	if format != "png" || cfg.Width != 12 || cfg.Height != 10 {
		t.Fatalf("generated static = %s %dx%d", format, cfg.Width, cfg.Height)
	}
}

func TestCustomEmojiStaticFilename(t *testing.T) {
	if got := customEmojiStaticFilename("party.large.gif"); got != "party.large.png" {
		t.Fatalf("static filename = %q", got)
	}
}

func TestRemoveReplacedCustomEmojiFilesDeletesOldPaperclipStyles(t *testing.T) {
	root := t.TempDir()
	s := &Server{cfg: config.Config{PublicDir: root}}
	previous := models.CustomEmoji{
		ID:            42,
		Shortcode:     "party",
		ImageFileName: sql.NullString{String: "old.gif", Valid: true},
	}
	next := models.CustomEmoji{
		ID:            42,
		Shortcode:     "party",
		ImageFileName: sql.NullString{String: "new.webp", Valid: true},
	}
	oldOriginal := s.customEmojiImagePathFor(previous, "original", "old.gif")
	oldStatic := s.customEmojiImagePathFor(previous, "static", "old.png")
	newOriginal := s.customEmojiImagePathFor(next, "original", "new.webp")
	newStatic := s.customEmojiImagePathFor(next, "static", "new.png")
	for _, path := range []string{oldOriginal, oldStatic, newOriginal, newStatic} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("file"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	s.removeReplacedCustomEmojiFiles(previous, next)

	for _, path := range []string{oldOriginal, oldStatic} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("old file %s should be removed, err=%v", path, err)
		}
	}
	for _, path := range []string{newOriginal, newStatic} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("new file %s should remain, err=%v", path, err)
		}
	}
}

func TestRemoveReplacedCustomEmojiFilesKeepsSharedStaticFilename(t *testing.T) {
	root := t.TempDir()
	s := &Server{cfg: config.Config{PublicDir: root}}
	previous := models.CustomEmoji{
		ID:            42,
		Shortcode:     "party",
		ImageFileName: sql.NullString{String: "party.gif", Valid: true},
	}
	next := models.CustomEmoji{
		ID:            42,
		Shortcode:     "party",
		ImageFileName: sql.NullString{String: "party.webp", Valid: true},
	}
	oldOriginal := s.customEmojiImagePathFor(previous, "original", "party.gif")
	sharedStatic := s.customEmojiImagePathFor(previous, "static", "party.png")
	for _, path := range []string{oldOriginal, sharedStatic} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("file"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	s.removeReplacedCustomEmojiFiles(previous, next)

	if _, err := os.Stat(oldOriginal); !os.IsNotExist(err) {
		t.Fatalf("old original should be removed, err=%v", err)
	}
	if _, err := os.Stat(sharedStatic); err != nil {
		t.Fatalf("shared static should remain, err=%v", err)
	}
}

func TestCustomEmojiAuditLogTargetUsesShortcode(t *testing.T) {
	target := customEmojiAuditLogTarget(models.CustomEmoji{ID: 42, Shortcode: "party"})
	if target.Type != "CustomEmoji" || target.ID != 42 || target.HumanIdentifier != "party" {
		t.Fatalf("target = %#v", target)
	}
}

func TestCustomEmojiCategoryIDsDeduplicatesValidCategoryIDs(t *testing.T) {
	got := customEmojiCategoryIDs([]models.CustomEmoji{
		{CategoryID: sql.NullInt64{Int64: 3, Valid: true}},
		{CategoryID: sql.NullInt64{Int64: 3, Valid: true}},
		{CategoryID: sql.NullInt64{Int64: 0, Valid: true}},
		{},
		{CategoryID: sql.NullInt64{Int64: 4, Valid: true}},
	})
	if len(got) != 2 || got[0] != 3 || got[1] != 4 {
		t.Fatalf("category ids = %#v", got)
	}
}

func TestAdminCustomEmojiBatchCleansUnusedCategories(t *testing.T) {
	src, err := os.ReadFile("admin_custom_emojis.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"categoryIDsBefore := customEmojiCategoryIDs(emojis)",
		"return cleanupUnusedCustomEmojiCategories(tx, categoryIDsBefore)",
		"DELETE FROM custom_emoji_categories",
		"WHERE custom_emojis.category_id = custom_emoji_categories.id",
	} {
		if !strings.Contains(string(src), want) {
			t.Fatalf("admin_custom_emojis.go missing %q", want)
		}
	}
}

func TestAdminCustomEmojiBatchDeleteRemovesPaperclipFilesAndEntityCache(t *testing.T) {
	src, err := os.ReadFile("admin_custom_emojis.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`s.removeCustomEmojiLocalFiles(emoji)`,
		`tx.Delete(&emoji)`,
		`s.invalidateCustomEmojiEntityCaches(c.Request().Context(), changed)`,
	} {
		if !functionBodyContains(t, src, "applyAdminCustomEmojiBatch", want) {
			t.Fatalf("applyAdminCustomEmojiBatch missing %q", want)
		}
	}
}

func TestCustomEmojiEntityCacheKeyMatchesRailsToKey(t *testing.T) {
	local := models.CustomEmoji{Shortcode: "Party"}
	if got := customEmojiEntityCacheKey(local); got != "emoji:party" {
		t.Fatalf("local cache key = %q", got)
	}
	remote := models.CustomEmoji{Shortcode: "Party", Domain: sql.NullString{String: "Remote.Example", Valid: true}}
	if got := customEmojiEntityCacheKey(remote); got != "emoji:party:remote.example" {
		t.Fatalf("remote cache key = %q", got)
	}
}

func TestRailsCacheRedisKeyCandidatesIncludesCacheNamespace(t *testing.T) {
	got := railsCacheRedisKeyCandidates(config.Config{RedisNamespace: "mastodon:"}, "emoji:party")
	for _, want := range []string{"emoji:party", "cache:emoji:party", "mastodon:emoji:party", "mastodon_cache:emoji:party"} {
		found := false
		for _, candidate := range got {
			if candidate == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("cache key candidates missing %q: %#v", want, got)
		}
	}
}

func TestAdminPaperclipIDPartition(t *testing.T) {
	if got := adminPaperclipIDPartition(42); got != "000/000/042" {
		t.Fatalf("partition = %q", got)
	}
}
