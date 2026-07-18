package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestDockerfileKeepsRunnableDropInRuntime(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)

	for _, want := range []string{
		`FROM golang:1.25-bookworm AS go-builder`,
		`COPY go.mod go.sum ./`,
		`RUN go mod download`,
		`RUN go list -mod=mod ./cmd/paon ./cmd/paon-admin ./cmd/paon-cutover ./cmd/paon-meili-deploy ./cmd/paon-migrate >/dev/null`,
		`CGO_ENABLED=0 go build -mod=mod -trimpath -ldflags="-s -w" -o /out/paon ./cmd/paon`,
		`CGO_ENABLED=0 go build -mod=mod -trimpath -ldflags="-s -w" -o /out/paon-admin ./cmd/paon-admin`,
		`CGO_ENABLED=0 go build -mod=mod -trimpath -ldflags="-s -w" -o /out/paon-cutover ./cmd/paon-cutover`,
		`CGO_ENABLED=0 go build -mod=mod -trimpath -ldflags="-s -w" -o /out/paon-migrate ./cmd/paon-migrate`,
		`CGO_ENABLED=0 go build -mod=mod -trimpath -ldflags="-s -w" -o /out/paon-meili-deploy ./cmd/paon-meili-deploy`,
		`FROM node:22-bookworm-slim AS assets`,
		`COPY package.json yarn.lock ./`,
		`RUN corepack enable && yarn install --pure-lockfile --production=false`,
		`RUN rm -rf public/packs public/packs-test && yarn build:production`,
		`ENV PAON_PUBLIC_DIR=/opt/mastodon/public`,
		`apt-get install -y --no-install-recommends ca-certificates ffmpeg pamtester tzdata tini wget`,
		`COPY --from=assets --chown=mastodon:mastodon /src/public /opt/mastodon/public`,
		`COPY --from=assets --chown=mastodon:mastodon /src/config/locales /opt/mastodon/config/locales`,
		`COPY --from=go-builder --chown=mastodon:mastodon /out/paon /usr/local/bin/paon`,
		`COPY --from=go-builder --chown=mastodon:mastodon /out/paon-meili-deploy /usr/local/bin/paon-meili-deploy`,
		`COPY --from=go-builder --chown=mastodon:mastodon /out/paon-cutover /usr/local/bin/paon-cutover`,
		`COPY --from=go-builder --chown=mastodon:mastodon /out/paon-admin /usr/local/bin/paon-admin`,
		`COPY --from=go-builder --chown=mastodon:mastodon /out/paon-migrate /usr/local/bin/paon-migrate`,
		`USER mastodon`,
		`ENTRYPOINT ["/usr/bin/tini", "--"]`,
		`CMD ["paon"]`,
		`HEALTHCHECK --interval=30s --timeout=10s --start-period=30s --retries=3 CMD if [ "${PAON_PROCESS_ROLE:-all}" = "worker" ]; then exit 0; fi; wget -q --spider --proxy=off "http://127.0.0.1:${PORT:-3000}/health/ready" || exit 1`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("Dockerfile missing %q", want)
		}
	}
	for _, want := range []string{
		`if [ "${PAON_PROCESS_ROLE:-all}" = "worker" ]; then exit 0; fi`,
		`wget -q --spider --proxy=off "http://127.0.0.1:${PORT:-3000}/health/ready" || exit 1`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("Dockerfile healthcheck is not role-aware: missing %q", want)
		}
	}
	if strings.Contains(body, "COPY vendor") || strings.Contains(body, "-mod=vendor") {
		t.Fatal("Dockerfile must not depend on a checked-in vendor/ tree")
	}
	for _, forbidden := range []string{"FROM ruby:", "bundle exec", "EXPOSE 3000 4000"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("Dockerfile retains Rails runtime contract %q", forbidden)
		}
	}
}

func TestDockerfileGoBuilderMatchesGoModToolchain(t *testing.T) {
	dockerRaw, err := os.ReadFile(filepath.Join("..", "..", "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	modRaw, err := os.ReadFile(filepath.Join("..", "..", "go.mod"))
	if err != nil {
		t.Fatal(err)
	}

	goModVersion := regexp.MustCompile(`(?m)^go\s+([0-9]+\.[0-9]+)`).FindStringSubmatch(string(modRaw))
	if len(goModVersion) != 2 {
		t.Fatalf("go.mod does not declare a Go toolchain version:\n%s", string(modRaw))
	}
	if !strings.Contains(string(dockerRaw), "FROM golang:"+goModVersion[1]) {
		t.Fatalf("Dockerfile builder does not match go.mod Go %s", goModVersion[1])
	}
}
