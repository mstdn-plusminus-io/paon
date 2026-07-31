package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/cutover"
	"github.com/redis/go-redis/v9"
)

func main() {
	jsonOutput := flag.Bool("json", false, "print the Sidekiq drain report as JSON")
	producerFenced := flag.Bool("producer-fenced", false, "assert Rails web and all other Sidekiq producers have been stopped")
	flag.Parse()

	if !*producerFenced {
		fmt.Fprintln(os.Stderr, "cutover refused: stop Rails producers and rerun with --producer-fenced")
		os.Exit(2)
	}
	if err := config.LoadDotenv(); err != nil {
		log.Fatalf("load dotenv: %v", err)
	}
	cfg := config.FromEnv()
	redisURL := strings.TrimSpace(cfg.SidekiqRedisURL)
	if redisURL == "" {
		redisURL = strings.TrimSpace(cfg.RedisURL)
	}
	options, err := redis.ParseURL(redisURL)
	if err != nil {
		log.Fatalf("parse Sidekiq Redis URL: %v", err)
	}
	client := redis.NewClient(options)
	defer client.Close()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	report, err := cutover.InspectSidekiq(ctx, client, cfg.RedisNamespace)
	if err != nil {
		log.Fatalf("inspect Sidekiq: %v", err)
	}
	if *jsonOutput {
		encoded, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println(string(encoded))
	} else {
		fmt.Print(report.String())
	}
	if !report.Safe() {
		fmt.Fprintln(os.Stderr, "cutover refused: Sidekiq is not fully drained")
		os.Exit(2)
	}
	fmt.Fprintln(os.Stderr, "cutover preflight passed: Sidekiq is drained")
}
