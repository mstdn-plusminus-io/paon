package migrate

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	paondb "github.com/mstdn-plusminus-io/paon/internal/paon/db"
	"gorm.io/gorm"
)

const CurrentSchemaVersion = "20230907150100"
const migrationAdvisoryLockID int64 = 0x50616f6e4d696772
const statementSeparator = "-- paon:statement"

//go:embed schema.sql
var schemaFiles embed.FS

func Run(ctx context.Context, database *gorm.DB) (bool, error) {
	if database == nil {
		return false, errors.New("migration database is not configured")
	}
	applied := false
	err := database.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(?)", migrationAdvisoryLockID).Error; err != nil {
			return fmt.Errorf("acquire migration lock: %w", err)
		}
		empty, current, err := databaseSchemaState(tx)
		if err != nil {
			return err
		}
		if current {
			return nil
		}
		if !empty {
			return fmt.Errorf("unsupported existing database schema; expected version %s", CurrentSchemaVersion)
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
	if err := paondb.SchemaAvailable(database); err != nil {
		return applied, fmt.Errorf("validate migrated schema: %w", err)
	}
	return applied, nil
}

func databaseSchemaState(tx *gorm.DB) (empty bool, current bool, err error) {
	var tableCount int64
	if err := tx.Raw(`SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = current_schema() AND table_type IN ('BASE TABLE', 'VIEW')`).Scan(&tableCount).Error; err != nil {
		return false, false, fmt.Errorf("inspect database tables: %w", err)
	}
	if tableCount == 0 {
		return true, false, nil
	}
	var migrationsTable string
	if err := tx.Raw(`SELECT COALESCE(to_regclass('schema_migrations')::text, '')`).Scan(&migrationsTable).Error; err != nil {
		return false, false, fmt.Errorf("inspect schema_migrations: %w", err)
	}
	if migrationsTable == "" {
		return false, false, nil
	}
	var count int64
	if err := tx.Raw(`SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, CurrentSchemaVersion).Scan(&count).Error; err != nil {
		return false, false, fmt.Errorf("read schema version: %w", err)
	}
	return false, count == 1, nil
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
