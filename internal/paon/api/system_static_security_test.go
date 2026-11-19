package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
)

const (
	systemStaticHTMLFile  = "cache/media_attachments/files/000/000/001/original/x.html"
	systemStaticPNGFile   = "media_attachments/files/000/000/002/original/photo.png"
	systemStaticMP4File   = "media_attachments/files/000/000/003/original/clip.mp4"
	systemStaticSVGFile   = "cache/preview_cards/images/000/000/005/original/a.svg"
	systemStaticIndexDir  = "cache/media_attachments/files/000/000/004/original/"
	systemStaticStrictCSP = "default-src 'none'; form-action 'none'; sandbox"
	systemStaticHTMLBody  = `<!doctype html><html><head><meta name="csrf-token" content=""></head>` +
		`<body><form method="post" action="/auth/sign_out"></form><script>alert(document.cookie)</script></body></html>`
	systemStaticPNGBody = "\x89PNG\r\n\x1a\nfixture"
	systemStaticMP4Body = "\x00\x00\x00\x18ftypmp42fixture"
)

// newSystemStaticTestServer builds a server whose public/system directory
// holds an attacker-controlled HTML file next to regular media. An empty
// rootURL keeps the default PAPERCLIP_ROOT_URL (/system).
func newSystemStaticTestServer(t *testing.T, rootURL string) *Server {
	t.Helper()
	publicDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(publicDir, "packs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(publicDir, "packs", "manifest.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Title: "Paon", LocalDomain: "example.com", PublicDir: publicDir}
	systemDir := filepath.Join(publicDir, "system")
	if rootURL != "" {
		systemDir = t.TempDir()
		cfg.PaperclipRootPath = systemDir
		cfg.PaperclipRootURL = rootURL
	}
	for name, body := range map[string]string{
		systemStaticHTMLFile:                systemStaticHTMLBody,
		systemStaticIndexDir + "index.html": systemStaticHTMLBody,
		systemStaticSVGFile:                 `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`,
		systemStaticPNGFile:                 systemStaticPNGBody,
		systemStaticMP4File:                 systemStaticMP4Body,
	} {
		target := filepath.Join(systemDir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s, err := NewServer(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func serveSystemStaticRequest(s *Server, method string, target string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)
	return rec
}

func assertSystemStaticDownload(t *testing.T, rec *httptest.ResponseRecorder, target string, wantBody string) {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("%s status = %d body = %s", target, rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "application/octet-stream" {
		t.Fatalf("%s Content-Type = %q, want application/octet-stream", target, got)
	}
	if got := rec.Header().Get("Content-Disposition"); got != "attachment" {
		t.Fatalf("%s Content-Disposition = %q, want attachment", target, got)
	}
	assertSystemStaticHardened(t, rec, target)
	// The stored file must be passed through untouched: no CSRF token or
	// other rewriting of user-controlled content.
	if got := rec.Body.String(); got != wantBody {
		t.Fatalf("%s body = %q, want the stored file unchanged", target, got)
	}
	for _, cookie := range rec.Header().Values("Set-Cookie") {
		if strings.HasPrefix(cookie, browserSessionCookieName+"=") {
			t.Fatalf("%s started a browser session for a stored file: %q", target, cookie)
		}
	}
}

func assertSystemStaticInline(t *testing.T, rec *httptest.ResponseRecorder, target string, wantType string, wantBody string) {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("%s status = %d body = %s", target, rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != wantType {
		t.Fatalf("%s Content-Type = %q, want %q", target, got, wantType)
	}
	if got := rec.Header().Get("Content-Disposition"); got != "" {
		t.Fatalf("%s Content-Disposition = %q, want inline media", target, got)
	}
	assertSystemStaticHardened(t, rec, target)
	if got := rec.Body.String(); got != wantBody {
		t.Fatalf("%s body = %q, want %q", target, got, wantBody)
	}
}

func assertSystemStaticHardened(t *testing.T, rec *httptest.ResponseRecorder, target string) {
	t.Helper()
	if got := rec.Header().Get("Content-Security-Policy"); got != systemStaticStrictCSP {
		t.Fatalf("%s Content-Security-Policy = %q, want %q", target, got, systemStaticStrictCSP)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("%s X-Content-Type-Options = %q, want nosniff", target, got)
	}
}

func assertSystemStaticNotServed(t *testing.T, s *Server, target string) {
	t.Helper()
	rec := serveSystemStaticRequest(s, http.MethodGet, target)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("%s status = %d, want 404; body = %s", target, rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); strings.Contains(body, "alert(document.cookie)") || strings.Contains(body, "fixture") {
		t.Fatalf("%s served a public/system file: %s", target, body)
	}
}

func TestSystemStaticMountDoesNotMatchPrefixWithoutSlash(t *testing.T) {
	s := newSystemStaticTestServer(t, "")
	for _, target := range []string{
		"/systemcache/media_attachments/files/000/000/001/original/x.html",
		"/systemmedia_attachments/files/000/000/002/original/photo.png",
		"/systemcache/" + systemStaticIndexDir,
	} {
		assertSystemStaticNotServed(t, s, target)
	}
}

func TestSystemStaticNonMediaFilesAreSandboxedDownloads(t *testing.T) {
	s := newSystemStaticTestServer(t, "")
	target := "/system/" + systemStaticHTMLFile
	rec := serveSystemStaticRequest(s, http.MethodGet, target)
	assertSystemStaticDownload(t, rec, target, systemStaticHTMLBody)
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=2419200, immutable" {
		t.Fatalf("%s Cache-Control = %q", target, got)
	}
	// mediaPrivacyStaticHandler never serves a directory, so its index.html
	// is not reachable through the directory URL at all.
	assertSystemStaticNotServed(t, s, "/system/"+systemStaticIndexDir)
	target = "/system/" + systemStaticSVGFile
	rec = serveSystemStaticRequest(s, http.MethodGet, target)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/octet-stream" || rec.Header().Get("Content-Disposition") != "attachment" {
		t.Fatalf("%s status = %d Content-Type = %q Content-Disposition = %q", target, rec.Code, rec.Header().Get("Content-Type"), rec.Header().Get("Content-Disposition"))
	}
	assertSystemStaticHardened(t, rec, target)

	head := serveSystemStaticRequest(s, http.MethodHead, "/system/"+systemStaticHTMLFile)
	if head.Code != http.StatusOK || head.Header().Get("Content-Type") != "application/octet-stream" || head.Header().Get("Content-Disposition") != "attachment" {
		t.Fatalf("HEAD status = %d Content-Type = %q Content-Disposition = %q", head.Code, head.Header().Get("Content-Type"), head.Header().Get("Content-Disposition"))
	}
	assertSystemStaticHardened(t, head, "HEAD /system/"+systemStaticHTMLFile)
}

func TestSystemStaticMediaFilesStayInline(t *testing.T) {
	s := newSystemStaticTestServer(t, "")
	pngTarget := "/system/" + systemStaticPNGFile
	png := serveSystemStaticRequest(s, http.MethodGet, pngTarget)
	assertSystemStaticInline(t, png, pngTarget, "image/png", systemStaticPNGBody)
	if got := png.Header().Get("Cache-Control"); got != "public, max-age=2419200, immutable" {
		t.Fatalf("%s Cache-Control = %q", pngTarget, got)
	}
	mp4Target := "/system/" + systemStaticMP4File
	assertSystemStaticInline(t, serveSystemStaticRequest(s, http.MethodGet, mp4Target), mp4Target, "video/mp4", systemStaticMP4Body)
}

func TestSystemStaticMissingFileKeepsErrorResponseType(t *testing.T) {
	s := newSystemStaticTestServer(t, "")
	for _, target := range []string{"/system/cache/missing.html", "/system/media_attachments/missing.png"} {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		rec := httptest.NewRecorder()
		s.echo.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d body = %s", target, rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
			t.Fatalf("%s Content-Type = %q, want the error handler's JSON type", target, got)
		}
		if got := rec.Header().Get("Content-Disposition"); got != "" {
			t.Fatalf("%s Content-Disposition = %q, want none on error responses", target, got)
		}
		if got := rec.Header().Get("Content-Security-Policy"); got != systemStaticStrictCSP {
			t.Fatalf("%s Content-Security-Policy = %q", target, got)
		}
	}
}

func TestCustomPaperclipRootURLStaticMountsAreHardened(t *testing.T) {
	s := newSystemStaticTestServer(t, "/uploads")
	// The canonical /system/ mount stays registered next to the custom root
	// and must be hardened even though railsPaperclipStaticPath only knows
	// the custom root.
	for _, prefix := range []string{"/system/", "/uploads/"} {
		htmlTarget := prefix + systemStaticHTMLFile
		assertSystemStaticDownload(t, serveSystemStaticRequest(s, http.MethodGet, htmlTarget), htmlTarget, systemStaticHTMLBody)
		pngTarget := prefix + systemStaticPNGFile
		assertSystemStaticInline(t, serveSystemStaticRequest(s, http.MethodGet, pngTarget), pngTarget, "image/png", systemStaticPNGBody)
		mp4Target := prefix + systemStaticMP4File
		assertSystemStaticInline(t, serveSystemStaticRequest(s, http.MethodGet, mp4Target), mp4Target, "video/mp4", systemStaticMP4Body)
	}
	for _, target := range []string{
		"/uploadscache/media_attachments/files/000/000/001/original/x.html",
		"/uploadsmedia_attachments/files/000/000/002/original/photo.png",
		"/systemcache/media_attachments/files/000/000/001/original/x.html",
	} {
		assertSystemStaticNotServed(t, s, target)
	}
}

func TestPaperclipStaticMountPrefixes(t *testing.T) {
	testCases := []struct {
		rootURL string
		want    []string
	}{
		{rootURL: "", want: []string{"/system/"}},
		{rootURL: "/system", want: []string{"/system/"}},
		{rootURL: "system/", want: []string{"/system/"}},
		{rootURL: "/uploads/", want: []string{"/system/", "/uploads/"}},
		{rootURL: "media/files", want: []string{"/system/", "/media/files/"}},
		{rootURL: "https://files.example.com/system", want: []string{"/system/"}},
	}
	for _, tc := range testCases {
		got := paperclipStaticMountPrefixes(config.Config{PaperclipRootURL: tc.rootURL})
		if strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Fatalf("PaperclipRootURL %q prefixes = %v, want %v", tc.rootURL, got, tc.want)
		}
	}
}

func TestPaperclipInlineContentTypeAllowsOnlyMediaExtensions(t *testing.T) {
	for wildcard, want := range map[string]string{
		"media_attachments/files/1/original/a.png":  "image/png",
		"media_attachments/files/1/original/a.JPG":  "image/jpeg",
		"media_attachments/files/1/original/a.webp": "image/webp",
		"media_attachments/files/1/original/a.mp4":  "video/mp4",
		"media_attachments/files/1/original/a.jfif": "image/jpeg",
		"media_attachments/files/1/original/a.jpe":  "image/jpeg",
		// Rails SiteUpload accepts SVG mascots and thumbnails.
		"site_uploads/files/000/000/001/original/mascot.svg": "image/svg+xml",
	} {
		got, ok := paperclipInlineContentType(wildcard)
		if !ok || got != want {
			t.Fatalf("%s = %q, %v; want %q inline", wildcard, got, ok, want)
		}
	}
	for _, wildcard := range []string{
		"",
		"cache/a.html",
		"cache/a.htm",
		"cache/a.xhtml",
		"cache/a.svg",
		"cache/a.xml",
		"cache/a.js",
		"cache/a.png.html",
		"cache/noextension",
		"cache/a.png/",
		"cache/a.png/.",
		"cache/a.png/..",
		// SVG stays an attachment outside site uploads, including dot segments
		// that clean to another directory.
		"media_attachments/files/1/original/a.svg",
		"site_uploadsx/files/1/original/a.svg",
		"site_uploads/../media_attachments/files/1/original/a.svg",
		"site_uploads/%2E%2E/media_attachments/files/1/original/a.svg",
		"site_uploads/%2e%2e/cache/a.svg",
	} {
		if got, ok := paperclipInlineContentType(wildcard); ok {
			t.Fatalf("%q = %q inline, want attachment", wildcard, got)
		}
	}
}

func TestBrowserHTMLResponseCandidateSkipsSystemMounts(t *testing.T) {
	defaultCfg := config.Config{}
	customCfg := config.Config{PaperclipRootURL: "/uploads"}
	for _, tc := range []struct {
		cfg  config.Config
		path string
		want bool
	}{
		{cfg: defaultCfg, path: "/system/" + systemStaticHTMLFile, want: false},
		{cfg: customCfg, path: "/system/" + systemStaticHTMLFile, want: false},
		{cfg: customCfg, path: "/uploads/" + systemStaticHTMLFile, want: false},
		{cfg: defaultCfg, path: "/api/v1/instance", want: false},
		{cfg: defaultCfg, path: "/about", want: true},
		{cfg: defaultCfg, path: "/settings/profile", want: true},
	} {
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		if got := browserHTMLResponseCandidate(tc.cfg, req); got != tc.want {
			t.Fatalf("browserHTMLResponseCandidate(%q, PaperclipRootURL=%q) = %v, want %v", tc.path, tc.cfg.PaperclipRootURL, got, tc.want)
		}
	}
}
