package api

import (
	"database/sql"
	"encoding/json"
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

func TestManifestIncludesRailsShareTarget(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/manifest", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	s := &Server{cfg: config.Config{Title: "Paon", WebDomain: "example.test", Scheme: "https"}}
	if err := s.manifest(c); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "max-age=180, public" {
		t.Fatalf("Cache-Control = %q", got)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["start_url"] != "/" || body["scope"] != "/" || body["id"] != "/home" {
		t.Fatalf("manifest route fields = %#v", body)
	}
	if body["name"] != "Mastodon" || body["short_name"] != "Mastodon" {
		t.Fatalf("manifest names = %#v", body)
	}
	icons, ok := body["icons"].([]any)
	if !ok || len(icons) != 9 {
		t.Fatalf("icons = %#v", body["icons"])
	}
	firstIcon, ok := icons[0].(map[string]any)
	if !ok {
		t.Fatalf("first icon = %#v", icons[0])
	}
	if firstIcon["src"] != "https://example.test/packs/media/icons/android-chrome-36x36.png" || firstIcon["sizes"] != "36x36" || firstIcon["type"] != "image/png" || firstIcon["purpose"] != "any maskable" {
		t.Fatalf("first icon = %#v", firstIcon)
	}
	lastIcon, ok := icons[len(icons)-1].(map[string]any)
	if !ok || lastIcon["sizes"] != "512x512" {
		t.Fatalf("last icon = %#v", icons[len(icons)-1])
	}
	shareTarget, ok := body["share_target"].(map[string]any)
	if !ok {
		t.Fatalf("share_target missing: %#v", body)
	}
	if shareTarget["action"] != "share" || shareTarget["method"] != "GET" || shareTarget["url_template"] != "share?title={title}&text={text}&url={url}" {
		t.Fatalf("share_target = %#v", shareTarget)
	}
	shortcuts, ok := body["shortcuts"].([]any)
	if !ok || len(shortcuts) != 2 {
		t.Fatalf("shortcuts = %#v", body["shortcuts"])
	}
	firstShortcut, ok := shortcuts[0].(map[string]any)
	if !ok || firstShortcut["url"] != "/publish" {
		t.Fatalf("first shortcut = %#v", shortcuts[0])
	}
}

func TestManifestUsesRailsSiteTitleSetting(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	body := functionBody(t, src, "manifest")
	for _, want := range []string{
		`title := s.settingStringValue("site_title", s.cfg.Title)`,
		`"name":             title`,
		`"short_name":       title`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("manifest must use Rails site_title setting; missing %q", want)
		}
	}
}

func TestWebAppOptionsUseRailsSiteTitleSetting(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	body := functionBody(t, src, "webAppOptions")
	for _, want := range []string{
		`SiteTitle:         s.settingRawValue("site_title", s.cfg.Title)`,
		`SiteTitleSet:      true`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("webAppOptions must pass raw Rails site_title setting into initial_state and HTML title; missing %q", want)
		}
	}
}

func TestWebAppRoutesRenderReactShellButUnknownWebRoutesUseRailsErrorNegotiation(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", WebDomain: "example.test", Scheme: "https", PublicDir: "../../../public"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "https://example.test/search", nil)
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("search status = %d body = %s", rec.Code, rec.Body.String())
	}
	if contentType := rec.Header().Get("Content-Type"); !strings.Contains(contentType, "text/html") {
		t.Fatalf("search Content-Type = %q", contentType)
	}
	if !strings.Contains(rec.Body.String(), `id="mastodon"`) {
		t.Fatalf("search route did not render React shell: %s", rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "https://example.test/not-a-rails-web-route", nil)
	rec = httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown status = %d body = %s", rec.Code, rec.Body.String())
	}
	if contentType := rec.Header().Get("Content-Type"); !strings.Contains(contentType, "text/html") {
		t.Fatalf("unknown Content-Type = %q", contentType)
	}
	if strings.Contains(rec.Body.String(), `id="mastodon"`) || !strings.Contains(rec.Body.String(), `The page you are looking for`) {
		t.Fatalf("unknown route should return Rails HTML 404, got: %s", rec.Body.String())
	}

	for _, path := range []string{"/timelines/home", "/timelines/public", "/timelines/tag/go", "/timelines/list/1"} {
		req = httptest.NewRequest(http.MethodGet, "https://example.test"+path, nil)
		rec = httptest.NewRecorder()
		s.echo.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d body = %s", path, rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), `id="mastodon"`) {
			t.Fatalf("%s should not render non-Rails React shell: %s", path, rec.Body.String())
		}
	}

	req = httptest.NewRequest(http.MethodGet, "https://example.test/not-a-rails-web-route", nil)
	req.Header.Set("Accept", "application/json")
	rec = httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("json unknown status = %d body = %s", rec.Code, rec.Body.String())
	}
	if contentType := rec.Header().Get("Content-Type"); !strings.Contains(contentType, "application/json") {
		t.Fatalf("json unknown Content-Type = %q", contentType)
	}
	if rec.Body.String() != "{\"error\":\"Not Found\"}\n" {
		t.Fatalf("unknown route should return Rails JSON 404, got: %s", rec.Body.String())
	}
}

func TestWebAppHeadRoutesUseRailsCompatibleEmptyBody(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", WebDomain: "example.test", Scheme: "https", PublicDir: "../../../public"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodHead, "https://example.test/search", nil)
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("HEAD status = %d body = %s", rec.Code, rec.Body.String())
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("HEAD body length = %d, body = %s", rec.Body.Len(), rec.Body.String())
	}
}

func TestManifestIconsUsePackManifestWhenAvailable(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", WebDomain: "example.test", Scheme: "https", PublicDir: "../../../public"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	icons := s.manifestIcons()
	if len(icons) != 9 {
		t.Fatalf("len(icons) = %d", len(icons))
	}
	if got := icons[len(icons)-1]["src"]; got == "https://example.test/packs/media/icons/android-chrome-512x512.png" || !strings.Contains(got, "android-chrome-512x512-") {
		t.Fatalf("512 icon src = %q", got)
	}
}

func TestIntentRedirectsFollowToAuthorizeInteraction(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/intent?uri="+url.QueryEscape("web+mastodon://follow?uri=acct:alice@example.com"), nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	s := &Server{}
	if err := s.intent(c); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/authorize_interaction?uri=alice%40example.com" {
		t.Fatalf("status = %d location = %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestIntentFormatRoutesRedirectFollowToAuthorizeInteraction(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{"/intent.json", "/intent.xml"} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path+"?uri="+url.QueryEscape("web+mastodon://follow?uri=acct:alice@example.com"), nil)
			rec := httptest.NewRecorder()
			s.echo.ServeHTTP(rec, req)

			if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/authorize_interaction?uri=alice%40example.com" {
				t.Fatalf("status = %d location = %q", rec.Code, rec.Header().Get("Location"))
			}
		})
	}
}

func TestIntentRedirectsShareToSharePage(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/intent?uri="+url.QueryEscape("web+mastodon://share?text=hello world&visibility=private&url=https://example.com/a"), nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	s := &Server{}
	if err := s.intent(c); err != nil {
		t.Fatal(err)
	}
	wantQuery := url.Values{"text": {"hello world"}}
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/share?"+wantQuery.Encode() {
		t.Fatalf("status = %d location = %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestIntentRedirectsEmptyShareLikeRails(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/intent?uri="+url.QueryEscape("web+mastodon://share?url=https://example.com/a"), nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())

	if err := (&Server{}).intent(c); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/share" {
		t.Fatalf("status = %d location = %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestSettingsRequireSignInAndPreferencesRedirect(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/settings/profile", nil)
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)
	want := "/auth/sign_in?redirect_to=" + url.QueryEscape("/settings/profile")
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != want {
		t.Fatalf("profile status = %d location = %q want %q", rec.Code, rec.Header().Get("Location"), want)
	}

	req = httptest.NewRequest(http.MethodGet, "/settings/preferences", nil)
	rec = httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)
	if rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != "/settings/preferences/appearance" {
		t.Fatalf("preferences status = %d location = %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestShareTextFromQueryJoinsTitleTextAndURL(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/share?title=Title&text=Body&url=https%3A%2F%2Fexample.com%2Fa", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	if got := shareTextFromQuery(c); got != "Title Body https://example.com/a" {
		t.Fatalf("shareTextFromQuery = %q", got)
	}
}

func TestComposeVisibilityFromQueryAllowsMastodonValues(t *testing.T) {
	e := echo.New()
	tests := map[string]string{
		"public":   "public",
		"unlisted": "unlisted",
		"private":  "private",
		"direct":   "direct",
		"PRIVATE":  "private",
		"bad":      "",
		"":         "",
	}
	for raw, want := range tests {
		req := httptest.NewRequest(http.MethodGet, "/share?visibility="+url.QueryEscape(raw), nil)
		rec := httptest.NewRecorder()
		c := echo.NewContext(req, rec, e)
		if got := composeVisibilityFromQuery(c); got != want {
			t.Fatalf("visibility %q = %q want %q", raw, got, want)
		}
	}
}

func TestComposeRouteAcceptsQueryOnlyForComposeRoutes(t *testing.T) {
	if !composeRouteAcceptsQuery("/publish") || !composeRouteAcceptsQuery("/statuses/new") {
		t.Fatal("compose routes should accept query seed values")
	}
	if composeRouteAcceptsQuery("/home") || composeRouteAcceptsQuery("/notifications") {
		t.Fatal("non-compose routes should not accept query seed values")
	}
}

func TestWebAppRoutesAcceptHead(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{"/", "/getting-started", "/keyboard-shortcuts", "/home", "/public", "/public/local", "/public/remote", "/conversations", "/lists/1", "/notifications", "/favourites", "/bookmarks", "/pinned", "/start", "/directory", "/publish", "/follow_requests", "/blocks", "/domain_blocks", "/mutes", "/followed_tags", "/search", "/explore/tags", "/statuses/1", "/@alice/1/reblogs", "/@alice@example.com/remote/path", "/deck/getting-started"} {
		req := httptest.NewRequest(http.MethodHead, path, nil)
		rec := httptest.NewRecorder()
		s.echo.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("HEAD %s status = %d body = %s", path, rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
			t.Fatalf("HEAD %s content-type = %q", path, got)
		}
	}
}

func TestWebAppRoutesAcceptRailsOptionalFormat(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	for _, tt := range []struct {
		method string
		path   string
	}{
		{method: http.MethodGet, path: "/home.json"},
		{method: http.MethodGet, path: "/getting-started.xml"},
		{method: http.MethodHead, path: "/home.json"},
		{method: http.MethodHead, path: "/getting-started.xml"},
	} {
		req := httptest.NewRequest(tt.method, tt.path, nil)
		rec := httptest.NewRecorder()
		s.echo.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s %s status = %d body = %s", tt.method, tt.path, rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
			t.Fatalf("%s %s content-type = %q", tt.method, tt.path, got)
		}
		if tt.method == http.MethodHead && rec.Body.Len() != 0 {
			t.Fatalf("%s %s wrote body = %s", tt.method, tt.path, rec.Body.String())
		}
	}
}

func TestWebAppRoutesMatchRailsVaryAndAnonymousCache(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/home", nil)
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)
	if got := rec.Header().Get("Vary"); got != "Accept, Accept-Language, Cookie" {
		t.Fatalf("Vary = %q", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "max-age=15, public, stale-while-revalidate=30, stale-if-error=86400" {
		t.Fatalf("Cache-Control = %q", got)
	}

	req = httptest.NewRequest(http.MethodGet, "/home", nil)
	req.AddCookie(&http.Cookie{Name: railsSessionCookieName, Value: "session"})
	rec = httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)
	if got := rec.Header().Get("Vary"); got != "Accept, Accept-Language, Cookie" {
		t.Fatalf("session Vary = %q", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Fatalf("session Cache-Control = %q, want private, no-store", got)
	}
}

func TestPublicAPIGETRoutesAcceptHead(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{"/health", "/manifest.json", "/nodeinfo/2.0", "/api/v1/instance", "/api/v2/instance", "/api/v1/custom_emojis"} {
		req := httptest.NewRequest(http.MethodHead, path, nil)
		rec := httptest.NewRecorder()
		s.echo.ServeHTTP(rec, req)
		if rec.Code == http.StatusNotFound || rec.Code == http.StatusMethodNotAllowed {
			t.Fatalf("HEAD %s status = %d body = %s", path, rec.Code, rec.Body.String())
		}
		if rec.Body.Len() != 0 {
			t.Fatalf("HEAD %s wrote body = %s", path, rec.Body.String())
		}
	}
}

func TestRailsPublicRootAssetsStayServed(t *testing.T) {
	publicDir := t.TempDir()
	files := map[string]string{
		"badge.png":           "badge",
		"favicon.ico":         "ico",
		"inert.css":           "body{}",
		"oops.gif":            "gif",
		"oops.png":            "png",
		"robots.txt":          "User-agent: *",
		"embed.js":            "window.MastodonEmbed=true;",
		"packs/sw.js":         "self.addEventListener('install',function(){});",
		"packs/manifest.json": `{"media/icons/favicon-32x32.png":"/packs/media/icons/favicon-32x32-hash.png","media/icons/android-chrome-192x192.png":"/packs/media/icons/android-chrome-192x192-hash.png","media/icons/apple-touch-icon-180x180.png":"/packs/media/icons/apple-touch-icon-180x180-hash.png","media/icons/apple-touch-icon-120x120.png":"/packs/media/icons/apple-touch-icon-120x120-hash.png"}`,
		"packs/media/icons/favicon-32x32-hash.png":            "png",
		"packs/media/icons/android-chrome-192x192-hash.png":   "png",
		"packs/media/icons/apple-touch-icon-180x180-hash.png": "png",
		"packs/media/icons/apple-touch-icon-120x120-hash.png": "png",
		"web-push-icon_expand.png":                            "expand",
		"web-push-icon_favourite.png":                         "favourite",
		"web-push-icon_reblog.png":                            "reblog",
		"sounds/boop.mp3":                                     "mp3",
		"sounds/boop.ogg":                                     "ogg",
		"ocr/lang-data/eng.traineddata.gz":                    "ocr",
	}
	for name, body := range files {
		path := filepath.Join(publicDir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com", PublicDir: publicDir}, nil)
	if err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{
		"/badge.png",
		"/favicon.ico",
		"/favicon-32x32.png",
		"/inert.css",
		"/oops.gif",
		"/oops.png",
		"/robots.txt",
		"/embed.js",
		"/sw.js",
		"/android-chrome-192x192.png",
		"/apple-touch-icon.png",
		"/apple-touch-icon-precomposed.png",
		"/apple-touch-icon-120x120.png",
		"/apple-touch-icon-120x120-precomposed.png",
		"/web-push-icon_expand.png",
		"/web-push-icon_favourite.png",
		"/web-push-icon_reblog.png",
		"/sounds/boop.mp3",
		"/sounds/boop.ogg",
		"/ocr/lang-data/eng.traineddata.gz",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		s.echo.ServeHTTP(rec, req)
		if path == "/android-chrome-192x192.png" {
			if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/packs/media/icons/android-chrome-192x192-hash.png" {
				t.Fatalf("%s status = %d location = %q", path, rec.Code, rec.Header().Get("Location"))
			}
			continue
		}
		if path == "/favicon-32x32.png" {
			if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/packs/media/icons/favicon-32x32-hash.png" {
				t.Fatalf("%s status = %d location = %q", path, rec.Code, rec.Header().Get("Location"))
			}
			continue
		}
		if path == "/apple-touch-icon.png" || path == "/apple-touch-icon-precomposed.png" {
			if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/packs/media/icons/apple-touch-icon-180x180-hash.png" {
				t.Fatalf("%s status = %d location = %q", path, rec.Code, rec.Header().Get("Location"))
			}
			continue
		}
		if path == "/apple-touch-icon-120x120.png" || path == "/apple-touch-icon-120x120-precomposed.png" {
			if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/packs/media/icons/apple-touch-icon-120x120-hash.png" {
				t.Fatalf("%s status = %d location = %q", path, rec.Code, rec.Header().Get("Location"))
			}
			continue
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d body = %s", path, rec.Code, rec.Body.String())
		}
		if rec.Body.Len() == 0 {
			t.Fatalf("%s served empty body", path)
		}
	}
}

func TestPaperclipRootPathStaticAssetsStayServed(t *testing.T) {
	publicDir := t.TempDir()
	packsDir := filepath.Join(publicDir, "packs")
	if err := os.MkdirAll(packsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(packsDir, "manifest.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	paperclipRoot := t.TempDir()
	path := filepath.Join(paperclipRoot, "media_attachments", "files", "000", "000", "123", "original", "photo.png")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("photo"), 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := NewServer(config.Config{
		Title:             "Paon",
		LocalDomain:       "example.com",
		PublicDir:         publicDir,
		PaperclipRootPath: paperclipRoot,
		PaperclipRootURL:  "/uploads",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	for _, requestPath := range []string{
		"/system/media_attachments/files/000/000/123/original/photo.png",
		"/uploads/media_attachments/files/000/000/123/original/photo.png",
	} {
		req := httptest.NewRequest(http.MethodGet, requestPath, nil)
		rec := httptest.NewRecorder()
		s.echo.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || rec.Body.String() != "photo" {
			t.Fatalf("%s status = %d body = %q", requestPath, rec.Code, rec.Body.String())
		}
	}
}

func TestPublishRouteSeedsComposeInitialState(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/publish?title=Hello&text=World&visibility=direct", nil)
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	for _, want := range []string{`"text":"Hello World"`, `"default_privacy":"direct"`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("publish html missing %s: %s", want, rec.Body.String())
		}
	}
}

func TestStatusesNewRouteSeedsComposeInitialState(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/statuses/new?text=Hello&url=https%3A%2F%2Fexample.com%2Fa&visibility=private", nil)
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	for _, want := range []string{`"text":"Hello https://example.com/a"`, `"default_privacy":"private"`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("statuses/new html missing %s: %s", want, rec.Body.String())
		}
	}
}

func TestUnknownAPIRouteMatchesRails404(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/does_not_exist", nil)
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"error":"Record not found"`) {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestUnknownWebRouteMatchesRails404(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{"/definitely-not-a-rails-route", "/definitely/not/a/rails/route"} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()
			s.echo.ServeHTTP(rec, req)
			if rec.Code != http.StatusNotFound {
				t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), `id="mastodon"`) {
				t.Fatalf("unknown web route fell back to React shell: %s", rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), `The page you are looking for`) {
				t.Fatalf("body = %s", rec.Body.String())
			}
		})
	}
}

func TestRailsCompatibleRedirectRoutes(t *testing.T) {
	e := echo.New()
	s := &Server{}
	e.GET("/.well-known/change-password", redirectTo("/auth/edit", http.StatusMovedPermanently))
	e.GET("/.well-known/change-password.json", redirectTo("/auth/edit", http.StatusMovedPermanently))
	e.GET("/.well-known/change-password.:format", redirectTo("/auth/edit", http.StatusMovedPermanently))
	e.GET("/.well-known/proxy", s.remoteInteractionRedirect)
	e.GET("/.well-known/proxy.json", s.remoteInteractionRedirectJSON)
	e.GET("/.well-known/proxy.:format", s.remoteInteractionRedirectFormat)
	e.GET("/authorize_follow", s.remoteInteractionRedirect)
	e.GET("/authorize_follow.json", s.remoteInteractionRedirectJSON)
	e.GET("/authorize_follow.:format", s.remoteInteractionRedirectFormat)
	e.GET("/settings", redirectTo("/settings/profile", http.StatusMovedPermanently))
	e.GET("/settings.json", redirectTo("/settings/profile", http.StatusMovedPermanently))
	e.GET("/settings.:format", redirectTo("/settings/profile", http.StatusMovedPermanently))
	e.GET("/settings/preferences", redirectTo("/settings/preferences/appearance", http.StatusMovedPermanently))
	e.GET("/settings/preferences.json", redirectTo("/settings/preferences/appearance", http.StatusMovedPermanently))
	e.GET("/settings/preferences.:format", redirectTo("/settings/preferences/appearance", http.StatusMovedPermanently))
	e.GET("/admin", redirectTo("/admin/dashboard", http.StatusFound))
	e.GET("/admin.json", redirectTo("/admin/dashboard", http.StatusFound))
	e.GET("/admin.:format", redirectTo("/admin/dashboard", http.StatusFound))
	e.GET("/admin/settings", redirectTo("/admin/settings/branding", http.StatusMovedPermanently))
	e.GET("/admin/settings.json", redirectTo("/admin/settings/branding", http.StatusMovedPermanently))
	e.GET("/admin/settings.:format", redirectTo("/admin/settings/branding", http.StatusMovedPermanently))
	e.GET("/admin/settings/edit", redirectTo("/admin/settings/branding", http.StatusMovedPermanently))
	e.GET("/admin/settings/edit.json", redirectTo("/admin/settings/branding", http.StatusMovedPermanently))
	e.GET("/admin/settings/edit.:format", redirectTo("/admin/settings/branding", http.StatusMovedPermanently))
	e.GET("/terms", redirectTo("/privacy-policy", http.StatusMovedPermanently))
	e.GET("/terms.json", redirectTo("/privacy-policy", http.StatusMovedPermanently))
	e.GET("/terms.:format", redirectTo("/privacy-policy", http.StatusMovedPermanently))
	e.GET("/about/more", redirectTo("/about", http.StatusMovedPermanently))
	e.GET("/about/more.json", redirectTo("/about", http.StatusMovedPermanently))
	e.GET("/about/more.:format", redirectTo("/about", http.StatusMovedPermanently))
	e.GET("/web", s.webRedirect)
	e.GET("/web/*", s.webRedirect)

	cases := []struct {
		name     string
		target   string
		status   int
		location string
	}{
		{
			name:     "change password",
			target:   "/.well-known/change-password",
			status:   http.StatusMovedPermanently,
			location: "/auth/edit",
		},
		{
			name:     "change password json",
			target:   "/.well-known/change-password.json",
			status:   http.StatusMovedPermanently,
			location: "/auth/edit",
		},
		{
			name:     "change password xml",
			target:   "/.well-known/change-password.xml",
			status:   http.StatusMovedPermanently,
			location: "/auth/edit",
		},
		{
			name:     "proxy",
			target:   "/.well-known/proxy?uri=acct%3Aalice%40example.com",
			status:   http.StatusMovedPermanently,
			location: "/authorize_interaction?uri=acct%3Aalice%40example.com",
		},
		{
			name:     "proxy json",
			target:   "/.well-known/proxy.json?uri=acct%3Aalice%40example.com",
			status:   http.StatusMovedPermanently,
			location: "/authorize_interaction?format=json&uri=acct%3Aalice%40example.com",
		},
		{
			name:     "proxy xml",
			target:   "/.well-known/proxy.xml?uri=acct%3Aalice%40example.com",
			status:   http.StatusMovedPermanently,
			location: "/authorize_interaction?format=xml&uri=acct%3Aalice%40example.com",
		},
		{
			name:     "authorize follow",
			target:   "/authorize_follow?acct=alice%40example.com",
			status:   http.StatusMovedPermanently,
			location: "/authorize_interaction?acct=alice%40example.com",
		},
		{
			name:     "authorize follow json",
			target:   "/authorize_follow.json?acct=alice%40example.com",
			status:   http.StatusMovedPermanently,
			location: "/authorize_interaction?acct=alice%40example.com&format=json",
		},
		{
			name:     "authorize follow xml",
			target:   "/authorize_follow.xml?acct=alice%40example.com",
			status:   http.StatusMovedPermanently,
			location: "/authorize_interaction?acct=alice%40example.com&format=xml",
		},
		{
			name:     "settings",
			target:   "/settings",
			status:   http.StatusMovedPermanently,
			location: "/settings/profile",
		},
		{
			name:     "settings json",
			target:   "/settings.json",
			status:   http.StatusMovedPermanently,
			location: "/settings/profile",
		},
		{
			name:     "settings xml",
			target:   "/settings.xml",
			status:   http.StatusMovedPermanently,
			location: "/settings/profile",
		},
		{
			name:     "settings preferences",
			target:   "/settings/preferences",
			status:   http.StatusMovedPermanently,
			location: "/settings/preferences/appearance",
		},
		{
			name:     "settings preferences json",
			target:   "/settings/preferences.json",
			status:   http.StatusMovedPermanently,
			location: "/settings/preferences/appearance",
		},
		{
			name:     "settings preferences xml",
			target:   "/settings/preferences.xml",
			status:   http.StatusMovedPermanently,
			location: "/settings/preferences/appearance",
		},
		{
			name:     "admin",
			target:   "/admin",
			status:   http.StatusFound,
			location: "/admin/dashboard",
		},
		{
			name:     "admin json",
			target:   "/admin.json",
			status:   http.StatusFound,
			location: "/admin/dashboard",
		},
		{
			name:     "admin xml",
			target:   "/admin.xml",
			status:   http.StatusFound,
			location: "/admin/dashboard",
		},
		{
			name:     "admin settings",
			target:   "/admin/settings",
			status:   http.StatusMovedPermanently,
			location: "/admin/settings/branding",
		},
		{
			name:     "admin settings json",
			target:   "/admin/settings.json",
			status:   http.StatusMovedPermanently,
			location: "/admin/settings/branding",
		},
		{
			name:     "admin settings xml",
			target:   "/admin/settings.xml",
			status:   http.StatusMovedPermanently,
			location: "/admin/settings/branding",
		},
		{
			name:     "admin settings edit",
			target:   "/admin/settings/edit",
			status:   http.StatusMovedPermanently,
			location: "/admin/settings/branding",
		},
		{
			name:     "admin settings edit json",
			target:   "/admin/settings/edit.json",
			status:   http.StatusMovedPermanently,
			location: "/admin/settings/branding",
		},
		{
			name:     "admin settings edit xml",
			target:   "/admin/settings/edit.xml",
			status:   http.StatusMovedPermanently,
			location: "/admin/settings/branding",
		},
		{
			name:     "terms",
			target:   "/terms",
			status:   http.StatusMovedPermanently,
			location: "/privacy-policy",
		},
		{
			name:     "terms json",
			target:   "/terms.json",
			status:   http.StatusMovedPermanently,
			location: "/privacy-policy",
		},
		{
			name:     "terms xml",
			target:   "/terms.xml",
			status:   http.StatusMovedPermanently,
			location: "/privacy-policy",
		},
		{
			name:     "about more",
			target:   "/about/more",
			status:   http.StatusMovedPermanently,
			location: "/about",
		},
		{
			name:     "about more json",
			target:   "/about/more.json",
			status:   http.StatusMovedPermanently,
			location: "/about",
		},
		{
			name:     "about more xml",
			target:   "/about/more.xml",
			status:   http.StatusMovedPermanently,
			location: "/about",
		},
		{
			name:     "web path",
			target:   "/web/@alice/123?foo=bar",
			status:   http.StatusFound,
			location: "/@alice/123?foo=bar",
		},
		{
			name:     "web root",
			target:   "/web?foo=bar",
			status:   http.StatusFound,
			location: "/?foo=bar",
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.target, nil)
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)
			if rec.Code != tt.status || rec.Header().Get("Location") != tt.location {
				t.Fatalf("status = %d location = %q", rec.Code, rec.Header().Get("Location"))
			}
		})
	}
}

func TestAuthorizeInteractionRequiresSignInAndPreservesReturnPath(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/authorize_interaction?uri=acct%3Aalice%40example.com", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	s := &Server{}
	if err := s.authorizeInteraction(c); err != nil {
		t.Fatal(err)
	}
	want := "/auth/sign_in?redirect_to=" + url.QueryEscape("/authorize_interaction?uri=acct%3Aalice%40example.com")
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != want {
		t.Fatalf("status = %d location = %q want %q", rec.Code, rec.Header().Get("Location"), want)
	}
}

func TestAuthorizeInteractionFormatRouteRequiresSignInAndPreservesReturnPath(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/authorize_interaction.xml?uri=acct%3Aalice%40example.com", nil)
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)
	want := "/auth/sign_in?redirect_to=" + url.QueryEscape("/authorize_interaction.xml?uri=acct%3Aalice%40example.com")
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != want {
		t.Fatalf("status = %d location = %q want %q", rec.Code, rec.Header().Get("Location"), want)
	}
}

func TestLocalInteractionPathNormalizesLocalAccountAndStatusURLs(t *testing.T) {
	cases := map[string]string{
		"https://social.example/@alice":                   "/@alice",
		"https://social.example/@alice/123":               "/@alice/123",
		"https://social.example/users/alice":              "/@alice",
		"https://social.example/users/alice/statuses/123": "/@alice/123",
		"https://remote.example/users/alice/statuses/123": "",
		"acct:alice@example.com":                          "",
	}
	for raw, want := range cases {
		got, ok := localInteractionPath("https://social.example", raw)
		if want == "" {
			if ok || got != "" {
				t.Fatalf("localInteractionPath(%q) = %q, %v; want empty false", raw, got, ok)
			}
			continue
		}
		if !ok || got != want {
			t.Fatalf("localInteractionPath(%q) = %q, %v; want %q true", raw, got, ok, want)
		}
	}
}

func TestResolveInteractionPathWithoutDatabaseLeavesRemoteResourcesUnresolved(t *testing.T) {
	s := &Server{cfg: config.Config{Scheme: "https", LocalDomain: "social.example", WebDomain: "social.example"}}
	for _, raw := range []string{"https://remote.example/users/alice", "alice@remote.example"} {
		got, err := s.resolveInteractionPath(raw, &models.Account{ID: 1})
		if err != nil {
			t.Fatal(err)
		}
		if got != "" {
			t.Fatalf("resolveInteractionPath(%q) = %q, want empty", raw, got)
		}
	}
}

func TestInteractionStatusVisibleMatchesCoreStatusPolicyCases(t *testing.T) {
	s := &Server{}
	current := &models.Account{ID: 10}
	if !s.interactionStatusVisible(models.Status{ID: 1, AccountID: 20, Visibility: 0}, current) {
		t.Fatal("public status should be visible")
	}
	if !s.interactionStatusVisible(models.Status{ID: 1, AccountID: 20, Visibility: 1}, current) {
		t.Fatal("unlisted status should be visible")
	}
	if !s.interactionStatusVisible(models.Status{ID: 1, AccountID: 10, Visibility: 3}, current) {
		t.Fatal("owned direct status should be visible")
	}
	if s.interactionStatusVisible(models.Status{ID: 1, AccountID: 20, Visibility: 3}, current) {
		t.Fatal("unmentioned direct status should be hidden")
	}
	if s.interactionStatusVisible(models.Status{ID: 1, AccountID: 20, Visibility: 0, DeletedAt: sql.NullTime{Valid: true}}, current) {
		t.Fatal("deleted status should be hidden")
	}
	if !s.interactionStatusVisible(models.Status{ID: 1, AccountID: 20, Visibility: 0}, nil) {
		t.Fatal("public status should be visible without current account")
	}
	if s.interactionStatusAuthorBlocksCurrent(20, 10) {
		t.Fatal("nil database should not report author block")
	}
	if s.interactionStatusAuthorDomainBlocksCurrent(20, &models.Account{ID: 10, Domain: sql.NullString{String: "remote.example", Valid: true}}) {
		t.Fatal("nil database should not report author domain block")
	}
}

func TestRailsCompatibleWebAppPathsServeExistingUI(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	paths := []string{
		"/home",
		"/getting-started",
		"/keyboard-shortcuts",
		"/public",
		"/public/local",
		"/public/remote",
		"/conversations",
		"/lists",
		"/lists/123",
		"/notifications",
		"/favourites",
		"/bookmarks",
		"/pinned",
		"/start",
		"/directory",
		"/explore",
		"/explore/tags",
		"/search",
		"/about",
		"/privacy-policy",
		"/instance-stats/remote.example",
		"/statuses",
		"/statuses/new",
		"/publish",
		"/deck",
		"/deck/getting-started",
		"/@alice/with_replies",
		"/@alice/media",
		"/@alice/tagged/go",
		"/@alice/followers",
		"/@alice/following",
		"/@alice/123/reblogs",
		"/@alice/123/favourites",
		"/@alice@example.com/remote/path",
		"/follow_requests",
		"/blocks",
		"/domain_blocks",
		"/mutes",
		"/followed_tags",
		"/tags/golang",
		"/statuses/123",
	}

	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()
			s.echo.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), `id="mastodon"`) || !strings.Contains(rec.Body.String(), `content="`+path+`"`) {
				t.Fatalf("unexpected UI shell for %s: %s", path, rec.Body.String())
			}
		})
	}
}

func TestEncodedAtRedirectMatchesRailsCompatibility(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/%40alice/123?foo=bar", nil)
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != "/@alice/123?foo=bar" {
		t.Fatalf("status = %d location = %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestRailsResourceUpdatePatchAliasesReachHandlers(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	paths := []string{
		"/api/v1/statuses/1",
		"/api/v1/media/1",
		"/api/v1/filters/1",
		"/api/v2/filters/1",
		"/api/v2/filters/keywords/1",
		"/api/v1/lists/1",
		"/api/v1/admin/domain_blocks/1",
		"/api/v1/admin/ip_blocks/1",
		"/api/web/settings",
	}

	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPatch, path, strings.NewReader(`{}`))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			s.echo.ServeHTTP(rec, req)
			if rec.Code == http.StatusMethodNotAllowed || rec.Code == http.StatusNotImplemented {
				t.Fatalf("PATCH route did not reach handler: status = %d body = %s", rec.Code, rec.Body.String())
			}
		})
	}
}
