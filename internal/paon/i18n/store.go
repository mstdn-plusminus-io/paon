// Package i18n loads Rails' config/locales/*.yml dictionaries and translates server-rendered
// HTML text into the request locale, mirroring Rails I18n.t. Dictionaries are loaded lazily
// per locale and cached.
package i18n

import (
	"os"
	"path/filepath"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// Store caches flattened translation dictionaries per locale. It loads
// <dir>/{,simple_form.,devise.,activerecord.,doorkeeper.}<locale>.yml on first use of a locale.
type Store struct {
	dir   string
	cache sync.Map // locale -> map[string]string (flattened, read-only after load)
}

// NewStore returns a Store that reads from dir (Rails' config/locales).
func NewStore(dir string) *Store {
	return &Store{dir: dir}
}

// Dict returns the flattened key->text dictionary for locale, loading it on first use.
// Missing files/parse errors yield an empty dictionary (callers fall back to "en").
func (s *Store) Dict(locale string) map[string]string {
	if locale == "" {
		return nil
	}
	if v, ok := s.cache.Load(locale); ok {
		return v.(map[string]string)
	}
	flat := loadLocale(s.dir, locale)
	s.cache.Store(locale, flat)
	return flat
}

// Preload loads the given locales eagerly (used at startup for the default + common locales).
func (s *Store) Preload(locales ...string) {
	for _, l := range locales {
		s.Dict(l)
	}
}

// HasTranslations reports whether the given locale loaded any keys. Used to detect a store
// pointed at a missing/empty locale directory (e.g. a test config without config/locales).
func (s *Store) HasTranslations(locale string) bool {
	return len(s.Dict(locale)) > 0
}

func loadLocale(dir, locale string) map[string]string {
	flat := map[string]string{}
	for _, prefix := range []string{"", "simple_form.", "devise.", "activerecord.", "doorkeeper."} {
		path := filepath.Join(dir, prefix+locale+".yml")
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var root map[string]interface{}
		if err := yaml.Unmarshal(data, &root); err != nil {
			continue
		}
		// Each yml is rooted at its locale key (e.g. `en:`); descend into that node. If the
		// expected key is missing, fall back to the first top-level value (some files differ).
		node, ok := root[locale]
		if !ok {
			for _, v := range root {
				node = v
				break
			}
		}
		flatten(node, "", flat)
	}
	return flat
}

// flatten recursively walks a yaml-decoded node into dotted keys. Maps recurse; strings become
// leaf values; other scalar/array types are skipped (server HTML only uses string translations).
func flatten(node interface{}, prefix string, out map[string]string) {
	switch v := node.(type) {
	case map[string]interface{}:
		for key, val := range v {
			k := key
			if prefix != "" {
				k = prefix + "." + key
			}
			flatten(val, k, out)
		}
	case string:
		if prefix != "" {
			out[prefix] = paonTranslationBranding(v)
		}
	}
}

// paonTranslationBranding applies the legacy Paon substitutions while loading dictionaries.
// Interpolation values remain untouched.
func paonTranslationBranding(value string) string {
	value = strings.ReplaceAll(value, "Mastodon", "Paon")
	value = strings.ReplaceAll(value, "mastodon", "paon")
	return strings.ReplaceAll(value, "マストドン", "ぱおん")
}
