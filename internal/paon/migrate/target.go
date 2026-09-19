package migrate

import (
	"fmt"
	"strings"

	paondb "github.com/mstdn-plusminus-io/paon/internal/paon/db"
	paonschema "github.com/mstdn-plusminus-io/paon/internal/paon/schema"
	"gorm.io/gorm"
)

const LatestTargetVersion = "4.6.6"

type migrationTarget struct {
	release  string
	marker   string
	snapshot string
}

func requestedMigrationTarget(version string) (migrationTarget, error) {
	version = strings.TrimSpace(version)
	if version == "" {
		version = LatestTargetVersion
	}
	for _, target := range []migrationTarget{
		{"4.3.23", Mastodon4323SchemaVersion, "schemas/4.3.23.sql"},
		{"4.4.22", Mastodon4422SchemaVersion, "schemas/4.4.22.sql"},
		{"4.5.15", Mastodon4515SchemaVersion, "schemas/4.5.15.sql"},
		{LatestTargetVersion, CurrentSchemaVersion, "schema.sql"},
	} {
		if version == target.release {
			return target, nil
		}
	}
	return migrationTarget{}, fmt.Errorf("unsupported migration target %q; expected 4.3.23, 4.4.22, 4.5.15, or %s", version, LatestTargetVersion)
}

// Reject partially applied later releases as well as completed upgrades. A
// lower target cannot silently succeed while retaining newer schema changes.
// The caller holds the migration lock and has not made any schema changes yet.
func rejectMigrationsAfterTarget(tx *gorm.DB, target migrationTarget) error {
	var migrationsTable string
	if err := tx.Raw(`SELECT COALESCE(to_regclass('schema_migrations')::text, '')`).Scan(&migrationsTable).Error; err != nil {
		return fmt.Errorf("inspect migration history for target %s: %w", target.release, err)
	}
	if migrationsTable == "" {
		return nil
	}
	var versions []string
	if err := tx.Raw(`SELECT version FROM schema_migrations WHERE version > ?`, LegacySchemaVersion).Scan(&versions).Error; err != nil {
		return fmt.Errorf("inspect migrations after target %s: %w", target.release, err)
	}
	for _, version := range versions {
		if migrationAfterTarget(version, target) {
			return fmt.Errorf("cannot migrate backwards to Mastodon %s: database contains later migration marker %s", target.release, version)
		}
	}
	return nil
}

func migrationAfterTarget(version string, target migrationTarget) bool {
	// The first 4.4 migration predates the final 4.3 marker. Comparing only
	// timestamps would admit this partially applied newer release.
	return version > target.marker ||
		(target.release < "4.4.22" && paonschema.Mastodon44UpgradeVersionKnown(version)) ||
		(target.release < "4.5.15" && paonschema.Mastodon45UpgradeVersionKnown(version)) ||
		(target.release < "4.6.6" && paonschema.Mastodon46UpgradeVersionKnown(version))
}

func migrationTargetReached(tx *gorm.DB, target migrationTarget) (bool, error) {
	switch target.release {
	case "4.3.23":
		return mastodon4323SchemaState(tx)
	case "4.4.22":
		return mastodon4422SchemaState(tx)
	case "4.5.15":
		return mastodon4515SchemaState(tx)
	default:
		_, current, _, err := databaseSchemaState(tx)
		return current, err
	}
}

// Every release boundary requires its entire reviewed migration inventory.
// The current application's release additionally uses its full startup schema
// guard. Exact older-release catalog parity is pinned by integration tests;
// this helper must not reconcile an older target with a newer schema snapshot.
func validateMigrationTarget(tx *gorm.DB, target migrationTarget) error {
	if err := rejectMigrationsAfterTarget(tx, target); err != nil {
		return err
	}
	complete, err := migrationTargetReached(tx, target)
	if err != nil {
		return err
	}
	if !complete {
		return fmt.Errorf("database has not reached Mastodon %s migration boundary", target.release)
	}
	if target.release == LatestTargetVersion {
		return paondb.SchemaAvailable(tx)
	}
	return nil
}
