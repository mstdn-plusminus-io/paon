package migrate

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	paondb "github.com/mstdn-plusminus-io/paon/internal/paon/db"
	paonotp "github.com/mstdn-plusminus-io/paon/internal/paon/otp"
	paonschema "github.com/mstdn-plusminus-io/paon/internal/paon/schema"
	"gorm.io/gorm"
)

const (
	LegacySchemaVersion  = paonschema.Mastodon4219Version
	CurrentSchemaVersion = paonschema.Mastodon4422Version
)
const migrationAdvisoryLockID int64 = 0x50616f6e4d696772
const statementSeparator = "-- paon:statement"

//go:embed schema.sql
var schemaFiles embed.FS

type Options struct {
	Phase                  UpgradePhase
	AcknowledgeContract    bool
	IgnoreInvalidOTPSecret bool
	OTPSecret              string
	ActiveRecordEncryption paonotp.Credentials
	// Mastodon44TagTrendBackfill imports the two legacy Redis sorted sets into
	// tag_trends. The callback is intentionally injected by the command layer so
	// the SQL migrator does not acquire a Redis dependency. It is invoked inside
	// the database transaction and its migration marker is recorded only after
	// the callback succeeds, making retries safe when the callback uses UPSERT.
	Mastodon44TagTrendBackfill func(context.Context, *gorm.DB) error
	// Mastodon44SkipTagTrendBackfill is an explicit operator assertion that the
	// legacy Redis trend sets are absent or intentionally disposable. A missing
	// callback is otherwise fail-closed so the migration cannot silently record
	// the data marker without inspecting Redis.
	Mastodon44SkipTagTrendBackfill bool
	// Mastodon44TagTrendBackfillPostCommit removes the legacy Redis source only
	// after the transaction that records the tag-trend marker has committed.
	// It is also retried when the marker already exists, which repairs a process
	// interruption between PostgreSQL commit and Redis cleanup without repeating
	// the database import.
	Mastodon44TagTrendBackfillPostCommit func(context.Context) error
	Logf                                 func(format string, arguments ...any)
}

func OptionsFromEnv() Options {
	return Options{
		Phase:                          UpgradePhase(os.Getenv("PAON_MIGRATION_PHASE")),
		AcknowledgeContract:            os.Getenv("PAON_MIGRATION_ACKNOWLEDGE_CONTRACT") == "true",
		IgnoreInvalidOTPSecret:         os.Getenv("MIGRATION_IGNORE_INVALID_OTP_SECRET") == "true",
		Mastodon44SkipTagTrendBackfill: os.Getenv("MIGRATION_SKIP_TAG_TREND_BACKFILL") == "true",
		OTPSecret:                      os.Getenv("OTP_SECRET"),
		ActiveRecordEncryption: paonotp.Credentials{
			PrimaryKey:        os.Getenv("ACTIVE_RECORD_ENCRYPTION_PRIMARY_KEY"),
			DeterministicKey:  os.Getenv("ACTIVE_RECORD_ENCRYPTION_DETERMINISTIC_KEY"),
			KeyDerivationSalt: os.Getenv("ACTIVE_RECORD_ENCRYPTION_KEY_DERIVATION_SALT"),
		},
	}
}

func Run(ctx context.Context, database *gorm.DB) (bool, error) {
	return RunWithOptions(ctx, database, OptionsFromEnv())
}

func RunWithOptions(ctx context.Context, database *gorm.DB, options Options) (bool, error) {
	if database == nil {
		return false, errors.New("migration database is not configured")
	}
	targetPhase, err := requestedUpgradePhase(options)
	if err != nil {
		return false, err
	}
	if targetPhase == UpgradePhaseContract && !options.AcknowledgeContract {
		return false, errors.New("Mastodon contract phase requires --acknowledge-contract or PAON_MIGRATION_ACKNOWLEDGE_CONTRACT=true after all older-version processes have stopped")
	}
	applied := false
	legacy42Schema := false
	mastodon43Schema := false
	err = database.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(?)", migrationAdvisoryLockID).Error; err != nil {
			return fmt.Errorf("acquire migration lock: %w", err)
		}
		empty, current, legacy, err := databaseSchemaState(tx)
		if err != nil {
			return err
		}
		if current {
			return nil
		}
		if legacy {
			if err := validateMastodon4219UpgradePrerequisites(tx); err != nil {
				return err
			}
			legacy42Schema = true
			return nil
		}
		if !empty {
			previous, err := mastodon4323SchemaState(tx)
			if err != nil {
				return err
			}
			if previous {
				mastodon43Schema = true
				return nil
			}
			return fmt.Errorf("unsupported existing database schema; expected version %s, %s, or %s", LegacySchemaVersion, Mastodon4323SchemaVersion, CurrentSchemaVersion)
		}
		snapshot, err := schemaFiles.ReadFile("schema.sql")
		if err != nil {
			return fmt.Errorf("read embedded schema: %w", err)
		}
		salt, err := randomMigrationHex(16)
		if err != nil {
			return fmt.Errorf("generate timestamp ID salt: %w", err)
		}
		for index, statement := range strings.Split(strings.ReplaceAll(string(snapshot), "__PAON_TIMESTAMP_ID_SALT__", salt), statementSeparator) {
			statement = strings.TrimSpace(statement)
			statement = strings.TrimSuffix(statement, ";")
			if statement == "" {
				continue
			}
			if err := tx.Exec(statement).Error; err != nil {
				return fmt.Errorf("apply schema statement %d: %w", index, err)
			}
		}
		if err := seedFreshDatabase(tx); err != nil {
			return err
		}
		applied = true
		return nil
	})
	if err != nil {
		return false, err
	}
	if legacy42Schema {
		for _, phase := range upgradePhasesThrough(targetPhase) {
			phaseApplied, err := runMastodon43Phase(ctx, database, phase, options)
			if err != nil {
				return applied, err
			}
			applied = applied || phaseApplied
		}
		// A contract invocation against 4.2 completes the 4.3 inventory in the
		// loop above. Re-evaluate the marker set so the same acknowledged run can
		// continue through 4.4 instead of requiring an otherwise surprising
		// second invocation. Earlier phases intentionally stop at their 4.3
		// boundary because Mastodon4323SchemaVersion has not been recorded yet.
		previous, err := mastodon4323SchemaState(database.WithContext(ctx))
		if err != nil {
			return applied, err
		}
		mastodon43Schema = previous
	}
	if mastodon43Schema {
		for _, phase := range upgradePhasesThrough(targetPhase) {
			phaseApplied, err := runMastodon44Phase(ctx, database, phase, options)
			applied = applied || phaseApplied
			if err != nil {
				return applied, err
			}
		}
	}

	_, current, _, err := databaseSchemaState(database.WithContext(ctx))
	if err != nil {
		return applied, err
	}
	if !current {
		// Expand, backfill, and validate intentionally leave the 4.2 marker as
		// the latest supported application schema. Their phase-specific checks
		// run inside the same transaction as the phase, while the final Paon
		// schema guard runs inside and after a completed contract migration.
		return applied, nil
	}
	if err := paondb.SchemaAvailable(database); err != nil {
		return applied, fmt.Errorf("validate migrated schema: %w", err)
	}
	return applied, nil
}

func databaseSchemaState(tx *gorm.DB) (empty bool, current bool, legacy bool, err error) {
	var tableCount int64
	if err := tx.Raw(`SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = current_schema() AND table_type IN ('BASE TABLE', 'VIEW')`).Scan(&tableCount).Error; err != nil {
		return false, false, false, fmt.Errorf("inspect database tables: %w", err)
	}
	if tableCount == 0 {
		return true, false, false, nil
	}
	var migrationsTable string
	if err := tx.Raw(`SELECT COALESCE(to_regclass('schema_migrations')::text, '')`).Scan(&migrationsTable).Error; err != nil {
		return false, false, false, fmt.Errorf("inspect schema_migrations: %w", err)
	}
	if migrationsTable == "" {
		return false, false, false, nil
	}
	var versions struct {
		Current  int64
		Previous int64
		Legacy   int64
		Future   int64
	}
	if err := tx.Raw(`SELECT COUNT(*) FILTER (WHERE version = ?) AS current, COUNT(*) FILTER (WHERE version = ?) AS previous, COUNT(*) FILTER (WHERE version = ?) AS legacy, COUNT(*) FILTER (WHERE version > ?) AS future FROM schema_migrations`, CurrentSchemaVersion, Mastodon4323SchemaVersion, LegacySchemaVersion, CurrentSchemaVersion).Scan(&versions).Error; err != nil {
		return false, false, false, fmt.Errorf("read schema version: %w", err)
	}
	if versions.Future != 0 {
		return false, false, false, fmt.Errorf("database schema is newer than supported Mastodon version %s; found %d future migration marker(s)", CurrentSchemaVersion, versions.Future)
	}
	if versions.Current == 1 || versions.Previous == 1 || versions.Legacy == 1 {
		var upgradeVersions []string
		if err := tx.Raw(`SELECT version FROM schema_migrations WHERE version > ? AND version <= ? ORDER BY version`, LegacySchemaVersion, CurrentSchemaVersion).Scan(&upgradeVersions).Error; err != nil {
			return false, false, false, fmt.Errorf("inspect reviewed Mastodon migration markers: %w", err)
		}
		for _, version := range upgradeVersions {
			if !mastodon43UpgradeVersionKnown(version) && !paonschema.Mastodon44UpgradeVersionKnown(version) {
				return false, false, false, fmt.Errorf("database schema contains unsupported migration marker %s between Mastodon 4.2 and 4.4", version)
			}
		}
		if versions.Current == 1 {
			mastodon43Count, err := mastodon43AppliedCount(tx)
			if err != nil {
				return false, false, false, err
			}
			if mastodon43Count != int64(paonschema.Mastodon43UpgradeVersionCount()) {
				return false, false, false, fmt.Errorf("database schema has final marker %s but only %d of %d reviewed Mastodon 4.3 migration markers", CurrentSchemaVersion, mastodon43Count, paonschema.Mastodon43UpgradeVersionCount())
			}
			var mastodon44Count int64
			for version := range mastodon44UpgradeVersionSet() {
				var count int64
				if err := tx.Raw(`SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, version).Scan(&count).Error; err != nil {
					return false, false, false, fmt.Errorf("inspect Mastodon 4.4 migration marker %s: %w", version, err)
				}
				mastodon44Count += count
			}
			if mastodon44Count != int64(paonschema.Mastodon44UpgradeVersionCount()) {
				return false, false, false, fmt.Errorf("database schema has final marker %s but only %d of %d reviewed Mastodon 4.4 migration markers", CurrentSchemaVersion, mastodon44Count, paonschema.Mastodon44UpgradeVersionCount())
			}
		}
	}
	return false, versions.Current == 1, versions.Current == 0 && versions.Previous == 0 && versions.Legacy == 1, nil
}

func mastodon4323SchemaState(tx *gorm.DB) (bool, error) {
	applied, err := upgradeVersionApplied(tx, Mastodon4323SchemaVersion)
	if err != nil || !applied {
		return false, err
	}
	count, err := mastodon43AppliedCount(tx)
	if err != nil {
		return false, err
	}
	if count != int64(paonschema.Mastodon43UpgradeVersionCount()) {
		return false, fmt.Errorf("database schema has marker %s but only %d of %d reviewed Mastodon 4.3 migration markers", Mastodon4323SchemaVersion, count, paonschema.Mastodon43UpgradeVersionCount())
	}
	return true, nil
}

func mastodon43AppliedCount(tx *gorm.DB) (int64, error) {
	var versions []string
	if err := tx.Raw(`SELECT version FROM schema_migrations WHERE version > ? AND version <= ?`, LegacySchemaVersion, Mastodon4323SchemaVersion).Scan(&versions).Error; err != nil {
		return 0, fmt.Errorf("inspect Mastodon 4.3 migration history: %w", err)
	}
	var count int64
	for _, version := range versions {
		if paonschema.Mastodon43UpgradeVersionKnown(version) {
			count++
		}
	}
	return count, nil
}

// Keep a local iteration set because the schema package intentionally exposes
// membership and count rather than a mutable map.
func mastodon44UpgradeVersionSet() map[string]struct{} {
	versions := map[string]struct{}{}
	for _, phase := range []UpgradePhase{UpgradePhaseExpand, UpgradePhaseBackfill, UpgradePhaseValidate, UpgradePhaseContract} {
		for _, version := range mastodon44PhaseVersions(phase) {
			versions[version] = struct{}{}
		}
	}
	return versions
}

func seedFreshDatabase(tx *gorm.DB) error {
	now := time.Now().UTC()
	roles := []struct {
		id          int64
		name        string
		position    int
		permissions int64
	}{
		{id: -99, name: "", position: -1, permissions: 1 << 16},
		{id: 1, name: "Moderator", position: 10, permissions: 1308},
		{id: 2, name: "Admin", position: 100, permissions: 983036},
		{id: 3, name: "Owner", position: 1000, permissions: 1},
	}
	for _, role := range roles {
		if err := tx.Exec(`INSERT INTO user_roles (id, name, color, position, permissions, highlighted, created_at, updated_at) VALUES (?, ?, '', ?, ?, ?, ?, ?)`, role.id, role.name, role.position, role.permissions, role.id != -99, now, now).Error; err != nil {
			return fmt.Errorf("seed role %s: %w", role.name, err)
		}
	}
	if err := tx.Exec(`SELECT setval(pg_get_serial_sequence('user_roles', 'id'), 3, true)`).Error; err != nil {
		return fmt.Errorf("seed user role sequence: %w", err)
	}
	if err := tx.Exec(`INSERT INTO accounts (id, username, actor_type, locked, created_at, updated_at) VALUES (-99, 'mastodon.internal', 'Application', true, ?, ?)`, now, now).Error; err != nil {
		return fmt.Errorf("seed instance actor: %w", err)
	}
	uid, err := randomMigrationHex(16)
	if err != nil {
		return err
	}
	secret, err := randomMigrationHex(32)
	if err != nil {
		return err
	}
	if err := tx.Exec(`INSERT INTO oauth_applications (name, uid, secret, redirect_uri, scopes, superapp, confidential, created_at, updated_at) VALUES ('Web', ?, ?, 'urn:ietf:wg:oauth:2.0:oob', 'read write follow push', true, true, ?, ?)`, uid, secret, now, now).Error; err != nil {
		return fmt.Errorf("seed web OAuth application: %w", err)
	}
	return nil
}

func randomMigrationHex(size int) (string, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}
