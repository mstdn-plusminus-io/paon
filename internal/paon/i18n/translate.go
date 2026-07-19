package i18n

import "strings"

// T translates key into locale, interpolating %{name} placeholders from vars. It falls back to
// "en", then to the raw key if no translation exists (matching Rails I18n.missing behaviour
// closely enough for server HTML — callers use real Rails keys so a miss is rare/visible).
func (s *Store) T(locale, key string, vars map[string]string) string {
	if s == nil {
		return interpolate(key, vars)
	}
	if text := s.lookup(locale, key); text != "" {
		return interpolate(text, vars)
	}
	if locale != "en" {
		if text := s.lookup("en", key); text != "" {
			return interpolate(text, vars)
		}
	}
	return key
}

func (s *Store) lookup(locale, key string) string {
	d := s.Dict(locale)
	if d == nil {
		return ""
	}
	return d[key]
}

// interpolate expands %{name} placeholders (Rails I18n interpolation syntax).
func interpolate(text string, vars map[string]string) string {
	if vars == nil {
		return text
	}
	for k, v := range vars {
		text = strings.ReplaceAll(text, "%{"+k+"}", v)
	}
	return text
}
