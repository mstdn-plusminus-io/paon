package config

import "testing"

func TestMastodonVersionFromEnvDefaultsToFinal46Release(t *testing.T) {
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

func TestDefaultReleaseIdentity(t *testing.T) {
	if DefaultVersion != "7.4.0" {
		t.Fatalf("DefaultVersion = %q, want 7.4.0", DefaultVersion)
	}
	if DefaultMastodonVersion != "4.6.6" {
		t.Fatalf("DefaultMastodonVersion = %q, want 4.6.6", DefaultMastodonVersion)
	}
	if DefaultMastodonAPIVersion != 11 {
		t.Fatalf("DefaultMastodonAPIVersion = %d, want 11", DefaultMastodonAPIVersion)
	}
}
