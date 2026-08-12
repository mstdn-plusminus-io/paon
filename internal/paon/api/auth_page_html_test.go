package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/web"
)

func TestBuildAppHeadIncludesMastodonAssets(t *testing.T) {
	appAssets.Store(appAssetPaths{}) // force hash-agnostic fallback paths regardless of test order
	head := buildAppHead("Sign in")
	// Use hash-agnostic substrings: packAssetPath may resolve to a hashed manifest path
	// (/packs/css/common-<hash>.css) when a prior test constructed a Server, or to the
	// un-hashed fallback otherwise.
	for _, want := range []string{
		`rel="stylesheet"`,
		`/packs/`,
		`common`,
		`default`,
		`<script`,
		`theme-color`,
		`apple-mobile-web-app-capable`,
		`plusminus-disable-remote-media-cache`,
		`rel="manifest" href="/manifest.json"`,
		`href="/inert.css"`,
		`href="/custom.css"`,
		`<title>Sign in</title>`,
		`mask-icon`,
		`apple-touch-icon-57x57.png`,
		`apple-touch-icon-1024x1024.png`,
	} {
		if !strings.Contains(head, want) {
			t.Fatalf("buildAppHead missing %q\nhead=%s", want, head)
		}
	}
}

func TestAuthShellHTMLMatchesRailsAuthLayout(t *testing.T) {
	appAssets.Store(appAssetPaths{}) // force hash-agnostic fallback paths regardless of test order
	got := authShellHTML("Sign in", "welcome", "boom", "<p>body</p>")
	for _, want := range []string{
		`class="container-alt"`,
		`class="logo-container"`,
		`class="form-container"`,
		`class="flash-message notice"`,
		`flash-message alert`,
		`public`,
		`<p>body</p>`,
		`<title>Sign in</title>`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("authShellHTML missing %q", want)
		}
	}
}

func TestSetAppAssetsUsesManifestResolvedPathsForServerRenderedHTML(t *testing.T) {
	publicDir := t.TempDir()
	packsDir := filepath.Join(publicDir, "packs")
	if err := os.MkdirAll(packsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{
		"common.css": "/packs/css/common-hash.css",
		"default.css": "/packs/css/default-hash.css",
		"contrast.css": "/packs/css/contrast-hash.css",
		"mastodon-light.css": "/packs/css/mastodon-light-hash.css",
		"single-column-chat-dark.css": "/packs/css/single-column-chat-dark-hash.css",
		"common.js": "/packs/js/common-hash.js",
		"locale/en-json.js": "/packs/js/locale/en-json-hash.chunk.js",
		"public.js": "/packs/js/public-hash.js",
		"admin.js": "/packs/js/admin-hash.js",
		"media/icons/favicon-16x16.png": "/packs/media/icons/favicon-16x16-hash.png",
		"media/icons/favicon-32x32.png": "/packs/media/icons/favicon-32x32-hash.png",
		"media/icons/favicon-48x48.png": "/packs/media/icons/favicon-48x48-hash.png",
		"media/icons/apple-touch-icon-57x57.png": "/packs/media/icons/apple-touch-icon-57x57-hash.png",
		"media/icons/apple-touch-icon-60x60.png": "/packs/media/icons/apple-touch-icon-60x60-hash.png",
		"media/icons/apple-touch-icon-72x72.png": "/packs/media/icons/apple-touch-icon-72x72-hash.png",
		"media/icons/apple-touch-icon-76x76.png": "/packs/media/icons/apple-touch-icon-76x76-hash.png",
		"media/icons/apple-touch-icon-114x114.png": "/packs/media/icons/apple-touch-icon-114x114-hash.png",
		"media/icons/apple-touch-icon-120x120.png": "/packs/media/icons/apple-touch-icon-120x120-hash.png",
		"media/icons/apple-touch-icon-144x144.png": "/packs/media/icons/apple-touch-icon-144x144-hash.png",
		"media/icons/apple-touch-icon-152x152.png": "/packs/media/icons/apple-touch-icon-152x152-hash.png",
		"media/icons/apple-touch-icon-167x167.png": "/packs/media/icons/apple-touch-icon-167x167-hash.png",
		"media/icons/apple-touch-icon-180x180.png": "/packs/media/icons/apple-touch-icon-180x180-hash.png",
		"media/icons/apple-touch-icon-1024x1024.png": "/packs/media/icons/apple-touch-icon-1024x1024-hash.png",
		"media/images/logo-symbol-icon.svg": "/packs/media/images/logo-symbol-icon-hash.svg",
		"media/images/logo-symbol-wordmark.svg": "/packs/media/images/logo-symbol-wordmark-hash.svg"
	}`
	if err := os.WriteFile(filepath.Join(packsDir, "manifest.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	renderer, err := web.NewRenderer(config.Config{PublicDir: publicDir, DisableRemoteMediaCacheSet: true})
	if err != nil {
		t.Fatal(err)
	}
	setAppAssets(renderer)
	t.Cleanup(func() { appAssets.Store(appAssetPaths{}) })

	head := buildAppHead("Settings", "mastodon-light")
	wants := []string{
		`href="/packs/css/common-hash.css"`,
		`href="/packs/css/mastodon-light-hash.css"`,
		`src="/packs/js/common-hash.js"`,
		`src="/packs/js/locale/en-json-hash.chunk.js"`,
		`href="/favicon-16x16.png"`,
		`href="/favicon-32x32.png"`,
		`href="/favicon-48x48.png"`,
		`href="/packs/media/images/logo-symbol-icon-hash.svg"`,
		`href="/manifest.json"`,
		`href="/inert.css"`,
		`href="/custom.css"`,
		`<meta name="plusminus-disable-remote-media-cache" content="true">`,
	}
	for _, size := range appAppleTouchIconSizes {
		wants = append(wants, `href="/apple-touch-icon-`+size+`x`+size+`.png"`)
	}
	for _, want := range wants {
		if !strings.Contains(head, want) {
			t.Fatalf("buildAppHead missing manifest path %q\nhead=%s", want, head)
		}
	}

	shell := authShellHTML("Sign in", "", "", "<p>body</p>")
	for _, want := range []string{
		`src="/packs/js/public-hash.js"`,
		`src="/packs/media/images/logo-symbol-wordmark-hash.svg"`,
		`class="logo logo--wordmark"`,
		`class="app-body lighter theme-system no-reduce-motion"`,
	} {
		if !strings.Contains(shell, want) {
			t.Fatalf("authShellHTML missing manifest path %q\nhtml=%s", want, shell)
		}
	}
	if got := currentAppAssets().adminJS; got != "/packs/js/admin-hash.js" {
		t.Fatalf("admin asset = %q, want manifest resolved admin.js", got)
	}
}

func TestServerPackAssetPathFallbackMatchesBuiltUIPackLayout(t *testing.T) {
	s := &Server{}
	for name, want := range map[string]string{
		"public.js":                    "/packs/js/public.js",
		"admin.js":                     "/packs/js/admin.js",
		"error.js":                     "/packs/js/error.js",
		"mailer.js":                    "/packs/js/mailer.js",
		"sign_up.js":                   "/packs/js/sign_up.js",
		"two_factor_authentication.js": "/packs/js/two_factor_authentication.js",
		"mailer.css":                   "/packs/css/mailer.css",
		"media/images/mailer/logo.png": "/packs/media/images/mailer/logo.png",
	} {
		if got := s.packAssetPath(name); got != want {
			t.Fatalf("packAssetPath(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestAuthSharedLinksHTMLMatchesRailsSharedLinksByController(t *testing.T) {
	tests := []struct {
		name       string
		controller string
		want       []string
		forbid     []string
	}{
		{
			name:       "sessions",
			controller: "sessions",
			want:       []string{`href="/auth/sign_up"`, `href="/auth/password/new"`, `href="/auth/confirmation/new"`, `>Sign up<`, `>Forgot your password?<`, `>Didn&#39;t receive a confirmation link?<`},
			forbid:     []string{`href="/auth/sign_in"`},
		},
		{
			name:       "passwords",
			controller: "passwords",
			want:       []string{`href="/auth/sign_in"`, `href="/auth/sign_up"`, `href="/auth/confirmation/new"`, `>Log in<`, `>Sign up<`, `>Didn&#39;t receive a confirmation link?<`},
			forbid:     []string{`href="/auth/password/new"`},
		},
		{
			name:       "confirmations",
			controller: "confirmations",
			want:       []string{`href="/auth/sign_in"`, `href="/auth/sign_up"`, `href="/auth/password/new"`, `>Log in<`, `>Sign up<`, `>Forgot your password?<`},
			forbid:     []string{`href="/auth/confirmation/new"`},
		},
		{
			name:       "registrations",
			controller: "registrations",
			want:       []string{`href="/auth/sign_in"`, `href="/auth/confirmation/new"`, `>Log in<`, `>Didn&#39;t receive a confirmation link?<`},
			forbid:     []string{`href="/auth/sign_up"`, `href="/auth/password/new"`},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := authSharedLinksHTML(tt.controller, "/auth/sign_up", "en")
			if !strings.HasPrefix(got, `<ul class="no-list">`) {
				t.Fatalf("shared links should render the Rails no-list wrapper: %s", got)
			}
			for _, want := range tt.want {
				if !strings.Contains(got, want) {
					t.Fatalf("shared links for %s missing %q: %s", tt.controller, want, got)
				}
			}
			for _, forbid := range tt.forbid {
				if strings.Contains(got, forbid) {
					t.Fatalf("shared links for %s unexpectedly contained %q: %s", tt.controller, forbid, got)
				}
			}
		})
	}
}

func TestAuthSharedLinksPreserveAvailableSignUpPathBoundary(t *testing.T) {
	blank := authSharedLinksHTML("sessions", "", "en")
	if !strings.Contains(blank, `href=""`) {
		t.Fatalf("empty signUpPath should be preserved for Rails ENV.fetch parity: %s", blank)
	}

	external := authSharedLinksHTML("sessions", `https://sso.example.test/sign-up?next="auth"`, "en")
	if !strings.Contains(external, `href="https://sso.example.test/sign-up?next=&#34;auth&#34;"`) {
		t.Fatalf("external signup path should be escaped and preserved: %s", external)
	}

	closed := (&Server{}).availableSignUpPath()
	if closed != "https://joinmastodon.org/#getting-started" {
		t.Fatalf("closed registrations signup path = %q", closed)
	}

	omniauth := (&Server{cfg: config.Config{OmniAuthOnly: true}}).availableSignUpPath()
	if omniauth != "https://joinmastodon.org/#getting-started" {
		t.Fatalf("omniauth-only signup path = %q", omniauth)
	}
}
