# Paon Go runtime

Paon is a Go 1.25 + labstack/echo/v5 drop-in replacement for this Mastodon fork. It preserves the existing PostgreSQL model, REST and ActivityPub contracts, and React UI while replacing Rails, Sidekiq, and the standalone Node streaming service.

## Process model

`PAON_PROCESS_ROLE` controls the process boundary:

| Value | HTTP | Asynq workers and schedulers |
|---|---:|---:|
| `all` (default) | yes | yes |
| `web` | yes | no |
| `worker` | no | yes |

The web process serves HTML, REST, ActivityPub, SSE, and WebSocket traffic on the same listener. `PAON_GO_ADDR` is the explicit listen override; otherwise `SOCKET` or `BIND`/`PORT` is used. The default TCP port is `3000`.

Worker processes subscribe to all Asynq queues by default. Set `ASYNQ_QUEUES`
to a comma-separated subset of `default`, `push`, `ingress`, `mailers`, `pull`,
`removal`, and `remote_removal` to dedicate a process to specific queues. The
removal queues contain potentially high-volume status deletion work. Local
removal has weight 2 and `remote_removal` has the lowest weight 1. Keep both out
of the normal worker's queue list and run a dedicated removal worker when strict
capacity isolation is required. `REDIS_NAMESPACE` is applied automatically, so
queue names remain logical:

```bash
PAON_PROCESS_ROLE=worker ASYNQ_QUEUES=default,push,ingress,mailers,pull ASYNQ_CONCURRENCY=20 paon
PAON_PROCESS_ROLE=worker ASYNQ_QUEUES=removal,remote_removal ASYNQ_CONCURRENCY=2 paon
```

Leaving `ASYNQ_QUEUES` unset still consumes every queue, including both removal
queues, with one shared concurrency limit. Existing removal tasks already queued
on `default` or `removal` are not migrated automatically; only newly enqueued
remote tasks use `remote_removal`.

When `STREAMING_API_BASE_URL` is unset, streaming uses `ws://LOCAL_DOMAIN` in development and `ws://` or `wss://WEB_DOMAIN` in production. A separate port 4000 process is neither required nor supported.

## Local development

The optimized image backend uses CGO through `github.com/cshum/vipsgen/vips816`.
Install libvips 8.16.1 or newer, pkg-config, FFmpeg, and ffprobe before building.
On macOS:

```bash
brew install vips pkg-config ffmpeg
```

Taskfile builds automatically enable the optimized backend when `pkg-config` finds
libvips. For direct `go` commands, set `GOFLAGS=-tags=libvips`. Builds without that
tag remain fully Go-native. If libvips is not compiled in or a libvips image
operation fails, Paon retries with the Go-native processor and emits a
`level=WARN event=image_processor_fallback` log entry. The fallback registers
CGo-free AVIF and HEIC decoders backed by embedded WASM, so those accepted upload
formats do not require a system codec library.

```bash
yarn install --frozen-lockfile
cp .env.sample .env
yarn docker:dev up -d
task db:migrate
task dev
```

`task dev` performs an initial development asset build and then runs the Go/Air backend watcher with the Rspack watcher. For a single non-watched process:

```bash
task check-config
task run
```

## Build and test

```bash
task build
task test:rtk
yarn build:production
```

`task build` creates:

- `bin/paon`
- `bin/paon-admin`
- `bin/paon-cutover`
- `bin/paon-meili-deploy`
- `bin/paon-migrate`

Real dependency suites are available through:

```bash
task test:integration
task test:external
```

## Containers

The standard `Dockerfile` builds both Go-native and libvips Go binaries plus Rspack
assets in separate stages, then selects libvips when both build and runtime packages
are available. If a base-image update makes libvips unavailable, the image selects
the Go-native binary and writes a WARN fallback message during the build. Ruby,
Bundler, Rails, Sidekiq, Node.js, and frontend source are absent from the runtime
image. Set `--build-arg PAON_IMAGE_PROCESSOR=native` to force the fallback image.

```bash
task compose:config
task compose:db-migrate
task compose:check-config
task compose:up
```

The standard `docker-compose.yml` defines PostgreSQL, Redis, Meilisearch, Go web, Go worker, and an opt-in migration service. Persistent media uses `public/system`.

## Database lifecycle

GORM AutoMigrate is disabled. `internal/paon/migrate/schema.sql` is embedded into `paon-migrate`.

- Empty database: the complete compatible schema and seed rows are created atomically.
- Supported existing version: the full schema guard runs without modifying data.
- Partial or unsupported version: startup and migration are refused.

See `docs/paon-go-schema-compatibility.md` for schema change requirements.

## Runtime validation

```bash
task check-config:bin
task meili-deploy:check-config:bin
```

`--check-config` validates environment values, PostgreSQL, schema shape/version, Redis, enabled Meilisearch, UI pack assets, locale dictionaries, media tools, and optional integrations before opening the HTTP listener.

`/health` is a lightweight liveness endpoint. `/health/ready` checks the deployable runtime and is used by Docker and Compose. Worker-only containers do not require an HTTP health probe.

## UI and localization

The existing React UI remains under `app/javascript` and is built by Rspack into `public/packs`. Server-rendered pages load dictionaries from `config/locales`. Production startup fails when the pack manifest, required chunks/static assets, or representative locale keys are missing.

The image preserves `public/system` as the local media volume. S3-compatible, Azure, and Swift storage continue to use the existing environment-variable contracts.

## Search

Meilisearch replaces Elasticsearch. Runtime and rebuild commands use `MEILI_ENABLED`, `MEILI_HOST`, `MEILI_MASTER_KEY`, `MEILI_PREFIX`, and `MEILI_LIBRARY_ONLY`.

```bash
task meili-deploy
```

## Operations

Administrative and maintenance commands are documented in `docs/paon-go-operations.md`. Existing Sidekiq queue state can be checked during one-time cutover with `paon-cutover`; normal production operation uses only Asynq.
