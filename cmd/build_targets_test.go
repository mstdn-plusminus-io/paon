package cmd_test

import (
	"os"
	"strings"
	"testing"
)

func TestTaskfileExposesPrimaryBuildAndRTKTestEntrypoints(t *testing.T) {
	src, err := os.ReadFile("../Taskfile.yml")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	for _, want := range []string{
		"GOTMPDIR: '{{.ROOT_DIR}}/.go-tmp'",
		"GOCACHE: '{{.ROOT_DIR}}/.go-buildcache'",
		"GOMODCACHE: '{{.ROOT_DIR}}/.go-modcache'",
		"build:",
		"go build -mod=mod -o bin/paon ./cmd/paon",
		"go build -mod=mod -o bin/paon-admin ./cmd/paon-admin",
		"go build -mod=mod -o bin/paon-cutover ./cmd/paon-cutover",
		"go build -mod=mod -o bin/paon-meili-deploy ./cmd/paon-meili-deploy",
		"go build -mod=mod -o bin/paon-migrate ./cmd/paon-migrate",
		"test:",
		"go test -mod=mod ./...",
		"Run the Paon test suite through rtk",
		"test:rtk:",
		"rtk go test -mod=mod ./...",
		"dev:",
		"task --parallel dev:backend dev:frontend",
		"yarn build:development",
		"yarn build:development --watch",
		"go run github.com/air-verse/air@v1.65.3 -c .air.toml",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("Taskfile.yml missing %q", want)
		}
	}
	if strings.Contains(body, "make paon-go-") {
		t.Fatal("Taskfile.yml must not expose stale Makefile wrappers; Makefile is not part of the Go replacement checkout")
	}
}

func TestCommandDirectoriesDoNotUseGoSuffix(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), "paon-go") {
			t.Fatalf("command source directory must not use the -go suffix: cmd/%s", entry.Name())
		}
	}
}

func TestDevelopmentWatcherBuildsAssetsBeforeStartingBackend(t *testing.T) {
	taskfile, err := os.ReadFile("../Taskfile.yml")
	if err != nil {
		t.Fatal(err)
	}
	body := string(taskfile)
	devStart := strings.Index(body, "  dev:\n")
	buildStart := strings.Index(body, "  build:\n")
	if devStart < 0 || buildStart <= devStart {
		t.Fatal("Taskfile.yml does not define the development task block before build")
	}
	dev := body[devStart:buildStart]
	for _, want := range []string{
		"deps:\n      - dev:assets",
		"task --parallel dev:backend dev:frontend",
		"yarn build:development --watch",
	} {
		if !strings.Contains(dev, want) {
			t.Fatalf("development task block missing %q", want)
		}
	}

	air, err := os.ReadFile("../.air.toml")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`cmd = "go build -mod=mod -o ./tmp/air/paon ./cmd/paon"`,
		`include_file = ["go.mod", "go.sum", "public/packs/manifest.json"]`,
		`send_interrupt = false`,
	} {
		if !strings.Contains(string(air), want) {
			t.Fatalf(".air.toml missing %q", want)
		}
	}
}

func TestComposePreservesEnvFileDatabaseCredentials(t *testing.T) {
	dockerfileBytes, err := os.ReadFile("../Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	dockerfile := string(dockerfileBytes)
	for _, want := range []string{
		"ENV DB_PORT=5432",
		"ENV DB_NAME=mastodon",
		"ENV DB_USER=postgres",
		"ENV DB_PASS=postgres",
		"ENV REDIS_PORT=6379",
		"ca-certificates ffmpeg pamtester tzdata tini wget",
	} {
		if !strings.Contains(dockerfile, want) {
			t.Fatalf("Dockerfile missing %q", want)
		}
	}

	composeBytes, err := os.ReadFile("../docker-compose.yml")
	if err != nil {
		t.Fatal(err)
	}
	compose := string(composeBytes)
	for _, want := range []string{
		"env_file:",
		"path: .env.production",
		`DATABASE_URL: ''`,
		`REDIS_URL: ''`,
		"DB_HOST: db",
		"DB_SSLMODE: disable",
		"REDIS_HOST: redis",
		`REDIS_PASSWORD: ''`,
		"MEILI_ENABLED: ${MEILI_ENABLED:-true}",
		"MEILI_HOST: http://meilisearch:7700",
		"meilisearch:\n    restart: always",
		"MEILI_ENV: production",
		"MEILI_MASTER_KEY: ${MEILI_MASTER_KEY:-aSampleMasterKey}",
		"db:\n        condition: service_healthy",
		"redis:\n        condition: service_healthy",
		"meilisearch:\n        condition: service_started",
		"wget -q --spider --proxy=off localhost:3000/health/ready || exit 1",
		"db-migrate:",
		"setup",
		`command: paon-migrate`,
		"image: plusminusio/paon:latest",
		"DB_HOST: db",
		"REDIS_HOST: redis",
	} {
		if !strings.Contains(compose, want) {
			t.Fatalf("docker-compose.yml missing %q", want)
		}
	}
	for _, forbidden := range []string{
		"      DB_PORT:",
		"      DB_NAME:",
		"      DB_USER:",
		"      DB_PASS:",
		"      REDIS_PORT:",
	} {
		if strings.Contains(compose, forbidden) {
			t.Fatalf("docker-compose.yml should not force env_file value %q", forbidden)
		}
	}
	for _, forbidden := range []string{"bundle exec", "node ./streaming", "streaming:", "sidekiq:"} {
		if strings.Contains(compose, forbidden) {
			t.Fatalf("docker-compose.yml retains legacy runtime contract %q", forbidden)
		}
	}
}

func TestComposeDoesNotHideBuiltUIPacks(t *testing.T) {
	composeBytes, err := os.ReadFile("../docker-compose.yml")
	if err != nil {
		t.Fatal(err)
	}
	compose := string(composeBytes)
	for _, want := range []string{
		"PAON_PUBLIC_DIR: /opt/mastodon/public",
		"volumes:",
		"./public/system:/opt/mastodon/public/system",
	} {
		if !strings.Contains(compose, want) {
			t.Fatalf("docker-compose.yml missing UI asset runtime contract %q", want)
		}
	}
	for _, forbidden := range []string{
		"./public:/opt/mastodon/public",
		"./public/:/opt/mastodon/public",
		"/opt/mastodon/public:",
	} {
		if strings.Contains(compose, forbidden) {
			t.Fatalf("docker-compose.yml must not mount over built UI packs with %q", forbidden)
		}
	}
}

func TestEnvSampleDocumentsPaonGoRuntimeSettings(t *testing.T) {
	src, err := os.ReadFile("../.env.sample")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	for _, want := range []string{
		"WEB_DOMAIN=",
		"PAON_SCHEME=",
		"PAON_VERSION=",
		"STREAMING_API_BASE_URL=",
		"PAON_GO_ADDR=",
		"SOCKET=",
		"PROXY_PROTO_V1=",
		"BIND=",
		"PORT=",
		"PAON_PUBLIC_DIR=",
		"REDIS_HOST=",
		"REDIS_PORT=",
		"REDIS_NAMESPACE=",
		"DB_HOST=",
		"DB_PORT=",
		"DB_USER=",
		"DB_PASS=",
		"DB_NAME=",
		"DB_POOL=",
		"PAON_DB_MAX_IDLE_CONNS=",
		"DB_SSLMODE=",
		"DATABASE_URL=",
		"MEILI_ENABLED=",
		"MEILI_HOST=",
		"MEILI_MASTER_KEY=",
		"MEILI_PREFIX=",
		"MEILI_LIBRARY_ONLY=",
		"OIDC_ENABLED=",
		"OIDC_SCOPE=",
		"OIDC_UID_FIELD=",
		"OIDC_CLIENT_ID=",
		"OIDC_CLIENT_SECRET=",
		"OIDC_REDIRECT_URI=",
		"STATUS_LENGTH_LIMIT=",
		"MAX_MEDIA_ATTACHMENTS=",
		"DISABLE_REMOTE_MEDIA_CACHE=",
		"CLOUDFLARE_TURNSTILE_ENABLED=",
		"CLOUDFLARE_TURNSTILE_SITE_KEY=",
		"CLOUDFLARE_TURNSTILE_SECRET_KEY=",
		"DYNAMODB_ENABLED=",
		"DYNAMODB_NAMESPACE=",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf(".env.sample missing paon-go runtime setting %q", want)
		}
	}
}

func TestDockerignoreExcludesPaonGoBuildCachesAndComposeState(t *testing.T) {
	src, err := os.ReadFile("../.dockerignore")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	for _, want := range []string{
		".go-buildcache",
		".go-modcache",
		".go-tmp",
		"meilisearch",
		"log",
		"public/packs-test",
		"bin/paon",
		"bin/paon-meili-deploy",
	} {
		if !dockerignoreContains(body, want) {
			t.Fatalf(".dockerignore missing %q", want)
		}
	}
}

func TestGitignoreExcludesPaonGoBuildOutputsAndCaches(t *testing.T) {
	src, err := os.ReadFile("../.gitignore")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	for _, want := range []string{
		"/.go-buildcache/",
		"/.go-modcache/",
		"/.go-tmp/",
		"bin/paon",
		"bin/paon-meili-deploy",
	} {
		if !ignoreFileContains(body, want) {
			t.Fatalf(".gitignore missing %q", want)
		}
	}
}

func dockerignoreContains(body string, want string) bool {
	return ignoreFileContains(body, want)
}

func ignoreFileContains(body string, want string) bool {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if strings.TrimPrefix(line, "/") == strings.TrimPrefix(want, "/") {
			return true
		}
	}
	return false
}
