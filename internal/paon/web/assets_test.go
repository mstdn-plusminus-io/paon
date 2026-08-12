package web

import (
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"github.com/mstdn-plusminus-io/paon/internal/paon/serializer"
)

func TestSettingsHTMLEscapesAccountFields(t *testing.T) {
	renderer := &Renderer{cfg: config.Config{Title: "Paon"}}
	html, err := renderer.SettingsHTML("/settings/profile", &models.Account{
		Username:    "alice",
		DisplayName: "Alice <Admin>",
		Domain:      sql.NullString{String: "remote.example", Valid: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(html, "Alice <Admin>") {
		t.Fatalf("display name was not escaped: %s", html)
	}
	if !strings.Contains(html, "Alice &lt;Admin&gt;") || !strings.Contains(html, "@alice@remote.example") {
		t.Fatalf("settings html missing escaped account data: %s", html)
	}
	if !strings.Contains(html, "/settings/preferences/appearance") || !strings.Contains(html, "/home") {
		t.Fatalf("settings html missing navigation: %s", html)
	}
	for _, want := range []string{"/settings/imports", "/settings/applications", "/settings/sessions", "/settings/security_keys", "/settings/delete"} {
		if !strings.Contains(html, want) {
			t.Fatalf("settings html missing %q navigation: %s", want, html)
		}
	}
	if strings.Contains(html, "still being migrated") {
		t.Fatalf("settings html exposes migration placeholder copy: %s", html)
	}
}

func TestSettingsHTMLUsesConfiguredLocale(t *testing.T) {
	renderer := &Renderer{cfg: config.Config{Title: "Paon", DefaultLocale: "en"}}
	html, err := renderer.SettingsHTML("/settings/profile", &models.Account{Username: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(html, `<html lang="en">`) {
		t.Fatalf("settings html missing configured lang: %s", html)
	}
}

func TestSettingsHTMLUsesRailsLocaleLabelsWhenPublicDirIsConfigured(t *testing.T) {
	renderer := &Renderer{cfg: config.Config{Title: "Paon", DefaultLocale: "ja", PublicDir: "../../../public"}}
	html, err := renderer.SettingsHTML("/settings/migration", &models.Account{Username: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`<html lang="ja">`,
		`<h1>設定</h1>`,
		`>アカウントの引っ越し</a>`,
		`>アプリに戻る</a>`,
		`Rails互換のフォームフィールド名`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("settings html missing localized fallback shell fragment %q: %s", want, html)
		}
	}
	if strings.Contains(html, `>Migration</a>`) || strings.Contains(html, `>Back to app</a>`) {
		t.Fatalf("settings html should not expose English fallback labels when locales are available: %s", html)
	}
}

func TestSettingsHTMLCanUseRailsSiteTitle(t *testing.T) {
	renderer := &Renderer{cfg: config.Config{Title: "Fallback"}}
	html, err := renderer.SettingsHTML("/settings/profile", &models.Account{Username: "alice"}, "Configured title")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(html, `<title>Configured title</title>`) {
		t.Fatalf("settings html missing configured site title: %s", html)
	}
}

func TestSupportedThemesMatchRailsThemesConfig(t *testing.T) {
	raw, err := os.ReadFile("../../../config/themes.yml")
	if err != nil {
		t.Fatal(err)
	}
	themes := railsThemeNames(string(raw))
	if !reflect.DeepEqual(supportedThemes(), themes) {
		t.Fatalf("supportedThemes = %#v, want Rails config/themes.yml themes %#v", supportedThemes(), themes)
	}
	for _, theme := range supportedThemes() {
		if !containsString(requiredPackAssets(config.Config{DefaultLocale: "ja"}), theme+".css") {
			t.Fatalf("requiredPackAssets missing supported theme css %q", theme+".css")
		}
	}
}

func TestRequiredPackAssetsIncludeRailsAvailableLocaleChunks(t *testing.T) {
	required := map[string]bool{}
	for _, name := range requiredPackAssets(config.Config{DefaultLocale: "ja"}) {
		required[name] = true
	}
	for _, locale := range config.RailsI18nAvailableLocales() {
		name := "locale/" + locale + "-json.js"
		if !required[name] {
			t.Fatalf("requiredPackAssets missing Rails available locale chunk %q", name)
		}
	}
	if required["locale/tai-json.js"] || required["locale/uz-json.js"] {
		t.Fatalf("requiredPackAssets should follow Rails config.i18n.available_locales, not every locale YAML/chunk: %#v", required)
	}
}

func TestRailsPackEntrypointsAreExplicitlyCovered(t *testing.T) {
	expected := map[string]bool{
		"admin.tsx":                    true,
		"application.js":               true,
		"embed.tsx":                    true,
		"error.js":                     true,
		"mailer.ts":                    true,
		"public-path.js":               true,
		"public.tsx":                   true,
		"remote_interaction_helper.ts": true,
		"share.jsx":                    true,
		"sign_up.ts":                   true,
		"two_factor_authentication.js": true,
	}
	requiredManifestEntries := map[string]string{
		"admin.tsx":                    "admin.js",
		"application.js":               "application.js",
		"embed.tsx":                    "embed.js",
		"error.js":                     "error.js",
		"mailer.ts":                    "mailer.js",
		"public.tsx":                   "public.js",
		"remote_interaction_helper.ts": "remote_interaction_helper.js",
		"share.jsx":                    "share.js",
		"sign_up.ts":                   "sign_up.js",
		"two_factor_authentication.js": "two_factor_authentication.js",
	}

	entries, err := os.ReadDir("../../../app/javascript/entrypoints")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !expected[name] {
			t.Fatalf("Rails pack entrypoint %s is not listed in the Go asset coverage inventory", name)
		}
		seen[name] = true
	}
	for name := range expected {
		if !seen[name] {
			t.Fatalf("expected Rails pack entrypoint %s was not found", name)
		}
	}

	required := map[string]bool{}
	for _, name := range requiredPackAssets(config.Config{DefaultLocale: "ja"}) {
		required[name] = true
	}
	for entrypoint, manifestName := range requiredManifestEntries {
		if !required[manifestName] {
			t.Fatalf("requiredPackAssets missing manifest entry %s for Rails pack %s", manifestName, entrypoint)
		}
	}
}

func TestParsePackManifestAcceptsStringAndObjectEntries(t *testing.T) {
	manifest := parsePackManifest([]byte(`{
		"application.js": "/packs/js/application-hash.js",
		"media/icons/android-chrome-512x512.png": {
			"src": "/packs/media/icons/android-chrome-512x512-hash.png",
			"integrity": "sha256-example"
		}
	}`))
	if manifest["application.js"] != "/packs/js/application-hash.js" {
		t.Fatalf("application asset = %q", manifest["application.js"])
	}
	if manifest["media/icons/android-chrome-512x512.png"] != "/packs/media/icons/android-chrome-512x512-hash.png" {
		t.Fatalf("icon asset = %q", manifest["media/icons/android-chrome-512x512.png"])
	}
}

func TestFallbackPackAssetPathMatchesShakapackerOutputLayout(t *testing.T) {
	for name, want := range map[string]string{
		"application.js":                       "/packs/js/application.js",
		"admin.js":                             "/packs/js/admin.js",
		"embed.js":                             "/packs/js/embed.js",
		"public.js":                            "/packs/js/public.js",
		"error.js":                             "/packs/js/error.js",
		"mailer.js":                            "/packs/js/mailer.js",
		"sign_up.js":                           "/packs/js/sign_up.js",
		"two_factor_authentication.js":         "/packs/js/two_factor_authentication.js",
		"features/home_timeline.js":            "/packs/js/features/home_timeline.js",
		"features/link_timeline.js":            "/packs/js/features/link_timeline.js",
		"modals/report_modal.js":               "/packs/js/modals/report_modal.js",
		"locale/ja-json.js":                    "/packs/js/locale/ja-json.js",
		"common.css":                           "/packs/css/common.css",
		"mailer.css":                           "/packs/css/mailer.css",
		"media/images/mailer/logo.png":         "/packs/media/images/mailer/logo.png",
		"/media/icons/favicon-32x32.png":       "/packs/media/icons/favicon-32x32.png",
		" media/images/logo-symbol-icon.svg  ": "/packs/media/images/logo-symbol-icon.svg",
	} {
		if got := FallbackPackAssetPath(name); got != want {
			t.Fatalf("FallbackPackAssetPath(%q) = %q, want %q", name, got, want)
		}
	}
	if got := FallbackPackAssetPath("manifest.json"); got != "" {
		t.Fatalf("FallbackPackAssetPath(non-pack) = %q", got)
	}
}

func TestResolvePackAssetPathUsesProductionManifestAndSafeFallback(t *testing.T) {
	publicDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(publicDir, "packs"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"media/images/mailer-new/common/header-bg-start.png":"/packs/media/images/header-bg-start-deadbeef.png"}`
	if err := os.WriteFile(filepath.Join(publicDir, "packs", "manifest.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	name := "media/images/mailer-new/common/header-bg-start.png"
	if got := ResolvePackAssetPath(config.Config{PublicDir: publicDir}, name); got != "/packs/media/images/header-bg-start-deadbeef.png" {
		t.Fatalf("production asset path = %q", got)
	}
	if got := ResolvePackAssetPath(config.Config{}, name); got != "/packs/"+name {
		t.Fatalf("fallback asset path = %q", got)
	}
}

func TestNewRendererAllowsEmptyPublicDirForHandlerUnitTests(t *testing.T) {
	renderer, err := NewRenderer(config.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if len(renderer.manifest) != 0 {
		t.Fatalf("manifest = %#v", renderer.manifest)
	}
}

func TestNewRendererFailsFastWithoutConfiguredBuiltUIPackManifest(t *testing.T) {
	if _, err := NewRenderer(config.Config{PublicDir: t.TempDir()}); err == nil || !strings.Contains(err.Error(), "read public packs manifest") {
		t.Fatalf("NewRenderer error = %v", err)
	}
}

func TestNewRendererLoadsBuiltUIPackManifest(t *testing.T) {
	publicDir := t.TempDir()
	packsDir := filepath.Join(publicDir, "packs")
	if err := os.MkdirAll(packsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(packsDir, "manifest.json"), []byte(`{"application.js":"/packs/js/application-hash.js"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	renderer, err := NewRenderer(config.Config{PublicDir: publicDir})
	if err != nil {
		t.Fatal(err)
	}
	if renderer.manifest["application.js"] != "/packs/js/application-hash.js" {
		t.Fatalf("manifest = %#v", renderer.manifest)
	}
}

func TestValidatePublicAssetsAcceptsBuiltUIPacks(t *testing.T) {
	publicDir := writePublicAssetManifest(t, map[string]string{
		"unused-remote-cdn.js": "https://cdn.example.test/packs/js/remote.js",
	}, true)
	if err := ValidatePublicAssets(config.Config{PublicDir: publicDir, DefaultLocale: "ja"}); err != nil {
		t.Fatalf("ValidatePublicAssets: %v", err)
	}
}

func TestValidatePublicAssetsRejectsMissingManifest(t *testing.T) {
	publicDir := t.TempDir()
	writeRequiredPublicFiles(t, publicDir)
	if err := ValidatePublicAssets(config.Config{PublicDir: publicDir}); err == nil {
		t.Fatal("ValidatePublicAssets returned nil without manifest")
	}
}

func TestValidatePublicAssetsRejectsMissingRequiredFile(t *testing.T) {
	publicDir := writePublicAssetManifest(t, map[string]string{
		"locale/en-json.js": "/packs/js/locale/missing.chunk.js",
	}, true)
	if err := os.Remove(filepath.Join(publicDir, "packs", "js", "locale", "missing.chunk.js")); err != nil {
		t.Fatal(err)
	}
	if err := ValidatePublicAssets(config.Config{PublicDir: publicDir, DefaultLocale: "ja"}); err == nil || !strings.Contains(err.Error(), "locale/en-json.js") {
		t.Fatalf("ValidatePublicAssets error = %v", err)
	}
}

func TestValidatePublicAssetsRejectsBrokenExtraManifestLocaleChunk(t *testing.T) {
	publicDir := writePublicAssetManifest(t, map[string]string{
		"locale/tai-json.js": "/packs/js/locale/tai-json-missing.chunk.js",
	}, true)
	if err := os.Remove(filepath.Join(publicDir, "packs", "js", "locale", "tai-json-missing.chunk.js")); err != nil {
		t.Fatal(err)
	}
	if err := ValidatePublicAssets(config.Config{PublicDir: publicDir, DefaultLocale: "ja"}); err == nil || !strings.Contains(err.Error(), "locale/tai-json.js") {
		t.Fatalf("ValidatePublicAssets error = %v", err)
	}
}

func TestValidatePublicAssetsRejectsMissingPreloadedFeatureChunk(t *testing.T) {
	publicDir := writePublicAssetManifest(t, map[string]string{
		"features/explore.js": "",
	}, true)
	if err := ValidatePublicAssets(config.Config{PublicDir: publicDir, DefaultLocale: "ja"}); err == nil || !strings.Contains(err.Error(), "features/explore.js") {
		t.Fatalf("ValidatePublicAssets error = %v", err)
	}
}

func TestValidatePublicAssetsRejectsMissingAdminDashboardPack(t *testing.T) {
	publicDir := writePublicAssetManifest(t, map[string]string{
		"admin.js": "",
	}, true)
	if err := ValidatePublicAssets(config.Config{PublicDir: publicDir, DefaultLocale: "ja"}); err == nil || !strings.Contains(err.Error(), "admin.js") {
		t.Fatalf("ValidatePublicAssets error = %v", err)
	}
}

func TestValidatePublicAssetsRejectsMissingAuthSetupAndTwoFactorPacks(t *testing.T) {
	for _, missing := range []string{"sign_up.js", "two_factor_authentication.js"} {
		publicDir := writePublicAssetManifest(t, map[string]string{
			missing: "",
		}, true)
		if err := ValidatePublicAssets(config.Config{PublicDir: publicDir, DefaultLocale: "ja"}); err == nil || !strings.Contains(err.Error(), missing) {
			t.Fatalf("ValidatePublicAssets missing %s error = %v", missing, err)
		}
	}
}

func TestValidatePublicAssetsRejectsMissingErrorAndMailerLayoutAssets(t *testing.T) {
	for _, missing := range []string{
		"error.js",
		"mailer.css",
		"media/images/mailer/logo.png",
		"media/images/mailer-new/common/header-bg-start.png",
		"media/images/mailer-new/common/header-bg-end.png",
		"media/images/mailer-new/common/logo-header.png",
		"media/images/mailer-new/common/logo-footer.png",
	} {
		publicDir := writePublicAssetManifest(t, map[string]string{
			missing: "",
		}, true)
		if err := ValidatePublicAssets(config.Config{PublicDir: publicDir, DefaultLocale: "ja"}); err == nil || !strings.Contains(err.Error(), missing) {
			t.Fatalf("ValidatePublicAssets missing %s error = %v", missing, err)
		}
	}
}

func TestRequiredPackAssetsDoNotRequireWebpackOptimizedMediaRuntimeChunks(t *testing.T) {
	manifestRaw, err := os.ReadFile("../../../public/packs/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	manifest := parsePackManifest(manifestRaw)
	required := map[string]bool{}
	for _, name := range requiredPackAssets(config.Config{DefaultLocale: "ja"}) {
		required[name] = true
	}
	for _, name := range []string{
		"status/media_gallery.js",
		"features/video.js",
		"features/audio.js",
	} {
		if manifest[name] == "" && required[name] {
			t.Fatalf("requiredPackAssets must not require absent optimized media runtime chunk %q", name)
		}
	}
}

func TestRequiredPackAssetsIncludeStartupAndPublicRuntimeChunks(t *testing.T) {
	required := map[string]bool{}
	for _, name := range requiredPackAssets(config.Config{DefaultLocale: "ja"}) {
		required[name] = true
	}
	for _, name := range []string{
		"base_polyfills.js",
		"extra_polyfills.js",
		"i18n-pluralrules-polyfill.js",
		"arrow-key-navigation.js",
		"containers/media_container.js",
	} {
		if !required[name] {
			t.Fatalf("requiredPackAssets missing startup/public runtime chunk %q", name)
		}
	}
}

func TestRequiredPackAssetsCoverManifestedFrontendAsyncComponentChunks(t *testing.T) {
	raw, err := os.ReadFile("../../../app/javascript/mastodon/features/ui/util/async-components.js")
	if err != nil {
		t.Fatal(err)
	}
	manifestRaw, err := os.ReadFile("../../../public/packs/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	manifest := parsePackManifest(manifestRaw)
	required := map[string]bool{}
	for _, name := range requiredPackAssets(config.Config{DefaultLocale: "ja"}) {
		required[name] = true
	}
	chunkPattern := regexp.MustCompile(`webpackChunkName:\s*"([^"]+)"`)
	for _, match := range chunkPattern.FindAllStringSubmatch(string(raw), -1) {
		name := match[1] + ".js"
		if manifest[name] == "" {
			continue
		}
		if !required[name] {
			t.Fatalf("requiredPackAssets missing async component chunk %q", name)
		}
	}
}

func TestValidatePublicAssetsAcceptsRepositoryBuiltUIPacks(t *testing.T) {
	if err := ValidatePublicAssets(config.Config{PublicDir: "../../../public", DefaultLocale: "ja"}); err != nil {
		t.Fatalf("ValidatePublicAssets with repository public assets: %v", err)
	}
}

func TestValidateServerRenderedLocalesAcceptsRepositoryLocales(t *testing.T) {
	if err := ValidateServerRenderedLocales(config.Config{PublicDir: "../../../public", DefaultLocale: "ja"}); err != nil {
		t.Fatalf("ValidateServerRenderedLocales with repository locales: %v", err)
	}
}

func TestValidateServerRenderedLocalesRejectsMissingLocales(t *testing.T) {
	publicDir := filepath.Join(t.TempDir(), "public")
	if err := os.MkdirAll(publicDir, 0o755); err != nil {
		t.Fatal(err)
	}
	err := ValidateServerRenderedLocales(config.Config{PublicDir: publicDir, DefaultLocale: "ja"})
	if err == nil || !strings.Contains(err.Error(), "config/locales") || !strings.Contains(err.Error(), "en") {
		t.Fatalf("ValidateServerRenderedLocales error = %v", err)
	}
}

func TestValidateServerRenderedLocalesRejectsMissingDefaultLocale(t *testing.T) {
	root := t.TempDir()
	publicDir := filepath.Join(root, "public")
	localesDir := filepath.Join(root, "config", "locales")
	if err := os.MkdirAll(localesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(publicDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeRequiredServerRenderedLocaleYAML(t, localesDir, "en")
	err := ValidateServerRenderedLocales(config.Config{PublicDir: publicDir, DefaultLocale: "ja"})
	if err == nil || !strings.Contains(err.Error(), "ja") {
		t.Fatalf("ValidateServerRenderedLocales error = %v", err)
	}
}

func TestValidateServerRenderedLocalesRejectsPartialLocaleDictionaries(t *testing.T) {
	root := t.TempDir()
	publicDir := filepath.Join(root, "public")
	localesDir := filepath.Join(root, "config", "locales")
	if err := os.MkdirAll(localesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(publicDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(localesDir, "en.yml"), []byte("en:\n  settings:\n    title: Settings\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeRequiredServerRenderedLocaleYAML(t, localesDir, "ja")
	err := ValidateServerRenderedLocales(config.Config{PublicDir: publicDir, DefaultLocale: "ja"})
	if err == nil || !strings.Contains(err.Error(), "server-rendered translation") || !strings.Contains(err.Error(), "en") {
		t.Fatalf("ValidateServerRenderedLocales error = %v", err)
	}
}

func TestValidatePublicAssetsRejectsMissingHeadIconAsset(t *testing.T) {
	publicDir := writePublicAssetManifest(t, map[string]string{
		"media/images/logo-symbol-icon.svg": "",
	}, true)
	if err := ValidatePublicAssets(config.Config{PublicDir: publicDir, DefaultLocale: "ja"}); err == nil || !strings.Contains(err.Error(), "media/images/logo-symbol-icon.svg") {
		t.Fatalf("ValidatePublicAssets error = %v", err)
	}
}

func TestValidatePublicAssetsRejectsMissingSupportedThemeCSS(t *testing.T) {
	publicDir := writePublicAssetManifest(t, map[string]string{
		"mastodon-light.css": "",
	}, true)
	if err := ValidatePublicAssets(config.Config{PublicDir: publicDir, DefaultLocale: "ja"}); err == nil || !strings.Contains(err.Error(), "mastodon-light.css") {
		t.Fatalf("ValidatePublicAssets error = %v", err)
	}
}

func TestValidatePublicAssetsRejectsMissingStaticRouteFile(t *testing.T) {
	publicDir := writePublicAssetManifest(t, map[string]string{}, true)
	if err := os.Remove(filepath.Join(publicDir, "embed.js")); err != nil {
		t.Fatal(err)
	}
	if err := ValidatePublicAssets(config.Config{PublicDir: publicDir, DefaultLocale: "ja"}); err == nil || !strings.Contains(err.Error(), "embed.js") {
		t.Fatalf("ValidatePublicAssets error = %v", err)
	}
}

func TestValidatePublicAssetsRejectsMissingExistingUIRuntimeAssets(t *testing.T) {
	for _, name := range []string{
		filepath.Join("avatars", "original", "missing.png"),
		filepath.Join("headers", "original", "missing.png"),
		filepath.Join("sounds", "boop.mp3"),
		filepath.Join("sounds", "boop.ogg"),
		filepath.Join("ocr", "lang-data", "eng.traineddata.gz"),
	} {
		t.Run(name, func(t *testing.T) {
			publicDir := writePublicAssetManifest(t, map[string]string{}, true)
			if err := os.Remove(filepath.Join(publicDir, name)); err != nil {
				t.Fatal(err)
			}
			if err := ValidatePublicAssets(config.Config{PublicDir: publicDir, DefaultLocale: "ja"}); err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("ValidatePublicAssets error = %v", err)
			}
		})
	}
}

func TestAppHTMLIncludesApplicationServerKey(t *testing.T) {
	renderer := &Renderer{cfg: config.Config{Title: "Paon", VapidPublicKey: "server-key"}}
	html, err := renderer.AppHTML("/home", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(html, `<meta name="applicationServerKey" content="server-key">`) {
		t.Fatalf("app html missing application server key: %s", html)
	}
}

func TestAppHTMLHasBalancedNoScriptFallback(t *testing.T) {
	renderer := &Renderer{cfg: config.Config{Title: "Paon"}}
	html, err := renderer.AppHTML("/home", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(html, "<noscript>") != 1 || strings.Count(html, "</noscript>") != 1 {
		t.Fatalf("noscript fallback is not balanced: %s", html)
	}
}

func writePublicAssetManifest(t *testing.T, entries map[string]string, createFiles bool) string {
	t.Helper()
	publicDir := t.TempDir()
	if createFiles {
		writeRequiredPublicFiles(t, publicDir)
	}
	for name, assetPath := range defaultRequiredPackEntries("ja") {
		if _, ok := entries[name]; !ok {
			entries[name] = assetPath
		}
	}
	manifest := "{"
	first := true
	for name, assetPath := range entries {
		if !first {
			manifest += ","
		}
		first = false
		manifest += strconv.Quote(name) + ":" + strconv.Quote(assetPath)
		if createFiles && strings.HasPrefix(assetPath, "/") {
			path := filepath.Join(publicDir, strings.TrimPrefix(assetPath, "/"))
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("// asset\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	manifest += "}"
	manifestPath := filepath.Join(publicDir, "packs", "manifest.json")
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	return publicDir
}

func defaultRequiredPackEntries(locale string) map[string]string {
	entries := map[string]string{
		"application.js":                      "/packs/js/application-hash.js",
		"admin.js":                            "/packs/js/admin-hash.js",
		"embed.js":                            "/packs/js/embed-hash.js",
		"public.js":                           "/packs/js/public-hash.js",
		"error.js":                            "/packs/js/error-hash.js",
		"mailer.js":                           "/packs/js/mailer-hash.js",
		"share.js":                            "/packs/js/share-hash.js",
		"sign_up.js":                          "/packs/js/sign_up-hash.js",
		"two_factor_authentication.js":        "/packs/js/two_factor_authentication-hash.js",
		"common.js":                           "/packs/js/common-hash.js",
		"common.css":                          "/packs/css/common-hash.css",
		"mailer.css":                          "/packs/css/mailer-hash.css",
		"base_polyfills.js":                   "/packs/js/base_polyfills-hash.chunk.js",
		"extra_polyfills.js":                  "/packs/js/extra_polyfills-hash.chunk.js",
		"i18n-pluralrules-polyfill.js":        "/packs/js/i18n-pluralrules-polyfill-hash.chunk.js",
		"arrow-key-navigation.js":             "/packs/js/arrow-key-navigation-hash.chunk.js",
		"default.css":                         "/packs/css/default-hash.css",
		"contrast.css":                        "/packs/css/contrast-hash.css",
		"mastodon-light.css":                  "/packs/css/mastodon-light-hash.css",
		"single-column-chat-dark.css":         "/packs/css/single-column-chat-dark-hash.css",
		"media/images/logo-symbol-icon.svg":   "/packs/media/images/logo-symbol-icon-hash.svg",
		"media/images/mailer/icon_cached.png": "/packs/media/images/mailer/icon_cached-hash.png",
		"media/images/mailer/icon_done.png":   "/packs/media/images/mailer/icon_done-hash.png",
		"media/images/mailer/icon_email.png":  "/packs/media/images/mailer/icon_email-hash.png",
		"media/images/mailer/icon_file_download.png":         "/packs/media/images/mailer/icon_file_download-hash.png",
		"media/images/mailer/icon_flag.png":                  "/packs/media/images/mailer/icon_flag-hash.png",
		"media/images/mailer/icon_grade.png":                 "/packs/media/images/mailer/icon_grade-hash.png",
		"media/images/mailer/icon_lock_open.png":             "/packs/media/images/mailer/icon_lock_open-hash.png",
		"media/images/mailer/icon_person_add.png":            "/packs/media/images/mailer/icon_person_add-hash.png",
		"media/images/mailer/icon_reply.png":                 "/packs/media/images/mailer/icon_reply-hash.png",
		"media/images/mailer/logo.png":                       "/packs/media/images/mailer/logo-hash.png",
		"media/images/mailer/wordmark.png":                   "/packs/media/images/mailer/wordmark-hash.png",
		"media/images/mailer-new/common/header-bg-start.png": "/packs/media/images/mailer-new/common/header-bg-start-hash.png",
		"media/images/mailer-new/common/header-bg-end.png":   "/packs/media/images/mailer-new/common/header-bg-end-hash.png",
		"media/images/mailer-new/common/logo-header.png":     "/packs/media/images/mailer-new/common/logo-header-hash.png",
		"media/images/mailer-new/common/logo-footer.png":     "/packs/media/images/mailer-new/common/logo-footer-hash.png",
		"media/icons/favicon-16x16.png":                      "/packs/media/icons/favicon-16x16-hash.png",
		"media/icons/favicon-32x32.png":                      "/packs/media/icons/favicon-32x32-hash.png",
		"media/icons/favicon-48x48.png":                      "/packs/media/icons/favicon-48x48-hash.png",
		"media/icons/android-chrome-36x36.png":               "/packs/media/icons/android-chrome-36x36-hash.png",
		"media/icons/android-chrome-48x48.png":               "/packs/media/icons/android-chrome-48x48-hash.png",
		"media/icons/android-chrome-72x72.png":               "/packs/media/icons/android-chrome-72x72-hash.png",
		"media/icons/android-chrome-96x96.png":               "/packs/media/icons/android-chrome-96x96-hash.png",
		"media/icons/android-chrome-144x144.png":             "/packs/media/icons/android-chrome-144x144-hash.png",
		"media/icons/android-chrome-192x192.png":             "/packs/media/icons/android-chrome-192x192-hash.png",
		"media/icons/android-chrome-256x256.png":             "/packs/media/icons/android-chrome-256x256-hash.png",
		"media/icons/android-chrome-384x384.png":             "/packs/media/icons/android-chrome-384x384-hash.png",
		"media/icons/android-chrome-512x512.png":             "/packs/media/icons/android-chrome-512x512-hash.png",
		"media/icons/apple-touch-icon-57x57.png":             "/packs/media/icons/apple-touch-icon-57x57-hash.png",
		"media/icons/apple-touch-icon-60x60.png":             "/packs/media/icons/apple-touch-icon-60x60-hash.png",
		"media/icons/apple-touch-icon-72x72.png":             "/packs/media/icons/apple-touch-icon-72x72-hash.png",
		"media/icons/apple-touch-icon-76x76.png":             "/packs/media/icons/apple-touch-icon-76x76-hash.png",
		"media/icons/apple-touch-icon-114x114.png":           "/packs/media/icons/apple-touch-icon-114x114-hash.png",
		"media/icons/apple-touch-icon-120x120.png":           "/packs/media/icons/apple-touch-icon-120x120-hash.png",
		"media/icons/apple-touch-icon-144x144.png":           "/packs/media/icons/apple-touch-icon-144x144-hash.png",
		"media/icons/apple-touch-icon-152x152.png":           "/packs/media/icons/apple-touch-icon-152x152-hash.png",
		"media/icons/apple-touch-icon-167x167.png":           "/packs/media/icons/apple-touch-icon-167x167-hash.png",
		"media/icons/apple-touch-icon-180x180.png":           "/packs/media/icons/apple-touch-icon-180x180-hash.png",
		"media/icons/apple-touch-icon-1024x1024.png":         "/packs/media/icons/apple-touch-icon-1024x1024-hash.png",
		"emoji_picker.js":                                    "/packs/js/emoji_picker-hash.chunk.js",
		"containers/media_container.js":                      "/packs/js/containers/media_container-hash.chunk.js",
		"features/compose.js":                                "/packs/js/features/compose-hash.chunk.js",
		"features/home_timeline.js":                          "/packs/js/features/home_timeline-hash.chunk.js",
		"features/notifications.js":                          "/packs/js/features/notifications-hash.chunk.js",
		"features/notifications/requests.js":                 "/packs/js/features/notifications/requests-hash.chunk.js",
		"features/notifications/request.js":                  "/packs/js/features/notifications/request-hash.chunk.js",
		"features/public_timeline.js":                        "/packs/js/features/public_timeline-hash.chunk.js",
		"features/community_timeline.js":                     "/packs/js/features/community_timeline-hash.chunk.js",
		"features/firehose.js":                               "/packs/js/features/firehose-hash.chunk.js",
		"features/hashtag_timeline.js":                       "/packs/js/features/hashtag_timeline-hash.chunk.js",
		"features/direct_timeline.js":                        "/packs/js/features/direct_timeline-hash.chunk.js",
		"features/list_timeline.js":                          "/packs/js/features/list_timeline-hash.chunk.js",
		"features/lists.js":                                  "/packs/js/features/lists-hash.chunk.js",
		"features/status.js":                                 "/packs/js/features/status-hash.chunk.js",
		"features/getting_started.js":                        "/packs/js/features/getting_started-hash.chunk.js",
		"features/keyboard_shortcuts.js":                     "/packs/js/features/keyboard_shortcuts-hash.chunk.js",
		"features/pinned_statuses.js":                        "/packs/js/features/pinned_statuses-hash.chunk.js",
		"features/account_timeline.js":                       "/packs/js/features/account_timeline-hash.chunk.js",
		"features/account_featured.js":                       "/packs/js/features/account_featured-hash.chunk.js",
		"features/account_gallery.js":                        "/packs/js/features/account_gallery-hash.chunk.js",
		"features/followers.js":                              "/packs/js/features/followers-hash.chunk.js",
		"features/following.js":                              "/packs/js/features/following-hash.chunk.js",
		"features/reblogs.js":                                "/packs/js/features/reblogs-hash.chunk.js",
		"features/favourites.js":                             "/packs/js/features/favourites-hash.chunk.js",
		"features/follow_requests.js":                        "/packs/js/features/follow_requests-hash.chunk.js",
		"features/favourited_statuses.js":                    "/packs/js/features/favourited_statuses-hash.chunk.js",
		"features/followed_tags.js":                          "/packs/js/features/followed_tags-hash.chunk.js",
		"features/bookmarked_statuses.js":                    "/packs/js/features/bookmarked_statuses-hash.chunk.js",
		"features/blocks.js":                                 "/packs/js/features/blocks-hash.chunk.js",
		"features/domain_blocks.js":                          "/packs/js/features/domain_blocks-hash.chunk.js",
		"features/mutes.js":                                  "/packs/js/features/mutes-hash.chunk.js",
		"modals/mute_modal.js":                               "/packs/js/modals/mute_modal-hash.chunk.js",
		"modals/block_modal.js":                              "/packs/js/modals/block_modal-hash.chunk.js",
		"modals/domain_block_modal.js":                       "/packs/js/modals/domain_block_modal-hash.chunk.js",
		"modals/report_modal.js":                             "/packs/js/modals/report_modal-hash.chunk.js",
		"modals/embed_modal.js":                              "/packs/js/modals/embed_modal-hash.chunk.js",
		"features/list_editor.js":                            "/packs/js/features/list_editor-hash.chunk.js",
		"features/list_adder.js":                             "/packs/js/features/list_adder-hash.chunk.js",
		"tesseract.js":                                       "/packs/js/tesseract-hash.chunk.js",
		"features/directory.js":                              "/packs/js/features/directory-hash.chunk.js",
		"features/onboarding.js":                             "/packs/js/features/onboarding-hash.chunk.js",
		"modals/compare_history_modal.js":                    "/packs/js/modals/compare_history_modal-hash.chunk.js",
		"features/explore.js":                                "/packs/js/features/explore-hash.chunk.js",
		"features/link_timeline.js":                          "/packs/js/features/link_timeline-hash.chunk.js",
		"modals/filter_modal.js":                             "/packs/js/modals/filter_modal-hash.chunk.js",
		"modals/interaction_modal.js":                        "/packs/js/modals/interaction_modal-hash.chunk.js",
		"modals/subscribed_languages_modal.js":               "/packs/js/modals/subscribed_languages_modal-hash.chunk.js",
		"modals/closed_registrations_modal.js":               "/packs/js/modals/closed_registrations_modal-hash.chunk.js",
		"modals/annual_report_modal.js":                      "/packs/js/modals/annual_report_modal-hash.chunk.js",
		"features/instance_stats.js":                         "/packs/js/features/instance_stats-hash.chunk.js",
		"features/about.js":                                  "/packs/js/features/about-hash.chunk.js",
		"features/privacy_policy.js":                         "/packs/js/features/privacy_policy-hash.chunk.js",
		"features/terms_of_service.js":                       "/packs/js/features/terms_of_service-hash.chunk.js",
		"remote_interaction_helper.js":                       "/packs/js/remote_interaction_helper-hash.js",
	}
	for _, loc := range append(config.RailsI18nAvailableLocales(), locale) {
		loc = strings.TrimSpace(loc)
		if loc == "" {
			continue
		}
		entries["locale/"+loc+"-json.js"] = "/packs/js/locale/" + loc + "-json-hash.chunk.js"
	}
	return entries
}

func railsThemeNames(raw string) []string {
	linePattern := regexp.MustCompile(`(?m)^([a-zA-Z0-9_-]+):\s+styles/`)
	matches := linePattern.FindAllStringSubmatch(raw, -1)
	themes := make([]string, 0, len(matches))
	for _, match := range matches {
		themes = append(themes, match[1])
	}
	return themes
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func writeRequiredPublicFiles(t *testing.T, publicDir string) {
	t.Helper()
	for _, name := range requiredPublicFiles() {
		path := filepath.Join(publicDir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("// public\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func writeRequiredServerRenderedLocaleYAML(t *testing.T, localesDir, locale string) {
	t.Helper()
	raw := locale + `:
  settings:
    title: Settings
  auth:
    login: Log in
    register: Sign up
  admin:
    dashboard:
      title: Dashboard
  simple_form:
    labels:
      defaults:
        email: Email
  doorkeeper:
    authorizations:
      buttons:
        authorize: Authorize
  devise:
    passwords:
      send_instructions: Instructions
`
	if err := os.WriteFile(filepath.Join(localesDir, locale+".yml"), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAppHTMLUsesConfiguredUserThemeCSS(t *testing.T) {
	renderer := &Renderer{
		cfg: config.Config{Title: "Paon"},
		manifest: map[string]string{
			"common.css":         "/packs/css/common-hash.css",
			"mastodon-light.css": "/packs/css/mastodon-light-hash.css",
		},
	}
	html, err := renderer.AppHTML("/home", nil, "", AppOptions{
		User: &models.User{Settings: sql.NullString{String: `{"theme":"mastodon-light"}`, Valid: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(html, `href="/packs/css/mastodon-light-hash.css"`) {
		t.Fatalf("app html missing user theme css: %s", html)
	}
	if !strings.Contains(html, `<body class="app-body theme-mastodon-light custom-scrollbars no-reduce-motion">`) {
		t.Fatalf("app html missing user theme body class: %s", html)
	}
}

func TestAppHTMLIncludesRailsBodyClassesFromUserSettings(t *testing.T) {
	renderer := &Renderer{cfg: config.Config{Title: "Paon", DefaultLocale: "ar"}}
	html, err := renderer.AppHTML("/home", nil, "", AppOptions{
		User: &models.User{Settings: sql.NullString{String: `{"theme":"single-column-chat-dark","web.use_system_font":true,"web.reduce_motion":true}`, Valid: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `<body class="app-body theme-single-column-chat-dark system-font custom-scrollbars reduce-motion rtl">`
	if !strings.Contains(html, want) {
		t.Fatalf("app html missing body classes %s: %s", want, html)
	}
}

func TestAppHTMLCanUseSystemScrollbarsLikeMastodon44(t *testing.T) {
	renderer := &Renderer{cfg: config.Config{Title: "Paon"}}
	html, err := renderer.AppHTML("/home", nil, "", AppOptions{
		User: &models.User{Settings: sql.NullString{String: `{"web.use_system_scrollbars":true}`, Valid: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(html, "custom-scrollbars") {
		t.Fatalf("system scrollbar preference retained custom scrollbar class: %s", html)
	}
}

func TestShareHTMLUsesRailsComposeStandaloneBodyClasses(t *testing.T) {
	renderer := &Renderer{cfg: config.Config{Title: "Paon"}}
	html, err := renderer.ShareHTML(nil, "", "hello")
	if err != nil {
		t.Fatal(err)
	}
	want := `<body class="modal-layout compose-standalone theme-system custom-scrollbars no-reduce-motion">`
	if !strings.Contains(html, want) {
		t.Fatalf("share html missing body classes %s: %s", want, html)
	}
}

func TestEmbedHTMLBootsStandaloneReactStatus(t *testing.T) {
	renderer := &Renderer{
		cfg: config.Config{Title: "Fallback", DefaultLocale: "ja"},
		manifest: map[string]string{
			"common.js":          "/packs/common-fingerprint.js",
			"common.css":         "/packs/common-fingerprint.css",
			"mastodon-light.css": "/packs/light-fingerprint.css",
			"embed.js":           "/packs/embed-fingerprint.js",
			"locale/ja-json.js":  "/packs/ja-fingerprint.js",
		},
	}
	html, err := renderer.EmbedHTML("123", "Paon Social")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`<html lang="ja">`,
		`<meta name="robots" content="noindex">`,
		`<title>Paon Social</title>`,
		`class="embed theme-mastodon-light no-reduce-motion"`,
		`id="initial-state"`,
		`id="mastodon-status"`,
		`data-props="{&#34;id&#34;:&#34;123&#34;,&#34;locale&#34;:&#34;ja&#34;}"`,
		`src="/packs/common-fingerprint.js"`,
		`src="/packs/embed-fingerprint.js"`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("embed html missing %q: %s", want, html)
		}
	}
}

func TestAppHTMLUsesRailsSiteTitleInHeadAndInitialState(t *testing.T) {
	renderer := &Renderer{cfg: config.Config{Title: "Fallback"}}
	html, err := renderer.AppHTML("/home", nil, "", AppOptions{SiteTitle: "Configured title", SiteTitleSet: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`<title>Configured title</title>`,
		`"title":"Configured title"`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("app html missing site title %s: %s", want, html)
		}
	}
}

func TestAppHTMLIncludesRailsCSRFMeta(t *testing.T) {
	renderer := &Renderer{cfg: config.Config{Title: "Paon"}}
	html, err := renderer.AppHTML("/home", &models.Account{ID: 42, Username: "alice"}, "access-token")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(html, `<meta name="csrf-param" content="authenticity_token">`) {
		t.Fatalf("app html missing csrf param: %s", html)
	}
	if strings.Contains(html, `<meta name="csrf-token" content="access-token">`) {
		t.Fatalf("csrf token exposed the access token: %s", html)
	}
	if !strings.Contains(html, `<meta name="csrf-token" content="`) {
		t.Fatalf("app html missing csrf token: %s", html)
	}
}

func TestAppHTMLPropsAreEscapedOnce(t *testing.T) {
	renderer := &Renderer{cfg: config.Config{Title: "Paon"}}
	html, err := renderer.AppHTML("/home", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(html, "&amp;#34;") {
		t.Fatalf("data-props was double escaped: %s", html)
	}
	if !strings.Contains(html, `data-props="{&#34;locale&#34;:&#34;en&#34;}"`) {
		t.Fatalf("data-props missing JSON payload: %s", html)
	}
}

func TestAppHTMLUsesConfiguredLocale(t *testing.T) {
	renderer := &Renderer{cfg: config.Config{Title: "Paon", DefaultLocale: "en"}}
	html, err := renderer.AppHTML("/home", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`<html lang="en" class="app-ready">`,
		`data-props="{&#34;locale&#34;:&#34;en&#34;}"`,
		`"locale":"en"`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("app html missing %s: %s", want, html)
		}
	}
}

func TestAppHTMLCanMarkRegistrationsOpen(t *testing.T) {
	renderer := &Renderer{cfg: config.Config{Title: "Paon"}}
	html, err := renderer.AppHTML("/home", nil, "", AppOptions{RegistrationsOpen: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(html, `"registrations_open":true`) {
		t.Fatalf("initial state did not include open registrations: %s", html)
	}
}

func TestAppHTMLIncludesWebSettings(t *testing.T) {
	renderer := &Renderer{cfg: config.Config{Title: "Paon"}}
	html, err := renderer.AppHTML("/home", nil, "", AppOptions{
		Settings: map[string]any{"boost_modal": true, "skin": "default"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(html, `"settings":{"boost_modal":true,"skin":"default"}`) {
		t.Fatalf("initial state did not include web settings: %s", html)
	}
}

func TestAppHTMLIncludesServerSettings(t *testing.T) {
	renderer := &Renderer{cfg: config.Config{Title: "Paon"}}
	settings := serializer.DefaultInitialStateServerSettings()
	settings.TimelinePreview = false
	settings.TrendsAsLandingPage = false
	settings.StatusPageURL = "https://status.example.test"

	html, err := renderer.AppHTML("/home", nil, "", AppOptions{ServerSettings: &settings})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"timeline_preview":false`, `"trends_as_landing_page":false`, `"status_page_url":"https://status.example.test"`} {
		if !strings.Contains(html, want) {
			t.Fatalf("initial state missing %s: %s", want, html)
		}
	}
}

func TestAppHTMLIncludesAnonymousMetaDefaults(t *testing.T) {
	renderer := &Renderer{cfg: config.Config{Title: "Paon"}}
	html, err := renderer.AppHTML("/public", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"auto_play_gif":null`, `"display_media":null`, `"reduce_motion":null`, `"use_blurhash":null`, `"crop_images":null`} {
		if !strings.Contains(html, want) {
			t.Fatalf("initial state missing %s: %s", want, html)
		}
	}
	for _, absent := range []string{`"advanced_layout":`, `"boost_modal":`, `"delete_modal":`, `"show_trends":`, `"unfollow_modal":`, `"use_pending_items":`} {
		if strings.Contains(html, absent) {
			t.Fatalf("anonymous initial state unexpectedly includes %s: %s", absent, html)
		}
	}
}

func TestAppHTMLIncludesEscapedHeadMetadataAndLinks(t *testing.T) {
	renderer := &Renderer{cfg: config.Config{Title: "Paon"}}
	html, err := renderer.AppHTML("/@alice/123", nil, "", AppOptions{
		DocumentTitle: `Alice <Admin>: "hello"`,
		HeadMeta: []HeadMeta{
			{Property: "og:title", Content: `Alice <Admin> & "friends"`},
			{Name: "description", Content: `A <post> & "quote"`},
		},
		HeadLinks: []HeadLink{
			{Rel: "alternate", Type: "application/activity+json", Href: `https://example.test/statuses/123?x=1&y=2`},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`<meta property="og:title" content="Alice &lt;Admin&gt; &amp; &#34;friends&#34;">`,
		`<meta name="description" content="A &lt;post&gt; &amp; &#34;quote&#34;">`,
		`<link rel="alternate" type="application/activity&#43;json" href="https://example.test/statuses/123?x=1&amp;y=2">`,
		`<title>Alice &lt;Admin&gt;: &#34;hello&#34;</title>`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("HTML missing %q: %s", want, html)
		}
	}
}

func TestAppHTMLIncludesComposeDefaults(t *testing.T) {
	renderer := &Renderer{cfg: config.Config{Title: "Paon"}}
	html, err := renderer.AppHTML("/home", &models.Account{ID: 42, Username: "alice"}, "token", AppOptions{
		User: &models.User{Settings: sql.NullString{String: `{"default_privacy":"private","default_sensitive":true,"default_language":"en"}`, Valid: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"me":"42"`, `"default_privacy":"private"`, `"default_sensitive":true`, `"default_language":"en"`} {
		if !strings.Contains(html, want) {
			t.Fatalf("initial state missing %s: %s", want, html)
		}
	}
}

func TestAppHTMLIncludesPushSubscription(t *testing.T) {
	renderer := &Renderer{cfg: config.Config{Title: "Paon", VapidPublicKey: "server-key"}}
	html, err := renderer.AppHTML("/home", nil, "", AppOptions{
		PushSubscription: &models.WebPushSubscription{
			ID:       12,
			Endpoint: "https://push.example/1",
			Data:     models.JSONValue(`{"policy":"all","alerts":{"mention":true}}`),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"push_subscription":{"id":"12"`, `"endpoint":"https://push.example/1"`, `"server_key":"server-key"`, `"alerts":{"mention":true}`} {
		if !strings.Contains(html, want) {
			t.Fatalf("initial state missing %s: %s", want, html)
		}
	}
}

func TestAppHTMLIncludesPublishComposeQueryDefaults(t *testing.T) {
	renderer := &Renderer{cfg: config.Config{Title: "Paon"}}
	html, err := renderer.AppHTML("/publish", nil, "", AppOptions{
		ComposeText:       "Hello from shortcut",
		ComposeVisibility: "direct",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"text":"Hello from shortcut"`, `"default_privacy":"direct"`} {
		if !strings.Contains(html, want) {
			t.Fatalf("initial state missing %s: %s", want, html)
		}
	}
}

func TestAppHTMLIncludesAuthenticatedMetaSettings(t *testing.T) {
	renderer := &Renderer{cfg: config.Config{Title: "Paon"}}
	html, err := renderer.AppHTML("/home", &models.Account{ID: 42, Username: "alice"}, "token", AppOptions{
		User: &models.User{Settings: sql.NullString{String: `{"web.disable_hover_cards":true,"web.display_media":"hide_all","web.use_blurhash":false}`, Valid: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"disable_hover_cards":true`, `"display_media":"hide_all"`, `"use_blurhash":false`} {
		if !strings.Contains(html, want) {
			t.Fatalf("initial state missing %s: %s", want, html)
		}
	}
}

func TestAppHTMLIncludesDisabledAndMovedAccountMeta(t *testing.T) {
	renderer := &Renderer{cfg: config.Config{Title: "Paon"}}
	html, err := renderer.AppHTML("/home", nil, "", AppOptions{
		DisabledAccount: &models.Account{ID: 42, Username: "alice", MovedToAccountID: sql.NullInt64{Int64: 84, Valid: true}},
		MovedToAccount:  &models.Account{ID: 84, Username: "alice_new"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(html, `"me":"42"`) {
		t.Fatalf("disabled account initial state should not include me: %s", html)
	}
	for _, want := range []string{`"disabled_account_id":"42"`, `"moved_to_account_id":"84"`, `"84":{"id":"84"`, `"acct":"alice_new"`} {
		if !strings.Contains(html, want) {
			t.Fatalf("initial state missing %s: %s", want, html)
		}
	}
}

func TestAppHTMLIncludesCriticalUpdatesPending(t *testing.T) {
	renderer := &Renderer{cfg: config.Config{Title: "Paon"}}
	pending := true
	html, err := renderer.AppHTML("/home", nil, "", AppOptions{CriticalUpdatesPending: &pending})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(html, `"critical_updates_pending":true`) {
		t.Fatalf("initial state missing critical_updates_pending: %s", html)
	}
}

func TestShareHTMLIncludesApplicationServerKey(t *testing.T) {
	renderer := &Renderer{cfg: config.Config{Title: "Paon", VapidPublicKey: "server-key"}}
	html, err := renderer.ShareHTML(nil, "", "hello")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(html, `<meta name="applicationServerKey" content="server-key">`) {
		t.Fatalf("share html missing application server key: %s", html)
	}
}

func TestShareHTMLIncludesRailsCSRFMeta(t *testing.T) {
	renderer := &Renderer{cfg: config.Config{Title: "Paon"}}
	html, err := renderer.ShareHTML(&models.Account{ID: 42, Username: "alice"}, "access-token", "hello")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`<meta name="csrf-param" content="authenticity_token">`, `<meta name="csrf-token" content="`} {
		if !strings.Contains(html, want) {
			t.Fatalf("share html missing %s: %s", want, html)
		}
	}
}

func TestShareHTMLIncludesComposeVisibilityOverride(t *testing.T) {
	renderer := &Renderer{cfg: config.Config{Title: "Paon"}}
	html, err := renderer.ShareHTML(&models.Account{ID: 42, Username: "alice"}, "token", "hello", AppOptions{
		User:              &models.User{Settings: sql.NullString{String: `{"default_privacy":"public"}`, Valid: true}},
		ComposeVisibility: "private",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(html, `"default_privacy":"private"`) {
		t.Fatalf("share html missing visibility override: %s", html)
	}
}
