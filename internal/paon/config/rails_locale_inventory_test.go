package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestRailsLocaleFamilyInventoryIsExplicitlyCovered(t *testing.T) {
	expectedLocales := []string{
		"af",
		"an",
		"ar",
		"ast",
		"az",
		"be",
		"bg",
		"bn",
		"br",
		"bs",
		"ca",
		"ckb",
		"co",
		"cs",
		"cy",
		"da",
		"de",
		"el",
		"en",
		"en-GB",
		"eo",
		"es",
		"es-AR",
		"es-MX",
		"et",
		"eu",
		"fa",
		"fi",
		"fil",
		"fo",
		"fr",
		"fr-CA",
		"fy",
		"ga",
		"gd",
		"gl",
		"he",
		"hi",
		"hr",
		"hu",
		"hy",
		"ia",
		"id",
		"ie",
		"ig",
		"io",
		"is",
		"it",
		"ja",
		"ka",
		"kab",
		"kk",
		"kn",
		"ko",
		"ku",
		"kw",
		"la",
		"lad",
		"lt",
		"lv",
		"mk",
		"ml",
		"mr",
		"ms",
		"my",
		"nan",
		"nan-TW",
		"ne",
		"nl",
		"nn",
		"no",
		"oc",
		"pa",
		"pl",
		"pt-BR",
		"pt-PT",
		"ro",
		"ru",
		"ry",
		"sa",
		"sc",
		"sco",
		"si",
		"sk",
		"sl",
		"sq",
		"sr",
		"sr-Latn",
		"sv",
		"szl",
		"ta",
		"tai",
		"te",
		"th",
		"tlh",
		"tok",
		"tr",
		"tt",
		"ug",
		"uk",
		"ur",
		"uz",
		"vi",
		"zgh",
		"zh-CN",
		"zh-HK",
		"zh-TW",
	}
	expectedFamilies := []string{"app", "activerecord", "devise", "doorkeeper", "simple_form"}

	seen := map[string]map[string]bool{}
	for _, family := range expectedFamilies {
		seen[family] = map[string]bool{}
	}

	err := filepath.WalkDir("../../../config/locales", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if filepath.Ext(path) != ".yml" {
			rel, relErr := filepath.Rel("../../../config/locales", path)
			if relErr != nil {
				return relErr
			}
			t.Fatalf("Rails locale file %s is not a YAML locale file", filepath.ToSlash(rel))
		}

		rel, err := filepath.Rel("../../../config/locales", path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		family, locale := railsLocaleFamilyAndLocale(rel)
		if !slices.Contains(expectedFamilies, family) {
			t.Fatalf("Rails locale family for %s is not listed in the Go locale coverage inventory", rel)
		}
		if !slices.Contains(expectedLocales, locale) {
			t.Fatalf("Rails locale %s from %s is not listed in the Go locale coverage inventory", locale, rel)
		}
		seen[family][locale] = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, family := range expectedFamilies {
		for _, locale := range expectedLocales {
			if !seen[family][locale] {
				t.Fatalf("expected Rails locale family %s for %s was not found", family, locale)
			}
		}
		if len(seen[family]) != len(expectedLocales) {
			t.Fatalf("Rails locale family %s has %d locales, want %d", family, len(seen[family]), len(expectedLocales))
		}
	}
}

func railsLocaleFamilyAndLocale(name string) (string, string) {
	locale := strings.TrimSuffix(name, ".yml")
	for _, family := range []string{"activerecord", "devise", "doorkeeper", "simple_form"} {
		prefix := family + "."
		if strings.HasPrefix(locale, prefix) {
			return family, strings.TrimPrefix(locale, prefix)
		}
	}
	return "app", locale
}
