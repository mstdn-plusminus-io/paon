package api

import (
	"testing"
)

func TestSharePageUsesPrivateNoStoreCacheHeaders(t *testing.T) {
	if !railsPrivateNoStoreWebPath("/share") {
		t.Fatal("/share must use private no-store cache headers because its authenticated HTML embeds compose initial state")
	}
}

func TestRailsPrivateNoStoreDefaultsCoverDynamicControllersButNotStaticAssets(t *testing.T) {
	for _, path := range []string{
		"/api/v1/instance",
		"/users/alice",
		"/@:alice",
		"/tags/go",
		"/about",
		"/oauth/authorize",
	} {
		if !railsPrivateNoStoreWebPath(path) {
			t.Fatalf("%s must receive the dynamic private no-store default before explicit public overrides", path)
		}
	}
	for _, path := range []string{"/packs/app.js", "/assets/app.css", "/avatars/original/avatar.png", "/system/accounts/avatar.png", "/oops.png", "/sw.js"} {
		if railsPrivateNoStoreWebPath(path) {
			t.Fatalf("%s static asset must not receive the dynamic cache default", path)
		}
	}
}
