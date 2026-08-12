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
	acknowledgeContract := flag.Bool("acknowledge-contract", false, "confirm all Mastodon 4.2 processes are stopped and apply irreversible 4.3 contract migrations")
	flag.Parse()
	if err := config.LoadDotenv(); err != nil {
		log.Fatalf("load dotenv: %v", err)
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
	options := migrate.OptionsFromEnv()
	if *phase != "" {
		options.Phase = migrate.UpgradePhase(*phase)
	}
	options.AcknowledgeContract = options.AcknowledgeContract || *acknowledgeContract
	options.Logf = log.Printf
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
