package config

import (
	"os"
	"strings"
	"testing"
)

func TestOpenTelemetryEnvironmentContractIsShippedToRuntimeChildren(t *testing.T) {
	sample, err := os.ReadFile("../../../.env.sample")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"OTEL_EXPORTER_OTLP_ENDPOINT",
		"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT",
		"OTEL_EXPORTER_OTLP_METRICS_ENDPOINT",
		"OTEL_EXPORTER_OTLP_HEADERS",
		"OTEL_EXPORTER_OTLP_PROTOCOL",
		"OTEL_SERVICE_NAME_PREFIX",
		"OTEL_SERVICE_NAME_SEPARATOR",
		"OTEL_TRACES_SAMPLER",
		"OTEL_TRACES_SAMPLER_ARG",
		"OTEL_PROPAGATORS",
	} {
		if !strings.Contains(string(sample), name) {
			t.Fatalf(".env.sample missing OpenTelemetry contract %s", name)
		}
	}
	if !strings.Contains(string(sample), "OTEL_TRACES_SAMPLER=parentbased_always_on") {
		t.Fatal(".env.sample must preserve the Mastodon SDK parent-based always-on sampler default")
	}

	compose, err := os.ReadFile("../../../docker-compose.yml")
	if err != nil {
		t.Fatal(err)
	}
	body := string(compose)
	for _, serviceRange := range [][2]string{{"web:", "worker:"}, {"worker:", "db-migrate:"}, {"db-migrate:", "networks:"}} {
		start := strings.Index(body, "  "+serviceRange[0])
		if start < 0 {
			t.Fatalf("compose service %s was not found", serviceRange[0])
		}
		end := strings.Index(body[start+1:], "  "+serviceRange[1])
		if end < 0 {
			t.Fatalf("compose service range %v was not found", serviceRange)
		}
		block := body[start : start+1+end]
		if !strings.Contains(block, "env_file:") || !strings.Contains(block, "path: .env.production") {
			t.Fatalf("compose service %s does not pass .env.production to its Go child", serviceRange[0])
		}
	}

	for _, path := range []string{"../../../cmd/paon/main.go", "../../../cmd/paon-migrate/main.go", "../../../cmd/paon-admin/main.go", "../../../cmd/paon-meili-deploy/main.go"} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		child := string(raw)
		if !strings.Contains(child, "config.LoadDotenv()") || !strings.Contains(child, "config.FromEnv()") {
			t.Fatalf("%s does not load the shared env contract in the actual Go child", path)
		}
		if !strings.Contains(child, "telemetry.Initialize(") {
			t.Fatalf("%s loads OpenTelemetry config but does not initialize the actual Go child", path)
		}
	}
}
