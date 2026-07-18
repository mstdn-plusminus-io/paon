package main

import (
	"os"
	"strings"
	"testing"
)

func TestMainUsesContextAwareServerStart(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	for _, want := range []string{
		`flag.Bool("check-config", false`,
		`flag.Bool("version", false`,
		`config.LoadDotenv()`,
		`signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)`,
		`cfg.ValidateRuntime()`,
		`cfg.RuntimeWarnings()`,
		`cfg.ShouldLog("warn")`,
		`web.ValidatePublicAssets(cfg)`,
		`web.ValidateServerRenderedLocales(cfg)`,
		`paondb.Available(database)`,
		`paondb.SchemaAvailable(database)`,
		`api.RedisAvailable(ctx, cfg)`,
		`api.WaitForMeiliAvailable(ctx, cfg, 30*time.Second)`,
		`cfg.ShouldStartBackgroundWorkers()`,
		`server.StartBackgroundWorkers(ctx)`,
		`cfg.ShouldStartHTTPServer()`,
		`log.Printf("paon worker role started")`,
		`cfg.ShouldLog("info")`,
		`server.StartContext(ctx, cfg.ListenAddr)`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("main.go missing lifecycle wiring %q", want)
		}
	}
	availabilityCheck := strings.Index(body, `paondb.Available(database)`)
	schemaCheck := strings.Index(body, `paondb.SchemaAvailable(database)`)
	serverCreate := strings.Index(body, `api.NewServer(cfg, database)`)
	if availabilityCheck < 0 || serverCreate < 0 || availabilityCheck > serverCreate {
		t.Fatal("main.go must validate database availability before creating the HTTP server")
	}
	if schemaCheck < availabilityCheck || schemaCheck > serverCreate {
		t.Fatal("main.go must validate Mastodon schema after database availability and before creating the HTTP server")
	}
	redisCheck := strings.Index(body, `api.RedisAvailable(ctx, cfg)`)
	meiliCheck := strings.Index(body, `api.WaitForMeiliAvailable(ctx, cfg, 30*time.Second)`)
	if redisCheck < schemaCheck || redisCheck > serverCreate {
		t.Fatal("main.go must validate Redis after database schema and before creating the HTTP server")
	}
	if meiliCheck < redisCheck || meiliCheck > serverCreate {
		t.Fatal("main.go must validate enabled Meilisearch after Redis and before creating the HTTP server")
	}
	runtimeCheck := strings.Index(body, `cfg.ValidateRuntime()`)
	databaseOpen := strings.Index(body, `paondb.Open(cfg)`)
	if runtimeCheck < 0 || databaseOpen < 0 || runtimeCheck > databaseOpen {
		t.Fatal("main.go must validate runtime configuration before opening the database")
	}
	dotenvLoad := strings.Index(body, `config.LoadDotenv()`)
	fromEnv := strings.Index(body, `config.FromEnv()`)
	if dotenvLoad < 0 || fromEnv < 0 || dotenvLoad > fromEnv {
		t.Fatal("main.go must load dotenv files before reading configuration from the environment")
	}
	assetCheck := strings.Index(body, `web.ValidatePublicAssets(cfg)`)
	localeCheck := strings.Index(body, `web.ValidateServerRenderedLocales(cfg)`)
	if assetCheck < 0 || localeCheck < 0 || databaseOpen < 0 || assetCheck > localeCheck || localeCheck > databaseOpen {
		t.Fatal("main.go must validate public UI assets and server-rendered locales before opening the database")
	}
	workerStartGuard := strings.Index(body, `if cfg.ShouldStartBackgroundWorkers() {`)
	workerStart := strings.Index(body, `server.StartBackgroundWorkers(ctx)`)
	httpStartGuard := strings.Index(body, `if !cfg.ShouldStartHTTPServer() {`)
	httpStart := strings.Index(body, `server.StartContext(ctx, cfg.ListenAddr)`)
	if workerStartGuard < 0 || workerStart < workerStartGuard || httpStartGuard < workerStart || httpStart < httpStartGuard {
		t.Fatal("main.go must gate worker startup before deciding whether to open the HTTP listener")
	}
}

func TestVersionFlagExitsBeforeRuntimeChecks(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	versionBranch := strings.Index(body, `if *showVersion {`)
	dotenvLoad := strings.Index(body, `config.LoadDotenv()`)
	runtimeCheck := strings.Index(body, `cfg.ValidateRuntime()`)
	assetCheck := strings.Index(body, `web.ValidatePublicAssets(cfg)`)
	localeCheck := strings.Index(body, `web.ValidateServerRenderedLocales(cfg)`)
	databaseOpen := strings.Index(body, `paondb.Open(cfg)`)
	if versionBranch < 0 || dotenvLoad < 0 || runtimeCheck < 0 || assetCheck < 0 || localeCheck < 0 || databaseOpen < 0 {
		t.Fatal("main.go must keep version, dotenv, runtime, asset, and database lifecycle steps explicit")
	}
	if versionBranch > dotenvLoad || versionBranch > runtimeCheck || versionBranch > assetCheck || versionBranch > localeCheck || versionBranch > databaseOpen {
		t.Fatal("--version must return before dotenv loading, runtime validation, asset/locale validation, or database access")
	}
	for _, want := range []string{
		`fmt.Println(config.VersionFromEnv())`,
		`return`,
	} {
		if !strings.Contains(body[versionBranch:dotenvLoad], want) {
			t.Fatalf("--version branch missing %q before runtime checks", want)
		}
	}
}
