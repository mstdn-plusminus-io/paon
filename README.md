![](./app/javascript/images/logo_full.svg)

[![GitHub release](https://img.shields.io/github/release/mstdn-plusminus-io/paon.svg)][releases]
[![build latest image](https://github.com/mstdn-plusminus-io/paon/actions/workflows/latest.yml/badge.svg?branch=master)](https://github.com/mstdn-plusminus-io/paon/actions/workflows/latest.yml)
[![build staging image](https://github.com/mstdn-plusminus-io/paon/actions/workflows/staging.yml/badge.svg?branch=staging)](https://github.com/mstdn-plusminus-io/paon/actions/workflows/staging.yml)
[![Docker Pulls](https://img.shields.io/docker/pulls/plusminusio/paon.svg)][docker]

[releases]: https://github.com/mstdn-plusminus-io/paon/releases
[docker]: https://hub.docker.com/r/plusminusio/paon/

Paon -ぱおん- is a Go + labstack/echo/v5 drop-in replacement for this Mastodon fork. It keeps the existing PostgreSQL schema, REST/ActivityPub contracts, and UI assets while replacing the Rails, Sidekiq, and standalone Node streaming runtimes.

The former Ruby application, RSpec suite, Sidekiq workers, Rails migrations, and standalone Node streaming server have been removed. The existing database contract is represented by Go models, startup schema guards, and the embedded PostgreSQL snapshot in `internal/paon/migrate/schema.sql`.

## Additional and/or changed features

### Toot

- Toot length limit is increase to 5000 characters
- Support quote (compatible; Mastodon 4.4.x, Misskey, maybe Fedibird)

### User interfaces

- Add theme of Slack like user interfaces
- Spoiler message preset
- Side navigation in right side or left side on phone
- Show relative time or absolute time in toot timeline
- Toot button position on phone
- Plain text or render GitHub Flavored Markdown (experimental)
- Preview search box by Misskey Flavored Markdown
- Show original post link in toot timeline
- ... and more!

### Server

- Configurable use [Cloudflare Turnstile](https://www.cloudflare.com/ja-jp/products/turnstile/) at signup
  - `CLOUDFLARE_TURNSTILE_ENABLED=true`
  - `CLOUDFLARE_TURNSTILE_SITE_KEY=1x00000000000000000000AA`
  - `CLOUDFLARE_TURNSTILE_SECRET_KEY=1x0000000000000000000000000000000AA`
- Configurable enable or disable signup by REST API
  - `DISABLE_SIGNUP_BY_API=true`
- Configurable enable or disable remote media cache like Pleroma
  - `DISABLE_REMOTE_MEDIA_CACHE=true`

## Start develop

Before developing, you need to install the following software.

- Go 1.25.x
- Node.js 22.x
- Yarn 1.22.x

Then run the following commands.

```sh
yarn install --frozen-lockfile
cp .env.sample .env
task db:migrate
task dev
```

## Go runtime

`paon` uses the existing Mastodon PostgreSQL schema and serves the existing built UI assets. The same web listener on port 3000 serves HTML, REST, ActivityPub, SSE, and WebSocket traffic; the worker role runs Asynq jobs.

Build and validate the local binaries:

```sh
task build
task check-config:bin
task meili-deploy:check-config:bin
```

Run it directly against an existing development database:

```sh
export DATABASE_URL=postgres://mastodon:mastodon@localhost:5432/mastodon_development?sslmode=disable
export LOCAL_DOMAIN=localhost:3000
export PAON_GO_ADDR=:3000
export PAON_PROCESS_ROLE=all

task check-config
task run
```

For local development with automatic backend restarts and frontend rebuilds, install the existing Yarn dependencies and run:

```sh
yarn install --frozen-lockfile
task dev
```

`task dev` first writes a complete development build to `public/packs`, then runs the Go backend watcher and Rspack watcher together. Changes to `public/packs/manifest.json` restart the backend so server-rendered pages use the latest hashed asset names. In `RAILS_ENV=development`, Paon preserves Rails 7.2's default authorization for `.localhost`, `.test`, IPv4/IPv6 hosts, and `RAILS_DEVELOPMENT_HOSTS`, in addition to this fork's configured instance domains.

Or replace the Rails `web` service in the existing Compose stack:

```sh
task compose:db-migrate
task compose:config
task compose:check-config
task compose:up
```

See [docs/paon-go.md](docs/paon-go.md) for compatibility notes, environment variable precedence, Meilisearch rebuild commands, readiness checks, and the currently implemented API surface.

## License

```
Copyright (C) Paon contributors  
Copyright (C) 2016-2023 Eugen Rochko & other Mastodon and contributors (see [AUTHORS.md](AUTHORS.md))

This program is free software: you can redistribute it and/or modify it under the terms of the GNU Affero General Public License as published by the Free Software Foundation, either version 3 of the License, or (at your option) any later version.

This program is distributed in the hope that it will be useful, but WITHOUT ANY WARRANTY; without even the implied warranty of MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License along with this program. If not, see <https://www.gnu.org/licenses/>.
```
