package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	paondb "github.com/mstdn-plusminus-io/paon/internal/paon/db"
	"github.com/mstdn-plusminus-io/paon/internal/paon/migrate"
	"github.com/mstdn-plusminus-io/paon/internal/paon/telemetry"
)

func main() {
	check := flag.Bool("check", false, "validate the current schema without applying a fresh schema")
	phase := flag.String("phase", "", "upgrade a Mastodon 4.2 schema through expand, backfill, validate, or contract (default: expand)")
	all := flag.Bool("all", false, "apply every migration phase through Mastodon 4.3.23; requires --acknowledge-contract")
	targetVersion := flag.String("target-version", "", "target Mastodon release (this branch: 4.3.23)")
	acknowledgeContract := flag.Bool("acknowledge-contract", false, "confirm all Mastodon 4.2 processes are stopped and apply irreversible 4.3 contract migrations")
	flag.Parse()
	if err := config.LoadDotenv(); err != nil {
		log.Fatalf("load dotenv: %v", err)
	}
	options := migrate.OptionsFromEnv()
	if *phase != "" {
		options.Phase = migrate.UpgradePhase(*phase)
	}
	if *targetVersion != "" {
		options.TargetVersion = *targetVersion
	}
	options.All = options.All || *all
	options.AcknowledgeContract = options.AcknowledgeContract || *acknowledgeContract
	options.Logf = log.Printf
	if *check && options.TargetVersion != "" && options.TargetVersion != migrate.LatestTargetVersion {
		log.Fatalf("--check validates only the current application schema, Mastodon %s", migrate.LatestTargetVersion)
	}
	cfg := config.FromEnv()
	if err := cfg.ValidateOpenTelemetry(); err != nil {
		log.Fatalf("check OpenTelemetry configuration: %v", err)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if cfg.OpenTelemetryEnabled {
		telemetryRuntime, err := telemetry.Initialize(ctx, telemetry.OptionsFromConfig(cfg, "paon-migrate"))
		if err != nil {
			log.Fatalf("initialize OpenTelemetry: %v", err)
		}
		defer func() {
			if err := telemetryRuntime.ShutdownWithTimeout(10 * time.Second); err != nil {
				log.Printf("shutdown OpenTelemetry: %v", err)
			}
		}()
	}
	database, err := paondb.Open(cfg)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	if *check {
		if err := paondb.SchemaAvailable(database); err != nil {
			log.Fatalf("check schema: %v", err)
		}
		fmt.Println("schema ok")
		return
	}

	applied, err := migrate.RunWithOptions(ctx, database, options)
	if err != nil {
		log.Fatal(err)
	}
	if applied {
		fmt.Println("schema migration applied")
	} else {
		fmt.Println("schema migration not needed for requested phase")
	}
}
