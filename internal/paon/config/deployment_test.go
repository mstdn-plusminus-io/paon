package config

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestPaonGoDockerfileBuildsAssetsAndRuntimeSeparately(t *testing.T) {
	raw, err := os.ReadFile("../../../Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, want := range []string{
		`ARG BASE_REGISTRY=docker.io`,
		`FROM ${BASE_REGISTRY}/golang:1.25.12-trixie AS go-builder`,
		`apt-get install -y --no-install-recommends libvips-dev pkg-config`,
		`FROM ${BASE_REGISTRY}/node:22-bookworm-slim AS assets`,
		`FROM ${BASE_REGISTRY}/debian:trixie-slim`,
		`ARG SOURCE_COMMIT=""`,
		`ENV SOURCE_COMMIT=${SOURCE_COMMIT}`,
		`COPY go.mod go.sum ./`,
		`RUN go mod download`,
		`RUN go list -mod=mod ./cmd/paon ./cmd/paon-admin ./cmd/paon-cutover ./cmd/paon-meili-deploy ./cmd/paon-migrate >/dev/null`,
		`ARG PAON_IMAGE_PROCESSOR=auto`,
		`CGO_ENABLED=0 go build`,
		`CGO_ENABLED=1 go build -tags=libvips`,
		`level=WARN event=image_processor_fallback processor=libvips fallback=go-native phase=docker_builder`,
		`yarn build:production`,
		`COPY --from=go-builder /out /tmp/paon-build`,
		`install -m 0755 "/tmp/paon-build/${image_processor}/${command}" "/usr/local/bin/${command}"`,
		`COPY --from=assets --chown=mastodon:mastodon /src/public /opt/mastodon/public`,
		`apt-get install -y --no-install-recommends ca-certificates ffmpeg pamtester tzdata tini wget`,
		`apt-get install -y --no-install-recommends libvips42t64`,
		`level=WARN event=image_processor_fallback processor=libvips fallback=go-native phase=docker_runtime`,
		`USER mastodon`,
		`ENTRYPOINT ["/usr/bin/tini", "--"]`,
		`CMD ["paon"]`,
		`/health/ready`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("Dockerfile missing runtime contract %q", want)
		}
	}
	copyPublic := strings.Index(body, `COPY --from=assets --chown=mastodon:mastodon /src/public /opt/mastodon/public`)
	recreateSystem := strings.LastIndex(body, `mkdir -p /opt/mastodon/public/system /opt/mastodon/tmp`)
	if copyPublic < 0 || recreateSystem < copyPublic {
		t.Fatal("Dockerfile must recreate public/system after copying built UI assets")
	}
	if strings.Contains(body, `COPY vendor`) || strings.Contains(body, `-mod=vendor`) {
		t.Fatal("Dockerfile must not depend on a checked-in vendor/ tree")
	}
}

func TestPaonGoTaskfileExposesDocumentedCheckConfigTask(t *testing.T) {
	raw, err := os.ReadFile("../../../Taskfile.yml")
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, want := range []string{
		`check-config:bin:`,
		`deps:`,
		`- build`,
		`bin/paon --check-config`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("Taskfile.yml missing documented check-config contract %q", want)
		}
	}
}

func TestDocumentedPaonGoTaskInvocationsExistInTaskfile(t *testing.T) {
	taskNames := documentedTaskfileTaskNames(t)
	for _, path := range []string{"../../../README.md", "../../../docs/paon-go.md"} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(raw), "\n") {
			fields := strings.Fields(strings.TrimSpace(line))
			if len(fields) < 2 || fields[0] != "task" {
				continue
			}
			name := fields[1]
			if !taskNames[name] {
				t.Fatalf("%s documents missing Taskfile task %q", path, name)
			}
		}
	}
}

func documentedTaskfileTaskNames(t *testing.T) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile("../../../Taskfile.yml")
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, match := range regexp.MustCompile(`(?m)^  ([A-Za-z0-9:_-]+):\s*$`).FindAllStringSubmatch(string(raw), -1) {
		names[match[1]] = true
	}
	if len(names) == 0 {
		t.Fatal("Taskfile.yml task parser found no tasks")
	}
	return names
}

func TestPaonGoComposeDefinesOnlyGoRuntimeServices(t *testing.T) {
	raw, err := os.ReadFile("../../../docker-compose.yml")
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, want := range []string{
		`image: plusminusio/paon:latest`,
		`command: paon`,
		`PAON_PROCESS_ROLE: web`,
		`worker:`,
		`PAON_PROCESS_ROLE: worker`,
		`PAON_PUBLIC_DIR: /opt/mastodon/public`,
		`DATABASE_URL: ''`,
		`DB_HOST: db`,
		`DB_SSLMODE: disable`,
		`REDIS_URL: ''`,
		`REDIS_HOST: redis`,
		`MEILI_HOST: http://meilisearch:7700`,
		`- ./public/system:/opt/mastodon/public/system`,
		`wget -q --spider --proxy=off localhost:3000/health/ready || exit 1`,
		`db-migrate:`,
		`command: paon-migrate`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("docker-compose.yml missing Go runtime contract %q", want)
		}
	}
	workerBlock := composeServiceBlock(t, body, "worker", "db-migrate")
	for _, want := range []string{
		`PAON_PROCESS_ROLE: worker`,
		`healthcheck:`,
		`disable: true`,
	} {
		if !strings.Contains(workerBlock, want) {
			t.Fatalf("docker-compose.yml worker service missing role-aware healthcheck contract %q", want)
		}
	}
	if strings.Contains(workerBlock, `/health/ready`) || strings.Contains(workerBlock, `localhost:3000`) {
		t.Fatalf("worker service must not inherit an HTTP readiness healthcheck because PAON_PROCESS_ROLE=worker opens no listener:\n%s", workerBlock)
	}
	for _, forbidden := range []string{"bundle exec", "node ./streaming", "sidekiq:", "streaming:"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("docker-compose.yml retains legacy runtime contract %q", forbidden)
		}
	}
}

func composeServiceBlock(t *testing.T, body string, service string, nextService string) string {
	t.Helper()
	startMarker := "\n  " + service + ":\n"
	start := strings.Index(body, startMarker)
	if start < 0 {
		t.Fatalf("docker-compose.yml missing service %s", service)
	}
	rest := body[start+len(startMarker):]
	endMarker := "\n  " + nextService + ":\n"
	end := strings.Index(rest, endMarker)
	if end < 0 {
		t.Fatalf("docker-compose.yml service %s missing following service %s", service, nextService)
	}
	return rest[:end]
}

func TestGoDeploymentProcessBoundariesStayExplicit(t *testing.T) {
	procfile, err := os.ReadFile("../../../Procfile")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`web: PAON_PROCESS_ROLE=web paon`,
		`worker: PAON_PROCESS_ROLE=worker paon`,
		`release: paon-migrate`,
	} {
		if !strings.Contains(string(procfile), want) {
			t.Fatalf("Procfile deployment boundary changed; missing %q", want)
		}
	}

}

func TestRailsPlatformAppManifestsKeepDatabaseAndRedisContracts(t *testing.T) {
	for _, path := range []string{"../../../app.json", "../../../scalingo.json"} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		body := string(raw)
		for _, want := range []string{
			`"LOCAL_DOMAIN"`,
			`"SECRET_KEY_BASE"`,
			`"ACTIVE_RECORD_ENCRYPTION_DETERMINISTIC_KEY"`,
			`"ACTIVE_RECORD_ENCRYPTION_KEY_DERIVATION_SALT"`,
			`"ACTIVE_RECORD_ENCRYPTION_PRIMARY_KEY"`,
			`"SINGLE_USER_MODE"`,
			`"S3_ENABLED"`,
			`"SMTP_SERVER"`,
			`"postdeploy": "paon-migrate"`,
		} {
			if !strings.Contains(body, want) {
				t.Fatalf("%s platform manifest changed; missing %q", path, want)
			}
		}
	}
	app, err := os.ReadFile("../../../app.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"addons": ["heroku-postgresql", "heroku-redis"]`, `"url": "heroku/go"`} {
		if !strings.Contains(string(app), want) {
			t.Fatalf("app.json platform manifest changed; missing %q", want)
		}
	}
	scalingo, err := os.ReadFile("../../../scalingo.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"addons": ["postgresql", "redis"]`, `"BUILDPACK_URL"`, `"WITH_FFPROBE"`} {
		if !strings.Contains(string(scalingo), want) {
			t.Fatalf("scalingo.json platform manifest changed; missing %q", want)
		}
	}
}

func TestGoNginxConfigKeepsWebStreamingAndStaticBoundaries(t *testing.T) {
	raw, err := os.ReadFile("../../../dist/nginx.conf")
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, want := range []string{
		`upstream backend {`,
		`server 127.0.0.1:3000 fail_timeout=0;`,
		`client_max_body_size 99m;`,
		`location ^~ /api/v1/streaming`,
		`proxy_read_timeout 3600s;`,
		`location @proxy`,
		`proxy_pass http://backend;`,
		`location ~ ^/system/`,
		`add_header X-Content-Type-Options nosniff;`,
		`add_header Content-Security-Policy "default-src 'none'; form-action 'none'";`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("dist/nginx.conf deployment boundary changed; missing %q", want)
		}
	}
}
