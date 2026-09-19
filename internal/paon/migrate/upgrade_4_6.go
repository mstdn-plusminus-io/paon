package migrate

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	paondb "github.com/mstdn-plusminus-io/paon/internal/paon/db"
	paonschema "github.com/mstdn-plusminus-io/paon/internal/paon/schema"
	"gopkg.in/yaml.v3"
	"gorm.io/gorm"
)

const Mastodon4515SchemaVersion = paonschema.Mastodon4515Version

// runMastodon46Phase applies the reviewed v4.5.15 -> v4.6.6 inventory. The
// fresh Rails 8.1 schema has a different physical column order, so this path
// intentionally follows the upstream migration order instead of attempting to
// rewrite staged databases into the fresh layout.
func runMastodon46Phase(ctx context.Context, database *gorm.DB, phase UpgradePhase, options Options) (bool, error) {
	applied := false
	err := database.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(?)", migrationAdvisoryLockID).Error; err != nil {
			return fmt.Errorf("acquire migration lock for Mastodon 4.6 phase %s: %w", phase, err)
		}
		current, err := upgradeVersionApplied(tx, CurrentSchemaVersion)
		if err != nil || current {
			return err
		}
		previous, err := mastodon4515SchemaState(tx)
		if err != nil {
			return err
		}
		if !previous {
			return fmt.Errorf("Mastodon 4.6 phase %s requires schema version %s", phase, Mastodon4515SchemaVersion)
		}
		before, err := migrationVersionCount(tx)
		if err != nil {
			return err
		}
		switch phase {
		case UpgradePhaseExpand:
			if err := applyMastodon46Steps(tx, mastodon46ExpandSteps()); err != nil {
				return err
			}
			// The final marker must stay fenced behind contract even though its
			// additive column is safe for older processes.
			if err := ensureMastodon46FinalAdditiveCatalog(tx); err != nil {
				return err
			}
		case UpgradePhaseBackfill:
			if err := requireMastodon46Phase(tx, UpgradePhaseExpand); err != nil {
				return err
			}
			if err := applyMastodon46Backfill(tx); err != nil {
				return err
			}
		case UpgradePhaseValidate:
			for _, prerequisite := range []UpgradePhase{UpgradePhaseExpand, UpgradePhaseBackfill} {
				if err := requireMastodon46Phase(tx, prerequisite); err != nil {
					return err
				}
			}
			if err := applyMastodon46Steps(tx, mastodon46ValidateSteps()); err != nil {
				return err
			}
			if err := validateMastodon46Data(tx); err != nil {
				return err
			}
		case UpgradePhaseContract:
			if !options.AcknowledgeContract {
				return fmt.Errorf("Mastodon 4.6 contract phase requires --acknowledge-contract or PAON_MIGRATION_ACKNOWLEDGE_CONTRACT=true after all 4.5 processes have stopped")
			}
			for _, prerequisite := range []UpgradePhase{UpgradePhaseExpand, UpgradePhaseBackfill, UpgradePhaseValidate} {
				if err := requireMastodon46Phase(tx, prerequisite); err != nil {
					return err
				}
			}
			if err := validateMastodon46Data(tx); err != nil {
				return err
			}
			if err := applyMastodon46Steps(tx, mastodon46ContractSteps()); err != nil {
				return err
			}
			if _, err := reconcileCurrentMastodonCatalog(tx); err != nil {
				return fmt.Errorf("reconcile canonical Mastodon 4.6 catalog before commit: %w", err)
			}
			if err := paondb.SchemaAvailable(tx); err != nil {
				return fmt.Errorf("validate contracted Mastodon 4.6 schema before commit: %w", err)
			}
		default:
			return fmt.Errorf("unsupported Mastodon 4.6 migration phase %q", phase)
		}
		if err := requireMastodon46Phase(tx, phase); err != nil {
			return err
		}
		after, err := migrationVersionCount(tx)
		if err != nil {
			return err
		}
		applied = after > before
		return nil
	})
	return applied, err
}

func applyMastodon46Steps(tx *gorm.DB, steps []upgradeStep) error {
	for _, step := range steps {
		alreadyApplied, err := upgradeVersionApplied(tx, step.version)
		if err != nil || alreadyApplied {
			if err != nil {
				return err
			}
			continue
		}
		for index, statement := range step.statements {
			if err := tx.Exec(statement).Error; err != nil {
				return fmt.Errorf("Mastodon 4.6 %s migration %s statement %d: %w", step.phase, step.version, index+1, err)
			}
		}
		if err := recordUpgradeVersion(tx, step.version); err != nil {
			return err
		}
	}
	return nil
}

func mastodon46ExpandSteps() []upgradeStep {
	return embeddedMigrationSteps("4.6.6", UpgradePhaseExpand)
}

func mastodon46BackfillSteps() []upgradeStep {
	return embeddedMigrationSteps("4.6.6", UpgradePhaseBackfill)
}

func mastodon46ValidateSteps() []upgradeStep {
	return embeddedMigrationSteps("4.6.6", UpgradePhaseValidate)
}

func mastodon46ContractSteps() []upgradeStep {
	return embeddedMigrationSteps("4.6.6", UpgradePhaseContract)
}

func mastodon46PhaseVersions(phase UpgradePhase) []string {
	var steps []upgradeStep
	switch phase {
	case UpgradePhaseExpand:
		steps = mastodon46ExpandSteps()
	case UpgradePhaseBackfill:
		steps = mastodon46BackfillSteps()
	case UpgradePhaseValidate:
		steps = mastodon46ValidateSteps()
	case UpgradePhaseContract:
		steps = mastodon46ContractSteps()
	}
	versions := make([]string, 0, len(steps))
	for _, step := range steps {
		versions = append(versions, step.version)
	}
	return versions
}

func requireMastodon46Phase(tx *gorm.DB, phase UpgradePhase) error {
	missing := []string{}
	for _, version := range mastodon46PhaseVersions(phase) {
		applied, err := upgradeVersionApplied(tx, version)
		if err != nil {
			return err
		}
		if !applied {
			missing = append(missing, version)
		}
	}
	if len(missing) != 0 {
		return fmt.Errorf("Mastodon 4.6 phase %s is incomplete; missing migration versions: %s", phase, strings.Join(missing, ", "))
	}
	return nil
}

func ensureMastodon46FinalAdditiveCatalog(tx *gorm.DB) error {
	for _, statement := range embeddedMigrationStatements("migrations/4.6.6/20260611150940_prepare.sql") {
		if err := tx.Exec(statement).Error; err != nil {
			return fmt.Errorf("Mastodon 4.6 expand final additive catalog: %w", err)
		}
	}
	return nil
}

func applyMastodon46Backfill(tx *gorm.DB) error {
	if applied, err := upgradeVersionApplied(tx, "20260209142402"); err != nil {
		return err
	} else if !applied {
		if err := migrateMastodon46DefaultTheme(tx); err != nil {
			return err
		}
		if err := recordUpgradeVersion(tx, "20260209142402"); err != nil {
			return err
		}
	}
	if applied, err := upgradeVersionApplied(tx, "20260209143308"); err != nil {
		return err
	} else if !applied {
		if err := migrateMastodon46UserThemes(tx); err != nil {
			return err
		}
		if err := recordUpgradeVersion(tx, "20260209143308"); err != nil {
			return err
		}
	}
	if applied, err := upgradeVersionApplied(tx, "20260318144837"); err != nil {
		return err
	} else if !applied {
		if err := tx.Exec(`UPDATE user_roles SET permissions = permissions | (1::bigint << 21) WHERE permissions & (1::bigint << 16) = (1::bigint << 16)`).Error; err != nil {
			return fmt.Errorf("Mastodon 4.6 add invite approval bypass permission: %w", err)
		}
		if err := recordUpgradeVersion(tx, "20260318144837"); err != nil {
			return err
		}
	}
	return ensureMastodon46CollectionItemCompositeIndex(tx)
}

func migrateMastodon46DefaultTheme(tx *gorm.DB) error {
	var raw sql.NullString
	err := tx.Raw(`SELECT value FROM settings WHERE var = 'theme' LIMIT 1`).Row().Scan(&raw)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil
		}
		return fmt.Errorf("Mastodon 4.6 read default theme setting: %w", err)
	}
	if !raw.Valid || strings.TrimSpace(raw.String) == "" {
		return nil
	}
	var theme any
	if err := yaml.Unmarshal([]byte(raw.String), &theme); err != nil {
		return fmt.Errorf("Mastodon 4.6 decode default theme setting: %w", err)
	}
	name, ok := theme.(string)
	if !ok || (name != "mastodon-light" && name != "contrast" && name != "system") {
		return nil
	}
	if err := tx.Exec(`UPDATE settings SET value = '--- default
' WHERE var = 'theme'`).Error; err != nil {
		return fmt.Errorf("Mastodon 4.6 update default theme setting: %w", err)
	}
	return nil
}

func migrateMastodon46UserThemes(tx *gorm.DB) error {
	type userSettingsRow struct {
		ID       int64
		Settings sql.NullString
	}
	var rows []userSettingsRow
	if err := tx.Raw(`SELECT id, settings FROM users WHERE settings IS NOT NULL ORDER BY id`).Scan(&rows).Error; err != nil {
		return fmt.Errorf("Mastodon 4.6 read user theme settings: %w", err)
	}
	for _, row := range rows {
		settings := map[string]any{}
		if err := json.Unmarshal([]byte(row.Settings.String), &settings); err != nil {
			return fmt.Errorf("Mastodon 4.6 decode settings for user id=%d: %w", row.ID, err)
		}
		theme, ok := settings["theme"].(string)
		if !ok || strings.TrimSpace(theme) == "" || (theme != "system" && theme != "default" && theme != "mastodon-light" && theme != "contrast") {
			continue
		}
		var updates []mastodonSettingUpdate
		switch theme {
		case "default":
			updates = append(updates, mastodonSettingUpdate{"web.color_scheme", "dark"}, mastodonSettingUpdate{"web.contrast", "auto"})
		case "contrast":
			updates = append(updates, mastodonSettingUpdate{"web.color_scheme", "dark"}, mastodonSettingUpdate{"web.contrast", "high"})
		case "mastodon-light":
			updates = append(updates, mastodonSettingUpdate{"web.color_scheme", "light"}, mastodonSettingUpdate{"web.contrast", "auto"})
		}
		updates = append(updates, mastodonSettingUpdate{"theme", "default"})
		encoded, err := rewriteMastodonSettings(row.Settings.String, updates)
		if err != nil {
			return fmt.Errorf("Mastodon 4.6 encode settings for user id=%d: %w", row.ID, err)
		}
		if err := tx.Exec(`UPDATE users SET settings = ? WHERE id = ?`, string(encoded), row.ID).Error; err != nil {
			return fmt.Errorf("Mastodon 4.6 update settings for user id=%d: %w", row.ID, err)
		}
	}
	return nil
}

// Upstream retries the concurrent unique-index creation only after a unique
// violation. Match its observable deletion semantics: null-account duplicates
// are removed only when a non-null duplicate made the first index attempt fail.
func ensureMastodon46CollectionItemCompositeIndex(tx *gorm.DB) error {
	var duplicateGroups int64
	if err := tx.Raw(`SELECT COUNT(*) FROM (SELECT 1 FROM collection_items WHERE account_id IS NOT NULL GROUP BY account_id, collection_id HAVING COUNT(*) > 1) duplicates`).Scan(&duplicateGroups).Error; err != nil {
		return fmt.Errorf("Mastodon 4.6 inspect collection item duplicates: %w", err)
	}
	if duplicateGroups > 0 {
		if err := tx.Exec(`DELETE FROM collection_items WHERE id NOT IN (SELECT DISTINCT ON(account_id, collection_id) id FROM collection_items ORDER BY account_id, collection_id, id ASC)`).Error; err != nil {
			return fmt.Errorf("Mastodon 4.6 deduplicate collection items: %w", err)
		}
	}
	for _, statement := range embeddedMigrationStatements("migrations/4.6.6/20260410083500_index.sql") {
		if err := tx.Exec(statement).Error; err != nil {
			return fmt.Errorf("Mastodon 4.6 create collection item account/collection index: %w", err)
		}
	}
	return nil
}

func validateMastodon46Data(tx *gorm.DB) error {
	var count int64
	if err := tx.Raw(`SELECT COUNT(*) FROM user_roles WHERE permissions & (1::bigint << 16) = (1::bigint << 16) AND permissions & (1::bigint << 21) = 0`).Scan(&count).Error; err != nil {
		return fmt.Errorf("Mastodon 4.6 validate invite approval bypass permission: %w", err)
	}
	if count != 0 {
		return fmt.Errorf("Mastodon 4.6 invite approval bypass backfill incomplete for %d role(s)", count)
	}
	if err := tx.Raw(`SELECT COUNT(*) FROM (SELECT 1 FROM collection_items WHERE account_id IS NOT NULL GROUP BY account_id, collection_id HAVING COUNT(*) > 1) duplicates`).Scan(&count).Error; err != nil {
		return fmt.Errorf("Mastodon 4.6 validate collection item uniqueness: %w", err)
	}
	if count != 0 {
		return fmt.Errorf("Mastodon 4.6 collection item deduplication incomplete for %d group(s)", count)
	}
	if err := tx.Raw(`SELECT COUNT(*) FROM pg_constraint WHERE conname = 'fk_rails_ad7840c8cf' AND conrelid = 'custom_emoji_categories'::regclass AND convalidated`).Scan(&count).Error; err != nil {
		return fmt.Errorf("Mastodon 4.6 validate featured emoji foreign key: %w", err)
	}
	if count != 1 {
		return fmt.Errorf("Mastodon 4.6 featured emoji foreign key is not validated")
	}
	var indexName sql.NullString
	if err := tx.Raw(`SELECT to_regclass('index_collection_items_on_account_id_and_collection_id')::text`).Row().Scan(&indexName); err != nil {
		return fmt.Errorf("Mastodon 4.6 validate collection item composite index: %w", err)
	}
	if !indexName.Valid || indexName.String == "" {
		return fmt.Errorf("Mastodon 4.6 collection item composite index is missing")
	}
	return nil
}
