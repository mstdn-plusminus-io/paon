package migrate

import (
	"context"
	"fmt"
	"strings"

	paondb "github.com/mstdn-plusminus-io/paon/internal/paon/db"
	paonschema "github.com/mstdn-plusminus-io/paon/internal/paon/schema"
	"gorm.io/gorm"
)

type upgradeStep struct {
	version    string
	phase      string
	statements []string
}

type UpgradePhase string

const (
	UpgradePhaseExpand   UpgradePhase = "expand"
	UpgradePhaseBackfill UpgradePhase = "backfill"
	UpgradePhaseValidate UpgradePhase = "validate"
	UpgradePhaseContract UpgradePhase = "contract"
)

func requestedUpgradePhase(options Options) (UpgradePhase, error) {
	phase := UpgradePhase(strings.ToLower(strings.TrimSpace(string(options.Phase))))
	if phase == "" {
		return UpgradePhaseExpand, nil
	}
	switch phase {
	case UpgradePhaseExpand, UpgradePhaseBackfill, UpgradePhaseValidate, UpgradePhaseContract:
		return phase, nil
	default:
		return "", fmt.Errorf("invalid Mastodon 4.3 migration phase %q; expected expand, backfill, validate, or contract", options.Phase)
	}
}

func upgradePhasesThrough(target UpgradePhase) []UpgradePhase {
	all := []UpgradePhase{UpgradePhaseExpand, UpgradePhaseBackfill, UpgradePhaseValidate, UpgradePhaseContract}
	for index, phase := range all {
		if phase == target {
			return all[:index+1]
		}
	}
	return nil
}

func runMastodon43Phase(ctx context.Context, database *gorm.DB, phase UpgradePhase, options Options) (bool, error) {
	applied := false
	err := database.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(?)", migrationAdvisoryLockID).Error; err != nil {
			return fmt.Errorf("acquire migration lock for phase %s: %w", phase, err)
		}
		_, current, legacy, err := databaseSchemaState(tx)
		if err != nil {
			return err
		}
		if current {
			return nil
		}
		if !legacy {
			return fmt.Errorf("Mastodon 4.3 phase %s requires legacy schema version %s", phase, LegacySchemaVersion)
		}
		if err := validateMastodon4219UpgradePrerequisites(tx); err != nil {
			return err
		}
		before, err := migrationVersionCount(tx)
		if err != nil {
			return err
		}
		switch phase {
		case UpgradePhaseExpand:
			if err := applyMastodon43Expand(tx, options); err != nil {
				return err
			}
		case UpgradePhaseBackfill:
			if err := requireMastodon43Phase(tx, UpgradePhaseExpand); err != nil {
				return err
			}
			if err := applyMastodon43Backfill(tx, options); err != nil {
				return err
			}
		case UpgradePhaseValidate:
			if err := requireMastodon43Phase(tx, UpgradePhaseExpand); err != nil {
				return err
			}
			if err := requireMastodon43Phase(tx, UpgradePhaseBackfill); err != nil {
				return err
			}
			logMigration(options, "Mastodon 4.3 migration phase=validate")
			if err := applyMastodon43Validate(tx, options); err != nil {
				return err
			}
			if err := validateMastodon43UpgradeData(tx, options); err != nil {
				return err
			}
		case UpgradePhaseContract:
			if !options.AcknowledgeContract {
				return fmt.Errorf("Mastodon 4.3 contract phase requires --acknowledge-contract or PAON_MIGRATION_ACKNOWLEDGE_CONTRACT=true after all 4.2 processes have stopped")
			}
			if err := requireMastodon43Phase(tx, UpgradePhaseExpand); err != nil {
				return err
			}
			if err := requireMastodon43Phase(tx, UpgradePhaseBackfill); err != nil {
				return err
			}
			if err := requireMastodon43Phase(tx, UpgradePhaseValidate); err != nil {
				return err
			}
			// Revalidate inside the destructive transaction. The operator's
			// acknowledgement is the fence that prevents old writers from racing
			// this final check.
			logMigration(options, "Mastodon 4.3 migration phase=validate before contract")
			if err := validateMastodon43UpgradeData(tx, options); err != nil {
				return err
			}
			if err := applyMastodon43Contract(tx, options); err != nil {
				return err
			}
			if err := paondb.SchemaAvailable(tx); err != nil {
				return fmt.Errorf("validate contracted Mastodon 4.3 schema before commit: %w", err)
			}
		default:
			return fmt.Errorf("unsupported Mastodon 4.3 migration phase %q", phase)
		}
		if err := requireMastodon43Phase(tx, phase); err != nil {
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

func applyMastodon43Expand(tx *gorm.DB, options Options) error {
	logMigration(options, "Mastodon 4.3 migration phase=expand from=%s to=%s", LegacySchemaVersion, CurrentSchemaVersion)
	for _, step := range mastodon43ExpandSteps() {
		if err := applyUpgradeStep(tx, step); err != nil {
			return err
		}
	}
	return nil
}

func applyMastodon43Backfill(tx *gorm.DB, options Options) error {
	if err := preflightMastodon43Backfill(tx, options); err != nil {
		return err
	}
	logMigration(options, "Mastodon 4.3 migration phase=backfill")
	for _, step := range mastodon43DuplicateBackfillSteps() {
		if err := applyBatchedDuplicateBackfillStep(tx, step); err != nil {
			return err
		}
	}
	if err := applyLocaleBackfill(tx); err != nil {
		return err
	}
	if err := applyNotificationPolicyBackfill(tx, "20240304090449", false); err != nil {
		return err
	}
	if err := applyOTPSecretBackfill(tx, options); err != nil {
		return err
	}
	if err := applyNotificationPolicyBackfill(tx, "20240321160706", true); err != nil {
		return err
	}
	if err := applyProfileScopeBackfill(tx); err != nil {
		return err
	}
	if err := applyNotificationPolicyV2Backfill(tx, "20240808124338"); err != nil {
		return err
	}
	if err := applyNotificationPolicyV2Backfill(tx, "20240808124339"); err != nil {
		return err
	}
	return nil
}

func applyMastodon43Validate(tx *gorm.DB, options Options) error {
	if err := preflightMastodon43Validate(tx); err != nil {
		return err
	}
	for _, step := range mastodon43ValidateSteps() {
		if err := applyUpgradeStep(tx, step); err != nil {
			return err
		}
	}
	return nil
}

func applyMastodon43Contract(tx *gorm.DB, options Options) error {
	logMigration(options, "Mastodon 4.3 migration phase=contract")
	for _, step := range mastodon43ContractSteps() {
		if err := applyUpgradeStep(tx, step); err != nil {
			return err
		}
	}
	if err := validateMastodon43ContractData(tx); err != nil {
		return err
	}
	logMigration(options, "Mastodon 4.3 migration complete version=%s", CurrentSchemaVersion)
	return nil
}

func migrationVersionCount(tx *gorm.DB) (int64, error) {
	var count int64
	if err := tx.Raw(`SELECT COUNT(*) FROM schema_migrations`).Scan(&count).Error; err != nil {
		return 0, fmt.Errorf("count migration versions: %w", err)
	}
	return count, nil
}

func requireMastodon43Phase(tx *gorm.DB, phase UpgradePhase) error {
	missing := make([]string, 0)
	for _, version := range mastodon43PhaseVersions(phase) {
		applied, err := upgradeVersionApplied(tx, version)
		if err != nil {
			return err
		}
		if !applied {
			missing = append(missing, version)
		}
	}
	if len(missing) != 0 {
		return fmt.Errorf("Mastodon 4.3 phase %s is incomplete; missing migration versions: %s", phase, strings.Join(missing, ", "))
	}
	return nil
}

func mastodon43PhaseVersions(phase UpgradePhase) []string {
	var steps []upgradeStep
	switch phase {
	case UpgradePhaseExpand:
		steps = mastodon43ExpandSteps()
	case UpgradePhaseBackfill:
		steps = embeddedMigrationSteps("4.3.23", phase)
	case UpgradePhaseValidate:
		steps = mastodon43ValidateSteps()
	case UpgradePhaseContract:
		steps = mastodon43ContractSteps()
	default:
		return nil
	}
	versions := make([]string, 0, len(steps))
	for _, step := range steps {
		versions = append(versions, step.version)
	}
	return versions
}

func mastodon43UpgradeVersionKnown(version string) bool {
	return paonschema.Mastodon43UpgradeVersionKnown(version)
}

func applyUpgradeStep(tx *gorm.DB, step upgradeStep) error {
	applied, err := upgradeVersionApplied(tx, step.version)
	if err != nil {
		return err
	}
	if applied {
		return nil
	}
	for index, statement := range step.statements {
		if err := tx.Exec(statement).Error; err != nil {
			return fmt.Errorf("Mastodon 4.3 %s migration %s statement %d: %w", step.phase, step.version, index+1, err)
		}
	}
	return recordUpgradeVersion(tx, step.version)
}

func applyBatchedDuplicateBackfillStep(tx *gorm.DB, step upgradeStep) error {
	applied, err := upgradeVersionApplied(tx, step.version)
	if err != nil || applied {
		return err
	}
	if len(step.statements) == 0 {
		return recordUpgradeVersion(tx, step.version)
	}
	requiresDedupe, err := duplicateBackfillRequiresDedupe(tx, step.version)
	if err != nil {
		return err
	}
	if !requiresDedupe {
		if err := tx.Exec(step.statements[1]).Error; err != nil {
			return fmt.Errorf("Mastodon 4.3 %s migration %s create unique index: %w", step.phase, step.version, err)
		}
		return recordUpgradeVersion(tx, step.version)
	}
	for batch := 1; ; batch++ {
		result := tx.Exec(step.statements[0], migrationBatchSize)
		if result.Error != nil {
			return fmt.Errorf("Mastodon 4.3 %s migration %s batch %d: %w", step.phase, step.version, batch, result.Error)
		}
		if result.RowsAffected == 0 {
			break
		}
	}
	for index, statement := range step.statements[1:] {
		if err := tx.Exec(statement).Error; err != nil {
			return fmt.Errorf("Mastodon 4.3 %s migration %s statement %d: %w", step.phase, step.version, index+2, err)
		}
	}
	return recordUpgradeVersion(tx, step.version)
}

func duplicateBackfillRequiresDedupe(tx *gorm.DB, version string) (bool, error) {
	var query string
	switch version {
	case "20231018192110":
		query = `SELECT EXISTS(SELECT 1 FROM webauthn_credentials WHERE user_id IS NOT NULL AND nickname IS NOT NULL GROUP BY user_id, nickname HAVING COUNT(*) > 1)`
	case "20231018193209":
		query = `SELECT EXISTS(SELECT 1 FROM account_aliases WHERE account_id IS NOT NULL AND uri IS NOT NULL GROUP BY account_id, uri HAVING COUNT(*) > 1)`
	case "20231018193355":
		query = `SELECT EXISTS(SELECT 1 FROM custom_filter_statuses WHERE status_id IS NOT NULL AND custom_filter_id IS NOT NULL GROUP BY status_id, custom_filter_id HAVING COUNT(*) > 1)`
	case "20231018193659":
		query = `SELECT EXISTS(SELECT 1 FROM identities WHERE uid IS NOT NULL AND provider IS NOT NULL GROUP BY uid, provider HAVING COUNT(*) > 1)`
	default:
		return false, fmt.Errorf("Mastodon 4.3 duplicate migration %s has no reviewed uniqueness predicate", version)
	}
	var required bool
	if err := tx.Raw(query).Scan(&required).Error; err != nil {
		return false, fmt.Errorf("Mastodon 4.3 duplicate migration %s preflight: %w", version, err)
	}
	return required, nil
}

func applyLocaleBackfill(tx *gorm.DB) error {
	const version = "20240109103012"
	applied, err := upgradeVersionApplied(tx, version)
	if err != nil || applied {
		return err
	}
	for {
		var ids []int64
		if err := tx.Raw(`SELECT id FROM users WHERE locale = 'fr-QC' ORDER BY id ASC LIMIT ?`, migrationBatchSize).Scan(&ids).Error; err != nil {
			return fmt.Errorf("Mastodon 4.3 locale backfill query: %w", err)
		}
		if len(ids) == 0 {
			break
		}
		if err := tx.Exec(`UPDATE users SET locale = 'fr-CA' WHERE id IN ?`, ids).Error; err != nil {
			return fmt.Errorf("Mastodon 4.3 locale backfill write: %w", err)
		}
	}
	return recordUpgradeVersion(tx, version)
}

func applyProfileScopeBackfill(tx *gorm.DB) error {
	const version = "20240603195202"
	applied, err := upgradeVersionApplied(tx, version)
	if err != nil || applied {
		return err
	}
	for _, table := range []string{"oauth_applications", "oauth_access_tokens"} {
		for {
			var ids []int64
			query := fmt.Sprintf(`SELECT id FROM %s WHERE scopes LIKE '%%read:me%%' ORDER BY id ASC LIMIT ?`, table)
			if err := tx.Raw(query, migrationBatchSize).Scan(&ids).Error; err != nil {
				return fmt.Errorf("Mastodon 4.3 profile scope backfill query %s: %w", table, err)
			}
			if len(ids) == 0 {
				break
			}
			statement := fmt.Sprintf(`UPDATE %s SET scopes = replace(scopes, 'read:me', 'profile') WHERE id IN ?`, table)
			if err := tx.Exec(statement, ids).Error; err != nil {
				return fmt.Errorf("Mastodon 4.3 profile scope backfill write %s: %w", table, err)
			}
		}
	}
	return recordUpgradeVersion(tx, version)
}

func applyNotificationPolicyV2Backfill(tx *gorm.DB, version string) error {
	applied, err := upgradeVersionApplied(tx, version)
	if err != nil || applied {
		return err
	}
	lastID := int64(0)
	for {
		var ids []int64
		if err := tx.Raw(`SELECT id FROM notification_policies WHERE id > ? ORDER BY id ASC LIMIT ?`, lastID, migrationBatchSize).Scan(&ids).Error; err != nil {
			return fmt.Errorf("Mastodon 4.3 notification policy v2 backfill %s query: %w", version, err)
		}
		if len(ids) == 0 {
			break
		}
		if err := tx.Exec(notificationPolicyV2BackfillSQL+` WHERE id IN ?`, ids).Error; err != nil {
			return fmt.Errorf("Mastodon 4.3 notification policy v2 backfill %s write: %w", version, err)
		}
		lastID = ids[len(ids)-1]
	}
	return recordUpgradeVersion(tx, version)
}

func upgradeVersionApplied(tx *gorm.DB, version string) (bool, error) {
	var count int64
	if err := tx.Raw(`SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, version).Scan(&count).Error; err != nil {
		return false, fmt.Errorf("read migration version %s: %w", version, err)
	}
	return count != 0, nil
}

func recordUpgradeVersion(tx *gorm.DB, version string) error {
	if err := tx.Exec(`INSERT INTO schema_migrations (version) VALUES (?) ON CONFLICT (version) DO NOTHING`, version).Error; err != nil {
		return fmt.Errorf("record migration version %s: %w", version, err)
	}
	return nil
}

func logMigration(options Options, format string, arguments ...any) {
	if options.Logf != nil {
		options.Logf(format, arguments...)
	}
}

const notificationPolicyV2BackfillSQL = `UPDATE notification_policies SET
  for_not_following = CASE WHEN filter_not_following THEN 1 ELSE 0 END,
  for_not_followers = CASE WHEN filter_not_followers THEN 1 ELSE 0 END,
  for_new_accounts = CASE WHEN filter_new_accounts THEN 1 ELSE 0 END,
  for_private_mentions = CASE WHEN filter_private_mentions THEN 1 ELSE 0 END`

func mastodon43DuplicateBackfillSteps() []upgradeStep {
	steps := []upgradeStep{}
	for _, step := range embeddedMigrationSteps("4.3.23", UpgradePhaseBackfill) {
		// Go-only backfills have header-only files and are invoked explicitly
		// by applyMastodon43Backfill after the duplicate-removal SQL batches.
		if len(step.statements) > 0 {
			steps = append(steps, step)
		}
	}
	return steps
}

func mastodon43ExpandSteps() []upgradeStep {
	return embeddedMigrationSteps("4.3.23", UpgradePhaseExpand)
}

func mastodon43ValidateSteps() []upgradeStep {
	return embeddedMigrationSteps("4.3.23", UpgradePhaseValidate)
}

func mastodon43ContractSteps() []upgradeStep {
	return embeddedMigrationSteps("4.3.23", UpgradePhaseContract)
}
