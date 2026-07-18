package api

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mstdn-plusminus-io/paon/internal/paon/i18n"
)

// TestMain initializes the package-global i18n store so server-rendered HTML helpers translate
// real Rails keys (e.g. "auth.login" -> "Log in") in tests that construct a bare &Server{}
// without going through NewServer. The locales directory is resolved relative to this package
// (internal/paon/api -> repo root config/locales).
func TestMain(m *testing.M) {
	localesDir := filepath.Join("..", "..", "..", "config", "locales")
	store := i18n.NewStore(localesDir)
	store.Preload("en")
	setWebI18n(store)
	setWebDefaultLocale("en")
	os.Exit(m.Run())
}
