package main

import (
	"os"
	"strings"
	"testing"
)

func TestMainValidatesDatabaseBeforeDeploy(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	checkConfigFlag := strings.Index(body, `flag.Bool("check-config", false`)
	versionFlag := strings.Index(body, `flag.Bool("version", false`)
	flagParse := strings.Index(body, `flag.Parse()`)
	batchSizeSet := strings.Index(body, `batchSizeSet := flagProvided("batch-size")`)
	resumeSet := strings.Index(body, `resumeSet := flagProvided("resume")`)
	dotenvLoad := strings.Index(body, `config.LoadDotenv()`)
	batchSizeEnv := strings.Index(body, `batchSize = batchSizeFromEnv()`)
	resumeEnv := strings.Index(body, `resume = resumeFromEnv()`)
	fromEnv := strings.Index(body, `config.FromEnv()`)
	runtimeCheck := strings.Index(body, `cfg.ValidateRuntime()`)
	runtimeWarnings := strings.Index(body, `cfg.RuntimeWarnings()`)
	warnLogGate := strings.Index(body, `cfg.ShouldLog("warn")`)
	meiliCheck := strings.Index(body, `!cfg.MeiliEnabled || strings.TrimSpace(cfg.MeiliHost) == ""`)
	meiliAvailabilityCheck := strings.Index(body, `api.WaitForMeiliAvailable(ctx, cfg, 30*time.Second)`)
	databaseOpen := strings.Index(body, `paondb.Open(cfg)`)
	availabilityCheck := strings.Index(body, `paondb.Available(database)`)
	versionCheck := strings.Index(body, `paondb.RequireSupportedVersion(database)`)
	schemaCheck := strings.Index(body, `paondb.SchemaAvailable(database)`)
	checkConfigBranch := strings.Index(body, `if *checkConfig {`)
	serverCreate := strings.Index(body, `api.NewServer(cfg, database)`)
	deploy := strings.Index(body, `server.DeployMeiliIndexes`)
	infoLogGate := strings.Index(body, `cfg.ShouldLog("info")`)
	if checkConfigFlag < 0 || versionFlag < 0 || flagParse < 0 || batchSizeSet < 0 || resumeSet < 0 || dotenvLoad < 0 || batchSizeEnv < 0 || resumeEnv < 0 || fromEnv < 0 || runtimeCheck < 0 || runtimeWarnings < 0 || warnLogGate < 0 || meiliCheck < 0 || meiliAvailabilityCheck < 0 || databaseOpen < 0 || availabilityCheck < 0 || versionCheck < 0 || schemaCheck < 0 || checkConfigBranch < 0 || serverCreate < 0 || deploy < 0 || infoLogGate < 0 {
		t.Fatal("main.go missing runtime validation, database validation, server creation, or deploy call")
	}
	if flagParse > dotenvLoad {
		t.Fatal("main.go must parse flags before loading dotenv so --help works without runtime configuration")
	}
	if batchSizeSet < flagParse || resumeSet < flagParse || batchSizeSet > dotenvLoad || resumeSet > dotenvLoad {
		t.Fatal("main.go must capture explicit flags after parsing and before loading dotenv")
	}
	if dotenvLoad > fromEnv {
		t.Fatal("main.go must load dotenv files before reading configuration from the environment")
	}
	if batchSizeEnv < dotenvLoad || batchSizeEnv > fromEnv || resumeEnv < dotenvLoad || resumeEnv > fromEnv {
		t.Fatal("main.go must apply .env-backed deploy defaults after loading dotenv and before reading runtime configuration")
	}
	if runtimeCheck < fromEnv || runtimeCheck > databaseOpen {
		t.Fatal("main.go must validate runtime configuration after reading environment and before opening the database")
	}
	if runtimeWarnings < runtimeCheck || runtimeWarnings > databaseOpen {
		t.Fatal("main.go must emit runtime warnings after validation and before opening the database")
	}
	if warnLogGate < runtimeWarnings || warnLogGate > databaseOpen {
		t.Fatal("main.go must gate runtime warnings through the Rails log level before opening the database")
	}
	if meiliCheck < runtimeWarnings || meiliCheck > databaseOpen {
		t.Fatal("main.go must validate Meilisearch configuration before opening the database")
	}
	if meiliAvailabilityCheck < meiliCheck || meiliAvailabilityCheck > databaseOpen {
		t.Fatal("main.go must validate Meilisearch availability before opening the database")
	}
	if availabilityCheck > serverCreate || availabilityCheck > deploy {
		t.Fatal("main.go must validate database availability before creating the server or deploying indexes")
	}
	if versionCheck < availabilityCheck || versionCheck > schemaCheck {
		t.Fatal("main.go must validate the PostgreSQL version after availability and before the schema")
	}
	if schemaCheck < versionCheck || schemaCheck > serverCreate || schemaCheck > deploy {
		t.Fatal("main.go must validate Mastodon schema after the database version and before deploying indexes")
	}
	if checkConfigBranch < schemaCheck || checkConfigBranch > serverCreate || checkConfigBranch > deploy {
		t.Fatal("--check-config must return after schema validation and before server creation or deploy")
	}
	if infoLogGate < deploy {
		t.Fatal("main.go must gate successful deploy summary logging through the Rails log level after deploy")
	}
}

func TestMainSupportsMastodon44OnlyMappingFlag(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	for _, want := range []string{
		`flag.Bool("only-mapping", false`,
		`OnlyMapping: *onlyMapping`,
		`log.Printf("meilisearch mappings updated")`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("main.go missing only-mapping behavior %q", want)
		}
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
	databaseOpen := strings.Index(body, `paondb.Open(cfg)`)
	if versionBranch < 0 || dotenvLoad < 0 || runtimeCheck < 0 || databaseOpen < 0 {
		t.Fatal("main.go must keep version, dotenv, runtime, and database lifecycle steps explicit")
	}
	if versionBranch > dotenvLoad || versionBranch > runtimeCheck || versionBranch > databaseOpen {
		t.Fatal("--version must return before dotenv loading, runtime validation, or database access")
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

func TestCheckConfigFlagExitsBeforeDeploy(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	checkConfigBranch := strings.Index(body, `if *checkConfig {`)
	serverCreate := strings.Index(body, `api.NewServer(cfg, database)`)
	deploy := strings.Index(body, `server.DeployMeiliIndexes`)
	if checkConfigBranch < 0 || serverCreate < 0 || deploy < 0 {
		t.Fatal("main.go must keep check-config, server creation, and deploy lifecycle steps explicit")
	}
	if checkConfigBranch > serverCreate || checkConfigBranch > deploy {
		t.Fatal("--check-config must return before server creation or deploy")
	}
	for _, want := range []string{
		`fmt.Println("configuration ok")`,
		`return`,
	} {
		if !strings.Contains(body[checkConfigBranch:serverCreate], want) {
			t.Fatalf("--check-config branch missing %q before server creation", want)
		}
	}
}

func TestBatchSizeFromEnv(t *testing.T) {
	t.Setenv("BATCH_SIZE", "250")
	if got := batchSizeFromEnv(); got != 250 {
		t.Fatalf("batchSizeFromEnv = %d", got)
	}
}

func TestBatchSizeFromEnvFallsBack(t *testing.T) {
	t.Setenv("BATCH_SIZE", "not-a-number")
	if got := batchSizeFromEnv(); got != 100 {
		t.Fatalf("batchSizeFromEnv = %d", got)
	}
}

func TestResumeFromEnv(t *testing.T) {
	t.Setenv("RESUME", "true")
	if !resumeFromEnv() {
		t.Fatal("resumeFromEnv = false")
	}
	t.Setenv("RESUME", "1")
	if resumeFromEnv() {
		t.Fatal("resumeFromEnv should only accept true")
	}
}
