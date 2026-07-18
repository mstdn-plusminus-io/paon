package api

import (
	"testing"
)

func TestLocalesDirForMatchesDropInRuntimePublicDir(t *testing.T) {
	if got, want := localesDirFor("/opt/mastodon/public"), "/opt/mastodon/config/locales"; got != want {
		t.Fatalf("localesDirFor(/opt/mastodon/public) = %q, want %q", got, want)
	}
}
