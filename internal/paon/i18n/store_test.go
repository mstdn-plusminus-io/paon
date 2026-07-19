package i18n

import (
	"os"
	"path/filepath"
	"testing"
)

func repoLocalesDir(t *testing.T) string {
	t.Helper()
	// internal/paon/i18n → repo root is three levels up.
	return filepath.Join("..", "..", "..", "config", "locales")
}

func TestStoreLoadsAndFlattensRailsYml(t *testing.T) {
	store := NewStore(repoLocalesDir(t))
	en := store.Dict("en")
	if en["auth.login"] != "Log in" {
		t.Fatalf("auth.login = %q", en["auth.login"])
	}
	if en["simple_form.labels.defaults.email"] != "E-mail address" {
		t.Fatalf("simple_form.labels.defaults.email = %q", en["simple_form.labels.defaults.email"])
	}
	if en["simple_form.labels.defaults.password"] != "Password" {
		t.Fatalf("simple_form.labels.defaults.password = %q", en["simple_form.labels.defaults.password"])
	}
	if en["doorkeeper.authorized_applications.index.title"] != "Your authorized applications" {
		t.Fatalf("doorkeeper.authorized_applications.index.title = %q", en["doorkeeper.authorized_applications.index.title"])
	}
}

func TestTranslateFallbackAndInterpolation(t *testing.T) {
	store := NewStore(repoLocalesDir(t))
	if got := store.T("en", "auth.login", nil); got != "Log in" {
		t.Fatalf("T(en, auth.login) = %q", got)
	}
	// Unknown locale falls back to en.
	if got := store.T("xx", "auth.login", nil); got != "Log in" {
		t.Fatalf("T(xx, auth.login) en-fallback = %q", got)
	}
	// Truly missing key returns the key itself.
	if got := store.T("en", "auth.does.not.exist", nil); got != "auth.does.not.exist" {
		t.Fatalf("missing key = %q", got)
	}
}

func TestStoreAppliesPaonBrandingBeforeInterpolation(t *testing.T) {
	dir := t.TempDir()
	yml := "en:\n  branded: 'Mastodon mastodon マストドン %{client_name}'\n"
	if err := os.WriteFile(filepath.Join(dir, "en.yml"), []byte(yml), 0o600); err != nil {
		t.Fatal(err)
	}
	store := NewStore(dir)
	got := store.T("en", "branded", map[string]string{"client_name": "Mastodon Client"})
	want := "Paon paon ぱおん Mastodon Client"
	if got != want {
		t.Fatalf("branded translation = %q, want %q", got, want)
	}
}

func TestNormalizeLocale(t *testing.T) {
	cases := map[string]string{
		"en-US":       "en-us",
		"ja,en;q=0.9": "ja",
		"  PT_BR ":    "pt-br",
		"":            "",
		"de":          "de",
	}
	for in, want := range cases {
		if got := NormalizeLocale(in); got != want {
			t.Fatalf("NormalizeLocale(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResolvePrecedence(t *testing.T) {
	if got := Resolve("ja", "de", "en"); got != "ja" {
		t.Fatalf("userLocale precedence = %q", got)
	}
	if got := Resolve("", "de", "en"); got != "de" {
		t.Fatalf("accept-language precedence = %q", got)
	}
	if got := Resolve("", "", "fr"); got != "fr" {
		t.Fatalf("default precedence = %q", got)
	}
	if got := Resolve("", "", ""); got != "en" {
		t.Fatalf("final fallback = %q", got)
	}
}

func TestResolveOnlyUsesRailsAvailableLocales(t *testing.T) {
	if got := Resolve("tai", "uz", "en"); got != "en" {
		t.Fatalf("non-available locale should fall back to default = %q", got)
	}
	if got := Resolve("pt-br", "", "en"); got != "pt-BR" {
		t.Fatalf("regional locale should be canonicalized to Rails available spelling = %q", got)
	}
	if got := Resolve("en-US", "", "ja"); got != "en" {
		t.Fatalf("compatible regional locale should fall back to base language = %q", got)
	}
	if got := Resolve("", "tai;q=1.0, de-DE;q=0.9, ja;q=0.1", "en"); got != "de" {
		t.Fatalf("Accept-Language should skip unavailable locales and use q order = %q", got)
	}
	if got := Resolve("", "ja;q=0.3, de;q=0.9", "en"); got != "de" {
		t.Fatalf("Accept-Language q priority = %q", got)
	}
}
