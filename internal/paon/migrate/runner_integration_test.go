//go:build integration

package migrate

import (
	"context"
	_ "embed"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	paondb "github.com/mstdn-plusminus-io/paon/internal/paon/db"
	paonotp "github.com/mstdn-plusminus-io/paon/internal/paon/otp"
	"gorm.io/gorm"
)

//go:embed testdata/mastodon_4_2_19_schema.sql
var mastodon4219Schema []byte

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
		`SELECT COUNT(*) FROM schema_migrations`:                               539,
	} {
		if err := database.Raw(query).Scan(&count).Error; err != nil || count != want {
			t.Fatalf("%s = %d, %v; want %d", query, count, err, want)
		}
	}
	assertMastodon43TimestampPrecisions(t, database)
	assertScalarString(t, database, `SELECT value FROM ar_internal_metadata WHERE key = 'schema_sha1'`, "b53e3b8de778cd1b53158326b97afa9368f3237e")
	assertRelationAvailable(t, database, "quotes", true)
	assertRelationAvailable(t, database, "imports", false)
	assertColumnAvailable(t, database, "users", "encrypted_otp_secret", false)
	assertColumnAvailable(t, database, "statuses", "quote_approval_policy", true)
}

func TestStagedMastodon4323UpgradeAgainstPostgreSQL(t *testing.T) {
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

	// Build a real 4.3.23 source using Paon's reviewed 4.2 fixture and 4.3
	// phased path, then exercise the independent 4.4 phase inventory.
	salt, err := randomMigrationHex(16)
	if err != nil {
		t.Fatal(err)
	}
	for index, statement := range strings.Split(strings.ReplaceAll(string(mastodon4219Schema), "__PAON_TIMESTAMP_ID_SALT__", salt), statementSeparator) {
		statement = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(statement), ";"))
		if statement == "" {
			continue
		}
		if err := database.Exec(statement).Error; err != nil {
			t.Fatalf("apply Mastodon 4.2.19 fixture statement %d: %v", index, err)
		}
	}
	credentials := paonotp.Credentials{
		PrimaryKey:        strings.Repeat("p", paonotp.MinimumCredentialLength),
		DeterministicKey:  strings.Repeat("d", paonotp.MinimumCredentialLength),
		KeyDerivationSalt: strings.Repeat("s", paonotp.MinimumCredentialLength),
	}
	for _, phase := range []UpgradePhase{UpgradePhaseExpand, UpgradePhaseBackfill, UpgradePhaseValidate, UpgradePhaseContract} {
		options := Options{Phase: phase, ActiveRecordEncryption: credentials}
		if phase == UpgradePhaseContract {
			options.AcknowledgeContract = true
		}
		applied, err := runMastodon43Phase(context.Background(), database, phase, options)
		if err != nil || !applied {
			t.Fatalf("prepare 4.3 phase %s = applied %v, err %v", phase, applied, err)
		}
	}
	assertMigrationVersionCount(t, database, Mastodon4323SchemaVersion, 1)
	assertMigrationVersionCount(t, database, CurrentSchemaVersion, 0)
	assertScalarInt64(t, database, `SELECT COUNT(*) FROM schema_migrations`, 472)
	encryptedOTP, err := paonotp.EncryptActiveRecord("JBSWY3DPEHPK3PXP", credentials)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Exec(`INSERT INTO accounts (id, username, created_at, updated_at) VALUES (7001, 'quoting', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP), (7002, 'quoted', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`).Error; err != nil {
		t.Fatalf("seed populated Mastodon 4.3 accounts: %v", err)
	}
	if err := database.Exec(`INSERT INTO users (id, email, account_id, otp_required_for_login, otp_secret, encrypted_otp_secret, created_at, updated_at) VALUES (7301, 'quote-migration@example.com', 7001, true, ?, 'legacy-column-retained-until-contract', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`, encryptedOTP).Error; err != nil {
		t.Fatalf("seed populated Mastodon 4.3 OTP user: %v", err)
	}
	if err := database.Exec(`INSERT INTO statuses (id, account_id, created_at, updated_at) VALUES (7101, 7001, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP), (7102, 7002, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP); INSERT INTO tags (id, name, created_at, updated_at) VALUES (7201, 'migration-trend', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP), (7202, 'migration-trend-higher', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP); INSERT INTO imports (id, type, approved, account_id, overwrite, created_at, updated_at) VALUES (7401, 0, false, 7001, false, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP); INSERT INTO settings (id, var, thing_type, thing_id, created_at, updated_at) VALUES (7501, 'notification_emails', 'User', 7301, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP); INSERT INTO account_aliases (id, account_id, acct, uri, created_at, updated_at) VALUES (7601, NULL, 'orphan@remote.example', 'https://remote.example/@orphan', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP); INSERT INTO web_push_subscriptions (id, endpoint, key_p256dh, key_auth, access_token_id, user_id, created_at, updated_at) VALUES (7701, 'https://push.example/legacy', 'p256dh', 'auth', NULL, NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`).Error; err != nil {
		t.Fatalf("seed populated Mastodon 4.3 fixture: %v", err)
	}

	applied, err := RunWithOptions(context.Background(), database, Options{Phase: UpgradePhaseExpand})
	if err != nil || !applied {
		t.Fatalf("4.4 expand = applied %v, err %v", applied, err)
	}
	assertRelationAvailable(t, database, "quotes", true)
	assertRelationAvailable(t, database, "fasp_follow_recommendations", true)
	assertRelationAvailable(t, database, "imports", true)
	assertMigrationVersionCount(t, database, CurrentSchemaVersion, 0)

	callbackCalls := 0
	backfill := func(ctx context.Context, tx *gorm.DB) error {
		callbackCalls++
		if callbackCalls == 1 {
			return errors.New("injected tag trend read failure")
		}
		return UpsertLegacyTagTrendRows(ctx, tx, []LegacyTagTrendRow{{TagID: 7201, Score: 2.5, Rank: 99, Allowed: true, Language: ""}, {TagID: 7202, Score: 9, Rank: 99, Language: ""}})
	}
	cleanupCalls := 0
	postCommit := func(_ context.Context) error {
		cleanupCalls++
		assertMigrationVersionCount(t, database, "20241123160722", 1)
		if cleanupCalls == 1 {
			return errors.New("injected tag trend Redis cleanup failure")
		}
		return nil
	}
	backfillOptions := Options{Phase: UpgradePhaseBackfill, Mastodon44TagTrendBackfill: backfill, Mastodon44TagTrendBackfillPostCommit: postCommit}
	applied, err = RunWithOptions(context.Background(), database, backfillOptions)
	if err == nil || applied {
		t.Fatalf("4.4 backfill accepted failed Redis read: applied %v, err %v", applied, err)
	}
	assertMigrationVersionCount(t, database, "20241123160722", 0)
	if cleanupCalls != 0 {
		t.Fatalf("cleanup ran before PostgreSQL commit: calls=%d", cleanupCalls)
	}
	applied, err = RunWithOptions(context.Background(), database, backfillOptions)
	if err == nil || !applied {
		t.Fatalf("4.4 backfill did not report post-commit cleanup failure: applied %v, err %v", applied, err)
	}
	assertMigrationVersionCount(t, database, "20241123160722", 1)
	assertScalarInt64(t, database, `SELECT COUNT(*) FROM tag_trends WHERE tag_id = 7201 AND score = 2.5 AND rank = 2 AND allowed = true AND language = ''`, 1)
	assertScalarInt64(t, database, `SELECT COUNT(*) FROM tag_trends WHERE tag_id = 7202 AND score = 9 AND rank = 1 AND allowed = false AND language = ''`, 1)
	assertScalarInt64(t, database, `SELECT COUNT(*) FROM settings WHERE id = 7501`, 0)
	applied, err = RunWithOptions(context.Background(), database, backfillOptions)
	if err != nil || applied {
		t.Fatalf("retry 4.4 post-commit cleanup = applied %v, err %v", applied, err)
	}
	if callbackCalls != 2 || cleanupCalls != 2 {
		t.Fatalf("tag trend retry calls = backfill %d cleanup %d, want 2/2", callbackCalls, cleanupCalls)
	}

	applied, err = RunWithOptions(context.Background(), database, Options{Phase: UpgradePhaseValidate, ActiveRecordEncryption: credentials})
	if err == nil || applied {
		t.Fatalf("4.4 validate accepted pending imports: applied %v, err %v", applied, err)
	}
	assertMigrationVersionCount(t, database, mastodon44ValidateSteps()[0].version, 0)
	if err := database.Exec(`DELETE FROM imports WHERE id = 7401`).Error; err != nil {
		t.Fatalf("mark legacy import complete: %v", err)
	}
	applied, err = RunWithOptions(context.Background(), database, Options{Phase: UpgradePhaseValidate, ActiveRecordEncryption: credentials})
	if err != nil || !applied {
		t.Fatalf("4.4 validate = applied %v, err %v", applied, err)
	}
	assertScalarInt64(t, database, `SELECT COUNT(*) FROM account_aliases WHERE id = 7601`, 0)
	assertScalarInt64(t, database, `SELECT COUNT(*) FROM web_push_subscriptions WHERE id = 7701`, 0)
	if err := database.Exec(`UPDATE users SET otp_secret = 'not-active-record-ciphertext' WHERE id = 7301`).Error; err != nil {
		t.Fatalf("inject invalid migrated OTP: %v", err)
	}
	applied, err = RunWithOptions(context.Background(), database, Options{Phase: UpgradePhaseContract, AcknowledgeContract: true, ActiveRecordEncryption: credentials})
	if err == nil || applied {
		t.Fatalf("4.4 contract accepted invalid migrated OTP: applied %v, err %v", applied, err)
	}
	assertMigrationVersionCount(t, database, CurrentSchemaVersion, 0)
	assertRelationAvailable(t, database, "imports", true)
	assertColumnAvailable(t, database, "users", "encrypted_otp_secret", true)
	if err := database.Exec(`UPDATE users SET otp_secret = ? WHERE id = 7301`, encryptedOTP).Error; err != nil {
		t.Fatalf("repair migrated OTP: %v", err)
	}
	applied, err = RunWithOptions(context.Background(), database, Options{Phase: UpgradePhaseContract, AcknowledgeContract: true, ActiveRecordEncryption: credentials})
	if err != nil || !applied {
		t.Fatalf("4.4 contract = applied %v, err %v", applied, err)
	}
	assertScalarInt64(t, database, `SELECT COUNT(*) FROM schema_migrations`, 539)
	assertMigrationVersionCount(t, database, CurrentSchemaVersion, 1)
	assertRelationAvailable(t, database, "fasp_follow_recommendations", true)
	assertRelationAvailable(t, database, "imports", false)
	assertColumnAvailable(t, database, "settings", "thing_type", false)
	assertColumnAvailable(t, database, "users", "encrypted_otp_secret", false)
	assertColumnNullable(t, database, "web_push_subscriptions", "user_id", false)
	assertColumnNullable(t, database, "polls", "status_id", false)
	assertScalarString(t, database, `SELECT value FROM ar_internal_metadata WHERE key = 'schema_sha1'`, "b53e3b8de778cd1b53158326b97afa9368f3237e")
	if err := paondb.SchemaAvailable(database); err != nil {
		t.Fatalf("validate contracted 4.4 schema: %v", err)
	}
}

func TestStagedMastodon4219UpgradeAgainstPostgreSQL(t *testing.T) {
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
	salt, err := randomMigrationHex(16)
	if err != nil {
		t.Fatal(err)
	}
	for index, statement := range strings.Split(strings.ReplaceAll(string(mastodon4219Schema), "__PAON_TIMESTAMP_ID_SALT__", salt), statementSeparator) {
		statement = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(statement), ";"))
		if statement == "" {
			continue
		}
		if err := database.Exec(statement).Error; err != nil {
			t.Fatalf("apply Mastodon 4.2.19 fixture statement %d: %v", index, err)
		}
	}
	assertScalarInt64(t, database, `SELECT COUNT(*) FROM schema_migrations`, 422)
	assertScalarString(t, database, `SELECT value FROM ar_internal_metadata WHERE key = 'schema_sha1'`, "7d5086228b379c66ff21a4396f443ba4daac5752")
	if err := database.Exec(`DELETE FROM schema_migrations WHERE version = '20180813113448'`).Error; err != nil {
		t.Fatal(err)
	}
	assertMigrationVersionCount(t, database, "20180813113448", 0)
	if err := database.Exec(`ALTER TABLE accounts DROP COLUMN devices_url`).Error; err != nil {
		t.Fatal(err)
	}
	applied, err := RunWithOptions(context.Background(), database, Options{})
	if err == nil || applied {
		t.Fatalf("expand accepted malformed Mastodon 4.2 base: applied %v, err %v", applied, err)
	}
	assertMigrationVersionCount(t, database, mastodon43ExpandSteps()[0].version, 0)
	if err := database.Exec(`ALTER TABLE accounts ADD COLUMN devices_url character varying`).Error; err != nil {
		t.Fatal(err)
	}
	seedWorstCaseMastodon4219Fixture(t, database)

	applied, err = RunWithOptions(context.Background(), database, Options{})
	if err != nil || !applied {
		t.Fatalf("expand RunWithOptions() = applied %v, err %v", applied, err)
	}
	assertMigrationVersionCount(t, database, mastodon43ExpandSteps()[len(mastodon43ExpandSteps())-1].version, 1)
	assertMigrationVersionCount(t, database, "20240109103012", 0)
	assertMigrationVersionCount(t, database, CurrentSchemaVersion, 0)
	assertRelationAvailable(t, database, "devices", true)
	assertColumnAvailable(t, database, "users", "admin", true)
	assertColumnNullable(t, database, "mentions", "status_id", true)
	assertColumnNullable(t, database, "mentions", "account_id", true)
	assertColumnDefaultAvailable(t, database, "status_pins", "created_at", true)

	applied, err = RunWithOptions(context.Background(), database, Options{})
	if err != nil || applied {
		t.Fatalf("second expand RunWithOptions() = applied %v, err %v", applied, err)
	}
	if err := database.Exec(`INSERT INTO schema_migrations (version) VALUES ('20231111111111')`).Error; err != nil {
		t.Fatal(err)
	}
	applied, err = RunWithOptions(context.Background(), database, Options{Phase: UpgradePhaseBackfill})
	if err == nil || applied {
		t.Fatalf("backfill with unknown migration marker RunWithOptions() = applied %v, err %v", applied, err)
	}
	assertMigrationVersionCount(t, database, "20240109103012", 0)
	if err := database.Exec(`DELETE FROM schema_migrations WHERE version = '20231111111111'`).Error; err != nil {
		t.Fatal(err)
	}

	credentials := paonotp.Credentials{
		PrimaryKey:        strings.Repeat("p", paonotp.MinimumCredentialLength),
		DeterministicKey:  strings.Repeat("d", paonotp.MinimumCredentialLength),
		KeyDerivationSalt: strings.Repeat("s", paonotp.MinimumCredentialLength),
	}
	if err := database.Exec(`CREATE FUNCTION fail_second_alias_backfill_batch() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF OLD.id = 3602 THEN RAISE EXCEPTION 'injected alias backfill failure'; END IF; RETURN OLD; END $$; CREATE TRIGGER fail_second_alias_backfill_batch BEFORE DELETE ON account_aliases FOR EACH ROW EXECUTE FUNCTION fail_second_alias_backfill_batch()`).Error; err != nil {
		t.Fatalf("install backfill failure injection: %v", err)
	}
	applied, err = RunWithOptions(context.Background(), database, Options{Phase: UpgradePhaseBackfill, ActiveRecordEncryption: credentials})
	if err == nil || applied {
		t.Fatalf("interrupted backfill RunWithOptions() = applied %v, err %v", applied, err)
	}
	assertMigrationVersionCount(t, database, "20231018193209", 0)
	assertScalarInt64(t, database, `SELECT COUNT(*) FROM account_aliases WHERE account_id = 1001 AND uri = 'https://remote.example/@alias'`, 502)
	assertScalarString(t, database, `SELECT locale FROM users WHERE id = 2001`, "fr-QC")
	assertScalarInt64(t, database, `SELECT COUNT(*) FROM users WHERE id = 2001 AND otp_secret IS NULL`, 1)
	if err := database.Exec(`DROP TRIGGER fail_second_alias_backfill_batch ON account_aliases; DROP FUNCTION fail_second_alias_backfill_batch()`).Error; err != nil {
		t.Fatalf("remove backfill failure injection: %v", err)
	}

	applied, err = RunWithOptions(context.Background(), database, Options{Phase: UpgradePhaseBackfill, ActiveRecordEncryption: credentials})
	if err != nil || !applied {
		t.Fatalf("backfill RunWithOptions() = applied %v, err %v", applied, err)
	}
	assertMigrationVersionCount(t, database, "20240808124339", 1)
	assertMigrationVersionCount(t, database, CurrentSchemaVersion, 0)
	assertRelationAvailable(t, database, "devices", true)
	assertRelationAvailable(t, database, "encrypted_messages_id_seq", true)
	assertColumnAvailable(t, database, "notification_policies", "filter_not_following", true)
	assertScalarInt64(t, database, `SELECT COUNT(*) FROM account_aliases WHERE account_id = 1001 AND uri = 'https://remote.example/@alias'`, 1)
	assertScalarInt64(t, database, `SELECT COUNT(*) FROM custom_filter_statuses WHERE custom_filter_id = 4001 AND status_id = 3001`, 1)
	assertScalarInt64(t, database, `SELECT COUNT(*) FROM identities WHERE uid = 'duplicate-uid' AND provider = 'oidc'`, 1)
	assertScalarInt64(t, database, `SELECT COUNT(*) FROM webauthn_credentials WHERE user_id = 2001 AND nickname = 'duplicate-key'`, 1)
	assertScalarString(t, database, `SELECT locale FROM users WHERE id = 2001`, "fr-CA")
	assertScalarString(t, database, `SELECT scopes FROM oauth_applications WHERE id = 5001`, "read profile crypto")
	assertScalarString(t, database, `SELECT scopes FROM oauth_access_tokens WHERE id = 5001`, "read profile crypto")
	assertScalarInt64(t, database, `SELECT COUNT(*) FROM notification_policies WHERE account_id = 1001 AND filter_not_following AND filter_not_followers AND NOT filter_private_mentions AND for_not_following = 1 AND for_not_followers = 1 AND for_private_mentions = 0`, 1)
	var migratedOTP string
	if err := database.Raw(`SELECT otp_secret FROM users WHERE id = 2001`).Scan(&migratedOTP).Error; err != nil {
		t.Fatal(err)
	}
	clearOTP, err := paonotp.DecryptActiveRecord(migratedOTP, credentials)
	if err != nil || clearOTP != "JBSWY3DPEHPK3PXP" {
		t.Fatalf("migrated OTP = %q, err %v", clearOTP, err)
	}

	applied, err = RunWithOptions(context.Background(), database, Options{Phase: UpgradePhaseValidate, ActiveRecordEncryption: credentials})
	if err == nil || applied {
		t.Fatalf("validate with NULL mentions RunWithOptions() = applied %v, err %v", applied, err)
	}
	assertMigrationVersionCount(t, database, mastodon43ValidateSteps()[0].version, 0)
	assertColumnNullable(t, database, "mentions", "status_id", true)
	assertColumnDefaultAvailable(t, database, "status_pins", "created_at", true)
	if err := database.Exec(`UPDATE mentions SET status_id = 3001, account_id = 1001 WHERE status_id IS NULL OR account_id IS NULL`).Error; err != nil {
		t.Fatalf("repair fixture mentions after validate refusal: %v", err)
	}
	applied, err = RunWithOptions(context.Background(), database, Options{Phase: UpgradePhaseValidate, ActiveRecordEncryption: credentials})
	if err != nil || !applied {
		t.Fatalf("validate RunWithOptions() = applied %v, err %v", applied, err)
	}
	assertMigrationVersionCount(t, database, mastodon43ValidateSteps()[len(mastodon43ValidateSteps())-1].version, 1)
	assertMigrationVersionCount(t, database, CurrentSchemaVersion, 0)
	assertColumnNullable(t, database, "mentions", "status_id", false)
	assertColumnNullable(t, database, "mentions", "account_id", false)
	assertColumnDefaultAvailable(t, database, "status_pins", "created_at", false)

	applied, err = RunWithOptions(context.Background(), database, Options{Phase: UpgradePhaseContract})
	if err == nil || applied {
		t.Fatalf("unacknowledged contract RunWithOptions() = applied %v, err %v", applied, err)
	}
	assertMigrationVersionCount(t, database, "20240808124339", 1)
	assertMigrationVersionCount(t, database, CurrentSchemaVersion, 0)
	assertRelationAvailable(t, database, "devices", true)

	if err := database.Exec(`DROP INDEX index_accounts_on_uri`).Error; err != nil {
		t.Fatalf("inject final schema guard failure: %v", err)
	}
	applied, err = RunWithOptions(context.Background(), database, Options{Phase: UpgradePhaseContract, AcknowledgeContract: true, ActiveRecordEncryption: credentials, Mastodon44SkipTagTrendBackfill: true})
	if err == nil || !applied {
		t.Fatalf("contract with malformed final schema RunWithOptions() = applied %v, err %v", applied, err)
	}
	assertMigrationVersionCount(t, database, CurrentSchemaVersion, 0)
	assertRelationAvailable(t, database, "devices", false)
	assertRelationAvailable(t, database, "encrypted_messages_id_seq", false)
	assertColumnAvailable(t, database, "users", "admin", false)
	assertRelationAvailable(t, database, "quotes", true)
	assertRelationAvailable(t, database, "imports", true)
	assertScalarString(t, database, `SELECT scopes FROM oauth_applications WHERE id = 5001`, "read profile")
	if err := database.Exec(`CREATE INDEX index_accounts_on_uri ON accounts (uri)`).Error; err != nil {
		t.Fatalf("repair final schema guard fixture: %v", err)
	}

	applied, err = RunWithOptions(context.Background(), database, Options{Phase: UpgradePhaseContract, AcknowledgeContract: true, ActiveRecordEncryption: credentials, Mastodon44SkipTagTrendBackfill: true})
	if err != nil || !applied {
		t.Fatalf("contract RunWithOptions() = applied %v, err %v", applied, err)
	}
	assertMigrationVersionCount(t, database, CurrentSchemaVersion, 1)
	assertMigrationVersionCount(t, database, "20180813113448", 1)
	assertScalarInt64(t, database, `SELECT COUNT(*) FROM schema_migrations`, 539)
	assertRelationAvailable(t, database, "devices", false)
	assertRelationAvailable(t, database, "encrypted_messages_id_seq", false)
	assertColumnAvailable(t, database, "users", "admin", false)
	assertScalarString(t, database, `SELECT scopes FROM oauth_applications WHERE id = 5001`, "read profile")
	assertScalarString(t, database, `SELECT scopes FROM oauth_access_tokens WHERE id = 5001`, "read profile")
	assertRelationAvailable(t, database, "imports", false)
	assertColumnAvailable(t, database, "users", "encrypted_otp_secret", false)
	assertScalarString(t, database, `SELECT value FROM ar_internal_metadata WHERE key = 'schema_sha1'`, "b53e3b8de778cd1b53158326b97afa9368f3237e")
	assertMastodon43TimestampPrecisions(t, database)
	if err := paondb.SchemaAvailable(database); err != nil {
		t.Fatalf("validate contracted schema: %v", err)
	}
	if err := database.Exec(`DELETE FROM schema_migrations WHERE version = '20240916190140'`).Error; err != nil {
		t.Fatal(err)
	}
	if err := paondb.SchemaAvailable(database); err == nil {
		t.Fatal("SchemaAvailable accepted a final marker with an incomplete data-migration history")
	}
	applied, err = RunWithOptions(context.Background(), database, Options{})
	if err == nil || applied {
		t.Fatalf("RunWithOptions accepted incomplete current migration history: applied %v, err %v", applied, err)
	}
	if err := database.Exec(`INSERT INTO schema_migrations (version) VALUES ('20240916190140')`).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Exec(`INSERT INTO schema_migrations (version) VALUES ('20231111111111')`).Error; err != nil {
		t.Fatal(err)
	}
	if err := paondb.SchemaAvailable(database); err == nil {
		t.Fatal("SchemaAvailable accepted an unknown post-4.2 migration marker")
	}
	if err := database.Exec(`DELETE FROM schema_migrations WHERE version = '20231111111111'`).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Exec(`INSERT INTO schema_migrations (version) VALUES ('20250101000000')`).Error; err != nil {
		t.Fatal(err)
	}
	if err := paondb.SchemaAvailable(database); err == nil {
		t.Fatal("SchemaAvailable accepted a future migration marker")
	}
	applied, err = RunWithOptions(context.Background(), database, Options{})
	if err == nil || applied {
		t.Fatalf("RunWithOptions accepted future migration marker: applied %v, err %v", applied, err)
	}
	if err := database.Exec(`DELETE FROM schema_migrations WHERE version = '20250101000000'`).Error; err != nil {
		t.Fatal(err)
	}

	applied, err = RunWithOptions(context.Background(), database, Options{Phase: UpgradePhaseContract, AcknowledgeContract: true, ActiveRecordEncryption: credentials, Mastodon44SkipTagTrendBackfill: true})
	if err != nil || applied {
		t.Fatalf("second contract RunWithOptions() = applied %v, err %v", applied, err)
	}
}

func assertMastodon43TimestampPrecisions(t *testing.T, database *gorm.DB) {
	t.Helper()
	for _, table := range []string{
		"account_relationship_severance_events",
		"account_statuses_cleanup_policies",
		"appeals",
		"ar_internal_metadata",
		"bulk_import_rows",
		"bulk_imports",
		"canonical_email_blocks",
		"custom_filter_keywords",
		"custom_filter_statuses",
		"follow_recommendation_mutes",
		"follow_recommendation_suppressions",
		"generated_annual_reports",
		"notification_permissions",
		"notification_policies",
		"notification_requests",
		"preview_card_providers",
		"relationship_severance_events",
		"severed_relationships",
		"software_updates",
		"status_edits",
		"tag_follows",
		"user_roles",
		"webhooks",
	} {
		assertColumnType(t, database, table, "created_at", "timestamp(6) without time zone")
		assertColumnType(t, database, table, "updated_at", "timestamp(6) without time zone")
	}
	assertColumnType(t, database, "generated_annual_reports", "viewed_at", "timestamp(6) without time zone")
	assertColumnType(t, database, "preview_cards", "published_at", "timestamp(6) without time zone")
}

func assertColumnType(t *testing.T, database *gorm.DB, table string, column string, want string) {
	t.Helper()
	var got string
	if err := database.Raw(`SELECT format_type(attribute_data.atttypid, attribute_data.atttypmod) FROM pg_attribute attribute_data JOIN pg_class table_data ON table_data.oid = attribute_data.attrelid JOIN pg_namespace namespace_data ON namespace_data.oid = table_data.relnamespace WHERE namespace_data.nspname = current_schema() AND table_data.relname = ? AND attribute_data.attname = ? AND attribute_data.attnum > 0 AND NOT attribute_data.attisdropped`, table, column).Scan(&got).Error; err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("column %s.%s type = %q, want %q", table, column, got, want)
	}
}

func seedWorstCaseMastodon4219Fixture(t *testing.T, database *gorm.DB) {
	t.Helper()
	statements := []string{
		`INSERT INTO accounts (id, username, created_at, updated_at) VALUES (1001, 'migration-user', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
		`INSERT INTO users (id, email, created_at, updated_at, account_id, locale, settings, otp_required_for_login, encrypted_otp_secret) VALUES (2001, 'migration@example.com', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, 1001, 'fr-QC', '{"interactions.must_be_follower":true,"interactions.must_be_following":true,"interactions.must_be_following_dm":false}', true, 'paon-go-totp:JBSWY3DPEHPK3PXP')`,
		`INSERT INTO statuses (id, account_id, created_at, updated_at) VALUES (3001, 1001, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
		`INSERT INTO mentions (id, created_at, updated_at) VALUES (3001, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
		`INSERT INTO account_aliases (id, account_id, acct, uri, created_at, updated_at) VALUES (3101, 1001, 'alias@remote.example', 'https://remote.example/@alias', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP), (3102, 1001, 'alias@remote.example', 'https://remote.example/@alias', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
		`INSERT INTO account_aliases (id, account_id, acct, uri, created_at, updated_at) SELECT id, 1001, 'alias@remote.example', 'https://remote.example/@alias', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP FROM generate_series(3103, 3602) AS id`,
		`INSERT INTO custom_filters (id, account_id, phrase, created_at, updated_at) VALUES (4001, 1001, 'migration', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
		`INSERT INTO custom_filter_statuses (id, custom_filter_id, status_id, created_at, updated_at) VALUES (4101, 4001, 3001, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP), (4102, 4001, 3001, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
		`INSERT INTO identities (id, provider, uid, user_id, created_at, updated_at) VALUES (4201, 'oidc', 'duplicate-uid', 2001, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP), (4202, 'oidc', 'duplicate-uid', 2001, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
		`INSERT INTO webauthn_credentials (id, external_id, public_key, nickname, user_id, created_at, updated_at) VALUES (4301, 'external-1', 'key-1', 'duplicate-key', 2001, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP), (4302, 'external-2', 'key-2', 'duplicate-key', 2001, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
		`INSERT INTO oauth_applications (id, name, uid, secret, redirect_uri, scopes, created_at, updated_at) VALUES (5001, 'Migration', 'migration-uid', 'migration-secret', 'urn:ietf:wg:oauth:2.0:oob', 'read read:me crypto', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
		`INSERT INTO oauth_access_tokens (id, token, created_at, scopes, application_id, resource_owner_id) VALUES (5001, 'migration-token', CURRENT_TIMESTAMP, 'read read:me crypto', 5001, 2001)`,
		`INSERT INTO devices (id, access_token_id, account_id, device_id, name, fingerprint_key, identity_key, created_at, updated_at) VALUES (6001, 5001, 1001, 'device-1', 'Fixture', 'fingerprint', 'identity', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
		`INSERT INTO one_time_keys (id, device_id, key_id, key, signature, created_at, updated_at) VALUES (6101, 6001, 'key-1', 'key', 'signature', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
		`INSERT INTO encrypted_messages (id, device_id, from_account_id, from_device_id, body, digest, message_franking, created_at, updated_at) VALUES (6201, 6001, 1001, 'device-1', 'body', 'digest', 'franking', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
		`INSERT INTO system_keys (id, key, created_at, updated_at) VALUES (6301, decode('00', 'hex'), CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
	}
	for index, statement := range statements {
		if err := database.Exec(statement).Error; err != nil {
			t.Fatalf("seed Mastodon 4.2.19 worst-case fixture statement %d: %v", index, err)
		}
	}
}

func assertMigrationVersionCount(t *testing.T, database *gorm.DB, version string, want int64) {
	t.Helper()
	var count int64
	if err := database.Raw(`SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, version).Scan(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != want {
		t.Fatalf("schema_migrations version %s count = %d, want %d", version, count, want)
	}
}

func assertRelationAvailable(t *testing.T, database *gorm.DB, relation string, want bool) {
	t.Helper()
	var available bool
	if err := database.Raw(`SELECT to_regclass(?) IS NOT NULL`, relation).Scan(&available).Error; err != nil {
		t.Fatal(err)
	}
	if available != want {
		t.Fatalf("relation %s available = %v, want %v", relation, available, want)
	}
}

func assertColumnAvailable(t *testing.T, database *gorm.DB, table string, column string, want bool) {
	t.Helper()
	var available bool
	if err := database.Raw(`SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = ? AND column_name = ?)`, table, column).Scan(&available).Error; err != nil {
		t.Fatal(err)
	}
	if available != want {
		t.Fatalf("column %s.%s available = %v, want %v", table, column, available, want)
	}
}

func assertColumnNullable(t *testing.T, database *gorm.DB, table string, column string, want bool) {
	t.Helper()
	var nullable string
	if err := database.Raw(`SELECT is_nullable FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = ? AND column_name = ?`, table, column).Scan(&nullable).Error; err != nil {
		t.Fatal(err)
	}
	got := nullable == "YES"
	if got != want {
		t.Fatalf("column %s.%s nullable = %v, want %v", table, column, got, want)
	}
}

func assertColumnDefaultAvailable(t *testing.T, database *gorm.DB, table string, column string, want bool) {
	t.Helper()
	var available bool
	if err := database.Raw(`SELECT column_default IS NOT NULL FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = ? AND column_name = ?`, table, column).Scan(&available).Error; err != nil {
		t.Fatal(err)
	}
	if available != want {
		t.Fatalf("column %s.%s default available = %v, want %v", table, column, available, want)
	}
}

func assertScalarInt64(t *testing.T, database *gorm.DB, query string, want int64) {
	t.Helper()
	var got int64
	if err := database.Raw(query).Scan(&got).Error; err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("query %q = %d, want %d", query, got, want)
	}
}

func assertScalarString(t *testing.T, database *gorm.DB, query string, want string) {
	t.Helper()
	var got string
	if err := database.Raw(query).Scan(&got).Error; err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("query %q = %q, want %q", query, got, want)
	}
}
