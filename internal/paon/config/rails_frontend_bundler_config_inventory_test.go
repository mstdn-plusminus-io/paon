package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestRailsFrontendBundlerConfigInventoryIsExplicitlyCovered(t *testing.T) {
	expected := []string{
		"rspack/rspack.config.js",
		"webpack/configuration.js",
		"webpack/development.js",
		"webpack/production.js",
		"webpack/rules/babel.js",
		"webpack/rules/css.js",
		"webpack/rules/file.js",
		"webpack/rules/index.js",
		"webpack/rules/mark.js",
		"webpack/rules/material_icons.js",
		"webpack/rules/node_modules.js",
		"webpack/rules/tesseract.js",
		"webpack/shared.js",
		"webpack/tests.js",
		"webpack/webpack.config.js",
	}
	seen := []string{}
	for _, root := range []string{"../../../config/rspack", "../../../config/webpack"} {
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			rel, err := filepath.Rel("../../../config", path)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			if !slices.Contains(expected, rel) {
				t.Fatalf("Rails frontend bundler config %s is not listed in the Go asset coverage inventory", rel)
			}
			seen = append(seen, rel)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	slices.Sort(seen)
	if !slices.Equal(seen, expected) {
		t.Fatalf("Rails frontend bundler configs = %#v, want %#v", seen, expected)
	}
}

func TestRailsFrontendBundlerConfigKeepsManifestAndThemeContracts(t *testing.T) {
	assertFileContains(t, "../../../config/webpack/configuration.js", []string{
		`const configFile = env.SHAKAPACKER_CONFIG || env.WEBPACKER_CONFIG || 'config/shakapacker.yml';`,
		`const themePath = resolve('config', 'themes.yml');`,
		`PUBLIC_OUTPUT_PATH: settings.public_output_path`,
		`path: resolve('public', settings.public_output_path)`,
		`publicPath: ` + "`/${settings.public_output_path}/`",
	})
	assertFileContains(t, "../../../config/webpack/shared.js", []string{
		`const packPaths = sync(join(entryPath, extensionGlob));`,
		`Object.keys(themes).reduce((themePaths, name) => {`,
		`themePaths[name] = resolve(join(settings.source_path, themes[name]));`,
		`filename: 'js/[name]-[chunkhash].js'`,
		`chunkFilename: 'js/[name]-[chunkhash].chunk.js'`,
		`filename: 'css/[name]-[contenthash:8].css'`,
		`new RspackManifestPlugin({`,
		`fileName: 'manifest.json'`,
		`writeToFileEmit: true`,
	})
	assertFileContains(t, "../../../config/webpack/production.js", []string{
		`new InjectManifest({`,
		`swDest: resolve(root, 'public', 'packs', 'sw.js')`,
		`swSrc: resolve(root, 'app', 'javascript', 'mastodon', 'service_worker', 'entry.js')`,
		`exclude: [`,
		`/mailer-.*\.(?:css|js)$/`,
	})
	assertFileContains(t, "../../../config/rspack/rspack.config.js", []string{
		`config = require('../webpack/production');`,
		`config = require('../webpack/tests');`,
		`config = require('../webpack/development');`,
	})
}

func assertFileContains(t *testing.T, path string, fragments []string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, fragment := range fragments {
		if !strings.Contains(body, fragment) {
			t.Fatalf("%s changed; missing %q", path, fragment)
		}
	}
}
