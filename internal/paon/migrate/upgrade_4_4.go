package migrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"

	paonotp "github.com/mstdn-plusminus-io/paon/internal/paon/otp"
	paonschema "github.com/mstdn-plusminus-io/paon/internal/paon/schema"
	"gorm.io/gorm"
)

const Mastodon4323SchemaVersion = paonschema.Mastodon4323Version
const Mastodon4422SchemaVersion = paonschema.Mastodon4422Version

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
		current, err := upgradeVersionApplied(tx, Mastodon4422SchemaVersion)
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
				return fmt.Errorf("reconcile canonical Mastodon 4.4 catalog before continuing: %w", err)
			}
			// The outer runner continues directly into the 4.5 phases. The full
			// current-schema guard is therefore intentionally deferred until the
			// 4.5 contract marker has been recorded.
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
	statements := []string{
		`CREATE TABLE IF NOT EXISTS fasp_follow_recommendations (id bigserial PRIMARY KEY, requesting_account_id bigint NOT NULL, recommended_account_id bigint NOT NULL, created_at timestamp(6) without time zone NOT NULL, updated_at timestamp(6) without time zone NOT NULL, CONSTRAINT fk_rails_71623d7e2c FOREIGN KEY (requesting_account_id) REFERENCES accounts(id), CONSTRAINT fk_rails_5c63a5fd1b FOREIGN KEY (recommended_account_id) REFERENCES accounts(id))`,
		`CREATE INDEX IF NOT EXISTS index_fasp_follow_recommendations_on_requesting_account_id ON fasp_follow_recommendations (requesting_account_id)`,
		`CREATE INDEX IF NOT EXISTS index_fasp_follow_recommendations_on_recommended_account_id ON fasp_follow_recommendations (recommended_account_id)`,
	}
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
	return []upgradeStep{
		{version: "20240918233930", phase: "expand", statements: []string{`ALTER TABLE statuses ADD COLUMN fetched_replies_at timestamp(6) without time zone`}},
		{version: "20241022214312", phase: "expand", statements: []string{`ALTER TABLE status_stats ADD COLUMN untrusted_favourites_count bigint`, `ALTER TABLE status_stats ADD COLUMN untrusted_reblogs_count bigint`}},
		{version: "20241104082851", phase: "expand", statements: []string{`CREATE TABLE annual_report_statuses_per_account_counts (id bigserial PRIMARY KEY, year integer NOT NULL, account_id bigint NOT NULL, statuses_count bigint NOT NULL)`, `CREATE UNIQUE INDEX idx_on_year_account_id_ff3e167cef ON annual_report_statuses_per_account_counts (year, account_id)`}},
		{version: "20241111141355", phase: "expand", statements: []string{`CREATE TABLE tag_trends (id bigserial PRIMARY KEY, tag_id bigint NOT NULL, score double precision DEFAULT 0.0 NOT NULL, rank integer DEFAULT 0 NOT NULL, allowed boolean DEFAULT false NOT NULL, language character varying DEFAULT '' NOT NULL, CONSTRAINT fk_rails_3033046460 FOREIGN KEY (tag_id) REFERENCES tags(id) ON DELETE CASCADE)`, `CREATE UNIQUE INDEX index_tag_trends_on_tag_id_and_language ON tag_trends (tag_id, language)`}},
		{version: "20241123224956", phase: "expand", statements: []string{`CREATE TABLE terms_of_services (id bigserial PRIMARY KEY, text text DEFAULT '' NOT NULL, changelog text DEFAULT '' NOT NULL, published_at timestamp(6) without time zone, notification_sent_at timestamp(6) without time zone, created_at timestamp(6) without time zone NOT NULL, updated_at timestamp(6) without time zone NOT NULL)`}},
		{version: "20241205103523", phase: "expand", statements: []string{`CREATE TABLE fasp_providers (id bigserial PRIMARY KEY, confirmed boolean DEFAULT false NOT NULL, name character varying NOT NULL, base_url character varying NOT NULL, sign_in_url character varying, remote_identifier character varying NOT NULL, provider_public_key_pem character varying NOT NULL, server_private_key_pem character varying NOT NULL, capabilities jsonb DEFAULT '[]'::jsonb NOT NULL, privacy_policy jsonb, contact_email character varying, fediverse_account character varying, created_at timestamp(6) without time zone NOT NULL, updated_at timestamp(6) without time zone NOT NULL)`, `CREATE UNIQUE INDEX index_fasp_providers_on_base_url ON fasp_providers (base_url)`}},
		{version: "20241206131513", phase: "expand", statements: []string{`CREATE TABLE fasp_debug_callbacks (id bigserial PRIMARY KEY, fasp_provider_id bigint NOT NULL, ip character varying NOT NULL, request_body text NOT NULL, created_at timestamp(6) without time zone NOT NULL, updated_at timestamp(6) without time zone NOT NULL, CONSTRAINT fk_rails_c1650087cd FOREIGN KEY (fasp_provider_id) REFERENCES fasp_providers(id))`, `CREATE INDEX index_fasp_debug_callbacks_on_fasp_provider_id ON fasp_debug_callbacks (fasp_provider_id)`}},
		{version: "20241213130230", phase: "expand", statements: []string{`CREATE TABLE fasp_subscriptions (id bigserial PRIMARY KEY, category character varying NOT NULL, subscription_type character varying NOT NULL, max_batch_size integer NOT NULL, threshold_timeframe integer, threshold_shares integer, threshold_likes integer, threshold_replies integer, fasp_provider_id bigint NOT NULL, created_at timestamp(6) without time zone NOT NULL, updated_at timestamp(6) without time zone NOT NULL, CONSTRAINT fk_rails_4c021f5938 FOREIGN KEY (fasp_provider_id) REFERENCES fasp_providers(id))`, `CREATE INDEX index_fasp_subscriptions_on_fasp_provider_id ON fasp_subscriptions (fasp_provider_id)`}},
		{version: "20241213170027", phase: "expand", statements: []string{`ALTER TABLE account_conversations ADD CONSTRAINT account_conversations_account_id_null CHECK (account_id IS NOT NULL) NOT VALID`}},
		{version: "20241213170043", phase: "expand", statements: []string{`ALTER TABLE account_conversations ADD CONSTRAINT account_conversations_conversation_id_null CHECK (conversation_id IS NOT NULL) NOT VALID`}},
		{version: "20241216223425", phase: "expand", statements: []string{`ALTER TABLE account_notes ADD CONSTRAINT account_notes_account_id_null CHECK (account_id IS NOT NULL) NOT VALID`}},
		{version: "20241216223446", phase: "expand", statements: []string{`ALTER TABLE account_notes ADD CONSTRAINT account_notes_target_account_id_null CHECK (target_account_id IS NOT NULL) NOT VALID`}},
		{version: "20241216223852", phase: "expand", statements: []string{`ALTER TABLE markers ADD CONSTRAINT markers_user_id_null CHECK (user_id IS NOT NULL) NOT VALID`}},
		{version: "20241216224211", phase: "expand", statements: []string{`ALTER TABLE poll_votes ADD CONSTRAINT poll_votes_account_id_null CHECK (account_id IS NOT NULL) NOT VALID`}},
		{version: "20241216224229", phase: "expand", statements: []string{`ALTER TABLE poll_votes ADD CONSTRAINT poll_votes_poll_id_null CHECK (poll_id IS NOT NULL) NOT VALID`}},
		{version: "20241216224507", phase: "expand", statements: []string{`ALTER TABLE polls ADD CONSTRAINT polls_account_id_null CHECK (account_id IS NOT NULL) NOT VALID`}},
		{version: "20241216224520", phase: "expand", statements: []string{`ALTER TABLE polls ADD CONSTRAINT polls_status_id_null CHECK (status_id IS NOT NULL) NOT VALID`}},
		{version: "20241216224813", phase: "expand", statements: []string{`ALTER TABLE tombstones ADD CONSTRAINT tombstones_account_id_null CHECK (account_id IS NOT NULL) NOT VALID`}},
		{version: "20250103131909", phase: "expand", statements: []string{`CREATE TABLE fasp_backfill_requests (id bigserial PRIMARY KEY, category character varying NOT NULL, max_count integer DEFAULT 100 NOT NULL, cursor character varying, fulfilled boolean DEFAULT false NOT NULL, fasp_provider_id bigint NOT NULL, created_at timestamp(6) without time zone NOT NULL, updated_at timestamp(6) without time zone NOT NULL, CONSTRAINT fk_rails_760d761775 FOREIGN KEY (fasp_provider_id) REFERENCES fasp_providers(id))`, `CREATE INDEX index_fasp_backfill_requests_on_fasp_provider_id ON fasp_backfill_requests (fasp_provider_id)`}},
		{version: "20250108111200", phase: "expand", statements: []string{`ALTER TABLE web_push_subscriptions ADD COLUMN standard boolean DEFAULT false NOT NULL`}},
		{version: "20250129144440", phase: "expand", statements: []string{`CREATE INDEX index_statuses_public_20250129 ON statuses (id DESC, language, account_id) WHERE deleted_at IS NULL AND visibility = 0 AND reblog_of_id IS NULL AND ((NOT reply) OR (in_reply_to_account_id = account_id))`}},
		{version: "20250221143646", phase: "expand", statements: []string{`ALTER TABLE announcements ADD COLUMN notification_sent_at timestamp(6) without time zone`}},
		{version: "20250224144617", phase: "expand", statements: []string{`ALTER TABLE terms_of_services ADD COLUMN effective_date date`}},
		{version: "20250305074104", phase: "expand", statements: []string{`CREATE UNIQUE INDEX index_terms_of_services_on_effective_date ON terms_of_services (effective_date) WHERE effective_date IS NOT NULL`}},
		{version: "20250313123400", phase: "expand", statements: []string{`ALTER TABLE users ADD COLUMN age_verified_at timestamp(6) without time zone`}},
		{version: "20250328153843", phase: "expand", statements: []string{`CREATE TABLE instance_moderation_notes (id bigserial PRIMARY KEY, domain character varying NOT NULL, account_id bigint NOT NULL, content text, created_at timestamp(6) without time zone NOT NULL, updated_at timestamp(6) without time zone NOT NULL, CONSTRAINT fk_rails_62f919e09b FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE)`, `CREATE INDEX index_instance_moderation_notes_on_domain ON instance_moderation_notes (domain)`}},
		{version: "20250411094808", phase: "expand", statements: []string{`CREATE TABLE quotes (id bigserial PRIMARY KEY, account_id bigint NOT NULL, status_id bigint NOT NULL, quoted_status_id bigint, quoted_account_id bigint, state integer DEFAULT 0 NOT NULL, approval_uri character varying, activity_uri character varying, created_at timestamp(6) without time zone NOT NULL, updated_at timestamp(6) without time zone NOT NULL, CONSTRAINT fk_rails_36d54169fc FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE, CONSTRAINT fk_rails_bd3ab4462c FOREIGN KEY (status_id) REFERENCES statuses(id) ON DELETE CASCADE, CONSTRAINT fk_rails_38068caa0e FOREIGN KEY (quoted_status_id) REFERENCES statuses(id) ON DELETE SET NULL, CONSTRAINT fk_rails_bfc5276b70 FOREIGN KEY (quoted_account_id) REFERENCES accounts(id) ON DELETE SET NULL)`, `CREATE UNIQUE INDEX index_quotes_on_status_id ON quotes (status_id)`, `CREATE INDEX index_quotes_on_quoted_status_id ON quotes (quoted_status_id)`, `CREATE INDEX index_quotes_on_quoted_account_id ON quotes (quoted_account_id)`, `CREATE INDEX index_quotes_on_account_id_and_quoted_account_id ON quotes (account_id, quoted_account_id)`, `CREATE INDEX index_quotes_on_approval_uri ON quotes (approval_uri) WHERE approval_uri IS NOT NULL`, `CREATE UNIQUE INDEX index_quotes_on_activity_uri ON quotes (activity_uri) WHERE activity_uri IS NOT NULL`}},
		{version: "20250411095859", phase: "expand", statements: []string{`ALTER TABLE status_edits ADD COLUMN quote_id bigint`}},
		{version: "20250422083912", phase: "expand", statements: []string{`ALTER TABLE web_push_subscriptions ADD CONSTRAINT web_push_subscriptions_user_id_null CHECK (user_id IS NOT NULL) NOT VALID`}},
		{version: "20250422085027", phase: "expand", statements: []string{`ALTER TABLE web_push_subscriptions ADD CONSTRAINT web_push_subscriptions_access_token_id_null CHECK (access_token_id IS NOT NULL) NOT VALID`}},
		{version: "20250425134308", phase: "expand", statements: []string{`ALTER TABLE quotes ALTER COLUMN id SET DEFAULT timestamp_id('quotes')`}},
		{version: "20250428095029", phase: "expand", statements: []string{`ALTER TABLE statuses ADD COLUMN quote_approval_policy integer DEFAULT 0 NOT NULL`}},
		{version: "20250428104538", phase: "expand", statements: []string{`ALTER TABLE users ADD COLUMN require_tos_interstitial boolean DEFAULT false NOT NULL`}},
		{version: "20250520204643", phase: "expand", statements: []string{`CREATE TABLE rule_translations (id bigserial PRIMARY KEY, text text DEFAULT '' NOT NULL, hint text DEFAULT '' NOT NULL, language character varying NOT NULL, rule_id bigint NOT NULL, created_at timestamp(6) without time zone NOT NULL, updated_at timestamp(6) without time zone NOT NULL, CONSTRAINT fk_rails_d5fd439dde FOREIGN KEY (rule_id) REFERENCES rules(id) ON DELETE CASCADE)`, `CREATE UNIQUE INDEX index_rule_translations_on_rule_id_and_language ON rule_translations (rule_id, language)`}},
		{version: "20250605110215", phase: "expand", statements: []string{`ALTER TABLE quotes ADD COLUMN legacy boolean DEFAULT false NOT NULL`}},
	}
}

func mastodon44BackfillSteps() []upgradeStep {
	return []upgradeStep{
		{version: "20241123160722", phase: "backfill"},
		{version: "20241205135901", phase: "backfill", statements: []string{`DELETE FROM settings WHERE (thing_type IS NOT NULL AND thing_id IS NOT NULL) OR var IN ('notification_emails', 'interactions', 'boost_modal', 'auto_play_gif', 'delete_modal', 'system_font_ui', 'default_sensitive', 'unfollow_modal', 'reduce_motion', 'display_sensitive_media', 'hide_network', 'expand_spoilers', 'display_media', 'aggregate_reblogs', 'show_application', 'advanced_layout', 'use_blurhash', 'use_pending_items')`}},
	}
}

func mastodon44ValidateSteps() []upgradeStep {
	return []upgradeStep{
		{version: "20241210140838", phase: "validate", statements: []string{`DELETE FROM account_pins WHERE account_id IS NULL OR target_account_id IS NULL`, `ALTER TABLE account_pins ALTER COLUMN account_id SET NOT NULL, ALTER COLUMN target_account_id SET NOT NULL`}},
		{version: "20241212152158", phase: "validate", statements: []string{`DELETE FROM account_aliases WHERE account_id IS NULL`, `ALTER TABLE account_aliases ALTER COLUMN account_id SET NOT NULL`}},
		{version: "20241212152618", phase: "validate", statements: []string{`DELETE FROM account_deletion_requests WHERE account_id IS NULL`, `ALTER TABLE account_deletion_requests ALTER COLUMN account_id SET NOT NULL`}},
		{version: "20241212152734", phase: "validate", statements: []string{`DELETE FROM account_domain_blocks WHERE account_id IS NULL OR domain IS NULL`, `ALTER TABLE account_domain_blocks ALTER COLUMN account_id SET NOT NULL, ALTER COLUMN domain SET NOT NULL`}},
		{version: "20241212152910", phase: "validate", statements: []string{`DELETE FROM admin_action_logs WHERE account_id IS NULL`, `ALTER TABLE admin_action_logs ALTER COLUMN account_id SET NOT NULL`}},
		{version: "20241212153054", phase: "validate", statements: []string{`DELETE FROM announcement_mutes WHERE account_id IS NULL OR announcement_id IS NULL`, `ALTER TABLE announcement_mutes ALTER COLUMN account_id SET NOT NULL, ALTER COLUMN announcement_id SET NOT NULL`}},
		{version: "20241212153202", phase: "validate", statements: []string{`DELETE FROM announcement_reactions WHERE account_id IS NULL OR announcement_id IS NULL`, `ALTER TABLE announcement_reactions ALTER COLUMN account_id SET NOT NULL, ALTER COLUMN announcement_id SET NOT NULL`}},
		{version: "20241212153254", phase: "validate", statements: []string{`DELETE FROM custom_filters WHERE account_id IS NULL`, `ALTER TABLE custom_filters ALTER COLUMN account_id SET NOT NULL`}},
		{version: "20241212154231", phase: "validate", statements: []string{`DELETE FROM scheduled_statuses WHERE account_id IS NULL`, `ALTER TABLE scheduled_statuses ALTER COLUMN account_id SET NOT NULL`}},
		{version: "20241212154346", phase: "validate", statements: []string{`DELETE FROM user_invite_requests WHERE user_id IS NULL`, `ALTER TABLE user_invite_requests ALTER COLUMN user_id SET NOT NULL`}},
		{version: "20241213170036", phase: "validate", statements: []string{`DELETE FROM account_conversations WHERE account_id IS NULL`, `ALTER TABLE account_conversations VALIDATE CONSTRAINT account_conversations_account_id_null`, `ALTER TABLE account_conversations ALTER COLUMN account_id SET NOT NULL`, `ALTER TABLE account_conversations DROP CONSTRAINT account_conversations_account_id_null`}},
		{version: "20241213170053", phase: "validate", statements: []string{`DELETE FROM account_conversations WHERE conversation_id IS NULL`, `ALTER TABLE account_conversations VALIDATE CONSTRAINT account_conversations_conversation_id_null`, `ALTER TABLE account_conversations ALTER COLUMN conversation_id SET NOT NULL`, `ALTER TABLE account_conversations DROP CONSTRAINT account_conversations_conversation_id_null`}},
		{version: "20241216223433", phase: "validate", statements: []string{`DELETE FROM account_notes WHERE account_id IS NULL`, `ALTER TABLE account_notes VALIDATE CONSTRAINT account_notes_account_id_null`, `ALTER TABLE account_notes ALTER COLUMN account_id SET NOT NULL`, `ALTER TABLE account_notes DROP CONSTRAINT account_notes_account_id_null`}},
		{version: "20241216223452", phase: "validate", statements: []string{`DELETE FROM account_notes WHERE target_account_id IS NULL`, `ALTER TABLE account_notes VALIDATE CONSTRAINT account_notes_target_account_id_null`, `ALTER TABLE account_notes ALTER COLUMN target_account_id SET NOT NULL`, `ALTER TABLE account_notes DROP CONSTRAINT account_notes_target_account_id_null`}},
		{version: "20241216223859", phase: "validate", statements: []string{`DELETE FROM markers WHERE user_id IS NULL`, `ALTER TABLE markers VALIDATE CONSTRAINT markers_user_id_null`, `ALTER TABLE markers ALTER COLUMN user_id SET NOT NULL`, `ALTER TABLE markers DROP CONSTRAINT markers_user_id_null`}},
		{version: "20241216224218", phase: "validate", statements: []string{`DELETE FROM poll_votes WHERE account_id IS NULL`, `ALTER TABLE poll_votes VALIDATE CONSTRAINT poll_votes_account_id_null`, `ALTER TABLE poll_votes ALTER COLUMN account_id SET NOT NULL`, `ALTER TABLE poll_votes DROP CONSTRAINT poll_votes_account_id_null`}},
		{version: "20241216224237", phase: "validate", statements: []string{`DELETE FROM poll_votes WHERE poll_id IS NULL`, `ALTER TABLE poll_votes VALIDATE CONSTRAINT poll_votes_poll_id_null`, `ALTER TABLE poll_votes ALTER COLUMN poll_id SET NOT NULL`, `ALTER TABLE poll_votes DROP CONSTRAINT poll_votes_poll_id_null`}},
		{version: "20241216224514", phase: "validate", statements: []string{`DELETE FROM polls WHERE account_id IS NULL`, `ALTER TABLE polls VALIDATE CONSTRAINT polls_account_id_null`, `ALTER TABLE polls ALTER COLUMN account_id SET NOT NULL`, `ALTER TABLE polls DROP CONSTRAINT polls_account_id_null`}},
		{version: "20241216224530", phase: "validate", statements: []string{`DELETE FROM polls WHERE status_id IS NULL`, `ALTER TABLE polls VALIDATE CONSTRAINT polls_status_id_null`, `ALTER TABLE polls ALTER COLUMN status_id SET NOT NULL`, `ALTER TABLE polls DROP CONSTRAINT polls_status_id_null`}},
		{version: "20241216224825", phase: "validate", statements: []string{`DELETE FROM tombstones WHERE account_id IS NULL`, `ALTER TABLE tombstones VALIDATE CONSTRAINT tombstones_account_id_null`, `ALTER TABLE tombstones ALTER COLUMN account_id SET NOT NULL`, `ALTER TABLE tombstones DROP CONSTRAINT tombstones_account_id_null`}},
		{version: "20250422084214", phase: "validate", statements: []string{`DELETE FROM web_push_subscriptions WHERE user_id IS NULL`, `ALTER TABLE web_push_subscriptions VALIDATE CONSTRAINT web_push_subscriptions_user_id_null`, `ALTER TABLE web_push_subscriptions ALTER COLUMN user_id SET NOT NULL`, `ALTER TABLE web_push_subscriptions DROP CONSTRAINT web_push_subscriptions_user_id_null`}},
		{version: "20250422085303", phase: "validate", statements: []string{`DELETE FROM web_push_subscriptions WHERE access_token_id IS NULL`, `ALTER TABLE web_push_subscriptions VALIDATE CONSTRAINT web_push_subscriptions_access_token_id_null`, `ALTER TABLE web_push_subscriptions ALTER COLUMN access_token_id SET NOT NULL`, `ALTER TABLE web_push_subscriptions DROP CONSTRAINT web_push_subscriptions_access_token_id_null`}},
	}
}

func mastodon44ContractSteps() []upgradeStep {
	return []upgradeStep{
		{version: "20241014010506", phase: "contract", statements: []string{`DROP INDEX IF EXISTS index_account_aliases_on_account_id`, `DROP INDEX IF EXISTS index_account_relationship_severance_events_on_account_id`, `DROP INDEX IF EXISTS index_custom_filter_statuses_on_status_id`, `DROP INDEX IF EXISTS index_webauthn_credentials_on_user_id`}},
		{version: "20241205135925", phase: "contract", statements: []string{`DELETE FROM settings WHERE (thing_type IS NOT NULL AND thing_id IS NOT NULL) OR var IN ('notification_emails', 'interactions', 'boost_modal', 'auto_play_gif', 'delete_modal', 'system_font_ui', 'default_sensitive', 'unfollow_modal', 'reduce_motion', 'display_sensitive_media', 'hide_network', 'expand_spoilers', 'display_media', 'aggregate_reblogs', 'show_application', 'advanced_layout', 'use_blurhash', 'use_pending_items')`, `DROP INDEX IF EXISTS index_settings_on_thing_type_and_thing_id_and_var`, `ALTER TABLE settings DROP COLUMN thing_type, DROP COLUMN thing_id`, `CREATE UNIQUE INDEX index_settings_on_var ON settings (var)`}},
		{version: "20241205162640", phase: "contract", statements: []string{`ALTER TABLE webauthn_credentials DROP CONSTRAINT IF EXISTS fk_rails_a4355aef77, DROP CONSTRAINT IF EXISTS webauthn_credentials_user_id_fkey`, `ALTER TABLE webauthn_credentials ADD CONSTRAINT fk_rails_a4355aef77 FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE`}},
		{version: "20241205163118", phase: "contract", statements: []string{`ALTER TABLE account_moderation_notes DROP CONSTRAINT IF EXISTS fk_rails_3f8b75089b, DROP CONSTRAINT IF EXISTS account_moderation_notes_account_id_fkey`, `ALTER TABLE account_moderation_notes ADD CONSTRAINT fk_rails_3f8b75089b FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE`, `ALTER TABLE account_moderation_notes DROP CONSTRAINT IF EXISTS fk_rails_dd62ed5ac3, DROP CONSTRAINT IF EXISTS account_moderation_notes_target_account_id_fkey`, `ALTER TABLE account_moderation_notes ADD CONSTRAINT fk_rails_dd62ed5ac3 FOREIGN KEY (target_account_id) REFERENCES accounts(id) ON DELETE CASCADE`}},
		{version: "20250129144813", phase: "contract", statements: []string{`DROP INDEX index_statuses_public_20200119`}},
		{version: "20250410144908", phase: "contract", statements: []string{`DROP TABLE imports`}},
		{version: "20250520192024", phase: "contract", statements: []string{`ALTER TABLE users DROP COLUMN encrypted_otp_secret, DROP COLUMN encrypted_otp_secret_iv, DROP COLUMN encrypted_otp_secret_salt`}},
		{version: "20250627132728", phase: "contract", statements: nil},
	}
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
