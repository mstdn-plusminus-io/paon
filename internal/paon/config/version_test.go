package config

import "testing"

func TestMastodonVersionFromEnvDefaultsToFinal42Release(t *testing.T) {
	unsetEnvForTest(t, "MASTODON_VERSION")
	unsetEnvForTest(t, "MASTODON_VERSION_PRERELEASE")
	unsetEnvForTest(t, "MASTODON_VERSION_METADATA")

	if got := MastodonVersionFromEnv(); got != DefaultMastodonVersion {
		t.Fatalf("MastodonVersionFromEnv() = %q, want %q", got, DefaultMastodonVersion)
	}

	t.Setenv("MASTODON_VERSION", "4.2.27")
	if got := MastodonVersionFromEnv(); got != "4.2.27" {
		t.Fatalf("explicit MASTODON_VERSION = %q, want 4.2.27", got)
	}
}
