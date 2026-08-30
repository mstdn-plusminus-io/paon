package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/schemacatalog"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

var (
	commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
	digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	databaseURL := flag.String("database-url", "", "read-only PostgreSQL reference database URL")
	schema := flag.String("schema", "public", "schema to capture")
	name := flag.String("name", "", "human-readable reference name")
	path := flag.String("path", "", "reference migration path, such as fresh or v4.2.19-to-v4.3.23")
	tag := flag.String("tag", "", "authoritative Mastodon target tag")
	commit := flag.String("commit", "", "authoritative 40-character Mastodon commit")
	schemaVersion := flag.String("schema-version", "", "latest Active Record schema version")
	schemaSHA256 := flag.String("schema-sha256", "", "SHA-256 of the authoritative db/schema.rb")
	snowflakeSHA256 := flag.String("snowflake-sha256", "", "SHA-256 of lib/mastodon/snowflake.rb")
	output := flag.String("output", "", "output JSON file, or - for stdout")
	flag.Parse()

	for field, value := range map[string]string{
		"database-url":   *databaseURL,
		"name":           *name,
		"path":           *path,
		"tag":            *tag,
		"schema-version": *schemaVersion,
		"output":         *output,
	} {
		if value == "" {
			return fmt.Errorf("-%s is required", field)
		}
	}
	if !commitPattern.MatchString(*commit) {
		return errors.New("-commit must be a lowercase 40-character hexadecimal Git commit")
	}
	if !digestPattern.MatchString(*schemaSHA256) {
		return errors.New("-schema-sha256 must be a lowercase 64-character SHA-256 digest")
	}
	if *snowflakeSHA256 != "" && !digestPattern.MatchString(*snowflakeSHA256) {
		return errors.New("-snowflake-sha256 must be a lowercase 64-character SHA-256 digest")
	}

	gormDatabase, err := gorm.Open(postgres.New(postgres.Config{
		DSN:                  *databaseURL,
		PreferSimpleProtocol: true,
	}), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		return fmt.Errorf("open reference database: %w", err)
	}
	database, err := gormDatabase.DB()
	if err != nil {
		return fmt.Errorf("open reference database pool: %w", err)
	}
	defer database.Close()
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := database.PingContext(ctx); err != nil {
		return fmt.Errorf("connect to reference database: %w", err)
	}

	var versionNumber int
	if err := database.QueryRowContext(ctx, `SELECT current_setting('server_version_num')::integer`).Scan(&versionNumber); err != nil {
		return fmt.Errorf("read reference PostgreSQL version: %w", err)
	}
	catalog, err := schemacatalog.Capture(ctx, database, *schema)
	if err != nil {
		return err
	}
	manifest, err := schemacatalog.BuildManifest(catalog)
	if err != nil {
		return err
	}
	encoded, err := schemacatalog.MarshalGolden(schemacatalog.Golden{
		Source: schemacatalog.Source{
			Name:                    *name,
			Path:                    *path,
			Tag:                     *tag,
			Commit:                  *commit,
			SchemaVersion:           *schemaVersion,
			SchemaSHA256:            *schemaSHA256,
			SnowflakeSHA256:         *snowflakeSHA256,
			PostgreSQLVersionNumber: versionNumber,
		},
		Catalog: manifest,
	})
	if err != nil {
		return err
	}
	if *output == "-" {
		_, err := os.Stdout.Write(encoded)
		return err
	}
	return writeAtomic(*output, encoded)
}

func writeAtomic(path string, data []byte) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".paon-schema-catalog-*.json")
	if err != nil {
		return fmt.Errorf("create temporary schema catalog: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o644); err != nil {
		temporary.Close()
		return fmt.Errorf("set schema catalog permissions: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return fmt.Errorf("write schema catalog: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close schema catalog: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("publish schema catalog: %w", err)
	}
	return nil
}
