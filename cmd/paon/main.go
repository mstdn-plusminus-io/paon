package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/api"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	paondb "github.com/mstdn-plusminus-io/paon/internal/paon/db"
	"github.com/mstdn-plusminus-io/paon/internal/paon/telemetry"
	"github.com/mstdn-plusminus-io/paon/internal/paon/web"
)

func main() {
	checkConfig := flag.Bool("check-config", false, "validate configuration and database connectivity without starting the HTTP server")
	showVersion := flag.Bool("version", false, "print the Paon version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(config.VersionFromEnv())
		return
	}

	if err := config.LoadDotenv(); err != nil {
		log.Fatalf("load dotenv: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg := config.FromEnv()

	if err := cfg.ValidateRuntime(); err != nil {
		log.Fatalf("check runtime configuration: %v", err)
	}
	for _, warning := range cfg.RuntimeWarnings() {
		if cfg.ShouldLog("warn") {
			log.Printf("configuration warning: %s", warning)
		}
	}
	if cfg.OpenTelemetryEnabled && !*checkConfig {
		telemetryRuntime, err := telemetry.Initialize(ctx, telemetry.OptionsFromConfig(cfg, telemetryServiceRole(cfg.ProcessRole)))
		if err != nil {
			log.Fatalf("initialize OpenTelemetry: %v", err)
		}
		defer func() {
			if err := telemetryRuntime.ShutdownWithTimeout(10 * time.Second); err != nil {
				log.Printf("shutdown OpenTelemetry: %v", err)
			}
		}()
	}
	if err := web.ValidatePublicAssets(cfg); err != nil {
		log.Fatalf("check public assets: %v", err)
	}
	if err := web.ValidateServerRenderedLocales(cfg); err != nil {
		log.Fatalf("check server-rendered locales: %v", err)
	}

	database, err := paondb.Open(cfg)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	if err := paondb.Available(database); err != nil {
		log.Fatalf("check database: %v", err)
	}
	if err := paondb.RequireSupportedVersion(database); err != nil {
		log.Fatalf("check database version: %v", err)
	}
	if err := paondb.SchemaAvailable(database); err != nil {
		log.Fatalf("check database schema: %v", err)
	}
	if err := api.RedisAvailable(ctx, cfg); err != nil {
		log.Fatalf("check redis: %v", err)
	}
	if cfg.MeiliEnabled {
		if err := api.WaitForMeiliAvailable(ctx, cfg, 30*time.Second); err != nil {
			log.Fatalf("check meilisearch: %v", err)
		}
	}
	if *checkConfig {
		fmt.Println("configuration ok")
		return
	}

	server, err := api.NewServer(cfg, database)
	if err != nil {
		log.Fatalf("create server: %v", err)
	}
	defer func() {
		if err := server.Close(); err != nil {
			log.Printf("close server resources: %v", err)
		}
	}()
	var workers *api.BackgroundWorkers
	if cfg.ShouldStartBackgroundWorkers() {
		workers = server.StartBackgroundWorkers(ctx)
	}
	removeWorkerReadiness, err := activateWorkerReadiness(ctx, cfg, workers)
	if err != nil {
		log.Fatalf("create worker readiness file: %v", err)
	}
	defer removeWorkerReadiness()
	if !cfg.ShouldStartHTTPServer() {
		if cfg.ShouldLog("info") {
			log.Printf("paon worker role started")
		}
		<-ctx.Done()
		waitForBackgroundWorkers(workers)
		return
	}

	if cfg.ShouldLog("info") {
		log.Printf("paon listening on %s", cfg.ListenAddr)
	}
	if err := server.StartContext(ctx, cfg.ListenAddr); err != nil {
		log.Fatal(err)
	}
	waitForBackgroundWorkers(workers)
}

func telemetryServiceRole(processRole string) string {
	switch processRole {
	case "web":
		return "web"
	case "worker":
		return "worker"
	default:
		return "web-worker"
	}
}

func activateWorkerReadiness(ctx context.Context, cfg config.Config, workers *api.BackgroundWorkers) (func(), error) {
	filename := cfg.WorkerReadyFilename
	if filename == "" || workers == nil {
		return func() {}, nil
	}
	if err := workers.WaitReady(ctx); err != nil {
		return nil, fmt.Errorf("wait for worker initialization: %w", err)
	}
	directory := "tmp"
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return nil, err
	}
	temporary, err := os.CreateTemp(directory, ".paon-worker-ready-*")
	if err != nil {
		return nil, err
	}
	temporaryPath := temporary.Name()
	if err := temporary.Close(); err != nil {
		_ = os.Remove(temporaryPath)
		return nil, err
	}
	readyPath := filepath.Join(directory, filename)
	if err := os.Rename(temporaryPath, readyPath); err != nil {
		_ = os.Remove(temporaryPath)
		return nil, err
	}
	return func() { _ = os.Remove(readyPath) }, nil
}

func waitForBackgroundWorkers(workers *api.BackgroundWorkers) {
	if workers == nil {
		return
	}
	drainCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := workers.Wait(drainCtx); err != nil {
		log.Printf("drain background workers: %v", err)
	}
}
