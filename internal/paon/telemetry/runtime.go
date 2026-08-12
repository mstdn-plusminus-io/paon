package telemetry

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.37.0"
)

const instrumentationName = "github.com/mstdn-plusminus-io/paon/internal/paon/telemetry"

// Options contains the already-validated OpenTelemetry runtime contract.
// OTLP endpoints, headers and TLS options remain standard OTEL_* environment
// variables and are consumed by the official exporters. Keeping credentials
// out of this value prevents accidental logging or span attributes.
type Options struct {
	Enabled              bool
	TracesEnabled        bool
	MetricsEnabled       bool
	ServiceNamePrefix    string
	ServiceNameSeparator string
	Role                 string
	ServiceVersion       string
	Sampler              string
	SamplerArg           string
	Propagators          []string
}

// Runtime owns the SDK providers that must be flushed during graceful
// shutdown. A disabled Runtime is safe to shut down.
type Runtime struct {
	tracerProvider *sdktrace.TracerProvider
	meterProvider  *sdkmetric.MeterProvider
}

var runtimeEnabled atomic.Bool

// Enabled reports whether the process initialized an OTLP-backed SDK.
func Enabled() bool {
	return runtimeEnabled.Load()
}

// ServiceName mirrors Mastodon 4.3's prefix/separator naming while preserving
// Paon's explicit web, worker and combined process roles.
func ServiceName(prefix string, separator string, role string) string {
	return strings.TrimSpace(prefix) + separator + strings.TrimSpace(role)
}

// Initialize configures official OpenTelemetry trace and metric providers.
// It deliberately relies on the official exporters' OTEL_* environment
// handling so credentials never need to be copied into application logs.
func Initialize(ctx context.Context, options Options) (*Runtime, error) {
	runtime := &Runtime{}
	if !options.Enabled {
		runtimeEnabled.Store(false)
		return runtime, nil
	}

	res, err := resource.New(ctx,
		resource.WithTelemetrySDK(),
		resource.WithAttributes(
			semconv.ServiceName(ServiceName(options.ServiceNamePrefix, options.ServiceNameSeparator, options.Role)),
			semconv.ServiceVersion(strings.TrimSpace(options.ServiceVersion)),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("create OpenTelemetry resource: %w", err)
	}

	var traceExporter sdktrace.SpanExporter
	if options.TracesEnabled {
		traceExporter, err = otlptracehttp.New(ctx)
		if err != nil {
			return nil, fmt.Errorf("create OTLP trace exporter: %w", err)
		}
	}

	var metricExporter sdkmetric.Exporter
	if options.MetricsEnabled {
		metricExporter, err = otlpmetrichttp.New(ctx)
		if err != nil {
			if traceExporter != nil {
				_ = traceExporter.Shutdown(ctx)
			}
			return nil, fmt.Errorf("create OTLP metric exporter: %w", err)
		}
	}

	if traceExporter != nil {
		runtime.tracerProvider = sdktrace.NewTracerProvider(
			sdktrace.WithBatcher(traceExporter),
			sdktrace.WithResource(res),
			sdktrace.WithSampler(traceSampler(options.Sampler, options.SamplerArg)),
		)
		otel.SetTracerProvider(runtime.tracerProvider)
	}
	if metricExporter != nil {
		runtime.meterProvider = sdkmetric.NewMeterProvider(
			sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter)),
			sdkmetric.WithResource(res),
		)
		otel.SetMeterProvider(runtime.meterProvider)
	}
	otel.SetTextMapPropagator(textMapPropagator(options.Propagators))
	runtimeEnabled.Store(true)
	return runtime, nil
}

func traceSampler(name string, rawArg string) sdktrace.Sampler {
	ratio := 1.0
	if parsed, err := strconv.ParseFloat(strings.TrimSpace(rawArg), 64); err == nil {
		ratio = parsed
	}
	base := sdktrace.AlwaysSample()
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "always_off":
		base = sdktrace.NeverSample()
	case "traceidratio":
		base = sdktrace.TraceIDRatioBased(ratio)
	case "parentbased_always_off":
		return sdktrace.ParentBased(sdktrace.NeverSample())
	case "parentbased_traceidratio":
		return sdktrace.ParentBased(sdktrace.TraceIDRatioBased(ratio))
	case "parentbased_always_on", "", "always_on":
		base = sdktrace.AlwaysSample()
	}
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(name)), "parentbased_") {
		return sdktrace.ParentBased(base)
	}
	return base
}

func textMapPropagator(names []string) propagation.TextMapPropagator {
	propagators := make([]propagation.TextMapPropagator, 0, len(names))
	for _, name := range names {
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "tracecontext":
			propagators = append(propagators, propagation.TraceContext{})
		case "baggage":
			propagators = append(propagators, propagation.Baggage{})
		case "none":
			return propagation.NewCompositeTextMapPropagator()
		}
	}
	if len(propagators) == 0 {
		return propagation.TraceContext{}
	}
	return propagation.NewCompositeTextMapPropagator(propagators...)
}

// Shutdown flushes metrics before traces so shutdown activity remains visible.
func (runtime *Runtime) Shutdown(ctx context.Context) error {
	if runtime == nil {
		return nil
	}
	var shutdownErrors []error
	if runtime.meterProvider != nil {
		shutdownErrors = append(shutdownErrors, runtime.meterProvider.ForceFlush(ctx))
		shutdownErrors = append(shutdownErrors, runtime.meterProvider.Shutdown(ctx))
	}
	if runtime.tracerProvider != nil {
		shutdownErrors = append(shutdownErrors, runtime.tracerProvider.ForceFlush(ctx))
		shutdownErrors = append(shutdownErrors, runtime.tracerProvider.Shutdown(ctx))
	}
	runtimeEnabled.Store(false)
	return errors.Join(shutdownErrors...)
}

// ShutdownWithTimeout is a convenience for command processes whose signal
// context may already be cancelled when exporters need to flush.
func (runtime *Runtime) ShutdownWithTimeout(timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return runtime.Shutdown(ctx)
}
