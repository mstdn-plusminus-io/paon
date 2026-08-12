package api

import (
	"path/filepath"
	"sync/atomic"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/i18n"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

// webI18n holds the request-locale translation store for package-level HTML helpers (which do
// not have a *Server receiver). It is set once at startup (see NewServer) and is safe for
// concurrent reads. Asset/locale data is identical across Server instances (single manifest and
// locale directory), so a package global is safe.
var webI18n atomic.Value // *i18n.Store

// webDefaultLocale is the instance default locale (cfg.Locale), used for pages that do not yet
// resolve a per-request locale (settings/admin).
var webDefaultLocale atomic.Value // string

func setWebI18n(store *i18n.Store) {
	if store == nil {
		return
	}
	// Don't clobber an already-loaded store with an empty one. This happens when a test
	// constructs a Server with a config whose PublicDir doesn't resolve to config/locales:
	// the new store has no translations, so keep the working one (e.g. the test-main store).
	if !store.HasTranslations("en") {
		if existing := currentWebI18n(); existing != nil && existing.HasTranslations("en") {
			return
		}
	}
	webI18n.Store(store)
}

func setWebDefaultLocale(locale string) {
	if locale == "" {
		locale = "en"
	}
	webDefaultLocale.Store(locale)
}

func webDefaultLocaleValue() string {
	if v, ok := webDefaultLocale.Load().(string); ok && v != "" {
		return v
	}
	return "en"
}

func resetWebI18nForTest() {
	webI18n = atomic.Value{}
}

func currentWebI18n() *i18n.Store {
	if v, ok := webI18n.Load().(*i18n.Store); ok {
		return v
	}
	return nil
}

// webT translates key into locale, interpolating %{name} placeholders, falling back to "en"
// then the raw key. Returns the key unchanged if no store is configured.
func webT(locale, key string, vars ...map[string]string) string {
	var v map[string]string
	if len(vars) > 0 {
		v = vars[0]
	}
	if store := currentWebI18n(); store != nil {
		return store.T(locale, key, v)
	}
	return key
}

// webLocale resolves the request locale: params[:lang], else the logged-in user's `locale`
// setting, else Accept-Language, else the configured default (cfg.Locale). Since Mastodon 4.4,
// setting DEFAULT_LOCALE alone no longer suppresses browser language negotiation; operators can
// opt back into that legacy behavior with FORCE_DEFAULT_LOCALE=true.
func (s *Server) webLocale(c *echo.Context, user *models.User) string {
	if c != nil {
		if locale := i18n.RailsAvailableLocale(c.QueryParam("lang")); locale != "" {
			return locale
		}
	}
	userLocale := ""
	if user != nil && user.ID != 0 {
		userLocale = userSettingString(*user, "locale", "")
		if userLocale == "" && user.Locale.Valid {
			userLocale = user.Locale.String
		}
	}
	accept := ""
	if !s.cfg.ForceDefaultLocale && c != nil && c.Request() != nil {
		accept = c.Request().Header.Get("Accept-Language")
	}
	return i18n.Resolve(userLocale, accept, s.cfg.Locale())
}

// localesDirFor returns the Rails config/locales directory derived from PublicDir: PublicDir's
// parent is the repo/app root that contains `config/`.
func localesDirFor(publicDir string) string {
	return filepath.Join(filepath.Dir(publicDir), "config", "locales")
}
