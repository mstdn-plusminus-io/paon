//go:build integration

package migrate

import (
	"context"
	"os"
	"testing"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	paondb "github.com/mstdn-plusminus-io/paon/internal/paon/db"
)

func TestFreshMigrationAgainstPostgreSQL(t *testing.T) {
	databaseURL := os.Getenv("PAON_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("PAON_TEST_DATABASE_URL is required for integration tests")
	}
	database, err := paondb.Open(config.Config{DatabaseURL: databaseURL, DatabaseMaxOpenConns: 5, DatabaseMaxIdleConns: 2})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Exec(`DROP SCHEMA public CASCADE; CREATE SCHEMA public`).Error; err != nil {
		t.Fatalf("reset integration schema: %v", err)
	}
	var count int64
	if err := database.Raw(`SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = current_schema()`).Scan(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("integration database must be empty, found %d tables", count)
	}
	applied, err := Run(context.Background(), database)
	if err != nil || !applied {
		t.Fatalf("fresh Run() = applied %v, err %v", applied, err)
	}
	applied, err = Run(context.Background(), database)
	if err != nil || applied {
		t.Fatalf("second Run() = applied %v, err %v", applied, err)
	}
	for query, want := range map[string]int64{
		`SELECT COUNT(*) FROM user_roles`:                                      4,
		`SELECT COUNT(*) FROM accounts WHERE id = -99`:                         1,
		`SELECT COUNT(*) FROM oauth_applications WHERE superapp = true`:        1,
		`SELECT COUNT(*) FROM pg_matviews WHERE schemaname = current_schema()`: 3,
	} {
		if err := database.Raw(query).Scan(&count).Error; err != nil || count != want {
			t.Fatalf("%s = %d, %v; want %d", query, count, err, want)
		}
	}
}
