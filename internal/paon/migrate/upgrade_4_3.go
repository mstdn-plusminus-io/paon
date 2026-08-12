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
		steps = mastodon43DuplicateBackfillSteps()
	case UpgradePhaseValidate:
		steps = mastodon43ValidateSteps()
	case UpgradePhaseContract:
		steps = mastodon43ContractSteps()
	default:
		return nil
	}
	versions := make([]string, 0, len(steps)+7)
	for _, step := range steps {
		versions = append(versions, step.version)
	}
	if phase == UpgradePhaseBackfill {
		versions = append(versions,
			"20240109103012",
			"20240304090449",
			"20240307180905",
			"20240321160706",
			"20240603195202",
			"20240808124338",
			"20240808124339",
		)
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
		if err := tx.Exec(`UPDATE users SET locale = 'fr-CA', updated_at = CURRENT_TIMESTAMP WHERE id IN ?`, ids).Error; err != nil {
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
			query := fmt.Sprintf(`SELECT id FROM %s WHERE scopes ~ '(^|[[:space:]])read:me([[:space:]]|$)' ORDER BY id ASC LIMIT ?`, table)
			if err := tx.Raw(query, migrationBatchSize).Scan(&ids).Error; err != nil {
				return fmt.Errorf("Mastodon 4.3 profile scope backfill query %s: %w", table, err)
			}
			if len(ids) == 0 {
				break
			}
			statement := fmt.Sprintf(`UPDATE %s SET scopes = array_to_string(array_replace(regexp_split_to_array(btrim(scopes), '\s+'), 'read:me', 'profile'), ' ') WHERE id IN ?`, table)
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
	return []upgradeStep{
		{
			version: "20231018192110", phase: "backfill",
			statements: []string{
				`DELETE FROM webauthn_credentials WHERE id IN (SELECT id FROM (SELECT id, ROW_NUMBER() OVER (PARTITION BY user_id, nickname ORDER BY id ASC) AS duplicate_rank FROM webauthn_credentials) ranked WHERE duplicate_rank > 1 ORDER BY id ASC LIMIT ?)`,
				`CREATE UNIQUE INDEX index_webauthn_credentials_on_user_id_and_nickname ON webauthn_credentials (user_id, nickname)`,
			},
		},
		{
			version: "20231018193209", phase: "backfill",
			statements: []string{
				`DELETE FROM account_aliases WHERE id IN (SELECT id FROM (SELECT id, ROW_NUMBER() OVER (PARTITION BY account_id, uri ORDER BY id ASC) AS duplicate_rank FROM account_aliases) ranked WHERE duplicate_rank > 1 ORDER BY id ASC LIMIT ?)`,
				`CREATE UNIQUE INDEX index_account_aliases_on_account_id_and_uri ON account_aliases (account_id, uri)`,
			},
		},
		{
			version: "20231018193355", phase: "backfill",
			statements: []string{
				`DELETE FROM custom_filter_statuses WHERE id IN (SELECT id FROM (SELECT id, ROW_NUMBER() OVER (PARTITION BY status_id, custom_filter_id ORDER BY id ASC) AS duplicate_rank FROM custom_filter_statuses) ranked WHERE duplicate_rank > 1 ORDER BY id ASC LIMIT ?)`,
				`CREATE UNIQUE INDEX index_custom_filter_statuses_on_status_id_and_custom_filter_id ON custom_filter_statuses (status_id, custom_filter_id)`,
			},
		},
		{
			version: "20231018193659", phase: "backfill",
			statements: []string{
				`DELETE FROM identities WHERE id IN (SELECT id FROM (SELECT id, ROW_NUMBER() OVER (PARTITION BY uid, provider ORDER BY id ASC) AS duplicate_rank FROM identities) ranked WHERE duplicate_rank > 1 ORDER BY id ASC LIMIT ?)`,
				`CREATE UNIQUE INDEX index_identities_on_uid_and_provider ON identities (uid, provider)`,
			},
		},
	}
}

func mastodon43ExpandSteps() []upgradeStep {
	return []upgradeStep{
		{version: "20231006183200", phase: "expand", statements: []string{`ALTER TABLE preview_cards_statuses ADD COLUMN url character varying`}},
		{version: "20231210154528", phase: "expand", statements: []string{`ALTER TABLE users ADD COLUMN otp_secret character varying`}},
		{version: "20231211234923", phase: "expand", statements: []string{
			`CREATE TABLE follow_recommendation_mutes (id bigserial PRIMARY KEY, account_id bigint NOT NULL, target_account_id bigint NOT NULL, created_at timestamp(6) without time zone NOT NULL, updated_at timestamp(6) without time zone NOT NULL, FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE, FOREIGN KEY (target_account_id) REFERENCES accounts(id) ON DELETE CASCADE)`,
			`CREATE UNIQUE INDEX idx_on_account_id_target_account_id_a8c8ddf44e ON follow_recommendation_mutes (account_id, target_account_id)`,
			`CREATE INDEX index_follow_recommendation_mutes_on_target_account_id ON follow_recommendation_mutes (target_account_id)`,
		}},
		{version: "20231212073317", phase: "expand", statements: []string{`CREATE INDEX idx_on_account_id_language_sensitive_250461e1eb ON account_summaries (account_id, language, sensitive)`}},
		{version: "20231222100226", phase: "expand", statements: []string{`ALTER TABLE email_domain_blocks ADD COLUMN allow_with_approval boolean DEFAULT false NOT NULL`}},
		{version: "20240111033014", phase: "expand", statements: []string{
			`CREATE TABLE generated_annual_reports (id bigserial PRIMARY KEY, account_id bigint NOT NULL, year integer NOT NULL, data jsonb NOT NULL, schema_version integer NOT NULL, viewed_at timestamp(6) without time zone, created_at timestamp(6) without time zone NOT NULL, updated_at timestamp(6) without time zone NOT NULL, FOREIGN KEY (account_id) REFERENCES accounts(id))`,
			`CREATE UNIQUE INDEX index_generated_annual_reports_on_account_id_and_year ON generated_annual_reports (account_id, year)`,
		}},
		{version: "20240221195424", phase: "expand", statements: []string{`ALTER TABLE notifications ADD COLUMN filtered boolean DEFAULT false NOT NULL`}},
		{version: "20240221195828", phase: "expand", statements: []string{
			`CREATE SEQUENCE notification_requests_id_seq`,
			`CREATE TABLE notification_requests (id bigint DEFAULT nextval('notification_requests_id_seq') NOT NULL PRIMARY KEY, account_id bigint NOT NULL, from_account_id bigint NOT NULL, last_status_id bigint NOT NULL, notifications_count bigint DEFAULT 0 NOT NULL, dismissed boolean DEFAULT false NOT NULL, created_at timestamp(6) without time zone NOT NULL, updated_at timestamp(6) without time zone NOT NULL, FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE, FOREIGN KEY (from_account_id) REFERENCES accounts(id) ON DELETE CASCADE, FOREIGN KEY (last_status_id) REFERENCES statuses(id) ON DELETE SET NULL)`,
			`CREATE UNIQUE INDEX index_notification_requests_on_account_id_and_from_account_id ON notification_requests (account_id, from_account_id)`,
			`CREATE INDEX index_notification_requests_on_from_account_id ON notification_requests (from_account_id)`,
			`CREATE INDEX index_notification_requests_on_last_status_id ON notification_requests (last_status_id)`,
			`CREATE INDEX index_notification_requests_on_account_id_and_id ON notification_requests (account_id, id DESC) WHERE dismissed = false`,
		}},
		{version: "20240221211359", phase: "expand", statements: []string{`ALTER TABLE notification_requests ALTER COLUMN id SET DEFAULT timestamp_id('notification_requests')`}},
		{version: "20240222193403", phase: "expand", statements: []string{
			`CREATE TABLE notification_permissions (id bigserial PRIMARY KEY, account_id bigint NOT NULL, from_account_id bigint NOT NULL, created_at timestamp(6) without time zone NOT NULL, updated_at timestamp(6) without time zone NOT NULL, FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE, FOREIGN KEY (from_account_id) REFERENCES accounts(id) ON DELETE CASCADE)`,
			`CREATE INDEX index_notification_permissions_on_account_id ON notification_permissions (account_id)`,
			`CREATE INDEX index_notification_permissions_on_from_account_id ON notification_permissions (from_account_id)`,
		}},
		{version: "20240222203722", phase: "expand", statements: []string{
			`CREATE TABLE notification_policies (id bigserial PRIMARY KEY, account_id bigint NOT NULL, filter_not_following boolean DEFAULT false NOT NULL, filter_not_followers boolean DEFAULT false NOT NULL, filter_new_accounts boolean DEFAULT false NOT NULL, filter_private_mentions boolean DEFAULT true NOT NULL, created_at timestamp(6) without time zone NOT NULL, updated_at timestamp(6) without time zone NOT NULL, FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE)`,
			`CREATE UNIQUE INDEX index_notification_policies_on_account_id ON notification_policies (account_id)`,
		}},
		{version: "20240227191620", phase: "expand", statements: []string{`CREATE INDEX index_notifications_on_filtered ON notifications (account_id, id DESC, type) WHERE filtered = false`}},
		{version: "20240310123453", phase: "expand", statements: []string{`ALTER TABLE rules ADD COLUMN hint text DEFAULT '' NOT NULL`}},
		{version: "20240312100644", phase: "expand", statements: []string{
			`CREATE TABLE relationship_severance_events (id bigserial PRIMARY KEY, type integer NOT NULL, target_name character varying NOT NULL, purged boolean DEFAULT false NOT NULL, created_at timestamp(6) without time zone NOT NULL, updated_at timestamp(6) without time zone NOT NULL)`,
			`CREATE INDEX index_relationship_severance_events_on_type_and_target_name ON relationship_severance_events (type, target_name)`,
		}},
		{version: "20240312105620", phase: "expand", statements: []string{
			`CREATE TABLE severed_relationships (id bigserial PRIMARY KEY, relationship_severance_event_id bigint NOT NULL, local_account_id bigint NOT NULL, remote_account_id bigint NOT NULL, direction integer NOT NULL, show_reblogs boolean, notify boolean, languages character varying[], created_at timestamp(6) without time zone NOT NULL, updated_at timestamp(6) without time zone NOT NULL, FOREIGN KEY (relationship_severance_event_id) REFERENCES relationship_severance_events(id) ON DELETE CASCADE, FOREIGN KEY (local_account_id) REFERENCES accounts(id) ON DELETE CASCADE, FOREIGN KEY (remote_account_id) REFERENCES accounts(id) ON DELETE CASCADE)`,
			`CREATE UNIQUE INDEX index_severed_relationships_on_unique_tuples ON severed_relationships (relationship_severance_event_id, local_account_id, direction, remote_account_id)`,
			`CREATE INDEX index_severed_relationships_on_local_account_and_event ON severed_relationships (local_account_id, relationship_severance_event_id)`,
			`CREATE INDEX index_severed_relationships_on_remote_account_id ON severed_relationships (remote_account_id)`,
		}},
		{version: "20240320140159", phase: "expand", statements: []string{
			`CREATE TABLE account_relationship_severance_events (id bigserial PRIMARY KEY, account_id bigint NOT NULL, relationship_severance_event_id bigint NOT NULL, relationships_count integer DEFAULT 0 NOT NULL, created_at timestamp(6) without time zone NOT NULL, updated_at timestamp(6) without time zone NOT NULL, FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE, FOREIGN KEY (relationship_severance_event_id) REFERENCES relationship_severance_events(id) ON DELETE CASCADE)`,
			`CREATE UNIQUE INDEX idx_on_account_id_relationship_severance_event_id_7bd82bf20e ON account_relationship_severance_events (account_id, relationship_severance_event_id)`,
			`CREATE INDEX index_account_relationship_severance_events_on_account_id ON account_relationship_severance_events (account_id)`,
			`CREATE INDEX idx_on_relationship_severance_event_id_403f53e707 ON account_relationship_severance_events (relationship_severance_event_id)`,
		}},
		{version: "20240320163441", phase: "expand", statements: []string{`ALTER TABLE notification_requests ALTER COLUMN last_status_id DROP NOT NULL`}},
		{version: "20240322125607", phase: "expand", statements: []string{
			`ALTER TABLE account_relationship_severance_events ADD COLUMN followers_count integer DEFAULT 0 NOT NULL`,
			`ALTER TABLE account_relationship_severance_events ADD COLUMN following_count integer DEFAULT 0 NOT NULL`,
		}},
		{version: "20240510192043", phase: "expand", statements: nil},
		{version: "20240513095755", phase: "expand", statements: []string{`ALTER TABLE notifications ADD COLUMN group_key character varying`}},
		{version: "20240513123807", phase: "expand", statements: []string{`CREATE INDEX index_notifications_on_account_id_and_group_key ON notifications (account_id, group_key) WHERE group_key IS NOT NULL`}},
		{version: "20240522041528", phase: "expand", statements: []string{
			`ALTER TABLE preview_cards ADD COLUMN author_account_id bigint`,
			`ALTER TABLE preview_cards ADD FOREIGN KEY (author_account_id) REFERENCES accounts(id) ON DELETE SET NULL`,
			`CREATE INDEX index_preview_cards_on_author_account_id ON preview_cards (author_account_id) WHERE author_account_id IS NOT NULL`,
		}},
		{version: "20240713171841", phase: "expand", statements: []string{
			`ALTER TABLE reports ADD COLUMN application_id bigint`,
			`ALTER TABLE reports ADD CONSTRAINT reports_application_id_fkey FOREIGN KEY (application_id) REFERENCES oauth_applications(id) ON DELETE SET NULL NOT VALID`,
		}},
		{version: "20240724181224", phase: "expand", statements: []string{
			`ALTER TABLE oauth_access_grants ADD COLUMN code_challenge character varying`,
			`ALTER TABLE oauth_access_grants ADD COLUMN code_challenge_method character varying`,
		}},
		{version: "20240808114841", phase: "expand", statements: []string{
			`ALTER TABLE notification_policies ADD COLUMN for_not_following integer DEFAULT 0 NOT NULL`,
			`ALTER TABLE notification_policies ADD COLUMN for_not_followers integer DEFAULT 0 NOT NULL`,
			`ALTER TABLE notification_policies ADD COLUMN for_new_accounts integer DEFAULT 0 NOT NULL`,
			`ALTER TABLE notification_policies ADD COLUMN for_private_mentions integer DEFAULT 1 NOT NULL`,
			`ALTER TABLE notification_policies ADD COLUMN for_limited_accounts integer DEFAULT 1 NOT NULL`,
		}},
		{version: "20240909014637", phase: "expand", statements: []string{`ALTER TABLE accounts ADD COLUMN attribution_domains character varying[] DEFAULT '{}'::character varying[]`}},
	}
}

func mastodon43ValidateSteps() []upgradeStep {
	return []upgradeStep{
		{version: "20240217171534", phase: "validate", statements: []string{
			`ALTER TABLE status_pins ALTER COLUMN created_at DROP DEFAULT`,
			`ALTER TABLE status_pins ALTER COLUMN updated_at DROP DEFAULT`,
		}},
		{version: "20240607093446", phase: "validate", statements: []string{`ALTER TABLE mentions ADD CONSTRAINT mentions_status_id_null CHECK (status_id IS NOT NULL) NOT VALID`}},
		{version: "20240607093954", phase: "validate", statements: []string{
			`ALTER TABLE mentions VALIDATE CONSTRAINT mentions_status_id_null`,
			`ALTER TABLE mentions ALTER COLUMN status_id SET NOT NULL`,
			`ALTER TABLE mentions DROP CONSTRAINT mentions_status_id_null`,
		}},
		{version: "20240607094603", phase: "validate", statements: []string{`ALTER TABLE mentions ADD CONSTRAINT mentions_account_id_null CHECK (account_id IS NOT NULL) NOT VALID`}},
		{version: "20240607094856", phase: "validate", statements: []string{
			`ALTER TABLE mentions VALIDATE CONSTRAINT mentions_account_id_null`,
			`ALTER TABLE mentions ALTER COLUMN account_id SET NOT NULL`,
			`ALTER TABLE mentions DROP CONSTRAINT mentions_account_id_null`,
		}},
		{version: "20240713171909", phase: "validate", statements: []string{`ALTER TABLE reports VALIDATE CONSTRAINT reports_application_id_fkey`}},
	}
}

func mastodon43ContractSteps() []upgradeStep {
	return []upgradeStep{
		{version: "20240322130318", phase: "contract", statements: []string{`ALTER TABLE account_relationship_severance_events DROP COLUMN relationships_count`}},
		{version: "20240322161611", phase: "contract", statements: []string{
			`ALTER TABLE users DROP COLUMN admin`,
			`ALTER TABLE users DROP COLUMN moderator`,
		}},
		{version: "20240712064044", phase: "contract", statements: []string{
			`DELETE FROM notification_requests WHERE dismissed`,
			`ALTER TABLE notification_requests DROP COLUMN dismissed`,
		}},
		{version: "20240720140205", phase: "contract", statements: []string{
			`DROP TABLE system_keys`,
			`DROP TABLE one_time_keys`,
			`DROP TABLE encrypted_messages`,
			`DROP SEQUENCE IF EXISTS encrypted_messages_id_seq`,
			`DROP TABLE devices`,
			`ALTER TABLE accounts DROP COLUMN devices_url`,
		}},
		{version: "20240808125420", phase: "contract", statements: []string{
			`ALTER TABLE notification_policies DROP COLUMN filter_not_following`,
			`ALTER TABLE notification_policies DROP COLUMN filter_not_followers`,
			`ALTER TABLE notification_policies DROP COLUMN filter_new_accounts`,
			`ALTER TABLE notification_policies DROP COLUMN filter_private_mentions`,
		}},
		{version: "20240916190140", phase: "contract", statements: []string{
			`UPDATE oauth_applications SET scopes = array_to_string(array_remove(regexp_split_to_array(btrim(scopes), '\s+'), 'crypto'), ' ') WHERE scopes ~ '(^|[[:space:]])crypto([[:space:]]|$)'`,
			`UPDATE oauth_access_tokens SET scopes = array_to_string(array_remove(regexp_split_to_array(btrim(scopes), '\s+'), 'crypto'), ' ') WHERE scopes ~ '(^|[[:space:]])crypto([[:space:]]|$)'`,
		}},
		{version: "20241007071624", phase: "contract", statements: []string{
			`INSERT INTO ar_internal_metadata (key, value, created_at, updated_at) VALUES ('schema_sha1', 'd03e3ba56d365d37ac099782d9d80efbce3abb8b', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP) ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = EXCLUDED.updated_at`,
			`INSERT INTO schema_migrations (version) VALUES
			  ('20180813113448'),
			  ('20181116184611'),
			  ('20190511152737'),
			  ('20190519130537'),
			  ('20190706233204'),
			  ('20190715031050'),
			  ('20190901040524'),
			  ('20190927124642'),
			  ('20200917193528'),
			  ('20200917222734'),
			  ('20201017234926'),
			  ('20210308133107'),
			  ('20210502233513'),
			  ('20210507001928'),
			  ('20210526193025'),
			  ('20210616214135'),
			  ('20210808071221'),
			  ('20211126000907'),
			  ('20220109213908'),
			  ('20220118183010'),
			  ('20220118183123'),
			  ('20220202201015'),
			  ('20220303203437'),
			  ('20220307083603'),
			  ('20220310060545'),
			  ('20220310060556'),
			  ('20220310060614'),
			  ('20220310060626'),
			  ('20220310060641'),
			  ('20220310060653'),
			  ('20220310060706'),
			  ('20220310060722'),
			  ('20220310060740'),
			  ('20220310060750'),
			  ('20220310060809'),
			  ('20220310060833'),
			  ('20220310060854'),
			  ('20220310060913'),
			  ('20220310060926'),
			  ('20220310060939'),
			  ('20220310060959'),
			  ('20220429101025'),
			  ('20220429101850'),
			  ('20220527114923'),
			  ('20220613110802'),
			  ('20220613110903'),
			  ('20220617202502'),
			  ('20220704024901'),
			  ('20220729171123'),
			  ('20220824164532'),
			  ('20221101190723'),
			  ('20221206114142'),
			  ('20230803082451'),
			  ('20230803112520'),
			  ('20230811103651'),
			  ('20230818142253'),
			  ('20230904134623')
			ON CONFLICT (version) DO NOTHING`,
			`ALTER TABLE account_statuses_cleanup_policies ALTER COLUMN created_at TYPE timestamp(6) without time zone, ALTER COLUMN updated_at TYPE timestamp(6) without time zone`,
			`ALTER TABLE appeals ALTER COLUMN created_at TYPE timestamp(6) without time zone, ALTER COLUMN updated_at TYPE timestamp(6) without time zone`,
			`ALTER TABLE ar_internal_metadata ALTER COLUMN created_at TYPE timestamp(6) without time zone, ALTER COLUMN updated_at TYPE timestamp(6) without time zone`,
			`ALTER TABLE bulk_import_rows ALTER COLUMN created_at TYPE timestamp(6) without time zone, ALTER COLUMN updated_at TYPE timestamp(6) without time zone`,
			`ALTER TABLE bulk_imports ALTER COLUMN created_at TYPE timestamp(6) without time zone, ALTER COLUMN updated_at TYPE timestamp(6) without time zone`,
			`ALTER TABLE canonical_email_blocks ALTER COLUMN created_at TYPE timestamp(6) without time zone, ALTER COLUMN updated_at TYPE timestamp(6) without time zone`,
			`ALTER TABLE custom_filter_keywords ALTER COLUMN created_at TYPE timestamp(6) without time zone, ALTER COLUMN updated_at TYPE timestamp(6) without time zone`,
			`ALTER TABLE custom_filter_statuses ALTER COLUMN created_at TYPE timestamp(6) without time zone, ALTER COLUMN updated_at TYPE timestamp(6) without time zone`,
			`ALTER TABLE follow_recommendation_suppressions ALTER COLUMN created_at TYPE timestamp(6) without time zone, ALTER COLUMN updated_at TYPE timestamp(6) without time zone`,
			`ALTER TABLE preview_card_providers ALTER COLUMN created_at TYPE timestamp(6) without time zone, ALTER COLUMN updated_at TYPE timestamp(6) without time zone`,
			`ALTER TABLE preview_cards ALTER COLUMN published_at TYPE timestamp(6) without time zone`,
			`ALTER TABLE software_updates ALTER COLUMN created_at TYPE timestamp(6) without time zone, ALTER COLUMN updated_at TYPE timestamp(6) without time zone`,
			`ALTER TABLE status_edits ALTER COLUMN created_at TYPE timestamp(6) without time zone, ALTER COLUMN updated_at TYPE timestamp(6) without time zone`,
			`ALTER TABLE tag_follows ALTER COLUMN created_at TYPE timestamp(6) without time zone, ALTER COLUMN updated_at TYPE timestamp(6) without time zone`,
			`ALTER TABLE user_roles ALTER COLUMN created_at TYPE timestamp(6) without time zone, ALTER COLUMN updated_at TYPE timestamp(6) without time zone`,
			`ALTER TABLE webhooks ALTER COLUMN created_at TYPE timestamp(6) without time zone, ALTER COLUMN updated_at TYPE timestamp(6) without time zone`,
		}},
	}
}
