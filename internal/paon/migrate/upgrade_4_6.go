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
	return []upgradeStep{
		{version: "20251117023614", phase: "expand", statements: []string{`ALTER TABLE media_attachments ADD COLUMN thumbnail_storage_schema_version integer`}},
		{version: "20251118115657", phase: "expand", statements: []string{
			`CREATE TABLE collections (id bigserial PRIMARY KEY, account_id bigint NOT NULL, name character varying NOT NULL, description text NOT NULL, uri character varying, local boolean NOT NULL, sensitive boolean NOT NULL, discoverable boolean NOT NULL, tag_id bigint, original_number_of_items integer, created_at timestamp(6) without time zone NOT NULL, updated_at timestamp(6) without time zone NOT NULL, CONSTRAINT fk_rails_544f142936 FOREIGN KEY (account_id) REFERENCES accounts(id), CONSTRAINT fk_rails_70f13aad15 FOREIGN KEY (tag_id) REFERENCES tags(id))`,
			`CREATE INDEX index_collections_on_account_id ON collections (account_id)`,
			`CREATE INDEX index_collections_on_tag_id ON collections (tag_id)`,
		}},
		{version: "20251119093332", phase: "expand", statements: []string{
			`CREATE TABLE collection_items (id bigserial PRIMARY KEY, collection_id bigint NOT NULL, account_id bigint, position integer DEFAULT 1 NOT NULL, object_uri character varying, approval_uri character varying, activity_uri character varying, approval_last_verified_at timestamp(6) without time zone, state integer DEFAULT 0 NOT NULL, created_at timestamp(6) without time zone NOT NULL, updated_at timestamp(6) without time zone NOT NULL, CONSTRAINT fk_rails_b1a778644b FOREIGN KEY (collection_id) REFERENCES collections(id) ON DELETE CASCADE, CONSTRAINT fk_rails_2eb992658d FOREIGN KEY (account_id) REFERENCES accounts(id))`,
			`CREATE INDEX index_collection_items_on_collection_id ON collection_items (collection_id)`,
			`CREATE INDEX index_collection_items_on_account_id ON collection_items (account_id)`,
			`CREATE UNIQUE INDEX index_collection_items_on_object_uri ON collection_items (object_uri) WHERE activity_uri IS NOT NULL`,
			`CREATE UNIQUE INDEX index_collection_items_on_approval_uri ON collection_items (approval_uri) WHERE approval_uri IS NOT NULL`,
		}},
		{version: "20251201154910", phase: "expand", statements: []string{
			`ALTER TABLE custom_emoji_categories ADD COLUMN featured_emoji_id bigint`,
			`ALTER TABLE custom_emoji_categories ADD CONSTRAINT fk_rails_ad7840c8cf FOREIGN KEY (featured_emoji_id) REFERENCES custom_emojis(id) ON DELETE SET NULL NOT VALID`,
		}},
		{version: "20251202140424", phase: "expand", statements: []string{`ALTER TABLE generated_annual_reports ADD COLUMN share_key character varying`}},
		{version: "20251209093813", phase: "expand", statements: []string{`ALTER TABLE collections ADD COLUMN item_count integer DEFAULT 0 NOT NULL`}},
		{version: "20251217091936", phase: "expand", statements: []string{`ALTER TABLE accounts ADD COLUMN feature_approval_policy integer DEFAULT 0 NOT NULL`}},
		{version: "20260115153219", phase: "expand", statements: []string{
			`ALTER TABLE collections ALTER COLUMN id SET DEFAULT timestamp_id('collections')`,
			`ALTER TABLE collection_items ALTER COLUMN id SET DEFAULT timestamp_id('collection_items')`,
		}},
		{version: "20260119153538", phase: "expand", statements: []string{`ALTER TABLE collections ADD COLUMN language character varying`}},
		{version: "20260127141459", phase: "expand", statements: []string{`ALTER TABLE accounts ADD COLUMN avatar_description character varying DEFAULT '' NOT NULL`}},
		{version: "20260127141820", phase: "expand", statements: []string{`ALTER TABLE accounts ADD COLUMN header_description character varying DEFAULT '' NOT NULL`}},
		{version: "20260211132603", phase: "expand", statements: []string{`ALTER TABLE user_roles ADD COLUMN require_2fa boolean DEFAULT false NOT NULL`}},
		{version: "20260212113020", phase: "expand", statements: []string{`ALTER TABLE collection_items ADD COLUMN uri character varying`}},
		{version: "20260212131934", phase: "expand", statements: []string{
			`CREATE TABLE collection_reports (id bigserial PRIMARY KEY, collection_id bigint NOT NULL, report_id bigint NOT NULL, created_at timestamp(6) without time zone NOT NULL, updated_at timestamp(6) without time zone NOT NULL, CONSTRAINT fk_rails_0720c1a3d6 FOREIGN KEY (collection_id) REFERENCES collections(id) ON DELETE CASCADE, CONSTRAINT fk_rails_4a504bd5e6 FOREIGN KEY (report_id) REFERENCES reports(id) ON DELETE CASCADE)`,
			`CREATE INDEX index_collection_reports_on_collection_id ON collection_reports (collection_id)`,
			`CREATE INDEX index_collection_reports_on_report_id ON collection_reports (report_id)`,
		}},
		{version: "20260217154542", phase: "expand", statements: []string{`ALTER TABLE accounts ADD COLUMN show_media boolean DEFAULT true NOT NULL, ADD COLUMN show_media_replies boolean DEFAULT true NOT NULL, ADD COLUMN show_featured boolean DEFAULT true NOT NULL`}},
		{version: "20260303144409", phase: "expand", statements: []string{
			`ALTER TABLE preview_cards ADD COLUMN unverified_author_account_id bigint`,
			`ALTER TABLE preview_cards ADD CONSTRAINT fk_rails_6fb2119894 FOREIGN KEY (unverified_author_account_id) REFERENCES accounts(id) ON DELETE SET NULL`,
			`CREATE INDEX index_preview_cards_on_unverified_author_account_id_and_id ON preview_cards (unverified_author_account_id, id) WHERE unverified_author_account_id IS NOT NULL`,
		}},
		{version: "20260310095021", phase: "expand", statements: []string{`ALTER TABLE collections ADD COLUMN description_html text`, `ALTER TABLE collections ALTER COLUMN description DROP NOT NULL`}},
		{version: "20260311152331", phase: "expand", statements: []string{`ALTER TABLE accounts ADD COLUMN collections_url character varying`}},
		{version: "20260311212130", phase: "expand", statements: []string{
			`CREATE TABLE email_subscriptions (id bigserial PRIMARY KEY, account_id bigint NOT NULL, email character varying NOT NULL, locale character varying NOT NULL, confirmation_token character varying, confirmed_at timestamp(6) without time zone, created_at timestamp(6) without time zone NOT NULL, updated_at timestamp(6) without time zone NOT NULL, CONSTRAINT fk_rails_282940e759 FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE)`,
			`CREATE INDEX index_email_subscriptions_on_account_id ON email_subscriptions (account_id)`,
			`CREATE UNIQUE INDEX index_email_subscriptions_on_confirmation_token ON email_subscriptions (confirmation_token) WHERE confirmation_token IS NOT NULL`,
			`CREATE UNIQUE INDEX index_email_subscriptions_on_account_id_and_email ON email_subscriptions (account_id, email)`,
		}},
		{version: "20260319142348", phase: "expand", statements: []string{
			`CREATE TABLE tagged_objects (id bigserial PRIMARY KEY, status_id bigint NOT NULL, object_type character varying, object_id bigint, ap_type character varying NOT NULL, uri character varying, created_at timestamp(6) without time zone NOT NULL, updated_at timestamp(6) without time zone NOT NULL, CONSTRAINT fk_rails_087c1d32f7 FOREIGN KEY (status_id) REFERENCES statuses(id) ON DELETE CASCADE)`,
			`CREATE INDEX index_tagged_objects_on_object ON tagged_objects (object_type, object_id)`,
			`CREATE UNIQUE INDEX idx_on_status_id_object_type_object_id_d6ebe374bd ON tagged_objects (status_id, object_type, object_id) WHERE object_type IS NOT NULL AND object_id IS NOT NULL`,
			`CREATE UNIQUE INDEX index_tagged_objects_on_status_id_and_uri ON tagged_objects (status_id, uri) WHERE uri IS NOT NULL`,
		}},
		{version: "20260323105645", phase: "expand", statements: []string{
			`CREATE TABLE keypairs (id bigserial PRIMARY KEY, account_id bigint NOT NULL, uri character varying NOT NULL, type integer NOT NULL, public_key character varying NOT NULL, private_key character varying, expires_at timestamp(6) without time zone, revoked boolean DEFAULT false NOT NULL, created_at timestamp(6) without time zone NOT NULL, updated_at timestamp(6) without time zone NOT NULL, CONSTRAINT fk_rails_f5ea7ac36a FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE)`,
			`CREATE INDEX index_keypairs_on_account_id ON keypairs (account_id)`,
			`CREATE UNIQUE INDEX index_keypairs_on_uri ON keypairs (uri)`,
		}},
		{version: "20260325151755", phase: "expand", statements: []string{
			`CREATE UNIQUE INDEX index_collections_on_uri ON collections (uri) WHERE uri IS NOT NULL`,
			`CREATE UNIQUE INDEX index_collection_items_on_uri ON collection_items (uri) WHERE uri IS NOT NULL`,
		}},
		{version: "20260415133505", phase: "expand", statements: []string{`ALTER TABLE collections ADD COLUMN url character varying`}},
		{version: "20260420124030", phase: "expand", statements: []string{`ALTER TABLE user_roles ADD COLUMN collection_limit integer DEFAULT 10 NOT NULL`}},
		{version: "20260423141611", phase: "expand", statements: []string{`CREATE INDEX index_collection_items_on_state ON collection_items (state) WHERE state IN (2, 3)`}},
		{version: "20260425144553", phase: "expand", statements: []string{`ALTER TABLE notification_policies ADD COLUMN for_bots integer DEFAULT 0 NOT NULL`}},
	}
}

func mastodon46BackfillSteps() []upgradeStep {
	return []upgradeStep{
		{version: "20260209142402", phase: "backfill"},
		{version: "20260209143308", phase: "backfill"},
		{version: "20260318144837", phase: "backfill"},
	}
}

func mastodon46ValidateSteps() []upgradeStep {
	return []upgradeStep{{version: "20251201155054", phase: "validate", statements: []string{`ALTER TABLE custom_emoji_categories VALIDATE CONSTRAINT fk_rails_ad7840c8cf`}}}
}

func mastodon46ContractSteps() []upgradeStep {
	return []upgradeStep{
		{version: "20260326112324", phase: "contract", statements: []string{`DROP INDEX IF EXISTS index_collection_items_on_object_uri`}},
		{version: "20260410083500", phase: "contract", statements: []string{`DROP INDEX IF EXISTS index_collection_items_on_account_id`}},
		{version: "20260505155103", phase: "contract", statements: []string{`DROP INDEX IF EXISTS index_email_subscriptions_on_account_id`}},
		{version: "20260611150940", phase: "contract"},
	}
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
	if err := tx.Exec(`ALTER TABLE bulk_imports ADD COLUMN IF NOT EXISTS missing_status boolean DEFAULT false NOT NULL`).Error; err != nil {
		return fmt.Errorf("Mastodon 4.6 expand final additive catalog: %w", err)
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
		switch theme {
		case "default":
			settings["web.color_scheme"] = "dark"
			settings["web.contrast"] = "auto"
		case "contrast":
			settings["web.color_scheme"] = "dark"
			settings["web.contrast"] = "high"
		case "mastodon-light":
			settings["web.color_scheme"] = "light"
			settings["web.contrast"] = "auto"
		}
		settings["theme"] = "default"
		encoded, err := json.Marshal(settings)
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
	if err := tx.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS index_collection_items_on_account_id_and_collection_id ON collection_items (account_id, collection_id)`).Error; err != nil {
		return fmt.Errorf("Mastodon 4.6 create collection item account/collection index: %w", err)
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
