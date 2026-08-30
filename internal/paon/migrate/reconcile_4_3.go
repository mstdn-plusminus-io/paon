package migrate

import (
	"fmt"
	"regexp"
	"strings"

	paondb "github.com/mstdn-plusminus-io/paon/internal/paon/db"
	"gorm.io/gorm"
)

var timestampIDSaltPattern = regexp.MustCompile(`[0-9a-f]{32}`)

// reconcileCurrentMastodon4323 only repairs catalog details whose canonical
// value is independent of how a database reached Mastodon 4.3.23. Physical
// layout and route-specific sequence state are intentionally not inferred:
// long-lived and dump/restored Mastodon databases can legitimately differ.
func reconcileCurrentMastodon4323(tx *gorm.DB) (bool, error) {
	functionChanged, err := reconcileTimestampIDFunction(tx)
	if err != nil {
		return false, err
	}
	foreignKeysChanged, err := reconcileMastodon4323ForeignKeyNames(tx)
	if err != nil {
		return false, err
	}
	return functionChanged || foreignKeysChanged, nil
}

func reconcileTimestampIDFunction(tx *gorm.DB) (bool, error) {
	var currentBody string
	if err := tx.Raw(`SELECT prosrc FROM pg_proc WHERE oid = to_regprocedure('timestamp_id(text)')`).Scan(&currentBody).Error; err != nil {
		return false, fmt.Errorf("inspect timestamp_id function body: %w", err)
	}
	salt := timestampIDSaltPattern.FindString(currentBody)
	if salt == "" {
		return false, fmt.Errorf("timestamp_id function does not contain the expected 32-character salt")
	}
	snapshot, err := schemaFiles.ReadFile("schema.sql")
	if err != nil {
		return false, fmt.Errorf("read embedded schema for timestamp_id reconciliation: %w", err)
	}
	var statement string
	for _, candidate := range strings.Split(strings.ReplaceAll(string(snapshot), "__PAON_TIMESTAMP_ID_SALT__", salt), statementSeparator) {
		candidate = strings.TrimSpace(candidate)
		if strings.HasPrefix(candidate, "CREATE OR REPLACE FUNCTION timestamp_id") {
			statement = strings.TrimSuffix(candidate, ";")
			break
		}
	}
	if statement == "" {
		return false, fmt.Errorf("embedded schema does not define timestamp_id")
	}
	bodyParts := strings.Split(statement, "$$")
	if len(bodyParts) != 3 {
		return false, fmt.Errorf("embedded timestamp_id definition has an unexpected dollar-quote layout")
	}
	if currentBody == bodyParts[1] {
		return false, nil
	}
	if err := tx.Exec(statement).Error; err != nil {
		return false, fmt.Errorf("reconcile timestamp_id function body: %w", err)
	}
	return true, nil
}

func reconcileMastodon4323ForeignKeyNames(tx *gorm.DB) (bool, error) {
	applied := false
	for _, foreignKey := range paondb.RequiredMastodonForeignKeys() {
		if foreignKey.Name == "" {
			return false, fmt.Errorf("canonical foreign key name is missing for %s", foreignKey.String())
		}
		var names []string
		if err := tx.Raw(
			`SELECT constraint_data.conname
			   FROM pg_constraint constraint_data
			   JOIN pg_class source_table ON source_table.oid = constraint_data.conrelid
			   JOIN pg_namespace source_namespace ON source_namespace.oid = source_table.relnamespace
			   JOIN pg_class target_table ON target_table.oid = constraint_data.confrelid
			   JOIN pg_namespace target_namespace ON target_namespace.oid = target_table.relnamespace
			   JOIN pg_attribute source_column
			     ON source_column.attrelid = source_table.oid
			    AND source_column.attnum = constraint_data.conkey[1]
			   JOIN pg_attribute target_column
			     ON target_column.attrelid = target_table.oid
			    AND target_column.attnum = constraint_data.confkey[1]
			  WHERE source_namespace.nspname = current_schema()
			    AND target_namespace.nspname = current_schema()
			    AND constraint_data.contype = 'f'
			    AND array_length(constraint_data.conkey, 1) = 1
			    AND source_table.relname = ?
			    AND source_column.attname = ?
			    AND target_table.relname = ?
			    AND target_column.attname = 'id'
			    AND constraint_data.confdeltype = ?
			    AND constraint_data.confupdtype = 'a'
			    AND constraint_data.confmatchtype = 's'
			    AND NOT constraint_data.condeferrable
			    AND constraint_data.convalidated
			  ORDER BY constraint_data.conname`,
			foreignKey.Table,
			foreignKey.Column,
			foreignKey.ForeignTable,
			foreignKey.OnDelete,
		).Scan(&names).Error; err != nil {
			return false, fmt.Errorf("inspect canonical foreign key %s: %w", foreignKey.String(), err)
		}
		if len(names) != 1 {
			return false, fmt.Errorf("expected one compatible foreign key for %s, found %d", foreignKey.String(), len(names))
		}
		if names[0] == foreignKey.Name {
			continue
		}
		var collision bool
		if err := tx.Raw(
			`SELECT EXISTS(
			   SELECT 1
			     FROM pg_constraint constraint_data
			     JOIN pg_class source_table ON source_table.oid = constraint_data.conrelid
			     JOIN pg_namespace source_namespace ON source_namespace.oid = source_table.relnamespace
			    WHERE source_namespace.nspname = current_schema()
			      AND source_table.relname = ?
			      AND constraint_data.conname = ?
			 )`,
			foreignKey.Table,
			foreignKey.Name,
		).Scan(&collision).Error; err != nil {
			return false, fmt.Errorf("inspect canonical foreign key name %s: %w", foreignKey.Name, err)
		}
		if collision {
			return false, fmt.Errorf("cannot rename %s foreign key %s to occupied canonical name %s", foreignKey.Table, names[0], foreignKey.Name)
		}
		statement := fmt.Sprintf(
			"ALTER TABLE %s RENAME CONSTRAINT %s TO %s",
			quotePostgresIdentifier(foreignKey.Table),
			quotePostgresIdentifier(names[0]),
			quotePostgresIdentifier(foreignKey.Name),
		)
		if err := tx.Exec(statement).Error; err != nil {
			return false, fmt.Errorf("rename canonical foreign key %s: %w", foreignKey.String(), err)
		}
		applied = true
	}
	return applied, nil
}

func quotePostgresIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}
