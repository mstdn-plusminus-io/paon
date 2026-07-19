package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/api"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	paondb "github.com/mstdn-plusminus-io/paon/internal/paon/db"
)

func main() {
	batchSize := 100
	resume := false
	progressPath := "tmp/meilisearch_deploy_progress.json"
	flag.IntVar(&batchSize, "batch-size", batchSize, "number of records loaded and indexed per batch")
	flag.BoolVar(&resume, "resume", resume, "resume from the saved Meilisearch deploy progress file")
	flag.StringVar(&progressPath, "progress-file", progressPath, "path to the Meilisearch deploy progress file")
	checkConfig := flag.Bool("check-config", false, "validate configuration and database connectivity without deploying Meilisearch indexes")
	showVersion := flag.Bool("version", false, "print the Paon version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(config.VersionFromEnv())
		return
	}

	batchSizeSet := flagProvided("batch-size")
	resumeSet := flagProvided("resume")

	if err := config.LoadDotenv(); err != nil {
		log.Fatalf("load dotenv: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if !batchSizeSet {
		batchSize = batchSizeFromEnv()
	}
	if !resumeSet {
		resume = resumeFromEnv()
	}

	cfg := config.FromEnv()
	if err := cfg.ValidateRuntime(); err != nil {
		log.Fatalf("check runtime configuration: %v", err)
	}
	for _, warning := range cfg.RuntimeWarnings() {
		if cfg.ShouldLog("warn") {
			log.Printf("configuration warning: %s", warning)
		}
	}
	if !cfg.MeiliEnabled || strings.TrimSpace(cfg.MeiliHost) == "" {
		log.Fatal("check meilisearch configuration: meilisearch disabled")
	}
	if err := api.WaitForMeiliAvailable(ctx, cfg, 30*time.Second); err != nil {
		log.Fatalf("check meilisearch: %v", err)
	}
	database, err := paondb.Open(cfg)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	if err := paondb.Available(database); err != nil {
		log.Fatalf("check database: %v", err)
	}
	if err := paondb.SchemaAvailable(database); err != nil {
		log.Fatalf("check database schema: %v", err)
	}
	if *checkConfig {
		fmt.Println("configuration ok")
		return
	}
	server, err := api.NewServer(cfg, database)
	if err != nil {
		log.Fatalf("create server: %v", err)
	}
	stats, err := server.DeployMeiliIndexes(ctx, api.MeiliDeployOptions{BatchSize: batchSize, Resume: resume, ProgressPath: progressPath, Writer: os.Stdout})
	if err != nil {
		log.Fatalf("deploy meilisearch indexes: %v", err)
	}
	if cfg.ShouldLog("info") {
		log.Printf("meilisearch deploy complete: accounts=%d statuses=%d tags=%d instances=%d", stats.Accounts, stats.Statuses, stats.Tags, stats.Instances)
	}
}

func batchSizeFromEnv() int {
	value := os.Getenv("BATCH_SIZE")
	if value == "" {
		return 100
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return 100
	}
	return parsed
}

func resumeFromEnv() bool {
	return os.Getenv("RESUME") == "true"
}

func flagProvided(name string) bool {
	provided := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == name {
			provided = true
		}
	})
	return provided
}
