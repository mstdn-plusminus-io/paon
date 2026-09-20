# Paon OpenTelemetry operations

Paon implements Mastodon 4.3's OpenTelemetry direction with the official Go
SDK and OTLP HTTP/protobuf exporters. It remains disabled when no OTLP endpoint
is configured, so an existing deployment keeps its previous behavior.

## Enable OTLP export

Set the base endpoint to export both traces and metrics:

```dotenv
OTEL_EXPORTER_OTLP_ENDPOINT=http://otel-collector:4318
OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf
```

Signal-specific endpoints can instead enable either signal independently:

```dotenv
OTEL_EXPORTER_OTLP_TRACES_ENDPOINT=http://otel-collector:4318/v1/traces
OTEL_EXPORTER_OTLP_METRICS_ENDPOINT=http://otel-collector:4318/v1/metrics
```

Endpoint URLs must be absolute HTTP(S) URLs and cannot contain userinfo, query
parameters, or fragments. Collector credentials belong in
`OTEL_EXPORTER_OTLP_HEADERS`, `OTEL_EXPORTER_OTLP_TRACES_HEADERS`, or
`OTEL_EXPORTER_OTLP_METRICS_HEADERS`. A header variable without a matching base
or signal endpoint is rejected by `paon --check-config`. Paon supports the
`http/protobuf` protocol; configuring `grpc` is rejected instead of silently
starting a mismatched exporter.

Docker Compose passes `.env.production` directly to the `web`, `worker`, and
one-shot migration service. `paon`, `paon-admin`, `paon-migrate`, and
`paon-meili-deploy` all load the same dotenv/config model. Run the check in the
actual container before deployment:

```bash
docker compose run --rm web paon --check-config
docker compose run --rm worker paon --check-config
```

For secret-safe diagnostics, report presence and length, never values:

```bash
for name in OTEL_EXPORTER_OTLP_ENDPOINT OTEL_EXPORTER_OTLP_HEADERS; do
  if test -n "${!name+x}"; then
    value=${!name}
    printf '%s present=true length=%d\n' "$name" "${#value}"
    unset value
  else
    printf '%s present=false length=0\n' "$name"
  fi
done
```

Use an equivalent platform-native presence/length check when the shell does
not support indirect expansion. Do not attach `.env.production`, exporter
headers, bearer tokens, or collector request dumps to release evidence.

## Service identity, sampling, and propagation

Mastodon 4.3's defaults are preserved:

```dotenv
OTEL_SERVICE_NAME_PREFIX=mastodon
OTEL_SERVICE_NAME_SEPARATOR=/
OTEL_TRACES_SAMPLER=parentbased_always_on
OTEL_TRACES_SAMPLER_ARG=
OTEL_PROPAGATORS=tracecontext,baggage
```

The resulting services are `mastodon/web`, `mastodon/worker`, and
`mastodon/web-worker` for Paon's combined process. Operational children use
`mastodon/paon-admin`, `mastodon/paon-migrate`, and
`mastodon/paon-meili-deploy`. Supported samplers are `always_on`, `always_off`,
`traceidratio`, and their `parentbased_` variants. Ratios must be between 0 and

1. To opt into 10% ratio sampling, set
   `OTEL_TRACES_SAMPLER=parentbased_traceidratio` and
   `OTEL_TRACES_SAMPLER_ARG=0.10`; this is not the Mastodon default. Supported
   propagators are `tracecontext`, `baggage`, and standalone `none`.

Incoming HTTP `traceparent` is extracted, ActivityPub ingress jobs carry only
the configured propagation headers in Asynq metadata, and outbound federation
and Meilisearch HTTP requests receive the same carrier. Baggage is propagated
but never copied into span attributes; do not place credentials or personal
data in baggage.

## Instrumented boundaries

| Boundary                  | Span data                                          | Metrics                                 |
| ------------------------- | -------------------------------------------------- | --------------------------------------- |
| Echo HTTP                 | method, registered route template, response status | request count and duration              |
| Asynq worker              | task type, logical queue, outcome                  | job count and duration                  |
| GORM/PostgreSQL           | operation verb and outcome                         | operation count and duration            |
| Redis                     | command verb and outcome                           | command count and duration              |
| Federation inbox/outbound | direction, outcome, response status when available | operation count and duration            |
| Meilisearch search        | only backend, offset, limit, result count          | operation count, duration, result count |

`/health` is intentionally untraced. Search spans have exactly
`search.backend`, `search.offset`, `search.limit`, and `search.result_count`;
the index and query are not attributes.

## Redaction contract

Telemetry never records:

- URL paths for outbound calls, URL query strings, form/query values, or raw
  request targets;
- `Authorization`, cookies, OTLP headers, API keys, access tokens, or signed
  ActivityPub headers;
- HTTP/ActivityPub bodies, Asynq payloads or task IDs;
- SQL text, bind values, database URLs, Redis keys/arguments/results;
- Meilisearch query text, index names, master keys, account/status IDs, e-mail
  addresses, or actor/inbox URLs;
- raw error strings, because upstream errors can embed sensitive values.

Errors are represented only by bounded outcome/status fields. Preserve this
allowlist when adding instrumentation and add a span-recorder redaction test.

## StatsD compatibility extension

`STATSD_ADDR`, `STATSD_NAMESPACE`, and `STATSD_SIDEKIQ` remain available only
for Paon deployments that need the old UDP compatibility stream. This is a
Paon extension, not the Mastodon 4.3 observability contract. When any OTLP
endpoint enables OpenTelemetry, Paon disables all StatsD web, worker, DB, and
cache emissions and logs a configuration warning. This prevents the same
operation from being counted twice.

## Release checks

Before publishing an image, record command, date, tool version, exit status,
and a redacted summary for:

```bash
task test:rtk
govulncheck ./...
yarn audit --groups dependencies
docker build .
trivy image plusminusio/paon:latest
```

Use repository-local `TMPDIR`, `GOCACHE`, and `GOMODCACHE`. A network-, Docker-,
or registry-blocked check is not a pass: record it as not executed with the
exact non-secret failure reason in the 4.3.23 release evidence. The current
record is [Mastodon 4.3.23 dependency and security evidence](release-evidence/4.3.23-dependency-security.md).
