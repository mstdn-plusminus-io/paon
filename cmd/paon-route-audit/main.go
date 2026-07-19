package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/mstdn-plusminus-io/paon/internal/paon/parity"
)

func main() {
	var railsPath string
	var goPath string
	flag.StringVar(&railsPath, "rails", "tmp/rails-routes.json", "Rails route manifest")
	flag.StringVar(&goPath, "go", "tmp/go-routes.json", "Go route manifest")
	flag.Parse()
	railsFile := mustOpen(railsPath)
	defer railsFile.Close()
	goFile := mustOpen(goPath)
	defer goFile.Close()
	railsRoutes, err := parity.LoadRailsRoutes(railsFile)
	if err != nil {
		fatal(err)
	}
	goRoutes, err := parity.LoadGoRoutes(goFile)
	if err != nil {
		fatal(err)
	}
	audit := parity.AuditRoutes(railsRoutes, goRoutes, []parity.AcceptedRoute{
		{Controller: "api/v1/crypto/deliveries", Reason: "Mastodon crypto API is disabled in this fork"},
		{Controller: "api/v1/crypto/encrypted_messages", Reason: "Mastodon crypto API is disabled in this fork"},
		{Controller: "api/v1/crypto/keys/claims", Reason: "Mastodon crypto API is disabled in this fork"},
		{Controller: "api/v1/crypto/keys/counts", Reason: "Mastodon crypto API is disabled in this fork"},
		{Controller: "api/v1/crypto/keys/queries", Reason: "Mastodon crypto API is disabled in this fork"},
		{Controller: "api/v1/crypto/keys/uploads", Reason: "Mastodon crypto API is disabled in this fork"},
	})
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(audit); err != nil {
		fatal(err)
	}
	if len(audit.Unmapped) > 0 {
		os.Exit(1)
	}
}

func mustOpen(path string) *os.File {
	file, err := os.Open(path)
	if err != nil {
		fatal(err)
	}
	return file
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(2)
}
