package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	paondb "github.com/mstdn-plusminus-io/paon/internal/paon/db"
	"github.com/mstdn-plusminus-io/paon/internal/paon/migrate"
)

func main() {
	check := flag.Bool("check", false, "validate the current schema without applying a fresh schema")
	flag.Parse()
	if err := config.LoadDotenv(); err != nil {
		log.Fatalf("load dotenv: %v", err)
	}
	cfg := config.FromEnv()
	database, err := paondb.Open(cfg)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if *check {
		if err := paondb.SchemaAvailable(database); err != nil {
			log.Fatalf("check schema: %v", err)
		}
		fmt.Println("schema ok")
		return
	}
	applied, err := migrate.Run(ctx, database)
	if err != nil {
		log.Fatal(err)
	}
	if applied {
		fmt.Println("fresh schema and seeds applied")
	} else {
		fmt.Println("schema already current")
	}
}
