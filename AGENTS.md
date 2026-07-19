# AGENTS.md

## Project

Paon is a Go 1.25 + labstack/echo/v5 drop-in replacement for this Mastodon fork. It preserves the existing PostgreSQL schema, REST and ActivityPub behavior, and the React/Rspack UI. Background work uses Asynq. SSE and WebSocket streaming are served by the Go web process on port 3000.

The removed Rails implementation is available only in Git history. Do not reintroduce Ruby, Rails, Sidekiq, or the standalone Node streaming server.

## Main directories

- `cmd/paon`: web/worker process
- `cmd/paon-admin`: administrative CLI
- `cmd/paon-migrate`: fresh-schema initialization and validation
- `cmd/paon-meili-deploy`: Meilisearch rebuild command
- `internal/paon/api`: REST, ActivityPub, HTML, streaming, and workers
- `internal/paon/models`: existing-schema GORM models
- `internal/paon/migrate/schema.sql`: embedded authoritative schema snapshot
- `app/javascript`: existing React UI
- `config/locales`: server-rendered locale dictionaries

## Setup and development

```bash
yarn install --frozen-lockfile
cp .env.sample .env
yarn docker:dev up -d
task db:migrate
task dev
```

`task dev` builds UI assets, watches the Go backend, and watches Rspack. The Go process serves HTTP, REST, ActivityPub, SSE, and WebSocket traffic on port 3000.

## Build and test

```bash
task build
task test:rtk
yarn build:production
docker compose config
docker build .
```

Use `task test:integration` for PostgreSQL/Redis integration tests and `task test:external` for Meilisearch, object storage, SMTP, and media-tool coverage.

## Database

GORM AutoMigrate is disabled. `paon-migrate` applies `internal/paon/migrate/schema.sql` only to an empty database and validates supported existing schemas. Any schema change requires reviewed SQL, an explicit upgrade path, and PostgreSQL integration tests.

## Constraints

- Preserve existing API, ActivityPub, UI, and PostgreSQL contracts.
- Keep frontend display text localized.
- Keep web and worker roles independently runnable.
- Do not add a separate streaming process or port 4000 dependency.
- Do not use a Makefile.
