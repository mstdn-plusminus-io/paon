package api

import (
	"context"
	"io"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestMeiliSearchTelemetryHasOnlyBoundedAttributes(t *testing.T) {
	previousProvider := otel.GetTracerProvider()
	previousPropagator := otel.GetTextMapPropagator()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() {
		_ = provider.Shutdown(context.Background())
		otel.SetTracerProvider(previousProvider)
		otel.SetTextMapPropagator(previousPropagator)
	})

	previousClient := meiliHTTPClient
	meiliHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Header.Get("Traceparent") == "" {
			t.Error("Meilisearch request did not receive trace context")
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"hits":[{"id":"42"}]}`)),
		}, nil
	})}
	t.Cleanup(func() { meiliHTTPClient = previousClient })

	server := &Server{cfg: config.Config{
		OpenTelemetryEnabled: true,
		MeiliEnabled:         true,
		MeiliHost:            "https://search.example.test",
		MeiliMasterKey:       "meili-secret-token",
		MeiliPrefix:          "private-index-",
	}}
	ids, err := server.searchMeiliIDs(context.Background(), "statuses", "private search words", meiliSearchOptions{Offset: 4, Limit: 10})
	if err != nil {
		t.Fatalf("search Meilisearch: %v", err)
	}
	if len(ids) != 1 || ids[0] != 42 {
		t.Fatalf("search ids = %#v", ids)
	}

	spans := recorder.Ended()
	if len(spans) != 1 || spans[0].Name() != "search" {
		t.Fatalf("search spans = %#v", spans)
	}
	keys := make([]string, 0, len(spans[0].Attributes()))
	var rendered strings.Builder
	for _, attr := range spans[0].Attributes() {
		keys = append(keys, string(attr.Key))
		rendered.WriteString(string(attr.Key))
		rendered.WriteString("=")
		rendered.WriteString(attr.Value.Emit())
	}
	sort.Strings(keys)
	want := []string{"search.backend", "search.limit", "search.offset", "search.result_count"}
	if strings.Join(keys, ",") != strings.Join(want, ",") {
		t.Fatalf("search attribute keys = %v, want %v", keys, want)
	}
	for _, secret := range []string{"private search words", "meili-secret-token", "private-index", "statuses"} {
		if strings.Contains(rendered.String(), secret) {
			t.Fatalf("search span leaked %q: %s", secret, rendered.String())
		}
	}
}

func TestOpenTelemetryDisablesLegacyStatsDClient(t *testing.T) {
	client := newStatsDClient(config.Config{
		OpenTelemetryEnabled: true,
		StatsDAddr:           "127.0.0.1:8125",
	})
	if client != nil {
		t.Fatal("StatsD client must be disabled while OpenTelemetry is enabled")
	}
}
