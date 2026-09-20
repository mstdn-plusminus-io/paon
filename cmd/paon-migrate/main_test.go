package main

import (
	"os"
	"strings"
	"testing"
)

func TestStagedMigrationNoopOutputDoesNotClaimFinalSchema(t *testing.T) {
	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(source)
	if !strings.Contains(body, `schema migration not needed for requested phase`) {
		t.Fatal("staged migration no-op output is missing")
	}
	if strings.Contains(body, `schema already current`) {
		t.Fatal("partial phase no-op must not claim the final schema is current")
	}
}
