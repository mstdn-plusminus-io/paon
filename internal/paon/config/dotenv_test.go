package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDotenvFilesPreservesProcessEnvAndFilePriority(t *testing.T) {
	dir := t.TempDir()
	high := filepath.Join(dir, ".env.production")
	low := filepath.Join(dir, ".env")
	if err := os.WriteFile(high, []byte("LOCAL_DOMAIN=from-production.test\nDB_NAME=prod_db\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(low, []byte("LOCAL_DOMAIN=from-env.test\nDB_NAME=env_db\nSECRET_KEY_BASE=from-env\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LOCAL_DOMAIN", "process.test")
	t.Setenv("DB_NAME", "")
	t.Setenv("SECRET_KEY_BASE", "")

	if err := LoadDotenvFiles(high, low); err != nil {
		t.Fatalf("LoadDotenvFiles: %v", err)
	}
	if got := os.Getenv("LOCAL_DOMAIN"); got != "process.test" {
		t.Fatalf("LOCAL_DOMAIN = %q", got)
	}
	if got := os.Getenv("DB_NAME"); got != "" {
		t.Fatalf("DB_NAME should not override explicitly empty process env, got %q", got)
	}
	if got := os.Getenv("SECRET_KEY_BASE"); got != "" {
		t.Fatalf("SECRET_KEY_BASE should not override explicitly empty process env, got %q", got)
	}

	t.Setenv("LOCAL_DOMAIN", "")
	t.Setenv("DB_NAME", "")
	t.Setenv("SECRET_KEY_BASE", "")
	_ = os.Unsetenv("LOCAL_DOMAIN")
	_ = os.Unsetenv("DB_NAME")
	_ = os.Unsetenv("SECRET_KEY_BASE")
	if err := LoadDotenvFiles(high, low); err != nil {
		t.Fatalf("LoadDotenvFiles second pass: %v", err)
	}
	if got := os.Getenv("LOCAL_DOMAIN"); got != "from-production.test" {
		t.Fatalf("LOCAL_DOMAIN from priority file = %q", got)
	}
	if got := os.Getenv("DB_NAME"); got != "prod_db" {
		t.Fatalf("DB_NAME from priority file = %q", got)
	}
	if got := os.Getenv("SECRET_KEY_BASE"); got != "from-env" {
		t.Fatalf("SECRET_KEY_BASE from fallback file = %q", got)
	}
}

func TestParseDotenvLineHandlesMastodonEnvShapes(t *testing.T) {
	tests := []struct {
		line      string
		wantKey   string
		wantValue string
		wantOK    bool
	}{
		{line: "", wantOK: false},
		{line: "# comment", wantOK: false},
		{line: "LOCAL_DOMAIN=example.test", wantKey: "LOCAL_DOMAIN", wantValue: "example.test", wantOK: true},
		{line: "export DB_PASS='pa ss#word'", wantKey: "DB_PASS", wantValue: "pa ss#word", wantOK: true},
		{line: `SMTP_FROM_ADDRESS="Paon\nAdmin <admin@example.test>"`, wantKey: "SMTP_FROM_ADDRESS", wantValue: "Paon\nAdmin <admin@example.test>", wantOK: true},
		{line: "MEILI_MASTER_KEY=abc#123", wantKey: "MEILI_MASTER_KEY", wantValue: "abc#123", wantOK: true},
		{line: "MEILI_HOST=http://localhost:7700 # local search", wantKey: "MEILI_HOST", wantValue: "http://localhost:7700", wantOK: true},
	}
	for _, tt := range tests {
		key, value, ok, err := parseDotenvLine(tt.line)
		if err != nil {
			t.Fatalf("parseDotenvLine(%q): %v", tt.line, err)
		}
		if ok != tt.wantOK || key != tt.wantKey || value != tt.wantValue {
			t.Fatalf("parseDotenvLine(%q) = %q/%q/%v", tt.line, key, value, ok)
		}
	}
}

func TestLoadDotenvUsesRailsEnvironmentSpecificFiles(t *testing.T) {
	dir := t.TempDir()
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chdir(oldWD); err != nil {
			t.Fatal(err)
		}
	}()
	if err := os.WriteFile(".env", []byte("LOCAL_DOMAIN=base.test\nDB_NAME=base_db\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(".env.production", []byte("LOCAL_DOMAIN=production.test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RAILS_ENV", "production")
	_ = os.Unsetenv("LOCAL_DOMAIN")
	_ = os.Unsetenv("DB_NAME")

	if err := LoadDotenv(); err != nil {
		t.Fatalf("LoadDotenv: %v", err)
	}
	if got := os.Getenv("LOCAL_DOMAIN"); got != "production.test" {
		t.Fatalf("LOCAL_DOMAIN = %q", got)
	}
	if got := os.Getenv("DB_NAME"); got != "base_db" {
		t.Fatalf("DB_NAME = %q", got)
	}
}

func TestLoadDotenvUsesRailsEnvBeforePaonEnvAndPreservesBlankRailsEnv(t *testing.T) {
	dir := t.TempDir()
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chdir(oldWD); err != nil {
			t.Fatal(err)
		}
	}()
	if err := os.WriteFile(".env.production", []byte("LOCAL_DOMAIN=production.test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(".env.staging", []byte("LOCAL_DOMAIN=staging.test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(".env", []byte("DB_NAME=base_db\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("RAILS_ENV", "production")
	t.Setenv("PAON_ENV", "staging")
	_ = os.Unsetenv("LOCAL_DOMAIN")
	_ = os.Unsetenv("DB_NAME")
	if err := LoadDotenv(); err != nil {
		t.Fatalf("LoadDotenv RAILS_ENV priority: %v", err)
	}
	if got := os.Getenv("LOCAL_DOMAIN"); got != "production.test" {
		t.Fatalf("RAILS_ENV should win over PAON_ENV, LOCAL_DOMAIN = %q", got)
	}

	t.Setenv("RAILS_ENV", "")
	t.Setenv("PAON_ENV", "staging")
	_ = os.Unsetenv("LOCAL_DOMAIN")
	_ = os.Unsetenv("DB_NAME")
	if err := LoadDotenv(); err != nil {
		t.Fatalf("LoadDotenv blank RAILS_ENV: %v", err)
	}
	if got := os.Getenv("LOCAL_DOMAIN"); got != "" {
		t.Fatalf("blank RAILS_ENV should not load .env.staging, LOCAL_DOMAIN = %q", got)
	}
	if got := os.Getenv("DB_NAME"); got != "base_db" {
		t.Fatalf("blank RAILS_ENV should still load .env fallback, DB_NAME = %q", got)
	}
}

func TestLoadDotenvFilesRejectsMalformedLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("not valid\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := LoadDotenvFiles(path); err == nil {
		t.Fatal("LoadDotenvFiles returned nil for malformed file")
	}
}
