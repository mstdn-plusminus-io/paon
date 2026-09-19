//go:build integration

package migrate

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	paondb "github.com/mstdn-plusminus-io/paon/internal/paon/db"
	paonotp "github.com/mstdn-plusminus-io/paon/internal/paon/otp"
	"gorm.io/gorm"
)

func TestBulkMigrationStopsAtEachReleaseAgainstPostgreSQL(t *testing.T) {
	database := openTargetIntegrationDatabase(t)
	for _, test := range []struct {
		version string
		pg14    []byte
		pg15    []byte
	}{
		{"4.3.23", mastodon4219To4323CatalogPG14, mastodon4219To4323Catalog},
		{"4.4.22", mastodon4219To4422CatalogPG14, mastodon4219To4422Catalog},
		{"4.5.15", mastodon4219To4515CatalogPG14, mastodon4219To4515Catalog},
		{"4.6.6", mastodon4219To466CatalogPG14, mastodon4219To466Catalog},
	} {
		t.Run(test.version, func(t *testing.T) {
			resetTargetIntegrationDatabase(t, database)
			snapshot := strings.ReplaceAll(string(mastodon4219Schema), "__PAON_TIMESTAMP_ID_SALT__", strings.Repeat("1", 32))
			for _, statement := range splitSQLStatements(snapshot) {
				if err := database.Exec(statement).Error; err != nil {
					t.Fatal(err)
				}
			}
			options := Options{
				TargetVersion: test.version, All: true, AcknowledgeContract: true,
				Mastodon44SkipTagTrendBackfill: true,
				ActiveRecordEncryption: paonotp.Credentials{
					PrimaryKey: strings.Repeat("p", 32), DeterministicKey: strings.Repeat("d", 32), KeyDerivationSalt: strings.Repeat("s", 32),
				},
			}
			applied, err := RunWithOptions(context.Background(), database, options)
			if err != nil || !applied {
				t.Fatalf("bulk migration to %s = applied %v, err %v", test.version, applied, err)
			}
			assertSchemaCatalogGolden(t, database, test.pg14, test.pg15)
			applied, err = RunWithOptions(context.Background(), database, options)
			if err != nil || applied {
				t.Fatalf("repeat bulk migration to %s = applied %v, err %v", test.version, applied, err)
			}
			assertSchemaCatalogGolden(t, database, test.pg14, test.pg15)
		})
	}
}

func TestFreshMigrationTargetsAgainstPostgreSQL(t *testing.T) {
	database := openTargetIntegrationDatabase(t)
	for _, test := range []struct {
		version string
		pg14    []byte
		pg15    []byte
	}{
		{"4.3.23", mastodon4323FreshCatalogPG14, mastodon4323FreshCatalog},
		{"4.4.22", mastodon4422FreshCatalogPG14, mastodon4422FreshCatalog},
		{"4.5.15", mastodon4515FreshCatalogPG14, mastodon4515FreshCatalog},
		{"4.6.6", mastodon466FreshCatalogPG14, mastodon466FreshCatalog},
	} {
		t.Run(test.version, func(t *testing.T) {
			resetTargetIntegrationDatabase(t, database)
			options := Options{TargetVersion: test.version}
			applied, err := RunWithOptions(context.Background(), database, options)
			if err != nil || !applied {
				t.Fatalf("fresh target %s = applied %v, err %v", test.version, applied, err)
			}
			moderatorPermissions := int64(1308)
			if test.version >= "4.5.15" {
				moderatorPermissions |= 1 << 20
			}
			assertScalarInt64(t, database, `SELECT permissions FROM user_roles WHERE id = 1`, moderatorPermissions)
			defaultPermissions := int64(1 << 16)
			if test.version >= "4.6.6" {
				defaultPermissions |= 1 << 21
			}
			assertScalarInt64(t, database, `SELECT permissions FROM user_roles WHERE id = -99`, defaultPermissions)
			assertSchemaCatalogGolden(t, database, test.pg14, test.pg15)
			applied, err = RunWithOptions(context.Background(), database, options)
			if err != nil || applied {
				t.Fatalf("repeat target %s = applied %v, err %v", test.version, applied, err)
			}
			assertSchemaCatalogGolden(t, database, test.pg14, test.pg15)
		})
	}
}

func TestMigrationTargetRejectsDowngradeBeforeReconciliationAgainstPostgreSQL(t *testing.T) {
	database := openTargetIntegrationDatabase(t)
	resetTargetIntegrationDatabase(t, database)
	if _, err := RunWithOptions(context.Background(), database, Options{}); err != nil {
		t.Fatal(err)
	}
	installLegacyPaonCanonicalNames(t, database)
	applied, err := RunWithOptions(context.Background(), database, Options{TargetVersion: "4.3.23", All: true, AcknowledgeContract: true})
	if applied || err == nil || !strings.Contains(err.Error(), "cannot migrate backwards") {
		t.Fatalf("downgrade = applied %v, err %v", applied, err)
	}
	assertScalarString(t, database, `SELECT conname FROM pg_constraint WHERE conrelid = 'account_aliases'::regclass AND contype = 'f'`, "account_aliases_account_id_fkey")

	resetTargetIntegrationDatabase(t, database)
	if _, err := RunWithOptions(context.Background(), database, Options{TargetVersion: "4.3.23"}); err != nil {
		t.Fatal(err)
	}
	if err := database.Exec(`INSERT INTO schema_migrations (version) VALUES ('20240918233930')`).Error; err != nil {
		t.Fatal(err)
	}
	applied, err = RunWithOptions(context.Background(), database, Options{TargetVersion: "4.3.23"})
	if applied || err == nil || !strings.Contains(err.Error(), "cannot migrate backwards") {
		t.Fatalf("partially applied 4.4 downgrade = applied %v, err %v", applied, err)
	}
}

func openTargetIntegrationDatabase(t *testing.T) *gorm.DB {
	t.Helper()
	databaseURL := os.Getenv("PAON_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("PAON_TEST_DATABASE_URL is required for integration tests")
	}
	database, err := paondb.Open(config.Config{DatabaseURL: databaseURL, DatabaseMaxOpenConns: 5, DatabaseMaxIdleConns: 2})
	if err != nil {
		t.Fatal(err)
	}
	sqlDatabase, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDatabase.Close() })
	return database
}

func resetTargetIntegrationDatabase(t *testing.T, database *gorm.DB) {
	t.Helper()
	if err := database.Exec(`DROP SCHEMA public CASCADE; CREATE SCHEMA public`).Error; err != nil {
		t.Fatal(err)
	}
}
