package migrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"

	paondb "github.com/mstdn-plusminus-io/paon/internal/paon/db"
	paonotp "github.com/mstdn-plusminus-io/paon/internal/paon/otp"
	paonschema "github.com/mstdn-plusminus-io/paon/internal/paon/schema"
	"gorm.io/gorm"
)

const Mastodon4323SchemaVersion = paonschema.Mastodon4323Version

// runMastodon44Phase preserves the same expand/backfill/validate/contract
// operator fence used by the 4.2 -> 4.3 upgrade. Every upstream marker is
// recorded only after its Paon equivalent succeeds, so an interrupted phase is
// safely resumable without pretending that the final catalog is available.
func runMastodon44Phase(ctx context.Context, database *gorm.DB, phase UpgradePhase, options Options) (bool, error) {
	applied := false
	phaseComplete := false
	err := database.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(?)", migrationAdvisoryLockID).Error; err != nil {
			return fmt.Errorf("acquire migration lock for Mastodon 4.4 phase %s: %w", phase, err)
		}
		current, err := upgradeVersionApplied(tx, CurrentSchemaVersion)
		if err != nil {
			return err
		}
		if current {
			return nil
		}
		previous, err := upgradeVersionApplied(tx, Mastodon4323SchemaVersion)
		if err != nil {
			return err
		}
		if !previous {
			return fmt.Errorf("Mastodon 4.4 phase %s requires schema version %s", phase, Mastodon4323SchemaVersion)
		}
		before, err := migrationVersionCount(tx)
		if err != nil {
			return err
		}
		switch phase {
		case UpgradePhaseExpand:
			if err := applyMastodon44Steps(tx, mastodon44ExpandSteps()); err != nil {
				return err
			}
			if err := ensureMastodon44FinalAdditiveCatalog(tx); err != nil {
				return err
			}
		case UpgradePhaseBackfill:
			if err := requireMastodon44Phase(tx, UpgradePhaseExpand); err != nil {
				return err
			}
			if err := applyMastodon44Backfill(ctx, tx, options); err != nil {
				return err
			}
		case UpgradePhaseValidate:
			if err := requireMastodon44Phase(tx, UpgradePhaseExpand); err != nil {
				return err
			}
			if err := requireMastodon44Phase(tx, UpgradePhaseBackfill); err != nil {
				return err
			}
			if err := applyMastodon44Steps(tx, mastodon44ValidateSteps()); err != nil {
				return err
			}
			if err := validateMastodon44Data(tx); err != nil {
				return err
			}
			if err := validateMastodon44ContractPrerequisites(tx, options); err != nil {
				return err
			}
		case UpgradePhaseContract:
			if !options.AcknowledgeContract {
				return fmt.Errorf("Mastodon 4.4 contract phase requires --acknowledge-contract or PAON_MIGRATION_ACKNOWLEDGE_CONTRACT=true after all 4.3 processes have stopped")
			}
			for _, prerequisite := range []UpgradePhase{UpgradePhaseExpand, UpgradePhaseBackfill, UpgradePhaseValidate} {
				if err := requireMastodon44Phase(tx, prerequisite); err != nil {
					return err
				}
			}
			if err := validateMastodon44Data(tx); err != nil {
				return err
			}
			if err := validateMastodon44ContractPrerequisites(tx, options); err != nil {
				return err
			}
			if err := applyMastodon44Steps(tx, mastodon44ContractSteps()); err != nil {
				return err
			}
			if _, err := reconcileCurrentMastodonCatalog(tx); err != nil {
				return fmt.Errorf("reconcile canonical Mastodon 4.4 catalog before commit: %w", err)
			}
			if err := paondb.SchemaAvailable(tx); err != nil {
				return fmt.Errorf("validate contracted Mastodon 4.4 schema before commit: %w", err)
			}
		default:
			return fmt.Errorf("unsupported Mastodon 4.4 migration phase %q", phase)
		}
		if err := requireMastodon44Phase(tx, phase); err != nil {
			return err
		}
		phaseComplete = true
		after, err := migrationVersionCount(tx)
		if err != nil {
			return err
		}
		applied = after > before
		return nil
	})
	if err != nil {
		return applied, err
	}
	if phase == UpgradePhaseBackfill && phaseComplete && options.Mastodon44TagTrendBackfill != nil && options.Mastodon44TagTrendBackfillPostCommit != nil {
		if err := options.Mastodon44TagTrendBackfillPostCommit(ctx); err != nil {
			return applied, fmt.Errorf("Mastodon 4.4 tag trend Redis cleanup after PostgreSQL commit: %w", err)
		}
	}
	return applied, nil
}

func applyMastodon44Steps(tx *gorm.DB, steps []upgradeStep) error {
	for _, step := range steps {
		applied, err := upgradeVersionApplied(tx, step.version)
		if err != nil || applied {
			if err != nil {
				return err
			}
			continue
		}
		for index, statement := range step.statements {
			if err := tx.Exec(statement).Error; err != nil {
				return fmt.Errorf("Mastodon 4.4 %s migration %s statement %d: %w", step.phase, step.version, index+1, err)
			}
		}
		if err := recordUpgradeVersion(tx, step.version); err != nil {
			return err
		}
	}
	return nil
}

// ensureMastodon44FinalAdditiveCatalog applies the additive catalog portion of
// the final upstream migration during expand without recording the final
// version marker. Recording 20250627132728 would make every process treat the
// database as fully contracted, so that marker remains fenced behind validate
// and contract. Re-running expand repairs missing indexes and is harmless.
func ensureMastodon44FinalAdditiveCatalog(tx *gorm.DB) error {
	statements := embeddedMigrationStatements("migrations/4.4.22/20250627132728_prepare.sql")
	for index, statement := range statements {
		if err := tx.Exec(statement).Error; err != nil {
			return fmt.Errorf("Mastodon 4.4 expand final additive catalog statement %d: %w", index+1, err)
		}
	}
	return nil
}

func applyMastodon44Backfill(ctx context.Context, tx *gorm.DB, options Options) error {
	logMigration(options, "Mastodon 4.4 migration phase=backfill")
	for _, step := range mastodon44BackfillSteps() {
		if step.version == "20241123160722" {
			applied, err := upgradeVersionApplied(tx, step.version)
			if err != nil || applied {
				if err != nil {
					return err
				}
				continue
			}
			if options.Mastodon44TagTrendBackfill != nil {
				if err := options.Mastodon44TagTrendBackfill(ctx, tx); err != nil {
					return fmt.Errorf("Mastodon 4.4 tag trend Redis backfill: %w", err)
				}
			} else if options.Mastodon44SkipTagTrendBackfill {
				logMigration(options, "Mastodon 4.4 tag trend Redis backfill explicitly skipped by operator assertion")
			} else {
				return errors.New("Mastodon 4.4 tag trend Redis backfill source is not configured; wire the Redis importer or set MIGRATION_SKIP_TAG_TREND_BACKFILL=true only after proving the legacy sets are empty")
			}
			if err := recordUpgradeVersion(tx, step.version); err != nil {
				return err
			}
			continue
		}
		if err := applyMastodon44Steps(tx, []upgradeStep{step}); err != nil {
			return err
		}
	}
	return nil
}

func requireMastodon44Phase(tx *gorm.DB, phase UpgradePhase) error {
	missing := []string{}
	for _, version := range mastodon44PhaseVersions(phase) {
		applied, err := upgradeVersionApplied(tx, version)
		if err != nil {
			return err
		}
		if !applied {
			missing = append(missing, version)
		}
	}
	if len(missing) != 0 {
		return fmt.Errorf("Mastodon 4.4 phase %s is incomplete; missing migration versions: %s", phase, strings.Join(missing, ", "))
	}
	return nil
}

func mastodon44PhaseVersions(phase UpgradePhase) []string {
	var steps []upgradeStep
	switch phase {
	case UpgradePhaseExpand:
		steps = mastodon44ExpandSteps()
	case UpgradePhaseBackfill:
		steps = mastodon44BackfillSteps()
	case UpgradePhaseValidate:
		steps = mastodon44ValidateSteps()
	case UpgradePhaseContract:
		steps = mastodon44ContractSteps()
	}
	versions := make([]string, 0, len(steps))
	for _, step := range steps {
		versions = append(versions, step.version)
	}
	return versions
}

func mastodon44ExpandSteps() []upgradeStep {
	return embeddedMigrationSteps("4.4.22", UpgradePhaseExpand)
}

func mastodon44BackfillSteps() []upgradeStep {
	return embeddedMigrationSteps("4.4.22", UpgradePhaseBackfill)
}

func mastodon44ValidateSteps() []upgradeStep {
	return embeddedMigrationSteps("4.4.22", UpgradePhaseValidate)
}

func mastodon44ContractSteps() []upgradeStep {
	return embeddedMigrationSteps("4.4.22", UpgradePhaseContract)
}

func validateMastodon44Data(tx *gorm.DB) error {
	checks := []struct {
		name  string
		query string
	}{
		{name: "NULL account pins", query: `SELECT COUNT(*) FROM account_pins WHERE account_id IS NULL OR target_account_id IS NULL`},
		{name: "NULL account aliases", query: `SELECT COUNT(*) FROM account_aliases WHERE account_id IS NULL`},
		{name: "NULL account conversations", query: `SELECT COUNT(*) FROM account_conversations WHERE account_id IS NULL OR conversation_id IS NULL`},
		{name: "NULL account notes", query: `SELECT COUNT(*) FROM account_notes WHERE account_id IS NULL OR target_account_id IS NULL`},
		{name: "NULL push subscription owners", query: `SELECT COUNT(*) FROM web_push_subscriptions WHERE user_id IS NULL OR access_token_id IS NULL`},
		{name: "self quotes", query: `SELECT COUNT(*) FROM quotes WHERE status_id = quoted_status_id`},
		{name: "duplicate quote statuses", query: `SELECT COUNT(*) FROM (SELECT status_id FROM quotes GROUP BY status_id HAVING COUNT(*) > 1) duplicate_quotes`},
		{name: "duplicate global settings", query: `SELECT COUNT(*) FROM (SELECT var FROM settings GROUP BY var HAVING COUNT(*) > 1) duplicate_settings`},
	}
	for _, check := range checks {
		var count int64
		if err := tx.Raw(check.query).Scan(&count).Error; err != nil {
			return fmt.Errorf("Mastodon 4.4 validate %s: %w", check.name, err)
		}
		if count != 0 {
			return fmt.Errorf("Mastodon 4.4 validate failed: %s count=%d", check.name, count)
		}
	}
	return nil
}

// validateMastodon44ContractPrerequisites protects the two irreversible 4.4
// drops. Legacy import rows have no reliable per-row completion state, so an
// operator must drain/archive the table before acknowledging contract. Every
// enabled OTP secret must also be readable through Active Record Encryption
// and encode a valid TOTP key before the legacy ciphertext columns disappear.
func validateMastodon44ContractPrerequisites(tx *gorm.DB, options Options) error {
	var importCount int64
	if err := tx.Raw(`SELECT COUNT(*) FROM imports`).Scan(&importCount).Error; err != nil {
		return fmt.Errorf("Mastodon 4.4 validate legacy imports: %w", err)
	}
	if importCount != 0 {
		return fmt.Errorf("Mastodon 4.4 contract refused: imports contains %d legacy row(s); complete and archive legacy imports before dropping the table", importCount)
	}

	type encryptedOTPRow struct {
		ID        int64
		OTPSecret sql.NullString
	}
	var rows []encryptedOTPRow
	if err := tx.Raw(`SELECT id, otp_secret FROM users WHERE otp_required_for_login = true ORDER BY id`).Scan(&rows).Error; err != nil {
		return fmt.Errorf("Mastodon 4.4 validate OTP rows: %w", err)
	}
	if len(rows) == 0 {
		return nil
	}
	if err := options.ActiveRecordEncryption.Validate(); err != nil {
		return fmt.Errorf("Mastodon 4.4 validate OTP encryption configuration: %w", err)
	}
	for _, row := range rows {
		if !row.OTPSecret.Valid || strings.TrimSpace(row.OTPSecret.String) == "" {
			return fmt.Errorf("Mastodon 4.4 contract refused: enabled user id=%d has no migrated OTP secret", row.ID)
		}
		secret, err := paonotp.DecryptActiveRecord(row.OTPSecret.String, options.ActiveRecordEncryption)
		if err != nil || normalizeMigrationOTPSecret(secret) == "" {
			return fmt.Errorf("Mastodon 4.4 contract refused: OTP secret is not decryptable for enabled user id=%d", row.ID)
		}
		if !sameMigrationTOTPCode(secret, secret) {
			return fmt.Errorf("Mastodon 4.4 contract refused: OTP secret is not a valid TOTP key for enabled user id=%d", row.ID)
		}
	}
	return nil
}

// LegacyTagTrendRow is the database hand-off shape for the Redis cutover.
// Language is empty for Mastodon's historical trending_tags:* sorted sets, but
// remains explicit so Paon-specific language rows can be retained as well.
type LegacyTagTrendRow struct {
	TagID    int64
	Score    float64
	Rank     int
	Allowed  bool
	Language string
}

// UpsertLegacyTagTrendRows is safe to retry and is intended to be called by
// Options.Mastodon44TagTrendBackfill after the command layer reads Redis. The
// Redis keys must only be deleted after this transaction commits.
func UpsertLegacyTagTrendRows(ctx context.Context, database *gorm.DB, rows []LegacyTagTrendRow) error {
	if database == nil {
		return errors.New("legacy tag trend import database is not configured")
	}
	for index, row := range rows {
		if row.TagID <= 0 || math.IsNaN(row.Score) || math.IsInf(row.Score, 0) || row.Rank < 0 {
			return fmt.Errorf("legacy tag trend row %d has invalid tag/score/rank", index+1)
		}
	}
	const batchSize = 1_000
	for start := 0; start < len(rows); start += batchSize {
		end := min(start+batchSize, len(rows))
		var statement strings.Builder
		statement.WriteString(`INSERT INTO tag_trends (tag_id, score, rank, allowed, language) VALUES `)
		arguments := make([]any, 0, (end-start)*5)
		for index, row := range rows[start:end] {
			if index != 0 {
				statement.WriteString(", ")
			}
			statement.WriteString("(?, ?, ?, ?, ?)")
			arguments = append(arguments, row.TagID, row.Score, row.Rank, row.Allowed, row.Language)
		}
		statement.WriteString(` ON CONFLICT (tag_id, language) DO UPDATE SET score = EXCLUDED.score, rank = EXCLUDED.rank, allowed = EXCLUDED.allowed`)
		result := database.WithContext(ctx).Exec(statement.String(), arguments...)
		if result.Error != nil {
			return fmt.Errorf("legacy tag trend rows %d-%d: %w", start+1, end, result.Error)
		}
	}
	// Match RankedTrend.recalculate_ordered_rank: ranks are 1-based and are
	// independently ordered for every language, including the legacy empty
	// language. This runs even for an empty Redis source so a retry repairs rank
	// values left behind by an interrupted/manual import.
	if err := database.WithContext(ctx).Exec(`UPDATE tag_trends SET rank = ranked.calculated_rank FROM (SELECT id, row_number() OVER (PARTITION BY language ORDER BY score DESC) AS calculated_rank FROM tag_trends) ranked WHERE tag_trends.id = ranked.id`).Error; err != nil {
		return fmt.Errorf("recalculate imported tag trend ranks: %w", err)
	}
	return nil
}
