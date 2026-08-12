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

func TestMigrationRequiresPostgreSQL13BeforeInspectingSchema(t *testing.T) {
	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(source)
	availabilityCheck := strings.Index(body, `paondb.Available(database)`)
	versionCheck := strings.Index(body, `paondb.RequireSupportedVersion(database)`)
	schemaCheck := strings.Index(body, `paondb.SchemaAvailable(database)`)
	if availabilityCheck < 0 || versionCheck < availabilityCheck || schemaCheck < versionCheck {
		t.Fatal("paon-migrate must check database availability, PostgreSQL version, and schema in that order")
	}
}
