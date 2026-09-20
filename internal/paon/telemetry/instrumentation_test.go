package telemetry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/hibiken/asynq"
	"github.com/labstack/echo/v5"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func installSpanRecorder(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
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
	return recorder
}

func TestHTTPMiddlewareContinuesTraceAndRedactsRequestData(t *testing.T) {
	recorder := installSpanRecorder(t)
	e := echo.New()
	e.Use(HTTPMiddleware())
	e.GET("/api/v1/search", func(c *echo.Context) error {
		return c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/search?q=private-search&access_token=secret-token", nil)
	req.Header.Set("Authorization", "Bearer secret-token")
	req.Header.Set("Traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	e.ServeHTTP(httptest.NewRecorder(), req)

	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("ended spans = %d, want 1", len(spans))
	}
	span := spans[0]
	if got := span.SpanContext().TraceID().String(); got != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatalf("trace id = %s, propagation was not continued", got)
	}
	assertAttributeKeys(t, span.Attributes(), "http.request.method", "http.response.status_code", "http.route")
	assertNoSensitiveTelemetry(t, span.Name(), span.Attributes(), "private-search", "secret-token", "access_token", "Authorization")
}

func TestWorkerRedisFederationAndSearchSpansExcludePayloadsAndArguments(t *testing.T) {
	recorder := installSpanRecorder(t)

	worker := WorkerMiddleware()(asynq.HandlerFunc(func(context.Context, *asynq.Task) error { return nil }))
	if err := worker.ProcessTask(context.Background(), asynq.NewTask("activitypub:processing", []byte(`{"token":"worker-secret"}`))); err != nil {
		t.Fatalf("worker middleware: %v", err)
	}

	_, finishRedis := StartRedis(context.Background(), "GET")
	finishRedis(nil)
	_, finishFederation := StartFederation(context.Background(), "inbox")
	finishFederation(http.StatusAccepted, nil)
	_, finishSearch := StartSearch(context.Background(), 7, 20)
	finishSearch(3, nil)

	spans := recorder.Ended()
	if len(spans) != 4 {
		t.Fatalf("ended spans = %d, want 4", len(spans))
	}
	for _, span := range spans {
		assertNoSensitiveTelemetry(t, span.Name(), span.Attributes(), "worker-secret", "token", "private-search")
		if span.Name() == "search" {
			assertAttributeKeys(t, span.Attributes(), "search.backend", "search.limit", "search.offset", "search.result_count")
		}
		if span.Name() == "redis GET" {
			assertAttributeKeys(t, span.Attributes(), "db.operation.name", "db.system.name")
		}
	}
}

func TestAsynqHeadersContinueProducerTraceWithoutBecomingAttributes(t *testing.T) {
	recorder := installSpanRecorder(t)
	producerCtx, producer := tracer().Start(context.Background(), "producer")
	task := asynq.NewTaskWithHeaders(
		"activitypub:processing",
		[]byte(`{"token":"worker-secret"}`),
		AsynqHeaders(producerCtx),
	)
	worker := WorkerMiddleware()(asynq.HandlerFunc(func(context.Context, *asynq.Task) error { return nil }))
	if err := worker.ProcessTask(context.Background(), task); err != nil {
		t.Fatalf("worker middleware: %v", err)
	}
	producer.End()

	spans := recorder.Ended()
	if len(spans) != 2 {
		t.Fatalf("ended spans = %d, want producer and worker", len(spans))
	}
	var workerSpan sdktrace.ReadOnlySpan
	for _, span := range spans {
		if strings.HasPrefix(span.Name(), "asynq ") {
			workerSpan = span
		}
	}
	if workerSpan == nil {
		t.Fatal("worker span was not recorded")
	}
	if workerSpan.SpanContext().TraceID() != producer.SpanContext().TraceID() {
		t.Fatal("worker did not continue the producer trace")
	}
	assertNoSensitiveTelemetry(t, workerSpan.Name(), workerSpan.Attributes(), "worker-secret", "traceparent", "tracestate", "baggage")
}

func TestRedactedHTTPTransportInjectsTraceWithoutURLOrCredentials(t *testing.T) {
	recorder := installSpanRecorder(t)
	var captured *http.Request
	transport := NewHTTPTransport(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		captured = req
		return &http.Response{StatusCode: http.StatusAccepted, Body: http.NoBody, Header: make(http.Header)}, nil
	}), "federation")

	ctx, parent := tracer().Start(context.Background(), "parent")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://remote.example/inbox?access_token=outbound-secret", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer outbound-secret")
	if _, err := transport.RoundTrip(req); err != nil {
		t.Fatalf("round trip: %v", err)
	}
	parent.End()
	if captured == nil || captured.Header.Get("Traceparent") == "" {
		t.Fatal("outbound request did not receive traceparent")
	}

	for _, span := range recorder.Ended() {
		assertNoSensitiveTelemetry(t, span.Name(), span.Attributes(), "outbound-secret", "access_token", "/inbox", "Authorization")
	}
}

func TestGORMInstrumentationRecordsOperationWithoutSQLOrBindValues(t *testing.T) {
	recorder := installSpanRecorder(t)
	previousEnabled := runtimeEnabled.Load()
	runtimeEnabled.Store(true)
	t.Cleanup(func() { runtimeEnabled.Store(previousEnabled) })

	database, err := gorm.Open(postgres.New(postgres.Config{
		DSN:                  "host=database.example.test user=paon password=database-secret dbname=mastodon sslmode=disable",
		PreferSimpleProtocol: true,
	}), &gorm.Config{DryRun: true, DisableAutomaticPing: true, SkipDefaultTransaction: true})
	if err != nil {
		t.Fatalf("open dry-run database: %v", err)
	}
	if err := InstrumentGORM(database); err != nil {
		t.Fatalf("instrument database: %v", err)
	}
	type account struct {
		ID    int64
		Email string
	}
	if err := database.Create(&account{ID: 42, Email: "private@example.test"}).Error; err != nil {
		t.Fatalf("dry-run create: %v", err)
	}

	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("ended spans = %d, want 1", len(spans))
	}
	assertAttributeKeys(t, spans[0].Attributes(), "db.operation.name", "db.system.name")
	assertNoSensitiveTelemetry(t, spans[0].Name(), spans[0].Attributes(), "database-secret", "private@example.test", "accounts", "INSERT INTO")
}

func assertAttributeKeys(t *testing.T, attrs []attribute.KeyValue, want ...string) {
	t.Helper()
	got := make([]string, 0, len(attrs))
	for _, attr := range attrs {
		got = append(got, string(attr.Key))
	}
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("attribute keys = %v, want %v", got, want)
	}
}

func assertNoSensitiveTelemetry(t *testing.T, spanName string, attrs []attribute.KeyValue, forbidden ...string) {
	t.Helper()
	var value strings.Builder
	value.WriteString(spanName)
	for _, attr := range attrs {
		value.WriteString(" ")
		value.WriteString(string(attr.Key))
		value.WriteString("=")
		value.WriteString(attr.Value.Emit())
	}
	for _, secret := range forbidden {
		if strings.Contains(value.String(), secret) {
			t.Fatalf("telemetry leaked %q: %s", secret, value.String())
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}
