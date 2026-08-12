package config

import (
	"strings"
	"testing"
)

func TestOpenTelemetryFromEnvDefaultsAndEndpointActivation(t *testing.T) {
	for _, name := range []string{
		"OTEL_EXPORTER_OTLP_ENDPOINT",
		"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT",
		"OTEL_EXPORTER_OTLP_METRICS_ENDPOINT",
		"OTEL_EXPORTER_OTLP_HEADERS",
		"OTEL_SERVICE_NAME_PREFIX",
		"OTEL_SERVICE_NAME_SEPARATOR",
		"OTEL_TRACES_SAMPLER",
		"OTEL_TRACES_SAMPLER_ARG",
		"OTEL_PROPAGATORS",
	} {
		unsetEnvForTest(t, name)
	}
	cfg := FromEnv()
	if cfg.OpenTelemetryEnabled || cfg.OpenTelemetryTracesEnabled || cfg.OpenTelemetryMetricsEnabled {
		t.Fatal("OpenTelemetry must remain disabled when no OTLP endpoint is configured")
	}
	if cfg.OTelServiceNamePrefix != "mastodon" || cfg.OTelServiceNameSeparator != "/" {
		t.Fatalf("service name defaults = %q %q", cfg.OTelServiceNamePrefix, cfg.OTelServiceNameSeparator)
	}

	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "https://collector.example.test/otel")
	t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "authorization=Bearer%20redacted")
	cfg = FromEnv()
	if !cfg.OpenTelemetryEnabled || !cfg.OpenTelemetryTracesEnabled || !cfg.OpenTelemetryMetricsEnabled {
		t.Fatal("base OTLP endpoint must enable both traces and metrics")
	}
	if len(cfg.OTelExporterOTLPHeaders) != len("authorization=Bearer%20redacted") {
		t.Fatal("OTLP headers did not reach the child configuration with the expected secret-safe length")
	}
	if err := cfg.validateOpenTelemetry(); err != nil {
		t.Fatalf("valid OpenTelemetry config: %v", err)
	}
}

func TestOpenTelemetryValidationRejectsUnsafeOrUnpairedValues(t *testing.T) {
	valid := Config{
		OpenTelemetryEnabled:        true,
		OpenTelemetryTracesEnabled:  true,
		OpenTelemetryMetricsEnabled: true,
		OTelServiceNamePrefix:       "mastodon",
		OTelServiceNameSeparator:    "/",
		OTelExporterOTLPEndpoint:    "https://collector.example.test",
		OTelExporterOTLPProtocol:    "http/protobuf",
		OTelTracesSampler:           "parentbased_traceidratio",
		OTelTracesSamplerArg:        "0.25",
		OTelPropagators:             []string{"tracecontext", "baggage"},
	}
	if err := valid.validateOpenTelemetry(); err != nil {
		t.Fatalf("valid config: %v", err)
	}

	for _, test := range []struct {
		name string
		edit func(*Config)
		want string
	}{
		{
			name: "credential in endpoint",
			edit: func(cfg *Config) { cfg.OTelExporterOTLPEndpoint = "https://token@collector.example.test/v1?key=secret" },
			want: "must not contain userinfo",
		},
		{
			name: "unsupported grpc exporter",
			edit: func(cfg *Config) { cfg.OTelExporterOTLPProtocol = "grpc" },
			want: "must be http/protobuf",
		},
		{
			name: "invalid sampling ratio",
			edit: func(cfg *Config) { cfg.OTelTracesSamplerArg = "1.1" },
			want: "between 0 and 1",
		},
		{
			name: "mixed none propagation",
			edit: func(cfg *Config) { cfg.OTelPropagators = []string{"none", "tracecontext"} },
			want: "cannot be combined",
		},
		{
			name: "header control character",
			edit: func(cfg *Config) { cfg.OTelExporterOTLPHeaders = "authorization=secret\r\ninjected=true" },
			want: "control-line",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := valid
			test.edit(&cfg)
			err := cfg.validateOpenTelemetry()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validation error = %v, want %q", err, test.want)
			}
		})
	}

	disabledWithSecret := Config{OTelExporterOTLPHeaders: "authorization=secret"}
	if err := disabledWithSecret.validateOpenTelemetry(); err == nil || !strings.Contains(err.Error(), "require OTEL_EXPORTER_OTLP_ENDPOINT") {
		t.Fatalf("unpaired headers error = %v", err)
	}
}

func TestStatsDWarningDocumentsNoDoubleCounting(t *testing.T) {
	cfg := Config{OpenTelemetryEnabled: true, StatsDAddr: "127.0.0.1:8125"}
	warnings := strings.Join(cfg.RuntimeWarnings(), "\n")
	if !strings.Contains(warnings, "disables the legacy StatsD extension") || !strings.Contains(warnings, "prevent double counting") {
		t.Fatalf("StatsD/OpenTelemetry warning = %q", warnings)
	}
}
