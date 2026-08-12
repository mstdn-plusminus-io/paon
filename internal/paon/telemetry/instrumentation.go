package telemetry

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/hibiken/asynq"
	"github.com/labstack/echo/v5"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

type metricInstruments struct {
	httpDuration       metric.Float64Histogram
	httpRequests       metric.Int64Counter
	workerDuration     metric.Float64Histogram
	workerJobs         metric.Int64Counter
	dbDuration         metric.Float64Histogram
	dbOperations       metric.Int64Counter
	redisDuration      metric.Float64Histogram
	redisOperations    metric.Int64Counter
	federationDuration metric.Float64Histogram
	federationOps      metric.Int64Counter
	searchDuration     metric.Float64Histogram
	searchOperations   metric.Int64Counter
	searchResults      metric.Int64Histogram
}

var (
	instrumentsOnce sync.Once
	instruments     metricInstruments
)

func metrics() metricInstruments {
	instrumentsOnce.Do(func() {
		meter := otel.Meter(instrumentationName)
		instruments.httpDuration, _ = meter.Float64Histogram("paon.http.server.request.duration", metric.WithUnit("s"))
		instruments.httpRequests, _ = meter.Int64Counter("paon.http.server.requests")
		instruments.workerDuration, _ = meter.Float64Histogram("paon.worker.job.duration", metric.WithUnit("s"))
		instruments.workerJobs, _ = meter.Int64Counter("paon.worker.jobs")
		instruments.dbDuration, _ = meter.Float64Histogram("paon.db.operation.duration", metric.WithUnit("s"))
		instruments.dbOperations, _ = meter.Int64Counter("paon.db.operations")
		instruments.redisDuration, _ = meter.Float64Histogram("paon.redis.command.duration", metric.WithUnit("s"))
		instruments.redisOperations, _ = meter.Int64Counter("paon.redis.commands")
		instruments.federationDuration, _ = meter.Float64Histogram("paon.federation.operation.duration", metric.WithUnit("s"))
		instruments.federationOps, _ = meter.Int64Counter("paon.federation.operations")
		instruments.searchDuration, _ = meter.Float64Histogram("paon.search.duration", metric.WithUnit("s"))
		instruments.searchOperations, _ = meter.Int64Counter("paon.search.operations")
		instruments.searchResults, _ = meter.Int64Histogram("paon.search.result_count")
	})
	return instruments
}

func tracer() trace.Tracer {
	return otel.Tracer(instrumentationName)
}

// HTTPMiddleware traces an Echo route template, never the raw request target.
// Headers, query strings, request bodies, client IPs and user identifiers are
// intentionally excluded from spans and metrics.
func HTTPMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			req := c.Request()
			if req == nil || req.URL == nil || req.URL.Path == "/health" {
				return next(c)
			}
			route := strings.TrimSpace(c.Path())
			if route == "" {
				route = "unmatched"
			}
			method := safeLabel(req.Method)
			parent := otel.GetTextMapPropagator().Extract(req.Context(), propagation.HeaderCarrier(req.Header))
			ctx, span := tracer().Start(parent, method+" "+route,
				trace.WithSpanKind(trace.SpanKindServer),
				trace.WithAttributes(
					attribute.String("http.request.method", method),
					attribute.String("http.route", route),
				),
			)
			c.SetRequest(req.WithContext(ctx))

			original := c.Response()
			recorder := &statusResponseWriter{ResponseWriter: original}
			c.SetResponse(recorder)
			start := time.Now()
			err := next(c)
			c.SetResponse(original)

			status := recorder.status
			if status == 0 && err != nil {
				status = echo.StatusCode(err)
			}
			if status == 0 {
				status = http.StatusOK
			}
			span.SetAttributes(attribute.Int("http.response.status_code", status))
			if status >= http.StatusInternalServerError {
				span.SetStatus(codes.Error, "request failed")
			}
			attrs := []attribute.KeyValue{
				attribute.String("http.request.method", method),
				attribute.String("http.route", route),
				attribute.Int("http.response.status_code", status),
			}
			m := metrics()
			m.httpDuration.Record(ctx, time.Since(start).Seconds(), metric.WithAttributes(attrs...))
			m.httpRequests.Add(ctx, 1, metric.WithAttributes(attrs...))
			span.End()
			return err
		}
	}
}

// WorkerMiddleware instruments Asynq execution without recording task IDs or
// payloads. This prevents access tokens, e-mail addresses and ActivityPub
// bodies carried by jobs from becoming telemetry attributes.
func WorkerMiddleware() asynq.MiddlewareFunc {
	return func(next asynq.Handler) asynq.Handler {
		return asynq.HandlerFunc(func(ctx context.Context, task *asynq.Task) error {
			taskType := "unknown"
			if task != nil {
				taskType = safeLabel(task.Type())
				if len(task.Headers()) > 0 {
					ctx = otel.GetTextMapPropagator().Extract(ctx, propagation.MapCarrier(task.Headers()))
				}
			}
			queue := "unknown"
			if value, ok := asynq.GetQueueName(ctx); ok {
				queue = safeLabel(value)
			}
			ctx, span := tracer().Start(ctx, "asynq "+taskType,
				trace.WithSpanKind(trace.SpanKindConsumer),
				trace.WithAttributes(
					attribute.String("messaging.system", "asynq"),
					attribute.String("messaging.destination.name", queue),
					attribute.String("messaging.operation.name", "process"),
					attribute.String("code.function.name", taskType),
				),
			)
			start := time.Now()
			err := next.ProcessTask(ctx, task)
			outcome := "success"
			if err != nil {
				outcome = "failure"
				span.SetStatus(codes.Error, "job failed")
			}
			attrs := []attribute.KeyValue{
				attribute.String("messaging.destination.name", queue),
				attribute.String("code.function.name", taskType),
				attribute.String("paon.outcome", outcome),
			}
			m := metrics()
			m.workerDuration.Record(ctx, time.Since(start).Seconds(), metric.WithAttributes(attrs...))
			m.workerJobs.Add(ctx, 1, metric.WithAttributes(attrs...))
			span.End()
			return err
		})
	}
}

// AsynqHeaders returns only the configured propagation carrier. Callers attach
// it with asynq.NewTaskWithHeaders; task payloads remain unchanged.
func AsynqHeaders(ctx context.Context) map[string]string {
	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)
	return map[string]string(carrier)
}

// StartRedis starts a client span containing only the command verb. Keys,
// arguments and returned values are deliberately excluded.
func StartRedis(ctx context.Context, command string) (context.Context, func(error)) {
	command = strings.ToUpper(safeLabel(command))
	if command == "" {
		command = "UNKNOWN"
	}
	ctx, span := tracer().Start(ctx, "redis "+command,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("db.system.name", "redis"),
			attribute.String("db.operation.name", command),
		),
	)
	start := time.Now()
	return ctx, func(err error) {
		outcome := "success"
		if err != nil {
			outcome = "failure"
			span.SetStatus(codes.Error, "redis command failed")
		}
		attrs := []attribute.KeyValue{
			attribute.String("db.operation.name", command),
			attribute.String("paon.outcome", outcome),
		}
		m := metrics()
		m.redisDuration.Record(ctx, time.Since(start).Seconds(), metric.WithAttributes(attrs...))
		m.redisOperations.Add(ctx, 1, metric.WithAttributes(attrs...))
		span.End()
	}
}

// StartFederation records only the direction and outcome. Actor IDs, domains,
// inbox URLs and ActivityPub documents are never attributes.
func StartFederation(ctx context.Context, direction string) (context.Context, func(int, error)) {
	direction = safeLabel(direction)
	ctx, span := tracer().Start(ctx, "federation."+direction,
		trace.WithAttributes(attribute.String("paon.federation.direction", direction)),
	)
	start := time.Now()
	return ctx, func(statusCode int, err error) {
		outcome := "success"
		if err != nil || statusCode >= http.StatusInternalServerError {
			outcome = "failure"
			span.SetStatus(codes.Error, "federation operation failed")
		}
		attrs := []attribute.KeyValue{
			attribute.String("paon.federation.direction", direction),
			attribute.String("paon.outcome", outcome),
		}
		if statusCode > 0 {
			span.SetAttributes(attribute.Int("http.response.status_code", statusCode))
		}
		m := metrics()
		m.federationDuration.Record(ctx, time.Since(start).Seconds(), metric.WithAttributes(attrs...))
		m.federationOps.Add(ctx, 1, metric.WithAttributes(attrs...))
		span.End()
	}
}

// StartSearch creates the constrained search span contract. Callers cannot
// add the index name, query body, authorization token or user identifiers.
func StartSearch(ctx context.Context, offset int, limit int) (context.Context, func(int, error)) {
	attrs := []attribute.KeyValue{
		attribute.String("search.backend", "meilisearch"),
		attribute.Int("search.offset", offset),
		attribute.Int("search.limit", limit),
	}
	ctx, span := tracer().Start(ctx, "search", trace.WithAttributes(attrs...))
	start := time.Now()
	return ctx, func(resultCount int, err error) {
		span.SetAttributes(attribute.Int("search.result_count", resultCount))
		if err != nil {
			span.SetStatus(codes.Error, "search failed")
		}
		metricAttrs := append(append([]attribute.KeyValue(nil), attrs...), attribute.Int("search.result_count", resultCount))
		m := metrics()
		m.searchDuration.Record(ctx, time.Since(start).Seconds(), metric.WithAttributes(metricAttrs...))
		m.searchOperations.Add(ctx, 1, metric.WithAttributes(metricAttrs...))
		m.searchResults.Record(ctx, int64(resultCount), metric.WithAttributes(attrs...))
		span.End()
	}
}

// InjectHTTPHeaders injects the configured W3C trace context without adding
// any request metadata to spans.
func InjectHTTPHeaders(ctx context.Context, header http.Header) {
	if header == nil {
		return
	}
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(header))
}

// NewHTTPTransport provides redacted outbound HTTP spans and W3C propagation.
// It records the peer host but never URL paths, query strings or headers.
func NewHTTPTransport(base http.RoundTripper, component string) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return &redactedHTTPTransport{base: base, component: safeLabel(component)}
}

type redactedHTTPTransport struct {
	base      http.RoundTripper
	component string
}

func (transport *redactedHTTPTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req == nil {
		return transport.base.RoundTrip(req)
	}
	method := safeLabel(req.Method)
	attrs := []attribute.KeyValue{attribute.String("http.request.method", method)}
	if req.URL != nil {
		if host := strings.TrimSpace(req.URL.Hostname()); host != "" {
			attrs = append(attrs, attribute.String("server.address", host))
		}
		if port, err := strconv.Atoi(req.URL.Port()); err == nil && port > 0 {
			attrs = append(attrs, attribute.Int("server.port", port))
		}
	}
	ctx, span := tracer().Start(req.Context(), transport.component+".http",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(attrs...),
	)
	clone := req.Clone(ctx)
	InjectHTTPHeaders(ctx, clone.Header)
	response, err := transport.base.RoundTrip(clone)
	if err != nil {
		span.SetStatus(codes.Error, "outbound request failed")
	} else if response != nil {
		span.SetAttributes(attribute.Int("http.response.status_code", response.StatusCode))
		if response.StatusCode >= http.StatusInternalServerError {
			span.SetStatus(codes.Error, "outbound request failed")
		}
	}
	span.End()
	return response, err
}

func safeLabel(value string) string {
	value = strings.TrimSpace(value)
	var builder strings.Builder
	for _, r := range value {
		if builder.Len() >= 80 {
			break
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("._:/-", r) {
			builder.WriteRune(r)
		} else {
			builder.WriteByte('_')
		}
	}
	return builder.String()
}

type statusResponseWriter struct {
	http.ResponseWriter
	status int
}

func (writer *statusResponseWriter) Unwrap() http.ResponseWriter {
	return writer.ResponseWriter
}

func (writer *statusResponseWriter) WriteHeader(statusCode int) {
	if writer.status == 0 {
		writer.status = statusCode
	}
	writer.ResponseWriter.WriteHeader(statusCode)
}

func (writer *statusResponseWriter) Write(data []byte) (int, error) {
	if writer.status == 0 {
		writer.status = http.StatusOK
	}
	return writer.ResponseWriter.Write(data)
}

func (writer *statusResponseWriter) Flush() {
	if writer.status == 0 {
		writer.status = http.StatusOK
	}
	if flusher, ok := writer.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (writer *statusResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := writer.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	return hijacker.Hijack()
}

func (writer *statusResponseWriter) Push(target string, options *http.PushOptions) error {
	if pusher, ok := writer.ResponseWriter.(http.Pusher); ok {
		return pusher.Push(target, options)
	}
	return http.ErrNotSupported
}

func (writer *statusResponseWriter) ReadFrom(reader io.Reader) (int64, error) {
	if writer.status == 0 {
		writer.status = http.StatusOK
	}
	if readerFrom, ok := writer.ResponseWriter.(io.ReaderFrom); ok {
		return readerFrom.ReadFrom(reader)
	}
	return io.Copy(struct{ io.Writer }{writer.ResponseWriter}, reader)
}
