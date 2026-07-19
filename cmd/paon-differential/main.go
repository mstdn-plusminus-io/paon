package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/mstdn-plusminus-io/paon/internal/paon/differential"
)

func main() {
	var railsURL string
	var goURL string
	var casesPath string
	flag.StringVar(&railsURL, "rails-url", os.Getenv("PAON_RAILS_REFERENCE_URL"), "Rails reference base URL")
	flag.StringVar(&goURL, "go-url", os.Getenv("PAON_GO_REFERENCE_URL"), "Go reference base URL")
	flag.StringVar(&casesPath, "cases", "testdata/differential/core.json", "differential case manifest")
	flag.Parse()
	if railsURL == "" || goURL == "" {
		fmt.Fprintln(os.Stderr, "--rails-url and --go-url are required")
		os.Exit(2)
	}
	file, err := os.Open(casesPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	defer file.Close()
	manifest, err := differential.Load(file)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	results := (differential.Runner{RailsBaseURL: railsURL, GoBaseURL: goURL}).Run(context.Background(), manifest)
	failed := false
	for _, result := range results {
		if result.Error != "" {
			failed = true
		}
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(results); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if failed {
		os.Exit(1)
	}
}
