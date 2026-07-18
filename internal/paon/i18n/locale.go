package i18n

import (
	"sort"
	"strconv"
	"strings"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
)

// Resolve picks a locale following Rails precedence: explicit user/session locale, then
// Accept-Language, then the configured default, then "en". Request-derived candidates must
// match Rails' config.i18n.available_locales, mirroring Localized#available_locale_or_nil and
// HttpAcceptLanguage#language_region_compatible_from.
func Resolve(userLocale, acceptLanguage, defaultLocale string) string {
	if loc := RailsAvailableLocale(userLocale); loc != "" {
		return loc
	}
	for _, cand := range acceptLanguageCandidates(acceptLanguage) {
		if loc := RailsAvailableLocale(cand); loc != "" {
			return loc
		}
	}
	if loc := RailsAvailableLocale(defaultLocale); loc != "" {
		return loc
	}
	return "en"
}

func RailsAvailableLocale(locale string) string {
	normalized := NormalizeLocale(locale)
	if normalized == "" {
		return ""
	}
	for _, available := range config.RailsI18nAvailableLocales() {
		if strings.EqualFold(available, normalized) {
			return available
		}
	}
	if idx := strings.Index(normalized, "-"); idx > 0 {
		base := normalized[:idx]
		for _, available := range config.RailsI18nAvailableLocales() {
			if strings.EqualFold(available, base) {
				return available
			}
		}
	}
	return ""
}

func acceptLanguageCandidates(header string) []string {
	header = strings.TrimSpace(header)
	if header == "" {
		return nil
	}
	type candidate struct {
		value string
		q     float64
		order int
	}
	var values []candidate
	for order, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		value := part
		q := 1.0
		if idx := strings.Index(part, ";"); idx >= 0 {
			value = strings.TrimSpace(part[:idx])
			for _, param := range strings.Split(part[idx+1:], ";") {
				param = strings.TrimSpace(param)
				if !strings.HasPrefix(param, "q=") {
					continue
				}
				parsed, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimPrefix(param, "q=")), 64)
				if err == nil {
					q = parsed
				}
				break
			}
		}
		if value == "" || q <= 0 {
			continue
		}
		values = append(values, candidate{value: value, q: q, order: order})
	}
	sort.SliceStable(values, func(i, j int) bool {
		if values[i].q == values[j].q {
			return values[i].order < values[j].order
		}
		return values[i].q > values[j].q
	})
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, value.value)
	}
	return out
}

// NormalizeLocale trims and lower-cases a locale string, taking the first segment of an
// Accept-Language list ("ja,en;q=0.9" -> "ja") and mapping "_" to "-". It keeps a regional
// suffix (e.g. "pt-br", "zh-cn") so region-specific dictionaries resolve; callers fall back to
// the base language / "en" via Store.T when a regional file is absent.
func NormalizeLocale(loc string) string {
	loc = strings.TrimSpace(loc)
	if loc == "" {
		return ""
	}
	if i := strings.IndexAny(loc, ",;"); i >= 0 {
		loc = loc[:i]
	}
	loc = strings.TrimSpace(loc)
	loc = strings.ReplaceAll(loc, "_", "-")
	return strings.ToLower(loc)
}
