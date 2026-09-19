package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCaptureOptionsRequiresCompleteValidWindow(t *testing.T) {
	start, finish := "2026-09-19T00:00:00.123456789Z", "2026-09-19T00:00:02.123456789Z"
	for _, pair := range [][2]string{{start, ""}, {"", finish}, {"invalid", finish}, {start, "invalid"}, {finish, start}, {start, start}} {
		if _, err := captureOptions(pair[0], pair[1]); err == nil {
			t.Fatalf("invalid clock window accepted: %q", pair)
		}
	}
	if options, err := captureOptions("", ""); err != nil || options.MigrationWindow != nil {
		t.Fatalf("raw comparison options = %+v, error = %v", options, err)
	}
	if options, err := captureOptions(start, finish); err != nil || options.MigrationWindow == nil || options.MigrationWindow.StartedAt.Nanosecond() != 123456789 {
		t.Fatalf("normalized comparison options = %+v, error = %v", options, err)
	}
}

func TestSnapshotOutputCannotOverwriteReference(t *testing.T) {
	directory := t.TempDir()
	reference := filepath.Join(directory, "reference.json")
	if err := validateSnapshotPaths(reference, reference); err == nil {
		t.Fatal("identical snapshot paths were accepted")
	}
	if err := os.WriteFile(reference, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(directory, "alias.json")
	if err := os.Symlink(reference, alias); err != nil {
		t.Fatal(err)
	}
	if err := validateSnapshotPaths(alias, reference); err == nil {
		t.Fatal("symlink alias could overwrite the reference")
	}
	if err := validateSnapshotPaths(filepath.Join(directory, "candidate.json"), reference); err != nil {
		t.Fatal(err)
	}
}
