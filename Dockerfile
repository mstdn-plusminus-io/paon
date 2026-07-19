FROM golang:1.25-trixie AS go-builder

WORKDIR /src

ARG PAON_IMAGE_PROCESSOR=auto

RUN set -eux; \
	mkdir -p /out/native /out/libvips; \
	apt-get update; \
	if [ "${PAON_IMAGE_PROCESSOR}" != "native" ] && apt-get install -y --no-install-recommends libvips-dev pkg-config; then \
		echo libvips > /out/image-processor; \
	else \
		echo 'level=WARN event=image_processor_fallback processor=libvips fallback=go-native phase=docker_builder'; \
		echo native > /out/image-processor; \
	fi; \
	rm -rf /var/lib/apt/lists/*

COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal

RUN go list -mod=mod ./cmd/paon ./cmd/paon-admin ./cmd/paon-cutover ./cmd/paon-meili-deploy ./cmd/paon-migrate >/dev/null
RUN set -eux; \
	for command in paon paon-admin paon-cutover paon-meili-deploy paon-migrate; do \
		CGO_ENABLED=0 go build -mod=mod -trimpath -ldflags="-s -w" -o "/out/native/${command}" "./cmd/${command}"; \
	done; \
	if [ "$(cat /out/image-processor)" = libvips ]; then \
		for command in paon paon-admin paon-cutover paon-meili-deploy paon-migrate; do \
			CGO_ENABLED=1 go build -tags=libvips -mod=mod -trimpath -ldflags="-s -w" -o "/out/libvips/${command}" "./cmd/${command}"; \
		done; \
	else \
		cp /out/native/* /out/libvips/; \
	fi

FROM node:22-bookworm-slim AS assets

WORKDIR /src

ENV RAILS_ENV=production
ENV NODE_ENV=production
ENV YARN_PRODUCTION=false

COPY package.json yarn.lock ./
RUN corepack enable && yarn install --pure-lockfile --production=false

COPY . .
RUN rm -rf public/packs public/packs-test && yarn build:production

FROM debian:trixie-slim

ENV RAILS_ENV=production
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

COPY --from=go-builder /out /tmp/paon-build

RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends ca-certificates ffmpeg pamtester tzdata tini wget; \
    image_processor=native; \
    if [ "$(cat /tmp/paon-build/image-processor)" = libvips ] && apt-get install -y --no-install-recommends libvips42t64; then \
        image_processor=libvips; \
    else \
        echo 'level=WARN event=image_processor_fallback processor=libvips fallback=go-native phase=docker_runtime'; \
    fi; \
    for command in paon paon-admin paon-cutover paon-meili-deploy paon-migrate; do \
        install -m 0755 "/tmp/paon-build/${image_processor}/${command}" "/usr/local/bin/${command}"; \
    done; \
    rm -rf /tmp/paon-build; \
    groupadd --gid "${GID}" mastodon; \
    useradd --home-dir /opt/mastodon --create-home --uid "${UID}" --gid "${GID}" mastodon; \
    mkdir -p /opt/mastodon/public/system /opt/mastodon/tmp; \
    chown -R mastodon:mastodon /opt/mastodon; \
    apt-get clean; \
    rm -rf /var/lib/apt/lists/*

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
