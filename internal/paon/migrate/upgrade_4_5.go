package migrate

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	paondb "github.com/mstdn-plusminus-io/paon/internal/paon/db"
	"gopkg.in/yaml.v3"
	"gorm.io/gorm"
)

// runMastodon45Phase keeps the v4.4.22 -> v4.5.15 catalog upgrade behind the
// same expand/backfill/validate/contract fence as the earlier Paon upgrades.
// The final upstream marker belongs to a data migration, so its data work runs
// during backfill while the marker itself remains withheld until contract.
func runMastodon45Phase(ctx context.Context, database *gorm.DB, phase UpgradePhase, options Options) (bool, error) {
	applied := false
	err := database.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(?)", migrationAdvisoryLockID).Error; err != nil {
			return fmt.Errorf("acquire migration lock for Mastodon 4.5 phase %s: %w", phase, err)
		}
		current, err := upgradeVersionApplied(tx, CurrentSchemaVersion)
		if err != nil || current {
			return err
		}
		previous, err := mastodon4422SchemaState(tx)
		if err != nil {
			return err
		}
		if !previous {
			return fmt.Errorf("Mastodon 4.5 phase %s requires schema version %s", phase, Mastodon4422SchemaVersion)
		}
		before, err := migrationVersionCount(tx)
		if err != nil {
			return err
		}
		switch phase {
		case UpgradePhaseExpand:
			if err := applyMastodon45Steps(tx, mastodon45ExpandSteps()); err != nil {
				return err
			}
			if err := seedMastodon45UsernameBlocks(tx, time.Now().UTC()); err != nil {
				return err
			}
			if err := ensureMastodon45ContractIndexes(tx); err != nil {
				return err
			}
		case UpgradePhaseBackfill:
			if err := requireMastodon45Phase(tx, UpgradePhaseExpand); err != nil {
				return err
			}
			if err := applyMastodon45Backfill(tx); err != nil {
				return err
			}
		case UpgradePhaseValidate:
			for _, prerequisite := range []UpgradePhase{UpgradePhaseExpand, UpgradePhaseBackfill} {
				if err := requireMastodon45Phase(tx, prerequisite); err != nil {
					return err
				}
			}
			if err := applyMastodon45Steps(tx, mastodon45ValidateSteps()); err != nil {
				return err
			}
			if err := validateMastodon45Data(tx); err != nil {
				return err
			}
		case UpgradePhaseContract:
			if !options.AcknowledgeContract {
				return fmt.Errorf("Mastodon 4.5 contract phase requires --acknowledge-contract or PAON_MIGRATION_ACKNOWLEDGE_CONTRACT=true after all 4.4 processes have stopped")
			}
			for _, prerequisite := range []UpgradePhase{UpgradePhaseExpand, UpgradePhaseBackfill, UpgradePhaseValidate} {
				if err := requireMastodon45Phase(tx, prerequisite); err != nil {
					return err
				}
			}
			if err := validateMastodon45Data(tx); err != nil {
				return err
			}
			if err := applyMastodon45Steps(tx, mastodon45ContractSteps()); err != nil {
				return err
			}
			if _, err := reconcileCurrentMastodonCatalog(tx); err != nil {
				return fmt.Errorf("reconcile canonical Mastodon 4.5 catalog before commit: %w", err)
			}
			if err := paondb.SchemaAvailable(tx); err != nil {
				return fmt.Errorf("validate contracted Mastodon 4.5 schema before commit: %w", err)
			}
		default:
			return fmt.Errorf("unsupported Mastodon 4.5 migration phase %q", phase)
		}
		if err := requireMastodon45Phase(tx, phase); err != nil {
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

func applyMastodon45Steps(tx *gorm.DB, steps []upgradeStep) error {
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
				return fmt.Errorf("Mastodon 4.5 %s migration %s statement %d: %w", step.phase, step.version, index+1, err)
			}
		}
		if err := recordUpgradeVersion(tx, step.version); err != nil {
			return err
		}
	}
	return nil
}

func mastodon45ExpandSteps() []upgradeStep {
	return []upgradeStep{
		{version: "20250717003848", phase: "expand", statements: []string{
			`CREATE TABLE username_blocks (id bigserial PRIMARY KEY, username character varying NOT NULL, normalized_username character varying NOT NULL, exact boolean DEFAULT false NOT NULL, allow_with_approval boolean DEFAULT false NOT NULL, created_at timestamp(6) without time zone NOT NULL, updated_at timestamp(6) without time zone NOT NULL)`,
			`CREATE UNIQUE INDEX index_username_blocks_on_username_lower_btree ON username_blocks (lower((username)::text))`,
			`CREATE INDEX index_username_blocks_on_normalized_username ON username_blocks (normalized_username)`,
		}},
		{version: "20250805075010", phase: "expand", statements: []string{`ALTER TABLE fasp_providers ADD COLUMN delivery_last_failed_at timestamp(6) without time zone`}},
		{version: "20250820084312", phase: "expand", statements: []string{`ALTER TABLE status_stats ADD COLUMN quotes_count bigint DEFAULT 0 NOT NULL`}},
		{version: "20250828222741", phase: "expand", statements: []string{`ALTER TABLE conversations ADD COLUMN parent_status_id bigint, ADD COLUMN parent_account_id bigint`}},
		{version: "20250902221600", phase: "expand", statements: []string{`CREATE INDEX index_statuses_on_conversation_id ON statuses (conversation_id)`}},
		{version: "20250909100506", phase: "expand", statements: []string{`CREATE UNIQUE INDEX index_conversations_on_parent_status_id ON conversations (parent_status_id) WHERE parent_status_id IS NOT NULL`}},
		{version: "20250912082651", phase: "expand", statements: []string{`ALTER TABLE accounts ADD COLUMN following_url character varying DEFAULT '' NOT NULL`}},
		{version: "20250924170259", phase: "expand", statements: []string{`ALTER TABLE accounts ADD COLUMN id_scheme integer DEFAULT 0`}},
		{version: "20251007100627", phase: "expand", statements: []string{`CREATE INDEX index_follows_on_target_account_id_and_account_id ON follows (target_account_id, account_id)`}},
		{version: "20251007142305", phase: "expand", statements: []string{`ALTER TABLE accounts ALTER COLUMN id_scheme SET DEFAULT 1`}},
	}
}

func mastodon45BackfillSteps() []upgradeStep {
	return []upgradeStep{
		{version: "20250911163952", phase: "backfill"},
		{version: "20251002140103", phase: "backfill"},
		// 20251023210145 is deliberately finalized in contract; its landing-page
		// data update is still performed and validated during backfill.
	}
}

func mastodon45ValidateSteps() []upgradeStep {
	// Upstream v4.5 has no separate validation migration marker.
	return nil
}

func mastodon45ContractSteps() []upgradeStep {
	return []upgradeStep{
		{version: "20250819100545", phase: "contract", statements: []string{
			`CREATE INDEX IF NOT EXISTS index_quotes_on_account_id_and_quoted_account_id_and_id ON quotes (account_id, quoted_account_id, id)`,
			`DROP INDEX IF EXISTS index_quotes_on_account_id_and_quoted_account_id`,
			`CREATE INDEX IF NOT EXISTS index_quotes_on_quoted_status_id_and_id ON quotes (quoted_status_id, id)`,
			`DROP INDEX IF EXISTS index_quotes_on_quoted_status_id`,
		}},
		{version: "20251007100813", phase: "contract", statements: []string{`DROP INDEX IF EXISTS index_follows_on_target_account_id`}},
		{version: "20251023210145", phase: "contract", statements: nil},
	}
}

func mastodon45PhaseVersions(phase UpgradePhase) []string {
	var steps []upgradeStep
	switch phase {
	case UpgradePhaseExpand:
		steps = mastodon45ExpandSteps()
	case UpgradePhaseBackfill:
		steps = mastodon45BackfillSteps()
	case UpgradePhaseValidate:
		steps = mastodon45ValidateSteps()
	case UpgradePhaseContract:
		steps = mastodon45ContractSteps()
	}
	versions := make([]string, 0, len(steps))
	for _, step := range steps {
		versions = append(versions, step.version)
	}
	return versions
}

func requireMastodon45Phase(tx *gorm.DB, phase UpgradePhase) error {
	missing := []string{}
	for _, version := range mastodon45PhaseVersions(phase) {
		applied, err := upgradeVersionApplied(tx, version)
		if err != nil {
			return err
		}
		if !applied {
			missing = append(missing, version)
		}
	}
	if len(missing) != 0 {
		return fmt.Errorf("Mastodon 4.5 phase %s is incomplete; missing migration versions: %s", phase, strings.Join(missing, ", "))
	}
	return nil
}

// The two replacement quote indexes are safe to install while v4.4 processes
// still run. Their old counterparts are retained until contract.
func ensureMastodon45ContractIndexes(tx *gorm.DB) error {
	for index, statement := range []string{
		`CREATE INDEX IF NOT EXISTS index_quotes_on_account_id_and_quoted_account_id_and_id ON quotes (account_id, quoted_account_id, id)`,
		`CREATE INDEX IF NOT EXISTS index_quotes_on_quoted_status_id_and_id ON quotes (quoted_status_id, id)`,
	} {
		if err := tx.Exec(statement).Error; err != nil {
			return fmt.Errorf("Mastodon 4.5 expand replacement index %d: %w", index+1, err)
		}
	}
	return nil
}

func applyMastodon45Backfill(tx *gorm.DB) error {
	if applied, err := upgradeVersionApplied(tx, "20250911163952"); err != nil {
		return err
	} else if !applied {
		if err := fillMastodon45DefaultQuotePolicy(tx); err != nil {
			return err
		}
		if err := recordUpgradeVersion(tx, "20250911163952"); err != nil {
			return err
		}
	}
	if applied, err := upgradeVersionApplied(tx, "20251002140103"); err != nil {
		return err
	} else if !applied {
		if err := migrateMastodon45TimelinePreviewSetting(tx); err != nil {
			return err
		}
		if err := recordUpgradeVersion(tx, "20251002140103"); err != nil {
			return err
		}
	}
	return migrateMastodon45LandingPageSetting(tx)
}

func fillMastodon45DefaultQuotePolicy(tx *gorm.DB) error {
	type userSettingsRow struct {
		ID       int64
		Settings sql.NullString
	}
	var rows []userSettingsRow
	if err := tx.Raw(`SELECT id, settings FROM users WHERE settings IS NOT NULL ORDER BY id`).Scan(&rows).Error; err != nil {
		return fmt.Errorf("Mastodon 4.5 read user quote-policy settings: %w", err)
	}
	for _, row := range rows {
		settings := map[string]any{}
		if err := json.Unmarshal([]byte(row.Settings.String), &settings); err != nil {
			return fmt.Errorf("Mastodon 4.5 decode settings for user id=%d: %w", row.ID, err)
		}
		changed := false
		if rubyBlankSettingValue(settings["notification_emails.quote"]) && settingIsFalse(settings["notification_emails.reblog"]) && settingIsFalse(settings["notification_emails.mention"]) {
			settings["notification_emails.quote"] = false
			changed = true
		}
		privacy, _ := settings["default_privacy"].(string)
		quotePolicy, quotePolicySet := settings["default_quote_policy"]
		quotePolicyName, _ := quotePolicy.(string)
		if privacy == "private" && quotePolicyName != "nobody" {
			settings["default_quote_policy"] = "nobody"
			changed = true
		} else if privacy == "unlisted" && (!quotePolicySet || quotePolicy == nil) {
			settings["default_quote_policy"] = "followers"
			changed = true
		}
		if !changed {
			continue
		}
		encoded, err := json.Marshal(settings)
		if err != nil {
			return fmt.Errorf("Mastodon 4.5 encode settings for user id=%d: %w", row.ID, err)
		}
		if err := tx.Exec(`UPDATE users SET settings = ? WHERE id = ?`, string(encoded), row.ID).Error; err != nil {
			return fmt.Errorf("Mastodon 4.5 update settings for user id=%d: %w", row.ID, err)
		}
	}
	return nil
}

func rubyBlankSettingValue(value any) bool {
	if value == nil || value == false {
		return true
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed) == ""
	case []any:
		return len(typed) == 0
	case map[string]any:
		return len(typed) == 0
	default:
		return false
	}
}

func settingIsFalse(value any) bool {
	boolean, ok := value.(bool)
	return ok && !boolean
}

func migrateMastodon45TimelinePreviewSetting(tx *gorm.DB) error {
	value, present, err := mastodon45TruthySetting(tx, "timeline_preview")
	if err != nil || !present {
		return err
	}
	access := "authenticated"
	if value {
		access = "public"
	}
	for _, name := range []string{"local_live_feed_access", "remote_live_feed_access", "local_topic_feed_access", "remote_topic_feed_access"} {
		if err := upsertMastodon45Setting(tx, name, "--- "+access+"\n"); err != nil {
			return err
		}
	}
	return nil
}

func migrateMastodon45LandingPageSetting(tx *gorm.DB) error {
	value, present, err := mastodon45TruthySetting(tx, "trends_as_landing_page")
	if err != nil || !present {
		return err
	}
	landingPage := "about"
	if value {
		landingPage = "trends"
	}
	return upsertMastodon45Setting(tx, "landing_page", "--- "+landingPage+"\n")
}

func mastodon45TruthySetting(tx *gorm.DB, name string) (bool, bool, error) {
	var raw sql.NullString
	err := tx.Raw(`SELECT value FROM settings WHERE var = ? LIMIT 1`, name).Row().Scan(&raw)
	if err != nil {
		if err == sql.ErrNoRows {
			return false, false, nil
		}
		return false, false, fmt.Errorf("Mastodon 4.5 read %s setting: %w", name, err)
	}
	if !raw.Valid || strings.TrimSpace(raw.String) == "" {
		return false, false, nil
	}
	var value any
	if err := yaml.Unmarshal([]byte(raw.String), &value); err != nil {
		return false, false, fmt.Errorf("Mastodon 4.5 decode %s setting: %w", name, err)
	}
	return mastodon45RubyTruthy(value), true, nil
}

func mastodon45RubyTruthy(value any) bool {
	if value == nil {
		return false
	}
	if boolean, ok := value.(bool); ok {
		return boolean
	}
	// Ruby treats every value except nil and false as truthy. Preserve that
	// behavior for legacy YAML strings, numbers, arrays, and hashes.
	return true
}

func upsertMastodon45Setting(tx *gorm.DB, name string, value string) error {
	if err := tx.Exec(`INSERT INTO settings (var, value, created_at, updated_at) VALUES (?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP) ON CONFLICT (var) DO UPDATE SET value = EXCLUDED.value, updated_at = EXCLUDED.updated_at`, name, value).Error; err != nil {
		return fmt.Errorf("Mastodon 4.5 upsert %s setting: %w", name, err)
	}
	return nil
}

func validateMastodon45Data(tx *gorm.DB) error {
	checks := []struct {
		name  string
		query string
	}{
		{name: "duplicate username block case-folds", query: `SELECT COUNT(*) FROM (SELECT lower(username) FROM username_blocks GROUP BY lower(username) HAVING COUNT(*) > 1) duplicate_username_blocks`},
		{name: "NULL username block values", query: `SELECT COUNT(*) FROM username_blocks WHERE username IS NULL OR normalized_username IS NULL`},
	}
	for _, check := range checks {
		var count int64
		if err := tx.Raw(check.query).Scan(&count).Error; err != nil {
			return fmt.Errorf("Mastodon 4.5 validate %s: %w", check.name, err)
		}
		if count != 0 {
			return fmt.Errorf("Mastodon 4.5 validate failed: %s count=%d", check.name, count)
		}
	}
	return nil
}

func seedMastodon45UsernameBlocks(tx *gorm.DB, now time.Time) error {
	for _, seed := range mastodon45UsernameBlockSeeds() {
		result := tx.Exec(`INSERT INTO username_blocks (username, normalized_username, exact, allow_with_approval, created_at, updated_at) SELECT ?, ?, ?, false, ?, ? WHERE NOT EXISTS (SELECT 1 FROM username_blocks WHERE lower(username) = lower(?))`, seed.username, normalizeMastodon45BlockedUsername(seed.username), seed.exact, now, now, seed.username)
		if result.Error != nil {
			return fmt.Errorf("seed Mastodon 4.5 username block %s: %w", seed.username, result.Error)
		}
	}
	return nil
}

type mastodon45UsernameBlockSeed struct {
	username string
	exact    bool
}

func mastodon45UsernameBlockSeeds() []mastodon45UsernameBlockSeed {
	seeds := make([]mastodon45UsernameBlockSeed, 0, 23)
	for _, username := range []string{
		"abuse", "account", "accounts", "admin", "administration", "administrator", "admins",
		"help", "helpdesk", "instance", "mod", "moderator", "moderators", "mods", "owner", "root",
		"security", "server", "staff", "support", "webmaster",
	} {
		seeds = append(seeds, mastodon45UsernameBlockSeed{username: username, exact: true})
	}
	for _, username := range []string{"mastodon", "mastadon"} {
		seeds = append(seeds, mastodon45UsernameBlockSeed{username: username})
	}
	return seeds
}

func normalizeMastodon45BlockedUsername(value string) string {
	replacer := strings.NewReplacer("1", "i", "2", "z", "3", "e", "4", "a", "5", "s", "7", "t", "8", "b", "9", "g", "0", "o")
	return replacer.Replace(strings.ToLower(value))
}
