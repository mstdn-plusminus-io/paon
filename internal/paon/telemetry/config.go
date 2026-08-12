package telemetry

import "github.com/mstdn-plusminus-io/paon/internal/paon/config"

// OptionsFromConfig keeps every Go child process on the same validated
// OpenTelemetry contract without copying exporter credentials.
func OptionsFromConfig(cfg config.Config, role string) Options {
	return Options{
		Enabled:              cfg.OpenTelemetryEnabled,
		TracesEnabled:        cfg.OpenTelemetryTracesEnabled,
		MetricsEnabled:       cfg.OpenTelemetryMetricsEnabled,
		ServiceNamePrefix:    cfg.OTelServiceNamePrefix,
		ServiceNameSeparator: cfg.OTelServiceNameSeparator,
		Role:                 role,
		ServiceVersion:       cfg.Version,
		SourceCommit:         cfg.SourceCommit,
		SourceURL:            cfg.SourceURL,
		Sampler:              cfg.OTelTracesSampler,
		SamplerArg:           cfg.OTelTracesSamplerArg,
		Propagators:          cfg.OTelPropagators,
	}
}
