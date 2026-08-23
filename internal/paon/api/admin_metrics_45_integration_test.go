//go:build integration

package api

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	paondb "github.com/mstdn-plusminus-io/paon/internal/paon/db"
	"github.com/mstdn-plusminus-io/paon/internal/paon/migrate"
	"gorm.io/gorm"
)

func TestMastodon4515AdminMediaStorageMetricAgainstFreshSchema(t *testing.T) {
	databaseURL := os.Getenv("PAON_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("PAON_TEST_DATABASE_URL is required for integration tests")
	}
	database, err := paondb.Open(config.Config{
		DatabaseURL:          databaseURL,
		DatabaseMaxOpenConns: 2,
		DatabaseMaxIdleConns: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()

	if err := database.Exec(`DROP SCHEMA public CASCADE; CREATE SCHEMA public`).Error; err != nil {
		t.Fatalf("reset integration schema: %v", err)
	}
	if applied, err := migrate.Run(context.Background(), database); err != nil || !applied {
		t.Fatalf("fresh migration = applied %v, err %v", applied, err)
	}
	for relation, want := range map[string]bool{"imports": false, "bulk_imports": true} {
		var exists bool
		if err := database.Raw(`SELECT to_regclass(?) IS NOT NULL`, "public."+relation).Scan(&exists).Error; err != nil {
			t.Fatal(err)
		}
		if exists != want {
			t.Fatalf("fresh schema relation %s exists = %v, want %v", relation, exists, want)
		}
	}

	capture := &statusMentionSQLCapture{}
	metricDatabase := database.Session(&gorm.Session{Logger: capture})
	if got := (&Server{db: metricDatabase}).adminMediaStorageBytes(); got != 0 {
		t.Fatalf("fresh schema media storage = %d, want 0", got)
	}
	if len(capture.queries) != len(adminMediaStorageSources) {
		t.Fatalf("media storage query count = %d, want %d: %#v", len(capture.queries), len(adminMediaStorageSources), capture.queries)
	}
	queries := strings.Join(capture.queries, "\n")
	for _, source := range adminMediaStorageSources {
		if !strings.Contains(queries, `FROM "`+source.table+`"`) {
			t.Errorf("media storage SQL did not query %s:\n%s", source.table, queries)
		}
	}
	if strings.Contains(queries, `FROM "imports"`) || strings.Contains(queries, `FROM "bulk_imports"`) {
		t.Fatalf("media storage SQL queried database-backed import state:\n%s", queries)
	}
}

func TestMastodon4514AdminRetentionSnowflakeQueryAgainstPostgres(t *testing.T) {
	databaseURL := os.Getenv("PAON_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("PAON_TEST_DATABASE_URL is required for integration tests")
	}
	database, err := paondb.Open(config.Config{
		DatabaseURL:          databaseURL,
		DatabaseMaxOpenConns: 2,
		DatabaseMaxIdleConns: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()

	start := time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 0, 1)
	cohorts, ok := (&Server{db: database}).adminRetentionCohortsPostgreSQL(start, end, "day")
	if !ok {
		t.Fatal("Mastodon 4.5.14 Snowflake-range retention query failed")
	}
	if len(cohorts) != 2 || len(cohorts[0].Data) != 2 || len(cohorts[1].Data) != 1 {
		t.Fatalf("retention cohort grid = %#v", cohorts)
	}
}
