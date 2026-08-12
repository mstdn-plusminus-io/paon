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
	CurrentSchemaVersion = paonschema.Mastodon4323Version
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
	Logf                   func(format string, arguments ...any)
}

func OptionsFromEnv() Options {
	return Options{
		Phase:                  UpgradePhase(os.Getenv("PAON_MIGRATION_PHASE")),
		AcknowledgeContract:    os.Getenv("PAON_MIGRATION_ACKNOWLEDGE_CONTRACT") == "true",
		IgnoreInvalidOTPSecret: os.Getenv("MIGRATION_IGNORE_INVALID_OTP_SECRET") == "true",
		OTPSecret:              os.Getenv("OTP_SECRET"),
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
		return false, errors.New("Mastodon 4.3 contract phase requires --acknowledge-contract or PAON_MIGRATION_ACKNOWLEDGE_CONTRACT=true after all 4.2 processes have stopped")
	}
	applied := false
	legacySchema := false
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
			legacySchema = true
			return nil
		}
		if !empty {
			return fmt.Errorf("unsupported existing database schema; expected version %s or %s", LegacySchemaVersion, CurrentSchemaVersion)
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
	if legacySchema {
		for _, phase := range upgradePhasesThrough(targetPhase) {
			phaseApplied, err := runMastodon43Phase(ctx, database, phase, options)
			if err != nil {
				return applied, err
			}
			applied = applied || phaseApplied
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
		Current int64
		Legacy  int64
		Future  int64
	}
	if err := tx.Raw(`SELECT COUNT(*) FILTER (WHERE version = ?) AS current, COUNT(*) FILTER (WHERE version = ?) AS legacy, COUNT(*) FILTER (WHERE version > ?) AS future FROM schema_migrations`, CurrentSchemaVersion, LegacySchemaVersion, CurrentSchemaVersion).Scan(&versions).Error; err != nil {
		return false, false, false, fmt.Errorf("read schema version: %w", err)
	}
	if versions.Future != 0 {
		return false, false, false, fmt.Errorf("database schema is newer than supported Mastodon version %s; found %d future migration marker(s)", CurrentSchemaVersion, versions.Future)
	}
	if versions.Current == 1 || versions.Legacy == 1 {
		var upgradeVersions []string
		if err := tx.Raw(`SELECT version FROM schema_migrations WHERE version > ? AND version <= ? ORDER BY version`, LegacySchemaVersion, CurrentSchemaVersion).Scan(&upgradeVersions).Error; err != nil {
			return false, false, false, fmt.Errorf("inspect Mastodon 4.3 migration markers: %w", err)
		}
		for _, version := range upgradeVersions {
			if !mastodon43UpgradeVersionKnown(version) {
				return false, false, false, fmt.Errorf("database schema contains unsupported migration marker %s between Mastodon 4.2 and 4.3", version)
			}
		}
		if versions.Current == 1 && len(upgradeVersions) != paonschema.Mastodon43UpgradeVersionCount() {
			return false, false, false, fmt.Errorf("database schema has final marker %s but only %d of %d reviewed Mastodon 4.3 migration markers", CurrentSchemaVersion, len(upgradeVersions), paonschema.Mastodon43UpgradeVersionCount())
		}
	}
	return false, versions.Current == 1, versions.Current == 0 && versions.Legacy == 1, nil
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
