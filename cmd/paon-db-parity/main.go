// paon-db-parity compares quiescent restored databases, including their data.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/dbparity"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	databaseURL := flag.String("database-url", "", "quiescent PostgreSQL database to capture")
	output := flag.String("output", "", "write private schema and data-digest snapshot to this file")
	reference := flag.String("reference", "", "compare against a previously captured snapshot")
	startedAt := flag.String("migration-started-at", "", "RFC3339Nano migration start; requires --migration-finished-at")
	finishedAt := flag.String("migration-finished-at", "", "RFC3339Nano migration finish; normalize only documented audit timestamps in this window")
	flag.Parse()
	if *databaseURL == "" || (*output == "" && *reference == "") {
		return fmt.Errorf("--database-url and --output or --reference are required")
	}
	if err := validateSnapshotPaths(*output, *reference); err != nil {
		return err
	}
	options, err := captureOptions(*startedAt, *finishedAt)
	if err != nil {
		return err
	}
	var expected *dbparity.Snapshot
	if *reference != "" {
		data, err := os.ReadFile(*reference)
		if err != nil {
			return err
		}
		expected = &dbparity.Snapshot{}
		if err := json.Unmarshal(data, expected); err != nil {
			return err
		}
	}
	db, err := gorm.Open(postgres.New(postgres.Config{DSN: *databaseURL, PreferSimpleProtocol: true}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return fmt.Errorf("open parity database: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	defer sqlDB.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	snapshot, err := dbparity.CaptureWithOptions(ctx, sqlDB, options)
	if err != nil {
		return err
	}
	if *output != "" {
		data, err := json.MarshalIndent(snapshot, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(*output, append(data, '\n'), 0o600); err != nil {
			return err
		}
	}
	if expected != nil {
		if difference := dbparity.Diff(*expected, snapshot); difference != "" {
			return fmt.Errorf("database parity failed:\n%s", difference)
		}
		fmt.Println("database parity passed (catalog, table data, sequence values)")
	}
	return nil
}

func captureOptions(startedAt, finishedAt string) (dbparity.Options, error) {
	if startedAt == "" && finishedAt == "" {
		return dbparity.Options{}, nil
	}
	if startedAt == "" || finishedAt == "" {
		return dbparity.Options{}, fmt.Errorf("--migration-started-at and --migration-finished-at must be provided together")
	}
	start, err := time.Parse(time.RFC3339Nano, startedAt)
	if err != nil {
		return dbparity.Options{}, fmt.Errorf("invalid --migration-started-at: %w", err)
	}
	finish, err := time.Parse(time.RFC3339Nano, finishedAt)
	if err != nil {
		return dbparity.Options{}, fmt.Errorf("invalid --migration-finished-at: %w", err)
	}
	window := &dbparity.MigrationWindow{StartedAt: start, FinishedAt: finish}
	if err := window.Validate(); err != nil {
		return dbparity.Options{}, err
	}
	return dbparity.Options{MigrationWindow: window}, nil
}

func validateSnapshotPaths(output, reference string) error {
	if output == "" || reference == "" {
		return nil
	}
	outputPath, err := filepath.Abs(output)
	if err != nil {
		return err
	}
	referencePath, err := filepath.Abs(reference)
	if err != nil {
		return err
	}
	if outputPath == referencePath {
		return fmt.Errorf("--output must not overwrite --reference")
	}
	outputInfo, outputErr := os.Stat(output)
	referenceInfo, referenceErr := os.Stat(reference)
	if outputErr == nil && referenceErr == nil && os.SameFile(outputInfo, referenceInfo) {
		return fmt.Errorf("--output and --reference refer to the same file")
	}
	return nil
}
