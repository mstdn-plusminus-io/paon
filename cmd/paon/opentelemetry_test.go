package main

import (
	"os"
	"strings"
	"testing"
)

func TestTelemetryServiceRoleNamesCombinedProcessExplicitly(t *testing.T) {
	for role, want := range map[string]string{
		"web":    "web",
		"worker": "worker",
		"all":    "web-worker",
	} {
		if got := telemetryServiceRole(role); got != want {
			t.Fatalf("telemetry role for %q = %q, want %q", role, got, want)
		}
	}
}

func TestOpenTelemetryInitializesBeforeDatabaseAndNotDuringCheckConfig(t *testing.T) {
	raw, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	initialize := strings.Index(body, "telemetry.Initialize(ctx")
	databaseOpen := strings.Index(body, "paondb.Open(cfg)")
	if initialize < 0 || databaseOpen < 0 || initialize > databaseOpen {
		t.Fatal("OpenTelemetry must initialize before GORM opens the database")
	}
	if !strings.Contains(body, "cfg.OpenTelemetryEnabled && !*checkConfig") {
		t.Fatal("--check-config must validate OTel config without starting exporters")
	}
}
