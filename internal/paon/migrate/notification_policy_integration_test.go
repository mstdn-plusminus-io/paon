//go:build integration

package migrate

import (
	"os"
	"testing"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	paondb "github.com/mstdn-plusminus-io/paon/internal/paon/db"
)

func TestNotificationPolicyBackfillPreservesUpstreamIDsAgainstPostgreSQL(t *testing.T) {
	databaseURL := os.Getenv("PAON_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("PAON_TEST_DATABASE_URL is required for integration tests")
	}
	database, err := paondb.Open(config.Config{DatabaseURL: databaseURL, DatabaseMaxOpenConns: 2, DatabaseMaxIdleConns: 1})
	if err != nil {
		t.Fatal(err)
	}
	sqlDatabase, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDatabase.Close() })
	for _, statement := range []string{
		`DROP SCHEMA public CASCADE; CREATE SCHEMA public`,
		`CREATE TABLE schema_migrations (version varchar PRIMARY KEY)`,
		`CREATE TABLE users (id bigint PRIMARY KEY, account_id bigint NOT NULL, settings text)`,
		`CREATE TABLE notification_policies (id bigserial PRIMARY KEY, account_id bigint UNIQUE NOT NULL, filter_not_following boolean, filter_not_followers boolean, filter_new_accounts boolean, filter_private_mentions boolean, created_at timestamp NOT NULL, updated_at timestamp NOT NULL)`,
		// The staging dump has heap order different from user primary-key
		// order. Rails allocates policy IDs in the yielded batch's heap order.
		`INSERT INTO users (id, account_id, settings) VALUES (2, 1002, '{}'), (1, 1001, '{}')`,
	} {
		if err := database.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := applyNotificationPolicyBackfill(database, "20240304090449", false); err != nil {
		t.Fatal(err)
	}
	assertScalarInt64(t, database, `SELECT account_id FROM notification_policies WHERE id = 1`, 1002)
	assertScalarInt64(t, database, `SELECT account_id FROM notification_policies WHERE id = 2`, 1001)
	assertScalarInt64(t, database, `SELECT last_value FROM notification_policies_id_seq`, 2)
	if err := applyNotificationPolicyBackfill(database, "20240321160706", true); err != nil {
		t.Fatal(err)
	}
	// A skipped existing policy must never allocate an unused sequence value.
	assertScalarInt64(t, database, `SELECT last_value FROM notification_policies_id_seq`, 2)
	assertScalarInt64(t, database, `SELECT COUNT(*) FROM notification_policies`, 2)
	if err := database.Exec(`UPDATE notification_policies SET updated_at = TIMESTAMP '2000-01-01'; DELETE FROM schema_migrations WHERE version = '20240304090449'`).Error; err != nil {
		t.Fatal(err)
	}
	if err := applyNotificationPolicyBackfill(database, "20240304090449", false); err != nil {
		t.Fatal(err)
	}
	// Rails upsert_all preserves updated_at when every supplied value equals
	// the stored value, even though its first-pass UPSERT consumes sequence IDs.
	assertScalarInt64(t, database, `SELECT COUNT(*) FROM notification_policies WHERE updated_at = TIMESTAMP '2000-01-01'`, 2)
	assertScalarInt64(t, database, `SELECT last_value FROM notification_policies_id_seq`, 4)
}
