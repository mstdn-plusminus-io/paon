FROM golang:1.25-bookworm AS go-builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal

RUN go list -mod=mod ./cmd/paon ./cmd/paon-admin ./cmd/paon-cutover ./cmd/paon-meili-deploy ./cmd/paon-migrate >/dev/null
RUN CGO_ENABLED=0 go build -mod=mod -trimpath -ldflags="-s -w" -o /out/paon ./cmd/paon && \
	CGO_ENABLED=0 go build -mod=mod -trimpath -ldflags="-s -w" -o /out/paon-admin ./cmd/paon-admin && \
	CGO_ENABLED=0 go build -mod=mod -trimpath -ldflags="-s -w" -o /out/paon-cutover ./cmd/paon-cutover && \
	CGO_ENABLED=0 go build -mod=mod -trimpath -ldflags="-s -w" -o /out/paon-meili-deploy ./cmd/paon-meili-deploy && \
	CGO_ENABLED=0 go build -mod=mod -trimpath -ldflags="-s -w" -o /out/paon-migrate ./cmd/paon-migrate

FROM node:22-bookworm-slim AS assets

WORKDIR /src

ENV RAILS_ENV=production
ENV NODE_ENV=production
ENV YARN_PRODUCTION=false

COPY package.json yarn.lock ./
RUN corepack enable && yarn install --pure-lockfile --production=false

COPY . .
RUN rm -rf public/packs public/packs-test && yarn build:production

FROM debian:bookworm-slim

ENV NODE_ENV=production
ENV BIND=0.0.0.0
ENV PORT=3000
ENV PAON_PUBLIC_DIR=/opt/mastodon/public
ENV DB_PORT=5432
ENV DB_NAME=mastodon
ENV DB_USER=postgres
ENV DB_PASS=postgres
ENV REDIS_PORT=6379

ARG UID=991
ARG GID=991

RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends ca-certificates ffmpeg pamtester tzdata tini wget; \
    groupadd --gid "${GID}" mastodon; \
    useradd --home-dir /opt/mastodon --create-home --uid "${UID}" --gid "${GID}" mastodon; \
    mkdir -p /opt/mastodon/public/system /opt/mastodon/tmp; \
    chown -R mastodon:mastodon /opt/mastodon; \
    apt-get clean; \
    rm -rf /var/lib/apt/lists/*

COPY --from=go-builder --chown=mastodon:mastodon /out/paon /usr/local/bin/paon
COPY --from=go-builder --chown=mastodon:mastodon /out/paon-admin /usr/local/bin/paon-admin
COPY --from=go-builder --chown=mastodon:mastodon /out/paon-cutover /usr/local/bin/paon-cutover
COPY --from=go-builder --chown=mastodon:mastodon /out/paon-meili-deploy /usr/local/bin/paon-meili-deploy
COPY --from=go-builder --chown=mastodon:mastodon /out/paon-migrate /usr/local/bin/paon-migrate
COPY --from=assets --chown=mastodon:mastodon /src/public /opt/mastodon/public
COPY --from=assets --chown=mastodon:mastodon /src/config/locales /opt/mastodon/config/locales

RUN set -eux; \
    mkdir -p /opt/mastodon/public/system /opt/mastodon/tmp; \
    chown -R mastodon:mastodon /opt/mastodon/public/system /opt/mastodon/tmp

USER mastodon
WORKDIR /opt/mastodon

ENTRYPOINT ["/usr/bin/tini", "--"]
CMD ["paon"]

EXPOSE 3000

HEALTHCHECK --interval=30s --timeout=10s --start-period=30s --retries=3 CMD if [ "${PAON_PROCESS_ROLE:-all}" = "worker" ]; then exit 0; fi; wget -q --spider --proxy=off "http://127.0.0.1:${PORT:-3000}/health/ready" || exit 1
