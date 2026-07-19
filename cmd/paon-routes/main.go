package main

import (
	"encoding/json"
	"log"
	"os"

	"github.com/mstdn-plusminus-io/paon/internal/paon/api"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
)

func main() {
	if err := config.LoadDotenv(); err != nil {
		log.Fatalf("load dotenv: %v", err)
	}
	server, err := api.NewServer(config.FromEnv(), nil)
	if err != nil {
		log.Fatal(err)
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(server.RouteManifest()); err != nil {
		log.Fatal(err)
	}
}
