//go:build integration

package migrate

import (
	"context"
	"embed"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	paondb "github.com/mstdn-plusminus-io/paon/internal/paon/db"
	"github.com/mstdn-plusminus-io/paon/internal/paon/schemacatalog"
)

// This schema-only fixture preserves the dump/restored physical lineage of the
// staging database. Its goldens come from independently migrating the original
// populated dump with the pinned official Mastodon releases, not from Paon.
// Application rows, installation timestamps, and the snowflake salt are omitted
// or sanitized; bin/test-migration-parity separately checks the private full dump.
//
//go:embed testdata/staging_4_2_schema.sql testdata/staging_v*_catalog_pg*.json
var stagingDumpFixtures embed.FS

func TestStagingDumpLineageAgainstPostgreSQL(t *testing.T) {
	databaseURL := os.Getenv("PAON_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("PAON_TEST_DATABASE_URL is required for integration tests")
	}
	database, err := paondb.Open(config.Config{
		DatabaseURL: databaseURL, DatabaseMaxOpenConns: 1, DatabaseMaxIdleConns: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	sqlDatabase, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDatabase.Close() })
	ctx := context.Background()
	var postgresVersion int
	if err := sqlDatabase.QueryRowContext(ctx, `SELECT current_setting('server_version_num')::integer`).Scan(&postgresVersion); err != nil {
		t.Fatal(err)
	}
	major := postgresVersion / 10000
	if major != 14 && major != 15 {
		t.Fatalf("staging dump catalog requires an independently captured golden for PostgreSQL %d", major)
	}
	fixture, err := stagingDumpFixtures.ReadFile("testdata/staging_4_2_schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	fixtureSQL := strings.ReplaceAll(string(fixture), "__PAON_TIMESTAMP_ID_SALT__", strings.Repeat("0", 32))
	for _, version := range []string{"4.4.22"} {
		t.Run(version, func(t *testing.T) {
			// Like the package's existing migration tests, this requires a
			// disposable database and never runs in parallel with another test.
			if _, err := sqlDatabase.ExecContext(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
				t.Fatalf("reset staging fixture schema: %v", err)
			}
			if _, err := sqlDatabase.ExecContext(ctx, fixtureSQL); err != nil {
				t.Fatalf("restore sanitized staging fixture: %v", err)
			}
			goldenPath := fmt.Sprintf("testdata/staging_v%s_catalog_pg%d.json", strings.ReplaceAll(version, ".", "_"), major)
			golden, err := stagingDumpFixtures.ReadFile(goldenPath)
			if err != nil {
				t.Fatal(err)
			}
			options := Options{
				TargetVersion: version, All: true, AcknowledgeContract: true,
				Mastodon44SkipTagTrendBackfill: true,
			}
			applied, err := RunWithOptions(ctx, database, options)
			if err != nil || !applied {
				t.Fatalf("migrate staging lineage to %s: applied=%v, err=%v", version, applied, err)
			}
			if err := schemacatalog.CheckGolden(ctx, sqlDatabase, "public", golden); err != nil {
				t.Fatalf("staging lineage differs from official Mastodon %s: %v", version, err)
			}
			applied, err = RunWithOptions(ctx, database, options)
			if err != nil || applied {
				t.Fatalf("second staging migration to %s must be a no-op: applied=%v, err=%v", version, applied, err)
			}
			if err := schemacatalog.CheckGolden(ctx, sqlDatabase, "public", golden); err != nil {
				t.Fatalf("no-op changed the staging catalog for %s: %v", version, err)
			}
		})
	}
}
