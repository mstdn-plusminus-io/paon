package db

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestOpenUsesConfiguredDatabasePool(t *testing.T) {
	src, err := os.ReadFile("db.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`postgres.New(postgres.Config{`,
		`primaryDSN := databaseDSNWithLockTimeout(cfg.DatabaseURL, cfg.DatabaseLockTimeout)`,
		`DSN:                  primaryDSN`,
		`PreferSimpleProtocol: !cfg.DatabasePreparedStatements`,
		`if strings.TrimSpace(cfg.ReplicaDatabaseURL) != ""`,
		`replicaDSN := databaseDSNWithLockTimeout(cfg.ReplicaDatabaseURL, cfg.DatabaseLockTimeout)`,
		`dbresolver.Register(dbresolver.Config{`,
		`Replicas: []gorm.Dialector{postgres.New(postgres.Config{`,
		`PreferSimpleProtocol: !cfg.ReplicaPreparedStatements`,
		`sqlDB.SetMaxOpenConns(cfg.DatabaseMaxOpenConns)`,
		`sqlDB.SetMaxIdleConns(cfg.DatabaseMaxIdleConns)`,
		`newGORMLogger(os.Stdout, gormLoggerLevel(cfg.RailsLogLevel))`,
		`func OpenPgHeroStats(cfg config.Config) (*gorm.DB, error)`,
		`cfg.PgHeroStatsDatabaseURL`,
		`func OpenPgHeroOther(cfg config.Config) (*gorm.DB, error)`,
		`cfg.PgHeroOtherDatabaseURL`,
	} {
		if !strings.Contains(string(src), want) {
			t.Fatalf("db.go missing configured pool use %q", want)
		}
	}
}

func TestDatabaseDSNWithLockTimeout(t *testing.T) {
	t.Run("adds timeout while preserving query", func(t *testing.T) {
		got := databaseDSNWithLockTimeout(
			"postgres://user:pass@db.example/paon?sslmode=require",
			5*time.Second,
		)
		if !strings.Contains(got, "lock_timeout=5000") {
			t.Fatalf("DSN = %q, want lock_timeout=5000", got)
		}
		if !strings.Contains(got, "sslmode=require") {
			t.Fatalf("DSN = %q, want sslmode=require", got)
		}
	})

	t.Run("keeps explicit timeout", func(t *testing.T) {
		const raw = "postgres://db.example/paon?lock_timeout=12000&sslmode=require"
		if got := databaseDSNWithLockTimeout(raw, 5*time.Second); got != raw {
			t.Fatalf("DSN = %q, want explicit DSN %q", got, raw)
		}
	})

	t.Run("zero disables default", func(t *testing.T) {
		const raw = "postgres://db.example/paon"
		if got := databaseDSNWithLockTimeout(raw, 0); got != raw {
			t.Fatalf("DSN = %q, want %q", got, raw)
		}
	})

	t.Run("rounds sub-millisecond timeout up", func(t *testing.T) {
		got := databaseDSNWithLockTimeout("postgres://db.example/paon", time.Microsecond)
		if !strings.Contains(got, "lock_timeout=1") {
			t.Fatalf("DSN = %q, want lock_timeout=1", got)
		}
	})
}

func TestGORMLoggerSuppressesExpectedRecordNotFound(t *testing.T) {
	var output bytes.Buffer
	databaseLogger := newGORMLogger(&output, logger.Warn)
	databaseLogger.Trace(context.Background(), time.Now(), func() (string, int64) {
		return `SELECT * FROM settings WHERE var = $1`, 0
	}, gorm.ErrRecordNotFound)
	if output.Len() != 0 {
		t.Fatalf("record-not-found log = %q", output.String())
	}

	databaseLogger.Trace(context.Background(), time.Now(), func() (string, int64) {
		return `SELECT * FROM settings WHERE var = $1`, 0
	}, errors.New("database unavailable"))
	if !strings.Contains(output.String(), "database unavailable") {
		t.Fatalf("database error was not logged: %q", output.String())
	}
}

func TestGormLoggerLevelMatchesRailsLogLevelShape(t *testing.T) {
	src, err := os.ReadFile("db.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`case "debug":`,
		`return logger.Info`,
		`case "info", "warn":`,
		`return logger.Warn`,
		`case "error", "fatal":`,
		`return logger.Error`,
		`case "unknown":`,
		`return logger.Silent`,
	} {
		if !strings.Contains(string(src), want) {
			t.Fatalf("db.go missing Rails log level mapping %q", want)
		}
	}
}

func TestProductionCodeNeverMutatesMastodonSchema(t *testing.T) {
	forbidden := []string{
		".AutoMigrate(",
		".CreateTable(",
		".DropTable(",
		".RenameTable(",
		".AddColumn(",
		".AlterColumn(",
		".DropColumn(",
		".RenameColumn(",
		".CreateIndex(",
		".DropIndex(",
		".RenameIndex(",
	}
	for _, root := range rawSQLReferenceRoots() {
		err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			src := string(raw)
			for _, fragment := range forbidden {
				if strings.Contains(src, fragment) {
					t.Fatalf("%s uses schema mutation API %q; paon-go must treat the existing Mastodon DB schema as authoritative", path, fragment)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestAvailableExplainsSupportedDatabaseConfiguration(t *testing.T) {
	err := Available(nil)
	if err == nil {
		t.Fatal("Available(nil) error = nil")
	}
	if !strings.Contains(err.Error(), "DATABASE_URL") || !strings.Contains(err.Error(), "DB_NAME") {
		t.Fatalf("Available(nil) error = %q", err)
	}
	if errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Available(nil) should return a configuration error, got %v", err)
	}
}

func TestValidatePostgreSQLVersionNumRequiresMastodon45Floor(t *testing.T) {
	for _, version := range []int{140000, 140021, 160003} {
		if err := validatePostgreSQLVersionNum(version); err != nil {
			t.Errorf("validatePostgreSQLVersionNum(%d) = %v", version, err)
		}
	}
	for _, version := range []int{0, 90624, 130021, 139999} {
		err := validatePostgreSQLVersionNum(version)
		if err == nil || !strings.Contains(err.Error(), "PostgreSQL 14.0 or newer") {
			t.Errorf("validatePostgreSQLVersionNum(%d) error = %v, want minimum version error", version, err)
		}
	}
}

func TestRequireSupportedVersionExplainsMissingDatabaseConfiguration(t *testing.T) {
	err := RequireSupportedVersion(nil)
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("RequireSupportedVersion(nil) error = %v", err)
	}
}

func TestRequiredMastodonTablesCoverDropInSchemaCore(t *testing.T) {
	tables := map[string]bool{}
	for _, table := range RequiredMastodonTables() {
		tables[table] = true
	}
	for _, want := range []string{
		"accounts",
		"account_aliases",
		"account_deletion_requests",
		"account_migrations",
		"account_moderation_notes",
		"account_notes",
		"account_stats",
		"account_pins",
		"account_statuses_cleanup_policies",
		"account_warning_presets",
		"account_warnings",
		"accounts_tags",
		"users",
		"user_roles",
		"user_invite_requests",
		"statuses",
		"status_stats",
		"status_edits",
		"status_pins",
		"status_trends",
		"tombstones",
		"mentions",
		"media_attachments",
		"follows",
		"follow_recommendation_suppressions",
		"follow_requests",
		"blocks",
		"mutes",
		"favourites",
		"bookmarks",
		"lists",
		"list_accounts",
		"conversations",
		"account_conversations",
		"tags",
		"statuses_tags",
		"polls",
		"preview_cards",
		"preview_card_trends",
		"preview_cards_statuses",
		"scheduled_statuses",
		"appeals",
		"custom_emojis",
		"custom_filters",
		"domain_allows",
		"domain_blocks",
		"unavailable_domains",
		"reports",
		"session_activations",
		"web_settings",
		"web_push_subscriptions",
		"webauthn_credentials",
		"backups",
		"bulk_imports",
		"bulk_import_rows",
		"identities",
		"instances",
		"account_summaries",
		"global_follow_recommendations",
		"login_activities",
		"user_ips",
		"relays",
		"webhooks",
		"site_uploads",
		"software_updates",
		"admin_action_logs",
		"notifications",
		"oauth_applications",
		"oauth_access_tokens",
		"oauth_access_grants",
		"settings",
		"quotes",
		"terms_of_services",
		"tag_trends",
		"rule_translations",
		"fasp_providers",
		"fasp_follow_recommendations",
		"instance_moderation_notes",
	} {
		if !tables[want] {
			t.Fatalf("RequiredMastodonTables missing %q: %#v", want, RequiredMastodonTables())
		}
	}
}

func TestRequiredMastodonTablesCoverConcreteGoModels(t *testing.T) {
	raw, err := os.ReadFile("../models/models.go")
	if err != nil {
		t.Fatal(err)
	}
	required := map[string]bool{}
	for _, table := range RequiredMastodonTables() {
		required[table] = true
	}
	excluded := map[string]bool{
		// pghero_space_stats is an optional PgHero-owned table rather than a Mastodon core table.
		"pghero_space_stats": true,
		// Mastodon 4.3 removed the former end-to-end message tables. Legacy
		// compile-only models are not part of the target database contract.
		"devices":            true,
		"encrypted_messages": true,
		"one_time_keys":      true,
		"system_keys":        true,
		// Mastodon 4.4 removes imports; the model only supports the one-way
		// legacy importer before the acknowledged contract phase.
		"imports": true,
	}
	for _, match := range modelTableNamePattern.FindAllStringSubmatch(string(raw), -1) {
		table := match[1]
		if excluded[table] {
			continue
		}
		if !required[table] {
			t.Fatalf("RequiredMastodonTables missing concrete Go model table %q", table)
		}
	}
}

func TestRawSQLSchemaReferenceScanCoversProductionGoTree(t *testing.T) {
	roots := rawSQLReferenceRoots()
	want := []string{"..", "../../../cmd"}
	if len(roots) != len(want) {
		t.Fatalf("rawSQLReferenceRoots = %#v, want %#v", roots, want)
	}
	for i := range want {
		if roots[i] != want[i] {
			t.Fatalf("rawSQLReferenceRoots = %#v, want %#v", roots, want)
		}
	}
}

func TestModelBackedMastodonColumnsCoverConcreteGoModels(t *testing.T) {
	raw, err := os.ReadFile("../models/models.go")
	if err != nil {
		t.Fatal(err)
	}
	covered := map[string]bool{}
	for table := range modelBackedMastodonColumns() {
		covered[table] = true
	}
	excluded := map[string]bool{
		// pghero_space_stats is an optional PgHero-owned table rather than a Mastodon core table.
		"pghero_space_stats": true,
		"devices":            true,
		"encrypted_messages": true,
		"one_time_keys":      true,
		"system_keys":        true,
		"imports":            true,
	}
	for _, match := range modelTableNamePattern.FindAllStringSubmatch(string(raw), -1) {
		table := match[1]
		if excluded[table] {
			continue
		}
		if !covered[table] {
			t.Fatalf("modelBackedMastodonColumns missing concrete Go model table %q", table)
		}
	}
}

func TestRequiredMastodonColumnsCoverConcreteGoModelColumns(t *testing.T) {
	required := RequiredMastodonColumns()
	for table, columns := range modelBackedMastodonColumns() {
		got := map[string]bool{}
		for _, column := range required[table] {
			got[column] = true
		}
		for _, column := range columns {
			if !got[column] {
				t.Fatalf("RequiredMastodonColumns[%s] missing Go model column %q", table, column)
			}
		}
	}
}

func TestRequiredMastodonColumnsCoverDropInSchemaCore(t *testing.T) {
	columns := RequiredMastodonColumns()
	checks := map[string][]string{
		"accounts":        {"id", "username", "domain", "private_key", "public_key"},
		"account_aliases": {"id", "account_id", "acct", "uri"},
		"account_deletion_requests": {
			"id",
			"account_id",
			"created_at",
			"updated_at",
		},
		"account_migrations":       {"id", "account_id", "acct", "followers_count", "target_account_id"},
		"account_moderation_notes": {"id", "account_id", "target_account_id", "content"},
		"account_notes":            {"id", "account_id", "target_account_id", "comment"},
		"users":                    {"id", "account_id", "email", "encrypted_password", "confirmed_at", "approved"},
		"user_invite_requests":     {"id", "user_id", "text"},
		"statuses":                 {"id", "account_id", "text", "visibility", "deleted_at"},
		"status_edits":             {"id", "status_id", "text", "ordered_media_attachment_ids", "poll_options"},
		"status_pins":              {"id", "account_id", "status_id"},
		"status_trends":            {"id", "status_id", "account_id", "score", "allowed"},
		"tombstones":               {"id", "account_id", "uri", "by_moderator"},
		"media_attachments":        {"id", "account_id", "status_id", "file_file_name", "file_meta"},
		"favourites":               {"id", "account_id", "status_id", "created_at"},
		"bookmarks":                {"id", "account_id", "status_id", "created_at"},
		"account_pins":             {"id", "account_id", "target_account_id"},
		"account_statuses_cleanup_policies": {
			"id",
			"account_id",
			"enabled",
			"min_status_age",
			"keep_direct",
			"keep_pinned",
		},
		"account_warning_presets":            {"id", "text", "title"},
		"account_warnings":                   {"id", "account_id", "target_account_id", "action", "text"},
		"accounts_tags":                      {"account_id", "tag_id"},
		"follow_recommendation_suppressions": {"id", "account_id"},
		"lists":                              {"id", "account_id", "title", "replies_policy", "exclusive"},
		"custom_filters":                     {"id", "account_id", "phrase", "context", "action"},
		"custom_emojis":                      {"id", "shortcode", "domain", "image_file_name", "visible_in_picker"},
		"tags":                               {"id", "name", "reviewed_at", "last_status_at"},
		"polls":                              {"id", "account_id", "status_id", "options", "cached_tallies", "votes_count"},
		"preview_cards":                      {"id", "url", "title", "description", "type", "html"},
		"preview_card_trends": {
			"id",
			"preview_card_id",
			"score",
			"allowed",
		},
		"preview_cards_statuses": {"status_id", "preview_card_id"},
		"appeals":                {"id", "account_id", "account_warning_id", "text"},
		"domain_allows":          {"id", "domain", "created_at", "updated_at"},
		"unavailable_domains":    {"id", "domain", "created_at", "updated_at"},
		"backups":                {"id", "user_id", "dump_file_name", "dump_file_size", "processed"},
		"bulk_imports":           {"id", "account_id", "type", "state", "original_filename"},
		"bulk_import_rows":       {"id", "bulk_import_id", "data"},
		"identities":             {"id", "user_id", "provider", "uid"},
		"instances":              {"domain", "accounts_count"},
		"account_summaries":      {"account_id", "language", "sensitive"},
		"global_follow_recommendations": {
			"account_id",
			"rank",
			"reason",
		},
		"login_activities":     {"id", "user_id", "authentication_method", "success", "ip", "created_at"},
		"user_ips":             {"user_id", "ip", "used_at"},
		"relays":               {"id", "inbox_url", "state"},
		"webhooks":             {"id", "url", "events", "secret", "enabled"},
		"site_uploads":         {"id", "var", "file_file_name", "file_content_type", "meta", "blurhash"},
		"software_updates":     {"id", "version", "urgent", "type", "release_notes"},
		"admin_action_logs":    {"id", "account_id", "action", "target_type", "target_id", "permalink"},
		"webauthn_credentials": {"id", "user_id", "external_id", "public_key", "nickname", "sign_count"},
		"web_settings":         {"id", "user_id", "data"},
		"web_push_subscriptions": {
			"id",
			"endpoint",
			"key_p256dh",
			"key_auth",
			"data",
			"access_token_id",
			"user_id",
		},
		"session_activations": {"id", "user_id", "session_id", "access_token_id", "web_push_subscription_id", "ip", "user_agent"},
		"oauth_access_tokens": {"id", "token", "resource_owner_id", "application_id", "scopes", "revoked_at"},
		"oauth_access_grants": {"id", "token", "resource_owner_id", "application_id", "redirect_uri", "scopes"},
		"settings":            {"id", "var", "value"},
		"quotes":              {"id", "account_id", "status_id", "quoted_status_id", "quoted_account_id", "state", "legacy"},
		"terms_of_services":   {"id", "text", "changelog", "effective_date"},
	}
	for table, wantColumns := range checks {
		got := map[string]bool{}
		for _, column := range columns[table] {
			got[column] = true
		}
		for _, column := range wantColumns {
			if !got[column] {
				t.Fatalf("RequiredMastodonColumns[%s] missing %q: %#v", table, column, columns[table])
			}
		}
	}
}

func TestRequiredMastodonIndexesCoverDropInSchemaCore(t *testing.T) {
	indexes := RequiredMastodonIndexes()
	checks := map[string][]string{
		"accounts": {
			"search_index",
			"index_accounts_on_domain_and_id",
			"index_accounts_on_moved_to_account_id",
			"index_accounts_on_username_and_domain_lower",
			"index_accounts_on_uri",
			"index_accounts_on_url",
		},
		"accounts_tags":             {"accounts_tags_pkey", "index_accounts_tags_on_account_id_and_tag_id"},
		"account_aliases":           {"index_account_aliases_on_account_id_and_uri"},
		"account_conversations":     {"index_unique_conversations", "index_account_conversations_on_conversation_id"},
		"account_deletion_requests": {"index_account_deletion_requests_on_account_id"},
		"account_migrations":        {"index_account_migrations_on_account_id", "index_account_migrations_on_target_account_id"},
		"account_moderation_notes":  {"index_account_moderation_notes_on_account_id", "index_account_moderation_notes_on_target_account_id"},
		"account_notes":             {"index_account_notes_on_account_id_and_target_account_id", "index_account_notes_on_target_account_id"},
		"account_pins":              {"index_account_pins_on_account_id_and_target_account_id", "index_account_pins_on_target_account_id"},
		"account_statuses_cleanup_policies": {
			"index_account_statuses_cleanup_policies_on_account_id",
		},
		"account_stats":    {"index_account_stats_on_account_id", "index_account_stats_on_last_status_at_and_account_id"},
		"account_warnings": {"index_account_warnings_on_account_id", "index_account_warnings_on_target_account_id"},
		"announcement_mutes": {
			"index_announcement_mutes_on_account_id_and_announcement_id",
			"index_announcement_mutes_on_announcement_id",
		},
		"announcement_reactions": {
			"index_announcement_reactions_on_account_id_and_announcement_id",
			"index_announcement_reactions_on_announcement_id",
			"index_announcement_reactions_on_custom_emoji_id",
		},
		"admin_action_logs":                  {"index_admin_action_logs_on_account_id", "index_admin_action_logs_on_target_type_and_target_id"},
		"appeals":                            {"index_appeals_on_account_id", "index_appeals_on_account_warning_id", "index_appeals_on_approved_by_account_id", "index_appeals_on_rejected_by_account_id"},
		"bookmarks":                          {"index_bookmarks_on_account_id_and_status_id", "index_bookmarks_on_status_id"},
		"blocks":                             {"index_blocks_on_account_id_and_target_account_id", "index_blocks_on_target_account_id"},
		"bulk_import_rows":                   {"index_bulk_import_rows_on_bulk_import_id"},
		"bulk_imports":                       {"index_bulk_imports_on_account_id", "index_bulk_imports_unconfirmed"},
		"canonical_email_blocks":             {"index_canonical_email_blocks_on_canonical_email_hash", "index_canonical_email_blocks_on_reference_account_id"},
		"conversation_mutes":                 {"index_conversation_mutes_on_account_id_and_conversation_id"},
		"conversations":                      {"index_conversations_on_uri"},
		"custom_emoji_categories":            {"index_custom_emoji_categories_on_name"},
		"custom_emojis":                      {"index_custom_emojis_on_shortcode_and_domain"},
		"custom_filter_keywords":             {"index_custom_filter_keywords_on_custom_filter_id"},
		"custom_filter_statuses":             {"index_custom_filter_statuses_on_custom_filter_id", "index_custom_filter_statuses_on_status_id_and_custom_filter_id"},
		"custom_filters":                     {"index_custom_filters_on_account_id"},
		"domain_allows":                      {"index_domain_allows_on_domain"},
		"domain_blocks":                      {"index_domain_blocks_on_domain"},
		"email_domain_blocks":                {"index_email_domain_blocks_on_domain"},
		"favourites":                         {"index_favourites_on_account_id_and_id", "index_favourites_on_account_id_and_status_id", "index_favourites_on_status_id"},
		"featured_tags":                      {"index_featured_tags_on_account_id_and_tag_id"},
		"follow_recommendation_suppressions": {"index_follow_recommendation_suppressions_on_account_id"},
		"follow_requests":                    {"index_follow_requests_on_account_id_and_target_account_id"},
		"follows":                            {"index_follows_on_account_id_and_target_account_id", "index_follows_on_target_account_id_and_account_id"},
		"backups":                            {"index_backups_on_user_id"},
		"instances":                          {"index_instances_on_domain", "index_instances_on_reverse_domain"},
		"invites":                            {"index_invites_on_code", "index_invites_on_user_id"},
		"ip_blocks":                          {"index_ip_blocks_on_ip"},
		"list_accounts":                      {"index_list_accounts_on_account_id_and_list_id", "index_list_accounts_on_follow_id", "index_list_accounts_on_follow_request_id", "index_list_accounts_on_list_id_and_account_id"},
		"lists":                              {"index_lists_on_account_id"},
		"account_summaries":                  {"index_account_summaries_on_account_id"},
		"global_follow_recommendations": {
			"index_global_follow_recommendations_on_account_id",
		},
		"identities":          {"index_identities_on_user_id"},
		"login_activities":    {"index_login_activities_on_user_id"},
		"markers":             {"index_markers_on_user_id_and_timeline"},
		"media_attachments":   {"index_media_attachments_on_account_id_and_status_id", "index_media_attachments_on_scheduled_status_id", "index_media_attachments_on_shortcode", "index_media_attachments_on_status_id"},
		"mentions":            {"index_mentions_on_account_id_and_status_id", "index_mentions_on_status_id"},
		"mutes":               {"index_mutes_on_account_id_and_target_account_id", "index_mutes_on_target_account_id"},
		"notifications":       {"index_notifications_on_account_id_and_id_and_type", "index_notifications_on_activity_id_and_activity_type", "index_notifications_on_from_account_id"},
		"oauth_access_grants": {"index_oauth_access_grants_on_resource_owner_id", "index_oauth_access_grants_on_token"},
		"oauth_access_tokens": {"index_oauth_access_tokens_on_refresh_token", "index_oauth_access_tokens_on_token", "index_oauth_access_tokens_on_resource_owner_id"},
		"oauth_applications":  {"index_oauth_applications_on_owner_id_and_owner_type", "index_oauth_applications_on_superapp", "index_oauth_applications_on_uid"},
		"poll_votes":          {"index_poll_votes_on_account_id", "index_poll_votes_on_poll_id"},
		"polls":               {"index_polls_on_account_id", "index_polls_on_status_id"},
		"preview_card_providers": {
			"index_preview_card_providers_on_domain",
		},
		"preview_card_trends":    {"index_preview_card_trends_on_preview_card_id"},
		"preview_cards":          {"index_preview_cards_on_url"},
		"preview_cards_statuses": {"preview_cards_statuses_pkey"},
		"report_notes":           {"index_report_notes_on_account_id", "index_report_notes_on_report_id"},
		"reports":                {"index_reports_on_account_id", "index_reports_on_action_taken_by_account_id", "index_reports_on_assigned_account_id", "index_reports_on_target_account_id"},
		"scheduled_statuses":     {"index_scheduled_statuses_on_account_id", "index_scheduled_statuses_on_scheduled_at"},
		"session_activations":    {"index_session_activations_on_access_token_id", "index_session_activations_on_session_id", "index_session_activations_on_user_id"},
		"settings":               {"index_settings_on_var"},
		"site_uploads":           {"index_site_uploads_on_var"},
		"software_updates":       {"index_software_updates_on_version"},
		"status_edits":           {"index_status_edits_on_account_id", "index_status_edits_on_status_id"},
		"status_pins":            {"index_status_pins_on_account_id_and_status_id", "index_status_pins_on_status_id"},
		"status_stats":           {"index_status_stats_on_status_id"},
		"status_trends":          {"index_status_trends_on_account_id", "index_status_trends_on_status_id"},
		"statuses": {
			"index_statuses_20190820",
			"index_statuses_on_account_id",
			"index_statuses_on_deleted_at",
			"index_statuses_on_in_reply_to_account_id",
			"index_statuses_on_in_reply_to_id",
			"index_statuses_on_reblog_of_id_and_account_id",
			"index_statuses_on_uri",
			"index_statuses_local_20190824",
			"index_statuses_public_20250129",
		},
		"statuses_tags":        {"statuses_tags_pkey", "index_statuses_tags_on_status_id"},
		"tag_follows":          {"index_tag_follows_on_account_id_and_tag_id", "index_tag_follows_on_tag_id"},
		"tags":                 {"index_tags_on_name_lower_btree"},
		"tombstones":           {"index_tombstones_on_account_id", "index_tombstones_on_uri"},
		"unavailable_domains":  {"index_unavailable_domains_on_domain"},
		"user_invite_requests": {"index_user_invite_requests_on_user_id"},
		"users": {
			"index_users_on_account_id",
			"index_users_on_confirmation_token",
			"index_users_on_created_by_application_id",
			"index_users_on_email",
			"index_users_on_reset_password_token",
			"index_users_on_role_id",
			"index_users_on_unconfirmed_email",
		},
		"web_push_subscriptions": {"index_web_push_subscriptions_on_access_token_id", "index_web_push_subscriptions_on_user_id"},
		"web_settings":           {"index_web_settings_on_user_id"},
		"webhooks":               {"index_webhooks_on_url"},
		"webauthn_credentials":   {"index_webauthn_credentials_on_external_id", "index_webauthn_credentials_on_user_id_and_nickname"},
		"quotes":                 {"index_quotes_on_status_id", "index_quotes_on_activity_uri"},
		"terms_of_services":      {"index_terms_of_services_on_effective_date"},
		"tag_trends":             {"index_tag_trends_on_tag_id_and_language"},
	}
	for table, wantIndexes := range checks {
		got := map[string]bool{}
		for _, index := range indexes[table] {
			got[index] = true
		}
		for _, index := range wantIndexes {
			if !got[index] {
				t.Fatalf("RequiredMastodonIndexes[%s] missing %q: %#v", table, index, indexes[table])
			}
		}
	}
}

func TestRequiredMastodonRelationKindsCoverDropInSchemaCore(t *testing.T) {
	kinds := map[string]MastodonRelationKind{}
	for _, relation := range RequiredMastodonRelationKinds() {
		kinds[relation.Name] = relation
	}
	checks := map[string][]string{
		"instances":                     {"m"},
		"account_summaries":             {"m"},
		"global_follow_recommendations": {"m"},
		"user_ips":                      {"v"},
	}
	for relation, wantKinds := range checks {
		got, ok := kinds[relation]
		if !ok {
			t.Fatalf("RequiredMastodonRelationKinds missing %q", relation)
		}
		for _, wantKind := range wantKinds {
			if !relationKindAllowed(wantKind, got.Kinds) {
				t.Fatalf("RequiredMastodonRelationKinds[%s] = %#v, missing kind %q", relation, got.Kinds, wantKind)
			}
		}
		if got.Description == "" {
			t.Fatalf("RequiredMastodonRelationKinds[%s] has empty Description", relation)
		}
	}
}

func TestRequiredMastodonUniqueIndexesCoverDropInSchemaCore(t *testing.T) {
	required := map[string]bool{}
	for _, index := range RequiredMastodonUniqueIndexes() {
		required[index] = true
	}
	for _, want := range []string{
		"index_accounts_on_username_and_domain_lower",
		"index_follows_on_account_id_and_target_account_id",
		"index_follow_requests_on_account_id_and_target_account_id",
		"index_appeals_on_account_warning_id",
		"index_favourites_on_account_id_and_status_id",
		"index_bookmarks_on_account_id_and_status_id",
		"index_list_accounts_on_account_id_and_list_id",
		"index_account_notes_on_account_id_and_target_account_id",
		"index_account_pins_on_account_id_and_target_account_id",
		"index_oauth_access_tokens_on_token",
		"index_oauth_access_tokens_on_refresh_token",
		"index_oauth_access_grants_on_token",
		"index_oauth_applications_on_uid",
		"index_session_activations_on_session_id",
		"index_web_settings_on_user_id",
		"index_users_on_email",
		"index_users_on_confirmation_token",
		"index_users_on_reset_password_token",
		"index_settings_on_var",
		"index_statuses_on_uri",
		"index_preview_cards_on_url",
		"index_tags_on_name_lower_btree",
		"index_webauthn_credentials_on_external_id",
		"index_quotes_on_status_id",
		"index_fasp_providers_on_base_url",
	} {
		if !required[want] {
			t.Fatalf("RequiredMastodonUniqueIndexes missing %q", want)
		}
	}
}

func TestRequiredMastodonIndexDefinitionFragmentsCoverDropInSchemaCore(t *testing.T) {
	required := RequiredMastodonIndexDefinitionFragments()
	checks := map[string][]string{
		"index_announcement_reactions_on_custom_emoji_id":      {"custom_emoji_id", `where: "(custom_emoji_id IS NOT NULL)"`},
		"index_account_migrations_on_target_account_id":        {`where: "(target_account_id IS NOT NULL)"`},
		"index_account_stats_on_last_status_at_and_account_id": {"last_status_at", "DESC NULLS LAST", "account_id"},
		"search_index":                                    {"using: :gin", "to_tsvector", "display_name", "username", "domain"},
		"index_accounts_on_moved_to_account_id":           {`where: "(moved_to_account_id IS NOT NULL)"`},
		"index_accounts_on_username_and_domain_lower":     {"lower((username)::text)", "COALESCE(lower((domain)::text), ''::text)"},
		"index_appeals_on_approved_by_account_id":         {`where: "(approved_by_account_id IS NOT NULL)"`},
		"index_appeals_on_rejected_by_account_id":         {`where: "(rejected_by_account_id IS NOT NULL)"`},
		"index_bulk_imports_unconfirmed":                  {`where: "(state = 0)"`},
		"index_list_accounts_on_follow_id":                {`where: "(follow_id IS NOT NULL)"`},
		"index_list_accounts_on_follow_request_id":        {`where: "(follow_request_id IS NOT NULL)"`},
		"index_conversations_on_uri":                      {"opclass: :text_pattern_ops", `where: "(uri IS NOT NULL)"`},
		"index_media_attachments_on_shortcode":            {"opclass: :text_pattern_ops", `where: "(shortcode IS NOT NULL)"`},
		"index_oauth_access_tokens_on_refresh_token":      {"opclass: :text_pattern_ops", `where: "(refresh_token IS NOT NULL)"`},
		"index_oauth_access_tokens_on_resource_owner_id":  {`where: "(resource_owner_id IS NOT NULL)"`},
		"index_oauth_applications_on_superapp":            {`where: "(superapp = true)"`},
		"index_reports_on_action_taken_by_account_id":     {`where: "(action_taken_by_account_id IS NOT NULL)"`},
		"index_statuses_20190820":                         {`where: "(deleted_at IS NULL)"`},
		"index_statuses_on_deleted_at":                    {`where: "(deleted_at IS NOT NULL)"`},
		"index_statuses_on_in_reply_to_id":                {`where: "(in_reply_to_id IS NOT NULL)"`},
		"index_statuses_local_20190824":                   {"local", "deleted_at IS NULL", "visibility = 0", "reblog_of_id IS NULL"},
		"index_statuses_public_20250129":                  {"language", "deleted_at IS NULL", "visibility = 0", "reblog_of_id IS NULL"},
		"index_terms_of_services_on_effective_date":       {"effective_date", `where: "(effective_date IS NOT NULL)"`},
		"index_quotes_on_activity_uri":                    {"activity_uri", `where: "(activity_uri IS NOT NULL)"`},
		"index_quotes_on_approval_uri":                    {"approval_uri", `where: "(approval_uri IS NOT NULL)"`},
		"index_statuses_on_uri":                           {"opclass: :text_pattern_ops", `where: "(uri IS NOT NULL)"`},
		"index_tags_on_name_lower_btree":                  {"lower((name)::text)", "text_pattern_ops"},
		"index_users_on_created_by_application_id":        {`where: "(created_by_application_id IS NOT NULL)"`},
		"index_users_on_reset_password_token":             {"opclass: :text_pattern_ops", `where: "(reset_password_token IS NOT NULL)"`},
		"index_users_on_role_id":                          {`where: "(role_id IS NOT NULL)"`},
		"index_users_on_unconfirmed_email":                {`where: "(unconfirmed_email IS NOT NULL)"`},
		"index_web_push_subscriptions_on_access_token_id": {`where: "(access_token_id IS NOT NULL)"`},
	}
	for index, fragments := range checks {
		if _, ok := required[index]; !ok {
			t.Fatalf("RequiredMastodonIndexDefinitionFragments missing %q", index)
		}
		for _, fragment := range fragments {
			if !schemaFragmentCoveredByRuntimeFragments(fragment, required[index]) {
				t.Fatalf("RequiredMastodonIndexDefinitionFragments[%s] does not cover Rails schema fragment %q: %#v", index, fragment, required[index])
			}
		}
	}
}

func TestRequiredMastodonPrimaryKeysCoverDropInCoreTables(t *testing.T) {
	primaryKeys := RequiredMastodonPrimaryKeys()
	checks := map[string][]string{
		"accounts":                           {"id"},
		"account_aliases":                    {"id"},
		"account_migrations":                 {"id"},
		"account_moderation_notes":           {"id"},
		"account_stats":                      {"id"},
		"accounts_tags":                      {"tag_id", "account_id"},
		"appeals":                            {"id"},
		"bookmarks":                          {"id"},
		"bulk_imports":                       {"id"},
		"canonical_email_blocks":             {"id"},
		"custom_emojis":                      {"id"},
		"domain_blocks":                      {"id"},
		"favourites":                         {"id"},
		"featured_tags":                      {"id"},
		"follow_recommendation_suppressions": {"id"},
		"follow_requests":                    {"id"},
		"follows":                            {"id"},
		"invites":                            {"id"},
		"list_accounts":                      {"id"},
		"lists":                              {"id"},
		"media_attachments":                  {"id"},
		"notifications":                      {"id"},
		"oauth_access_grants":                {"id"},
		"oauth_access_tokens":                {"id"},
		"oauth_applications":                 {"id"},
		"polls":                              {"id"},
		"preview_cards":                      {"id"},
		"preview_cards_statuses":             {"status_id", "preview_card_id"},
		"reports":                            {"id"},
		"session_activations":                {"id"},
		"settings":                           {"id"},
		"software_updates":                   {"id"},
		"status_stats":                       {"id"},
		"statuses":                           {"id"},
		"statuses_tags":                      {"tag_id", "status_id"},
		"tags":                               {"id"},
		"user_roles":                         {"id"},
		"users":                              {"id"},
		"web_push_subscriptions":             {"id"},
		"web_settings":                       {"id"},
		"webauthn_credentials":               {"id"},
		"webhooks":                           {"id"},
		"quotes":                             {"id"},
		"terms_of_services":                  {"id"},
		"tag_trends":                         {"id"},
	}
	for table, wantColumns := range checks {
		gotColumns, ok := primaryKeys[table]
		if !ok {
			t.Fatalf("RequiredMastodonPrimaryKeys missing %q", table)
		}
		if strings.Join(gotColumns, ",") != strings.Join(wantColumns, ",") {
			t.Fatalf("RequiredMastodonPrimaryKeys[%s] = %#v, want %#v", table, gotColumns, wantColumns)
		}
	}
}

func TestRequiredMastodonColumnDefinitionsCoverDropInCoreDefaults(t *testing.T) {
	required := map[string]MastodonColumnDefinition{}
	for _, definition := range RequiredMastodonColumnDefinitions() {
		required[definition.String()] = definition
	}
	checks := []MastodonColumnDefinition{
		{Table: "accounts", Column: "username", NotNull: true, DefaultFragments: []string{"''::character varying"}},
		{Table: "accounts", Column: "public_key", NotNull: true, DefaultFragments: []string{"''::text"}},
		{Table: "accounts", Column: "uri", NotNull: true, DefaultFragments: []string{"''::character varying"}},
		{Table: "accounts", Column: "header_remote_url", NotNull: true, DefaultFragments: []string{"''::character varying"}},
		{Table: "accounts", Column: "inbox_url", NotNull: true, DefaultFragments: []string{"''::character varying"}},
		{Table: "accounts", Column: "outbox_url", NotNull: true, DefaultFragments: []string{"''::character varying"}},
		{Table: "accounts", Column: "shared_inbox_url", NotNull: true, DefaultFragments: []string{"''::character varying"}},
		{Table: "accounts", Column: "followers_url", NotNull: true, DefaultFragments: []string{"''::character varying"}},
		{Table: "accounts", Column: "locked", NotNull: true, DefaultFragments: []string{"false"}},
		{Table: "accounts", Column: "protocol", NotNull: true, DefaultFragments: []string{"0"}},
		{Table: "accounts_tags", Column: "account_id", NotNull: true},
		{Table: "accounts_tags", Column: "tag_id", NotNull: true},
		{Table: "account_aliases", Column: "acct", NotNull: true, DefaultFragments: []string{"''::character varying"}},
		{Table: "account_aliases", Column: "uri", NotNull: true, DefaultFragments: []string{"''::character varying"}},
		{Table: "account_migrations", Column: "acct", NotNull: true, DefaultFragments: []string{"''::character varying"}},
		{Table: "account_migrations", Column: "followers_count", NotNull: true, DefaultFragments: []string{"0"}},
		{Table: "account_moderation_notes", Column: "content", NotNull: true},
		{Table: "account_moderation_notes", Column: "account_id", NotNull: true},
		{Table: "account_moderation_notes", Column: "target_account_id", NotNull: true},
		{Table: "account_stats", Column: "account_id", NotNull: true},
		{Table: "account_stats", Column: "statuses_count", NotNull: true, DefaultFragments: []string{"0"}},
		{Table: "account_statuses_cleanup_policies", Column: "account_id", NotNull: true},
		{Table: "account_statuses_cleanup_policies", Column: "enabled", NotNull: true, DefaultFragments: []string{"true"}},
		{Table: "account_statuses_cleanup_policies", Column: "min_status_age", NotNull: true, DefaultFragments: []string{"1209600"}},
		{Table: "account_statuses_cleanup_policies", Column: "keep_direct", NotNull: true, DefaultFragments: []string{"true"}},
		{Table: "account_statuses_cleanup_policies", Column: "keep_pinned", NotNull: true, DefaultFragments: []string{"true"}},
		{Table: "account_statuses_cleanup_policies", Column: "keep_polls", NotNull: true, DefaultFragments: []string{"false"}},
		{Table: "account_statuses_cleanup_policies", Column: "keep_media", NotNull: true, DefaultFragments: []string{"false"}},
		{Table: "account_statuses_cleanup_policies", Column: "keep_self_fav", NotNull: true, DefaultFragments: []string{"true"}},
		{Table: "account_statuses_cleanup_policies", Column: "keep_self_bookmark", NotNull: true, DefaultFragments: []string{"true"}},
		{Table: "account_notes", Column: "comment", NotNull: true},
		{Table: "account_warning_presets", Column: "text", NotNull: true, DefaultFragments: []string{"''::text"}},
		{Table: "account_warning_presets", Column: "title", NotNull: true, DefaultFragments: []string{"''::character varying"}},
		{Table: "account_warnings", Column: "action", NotNull: true, DefaultFragments: []string{"0"}},
		{Table: "account_warnings", Column: "text", NotNull: true, DefaultFragments: []string{"''::text"}},
		{Table: "admin_action_logs", Column: "action", NotNull: true, DefaultFragments: []string{"''::character varying"}},
		{Table: "account_conversations", Column: "participant_account_ids", NotNull: true, DefaultFragments: []string{"'{}'::bigint[]"}},
		{Table: "account_conversations", Column: "status_ids", NotNull: true, DefaultFragments: []string{"'{}'::bigint[]"}},
		{Table: "account_conversations", Column: "lock_version", NotNull: true, DefaultFragments: []string{"0"}},
		{Table: "account_conversations", Column: "unread", NotNull: true, DefaultFragments: []string{"false"}},
		{Table: "announcement_reactions", Column: "name", NotNull: true, DefaultFragments: []string{"''::character varying"}},
		{Table: "announcements", Column: "text", NotNull: true, DefaultFragments: []string{"''::text"}},
		{Table: "announcements", Column: "published", NotNull: true, DefaultFragments: []string{"false"}},
		{Table: "announcements", Column: "all_day", NotNull: true, DefaultFragments: []string{"false"}},
		{Table: "appeals", Column: "account_id", NotNull: true},
		{Table: "appeals", Column: "account_warning_id", NotNull: true},
		{Table: "appeals", Column: "text", NotNull: true, DefaultFragments: []string{"''::text"}},
		{Table: "backups", Column: "processed", NotNull: true, DefaultFragments: []string{"false"}},
		{Table: "blocks", Column: "account_id", NotNull: true},
		{Table: "blocks", Column: "target_account_id", NotNull: true},
		{Table: "bookmarks", Column: "account_id", NotNull: true},
		{Table: "bookmarks", Column: "status_id", NotNull: true},
		{Table: "bulk_import_rows", Column: "bulk_import_id", NotNull: true},
		{Table: "bulk_imports", Column: "type", NotNull: true},
		{Table: "bulk_imports", Column: "state", NotNull: true},
		{Table: "bulk_imports", Column: "total_items", NotNull: true, DefaultFragments: []string{"0"}},
		{Table: "bulk_imports", Column: "imported_items", NotNull: true, DefaultFragments: []string{"0"}},
		{Table: "bulk_imports", Column: "processed_items", NotNull: true, DefaultFragments: []string{"0"}},
		{Table: "bulk_imports", Column: "overwrite", NotNull: true, DefaultFragments: []string{"false"}},
		{Table: "bulk_imports", Column: "likely_mismatched", NotNull: true, DefaultFragments: []string{"false"}},
		{Table: "bulk_imports", Column: "original_filename", NotNull: true, DefaultFragments: []string{"''::character varying"}},
		{Table: "bulk_imports", Column: "account_id", NotNull: true},
		{Table: "canonical_email_blocks", Column: "canonical_email_hash", NotNull: true, DefaultFragments: []string{"''::character varying"}},
		{Table: "custom_filter_keywords", Column: "custom_filter_id", NotNull: true},
		{Table: "custom_filter_keywords", Column: "keyword", NotNull: true, DefaultFragments: []string{"''::text"}},
		{Table: "custom_filter_keywords", Column: "whole_word", NotNull: true, DefaultFragments: []string{"true"}},
		{Table: "custom_filter_statuses", Column: "custom_filter_id", NotNull: true},
		{Table: "custom_filter_statuses", Column: "status_id", NotNull: true},
		{Table: "custom_filters", Column: "phrase", NotNull: true, DefaultFragments: []string{"''::text"}},
		{Table: "custom_filters", Column: "context", NotNull: true, DefaultFragments: []string{"ARRAY[]::character varying[]"}},
		{Table: "custom_filters", Column: "action", NotNull: true, DefaultFragments: []string{"0"}},
		{Table: "custom_emojis", Column: "shortcode", NotNull: true, DefaultFragments: []string{"''::character varying"}},
		{Table: "custom_emojis", Column: "disabled", NotNull: true, DefaultFragments: []string{"false"}},
		{Table: "custom_emojis", Column: "visible_in_picker", NotNull: true, DefaultFragments: []string{"true"}},
		{Table: "domain_allows", Column: "domain", NotNull: true, DefaultFragments: []string{"''::character varying"}},
		{Table: "domain_blocks", Column: "domain", NotNull: true, DefaultFragments: []string{"''::character varying"}},
		{Table: "domain_blocks", Column: "severity", DefaultFragments: []string{"0"}},
		{Table: "domain_blocks", Column: "reject_media", NotNull: true, DefaultFragments: []string{"false"}},
		{Table: "domain_blocks", Column: "reject_reports", NotNull: true, DefaultFragments: []string{"false"}},
		{Table: "domain_blocks", Column: "obfuscate", NotNull: true, DefaultFragments: []string{"false"}},
		{Table: "conversation_mutes", Column: "conversation_id", NotNull: true},
		{Table: "conversation_mutes", Column: "account_id", NotNull: true},
		{Table: "email_domain_blocks", Column: "domain", NotNull: true, DefaultFragments: []string{"''::character varying"}},
		{Table: "favourites", Column: "account_id", NotNull: true},
		{Table: "favourites", Column: "status_id", NotNull: true},
		{Table: "featured_tags", Column: "account_id", NotNull: true},
		{Table: "featured_tags", Column: "tag_id", NotNull: true},
		{Table: "featured_tags", Column: "statuses_count", NotNull: true, DefaultFragments: []string{"0"}},
		{Table: "follow_recommendation_suppressions", Column: "account_id", NotNull: true},
		{Table: "follow_requests", Column: "show_reblogs", NotNull: true, DefaultFragments: []string{"true"}},
		{Table: "follow_requests", Column: "notify", NotNull: true, DefaultFragments: []string{"false"}},
		{Table: "follows", Column: "show_reblogs", NotNull: true, DefaultFragments: []string{"true"}},
		{Table: "follows", Column: "notify", NotNull: true, DefaultFragments: []string{"false"}},
		{Table: "identities", Column: "provider", NotNull: true, DefaultFragments: []string{"''::character varying"}},
		{Table: "identities", Column: "uid", NotNull: true, DefaultFragments: []string{"''::character varying"}},
		{Table: "invites", Column: "user_id", NotNull: true},
		{Table: "invites", Column: "code", NotNull: true, DefaultFragments: []string{"''::character varying"}},
		{Table: "invites", Column: "uses", NotNull: true, DefaultFragments: []string{"0"}},
		{Table: "invites", Column: "autofollow", NotNull: true, DefaultFragments: []string{"false"}},
		{Table: "ip_blocks", Column: "ip", NotNull: true, DefaultFragments: []string{"'0.0.0.0'::inet"}},
		{Table: "ip_blocks", Column: "severity", NotNull: true, DefaultFragments: []string{"0"}},
		{Table: "ip_blocks", Column: "comment", NotNull: true, DefaultFragments: []string{"''::text"}},
		{Table: "list_accounts", Column: "list_id", NotNull: true},
		{Table: "list_accounts", Column: "account_id", NotNull: true},
		{Table: "lists", Column: "account_id", NotNull: true},
		{Table: "lists", Column: "title", NotNull: true, DefaultFragments: []string{"''::character varying"}},
		{Table: "lists", Column: "replies_policy", NotNull: true, DefaultFragments: []string{"0"}},
		{Table: "lists", Column: "exclusive", NotNull: true, DefaultFragments: []string{"false"}},
		{Table: "login_activities", Column: "user_id", NotNull: true},
		{Table: "markers", Column: "timeline", NotNull: true, DefaultFragments: []string{"''::character varying"}},
		{Table: "markers", Column: "last_read_id", NotNull: true, DefaultFragments: []string{"0"}},
		{Table: "markers", Column: "lock_version", NotNull: true, DefaultFragments: []string{"0"}},
		{Table: "media_attachments", Column: "remote_url", NotNull: true, DefaultFragments: []string{"''::character varying"}},
		{Table: "media_attachments", Column: "type", NotNull: true, DefaultFragments: []string{"0"}},
		{Table: "mentions", Column: "silent", NotNull: true, DefaultFragments: []string{"false"}},
		{Table: "mutes", Column: "account_id", NotNull: true},
		{Table: "mutes", Column: "target_account_id", NotNull: true},
		{Table: "mutes", Column: "hide_notifications", NotNull: true, DefaultFragments: []string{"true"}},
		{Table: "notifications", Column: "activity_id", NotNull: true},
		{Table: "oauth_access_tokens", Column: "token", NotNull: true},
		{Table: "oauth_access_grants", Column: "expires_in", NotNull: true},
		{Table: "oauth_applications", Column: "scopes", NotNull: true, DefaultFragments: []string{"''::character varying"}},
		{Table: "oauth_applications", Column: "confidential", NotNull: true, DefaultFragments: []string{"true"}},
		{Table: "poll_votes", Column: "choice", NotNull: true, DefaultFragments: []string{"0"}},
		{Table: "polls", Column: "options", NotNull: true, DefaultFragments: []string{"ARRAY[]::character varying[]"}},
		{Table: "polls", Column: "cached_tallies", NotNull: true, DefaultFragments: []string{"'{}'::bigint[]"}},
		{Table: "polls", Column: "multiple", NotNull: true, DefaultFragments: []string{"false"}},
		{Table: "polls", Column: "hide_totals", NotNull: true, DefaultFragments: []string{"false"}},
		{Table: "polls", Column: "votes_count", NotNull: true, DefaultFragments: []string{"0"}},
		{Table: "polls", Column: "lock_version", NotNull: true, DefaultFragments: []string{"0"}},
		{Table: "preview_card_providers", Column: "domain", NotNull: true, DefaultFragments: []string{"''::character varying"}},
		{Table: "preview_card_trends", Column: "preview_card_id", NotNull: true},
		{Table: "preview_card_trends", Column: "score", NotNull: true, DefaultFragments: []string{"0"}},
		{Table: "preview_card_trends", Column: "rank", NotNull: true, DefaultFragments: []string{"0"}},
		{Table: "preview_card_trends", Column: "allowed", NotNull: true, DefaultFragments: []string{"false"}},
		{Table: "preview_cards", Column: "url", NotNull: true, DefaultFragments: []string{"''::character varying"}},
		{Table: "preview_cards", Column: "title", NotNull: true, DefaultFragments: []string{"''::character varying"}},
		{Table: "preview_cards", Column: "description", NotNull: true, DefaultFragments: []string{"''::character varying"}},
		{Table: "preview_cards", Column: "type", NotNull: true, DefaultFragments: []string{"0"}},
		{Table: "preview_cards", Column: "html", NotNull: true, DefaultFragments: []string{"''::text"}},
		{Table: "preview_cards", Column: "author_name", NotNull: true, DefaultFragments: []string{"''::character varying"}},
		{Table: "preview_cards", Column: "author_url", NotNull: true, DefaultFragments: []string{"''::character varying"}},
		{Table: "preview_cards", Column: "provider_name", NotNull: true, DefaultFragments: []string{"''::character varying"}},
		{Table: "preview_cards", Column: "provider_url", NotNull: true, DefaultFragments: []string{"''::character varying"}},
		{Table: "preview_cards", Column: "width", NotNull: true, DefaultFragments: []string{"0"}},
		{Table: "preview_cards", Column: "height", NotNull: true, DefaultFragments: []string{"0"}},
		{Table: "preview_cards", Column: "embed_url", NotNull: true, DefaultFragments: []string{"''::character varying"}},
		{Table: "preview_cards", Column: "image_description", NotNull: true, DefaultFragments: []string{"''::character varying"}},
		{Table: "preview_cards_statuses", Column: "preview_card_id", NotNull: true},
		{Table: "preview_cards_statuses", Column: "status_id", NotNull: true},
		{Table: "reports", Column: "status_ids", NotNull: true, DefaultFragments: []string{"'{}'::bigint[]"}},
		{Table: "report_notes", Column: "content", NotNull: true},
		{Table: "report_notes", Column: "report_id", NotNull: true},
		{Table: "report_notes", Column: "account_id", NotNull: true},
		{Table: "relays", Column: "inbox_url", NotNull: true, DefaultFragments: []string{"''::character varying"}},
		{Table: "relays", Column: "state", NotNull: true, DefaultFragments: []string{"0"}},
		{Table: "rules", Column: "priority", NotNull: true, DefaultFragments: []string{"0"}},
		{Table: "rules", Column: "text", NotNull: true, DefaultFragments: []string{"''::text"}},
		{Table: "session_activations", Column: "session_id", NotNull: true},
		{Table: "settings", Column: "var", NotNull: true},
		{Table: "site_uploads", Column: "var", NotNull: true, DefaultFragments: []string{"''::character varying"}},
		{Table: "software_updates", Column: "version", NotNull: true},
		{Table: "software_updates", Column: "urgent", NotNull: true, DefaultFragments: []string{"false"}},
		{Table: "software_updates", Column: "type", NotNull: true, DefaultFragments: []string{"0"}},
		{Table: "software_updates", Column: "release_notes", NotNull: true, DefaultFragments: []string{"''::character varying"}},
		{Table: "status_edits", Column: "status_id", NotNull: true},
		{Table: "status_edits", Column: "text", NotNull: true, DefaultFragments: []string{"''::text"}},
		{Table: "status_edits", Column: "spoiler_text", NotNull: true, DefaultFragments: []string{"''::text"}},
		{Table: "status_pins", Column: "account_id", NotNull: true},
		{Table: "status_pins", Column: "status_id", NotNull: true},
		{Table: "status_stats", Column: "favourites_count", NotNull: true, DefaultFragments: []string{"0"}},
		{Table: "status_trends", Column: "status_id", NotNull: true},
		{Table: "status_trends", Column: "account_id", NotNull: true},
		{Table: "status_trends", Column: "score", NotNull: true, DefaultFragments: []string{"0"}},
		{Table: "status_trends", Column: "rank", NotNull: true, DefaultFragments: []string{"0"}},
		{Table: "status_trends", Column: "allowed", NotNull: true, DefaultFragments: []string{"false"}},
		{Table: "statuses", Column: "visibility", NotNull: true, DefaultFragments: []string{"0"}},
		{Table: "statuses", Column: "sensitive", NotNull: true, DefaultFragments: []string{"false"}},
		{Table: "statuses", Column: "reply", NotNull: true, DefaultFragments: []string{"false"}},
		{Table: "statuses_tags", Column: "status_id", NotNull: true},
		{Table: "statuses_tags", Column: "tag_id", NotNull: true},
		{Table: "tag_follows", Column: "tag_id", NotNull: true},
		{Table: "tag_follows", Column: "account_id", NotNull: true},
		{Table: "tags", Column: "name", NotNull: true, DefaultFragments: []string{"''::character varying"}},
		{Table: "tombstones", Column: "uri", NotNull: true},
		{Table: "unavailable_domains", Column: "domain", NotNull: true, DefaultFragments: []string{"''::character varying"}},
		{Table: "user_roles", Column: "name", NotNull: true, DefaultFragments: []string{"''::character varying"}},
		{Table: "user_roles", Column: "color", NotNull: true, DefaultFragments: []string{"''::character varying"}},
		{Table: "user_roles", Column: "position", NotNull: true, DefaultFragments: []string{"0"}},
		{Table: "user_roles", Column: "permissions", NotNull: true, DefaultFragments: []string{"0"}},
		{Table: "user_roles", Column: "highlighted", NotNull: true, DefaultFragments: []string{"false"}},
		{Table: "users", Column: "email", NotNull: true, DefaultFragments: []string{"''::character varying"}},
		{Table: "users", Column: "sign_in_count", NotNull: true, DefaultFragments: []string{"0"}},
		{Table: "users", Column: "approved", NotNull: true, DefaultFragments: []string{"true"}},
		{Table: "web_push_subscriptions", Column: "endpoint", NotNull: true},
		{Table: "web_settings", Column: "user_id", NotNull: true},
		{Table: "webhooks", Column: "url", NotNull: true},
		{Table: "webhooks", Column: "events", NotNull: true, DefaultFragments: []string{"ARRAY[]::character varying[]"}},
		{Table: "webhooks", Column: "secret", NotNull: true, DefaultFragments: []string{"''::character varying"}},
		{Table: "webhooks", Column: "enabled", NotNull: true, DefaultFragments: []string{"true"}},
		{Table: "webauthn_credentials", Column: "external_id", NotNull: true},
		{Table: "webauthn_credentials", Column: "public_key", NotNull: true},
		{Table: "webauthn_credentials", Column: "nickname", NotNull: true},
		{Table: "webauthn_credentials", Column: "sign_count", NotNull: true, DefaultFragments: []string{"0"}},
	}
	for _, check := range checks {
		got, ok := required[check.String()]
		if !ok {
			t.Fatalf("RequiredMastodonColumnDefinitions missing %s", check.String())
		}
		if got.NotNull != check.NotNull {
			t.Fatalf("RequiredMastodonColumnDefinitions[%s].NotNull = %v, want %v", check.String(), got.NotNull, check.NotNull)
		}
		for _, fragment := range check.DefaultFragments {
			if !runtimeDefaultFragmentsContain(got.DefaultFragments, fragment) {
				t.Fatalf("RequiredMastodonColumnDefinitions[%s] missing default fragment %q: %#v", check.String(), fragment, got.DefaultFragments)
			}
		}
	}
}

func TestRequiredMastodon44CatalogContract(t *testing.T) {
	tables := map[string]bool{}
	for _, table := range RequiredMastodonTables() {
		tables[table] = true
	}
	for _, table := range []string{
		"annual_report_statuses_per_account_counts", "tag_trends", "terms_of_services",
		"fasp_providers", "fasp_debug_callbacks", "fasp_subscriptions",
		"fasp_backfill_requests", "fasp_follow_recommendations",
		"instance_moderation_notes", "quotes", "rule_translations",
	} {
		if !tables[table] {
			t.Fatalf("RequiredMastodonTables missing Mastodon 4.4 table %q", table)
		}
	}
	if slices.Contains(RequiredMastodonTables(), "imports") || !slices.Contains(ForbiddenMastodonRelations(), "imports") {
		t.Fatal("Mastodon 4.4 imports relation must be forbidden, not required")
	}

	definitions := map[string]MastodonColumnDefinition{}
	for _, definition := range RequiredMastodonColumnDefinitions() {
		definitions[definition.String()] = definition
	}
	checks := []MastodonColumnDefinition{
		{Table: "account_aliases", Column: "account_id", NotNull: true},
		{Table: "account_conversations", Column: "conversation_id", NotNull: true},
		{Table: "account_notes", Column: "target_account_id", NotNull: true},
		{Table: "poll_votes", Column: "poll_id", NotNull: true},
		{Table: "polls", Column: "status_id", NotNull: true},
		{Table: "tombstones", Column: "account_id", NotNull: true},
		{Table: "web_push_subscriptions", Column: "user_id", NotNull: true},
		{Table: "web_push_subscriptions", Column: "access_token_id", NotNull: true},
		{Table: "web_push_subscriptions", Column: "standard", NotNull: true, DefaultFragments: []string{"false"}},
		{Table: "statuses", Column: "quote_approval_policy", NotNull: true, DefaultFragments: []string{"0"}},
		{Table: "users", Column: "require_tos_interstitial", NotNull: true, DefaultFragments: []string{"false"}},
		{Table: "quotes", Column: "id", NotNull: true, DefaultFragments: []string{"timestamp_id('quotes'"}},
		{Table: "quotes", Column: "state", NotNull: true, DefaultFragments: []string{"0"}},
		{Table: "quotes", Column: "legacy", NotNull: true, DefaultFragments: []string{"false"}},
		{Table: "tag_trends", Column: "language", NotNull: true, DefaultFragments: []string{"''::character varying"}},
		{Table: "fasp_providers", Column: "capabilities", NotNull: true, DefaultFragments: []string{"'[]'::jsonb"}},
	}
	for _, check := range checks {
		got, ok := definitions[check.String()]
		if !ok || got.NotNull != check.NotNull {
			t.Fatalf("RequiredMastodonColumnDefinitions missing 4.4 definition %#v; got %#v", check, got)
		}
		for _, fragment := range check.DefaultFragments {
			if !runtimeDefaultFragmentsContain(got.DefaultFragments, fragment) {
				t.Fatalf("%s missing default fragment %q: %#v", check.String(), fragment, got.DefaultFragments)
			}
		}
	}

	foreignKeys := map[string]bool{}
	for _, foreignKey := range RequiredMastodonForeignKeys() {
		foreignKeys[foreignKey.String()] = true
	}
	for _, foreignKey := range []MastodonForeignKey{
		{Table: "account_moderation_notes", Column: "account_id", ForeignTable: "accounts", OnDelete: "c"},
		{Table: "webauthn_credentials", Column: "user_id", ForeignTable: "users", OnDelete: "c"},
		{Table: "quotes", Column: "account_id", ForeignTable: "accounts", OnDelete: "c"},
		{Table: "quotes", Column: "quoted_status_id", ForeignTable: "statuses", OnDelete: "n"},
		{Table: "tag_trends", Column: "tag_id", ForeignTable: "tags", OnDelete: "c"},
		{Table: "rule_translations", Column: "rule_id", ForeignTable: "rules", OnDelete: "c"},
		{Table: "fasp_follow_recommendations", Column: "recommended_account_id", ForeignTable: "accounts", OnDelete: "a"},
	} {
		if !foreignKeys[foreignKey.String()] {
			t.Fatalf("RequiredMastodonForeignKeys missing 4.4 FK %s", foreignKey.String())
		}
	}
	if !slices.Contains(RequiredMastodonSequences(), "quotes_id_seq") {
		t.Fatal("RequiredMastodonSequences missing quotes_id_seq")
	}
}

func TestRequiredMastodonForeignKeysCoverDropInCascadeCore(t *testing.T) {
	foreignKeys := RequiredMastodonForeignKeys()
	required := map[string]bool{}
	for _, foreignKey := range foreignKeys {
		required[foreignKey.String()] = true
	}
	checks := []MastodonForeignKey{
		{Table: "account_aliases", Column: "account_id", ForeignTable: "accounts", OnDelete: "c"},
		{Table: "account_conversations", Column: "conversation_id", ForeignTable: "conversations", OnDelete: "c"},
		{Table: "account_deletion_requests", Column: "account_id", ForeignTable: "accounts", OnDelete: "c"},
		{Table: "account_domain_blocks", Column: "account_id", ForeignTable: "accounts", OnDelete: "c"},
		{Table: "account_migrations", Column: "account_id", ForeignTable: "accounts", OnDelete: "c"},
		{Table: "account_migrations", Column: "target_account_id", ForeignTable: "accounts", OnDelete: "n"},
		{Table: "account_moderation_notes", Column: "account_id", ForeignTable: "accounts", OnDelete: "c"},
		{Table: "account_moderation_notes", Column: "target_account_id", ForeignTable: "accounts", OnDelete: "c"},
		{Table: "account_notes", Column: "target_account_id", ForeignTable: "accounts", OnDelete: "c"},
		{Table: "account_pins", Column: "target_account_id", ForeignTable: "accounts", OnDelete: "c"},
		{Table: "account_warnings", Column: "report_id", ForeignTable: "reports", OnDelete: "c"},
		{Table: "admin_action_logs", Column: "account_id", ForeignTable: "accounts", OnDelete: "c"},
		{Table: "announcement_mutes", Column: "announcement_id", ForeignTable: "announcements", OnDelete: "c"},
		{Table: "announcement_reactions", Column: "custom_emoji_id", ForeignTable: "custom_emojis", OnDelete: "c"},
		{Table: "appeals", Column: "account_warning_id", ForeignTable: "account_warnings", OnDelete: "c"},
		{Table: "appeals", Column: "approved_by_account_id", ForeignTable: "accounts", OnDelete: "n"},
		{Table: "appeals", Column: "rejected_by_account_id", ForeignTable: "accounts", OnDelete: "n"},
		{Table: "backups", Column: "user_id", ForeignTable: "users", OnDelete: "n"},
		{Table: "bookmarks", Column: "status_id", ForeignTable: "statuses", OnDelete: "c"},
		{Table: "bulk_import_rows", Column: "bulk_import_id", ForeignTable: "bulk_imports", OnDelete: "c"},
		{Table: "bulk_imports", Column: "account_id", ForeignTable: "accounts", OnDelete: "c"},
		{Table: "canonical_email_blocks", Column: "reference_account_id", ForeignTable: "accounts", OnDelete: "c"},
		{Table: "custom_filter_statuses", Column: "status_id", ForeignTable: "statuses", OnDelete: "c"},
		{Table: "custom_filters", Column: "account_id", ForeignTable: "accounts", OnDelete: "c"},
		{Table: "email_domain_blocks", Column: "parent_id", ForeignTable: "email_domain_blocks", OnDelete: "c"},
		{Table: "favourites", Column: "status_id", ForeignTable: "statuses", OnDelete: "c"},
		{Table: "follow_recommendation_suppressions", Column: "account_id", ForeignTable: "accounts", OnDelete: "c"},
		{Table: "follow_requests", Column: "target_account_id", ForeignTable: "accounts", OnDelete: "c"},
		{Table: "follows", Column: "target_account_id", ForeignTable: "accounts", OnDelete: "c"},
		{Table: "identities", Column: "user_id", ForeignTable: "users", OnDelete: "c"},
		{Table: "invites", Column: "user_id", ForeignTable: "users", OnDelete: "c"},
		{Table: "list_accounts", Column: "follow_request_id", ForeignTable: "follow_requests", OnDelete: "c"},
		{Table: "login_activities", Column: "user_id", ForeignTable: "users", OnDelete: "c"},
		{Table: "markers", Column: "user_id", ForeignTable: "users", OnDelete: "c"},
		{Table: "media_attachments", Column: "status_id", ForeignTable: "statuses", OnDelete: "n"},
		{Table: "mentions", Column: "status_id", ForeignTable: "statuses", OnDelete: "c"},
		{Table: "notifications", Column: "from_account_id", ForeignTable: "accounts", OnDelete: "c"},
		{Table: "oauth_access_tokens", Column: "resource_owner_id", ForeignTable: "users", OnDelete: "c"},
		{Table: "oauth_applications", Column: "owner_id", ForeignTable: "users", OnDelete: "c"},
		{Table: "poll_votes", Column: "poll_id", ForeignTable: "polls", OnDelete: "c"},
		{Table: "polls", Column: "status_id", ForeignTable: "statuses", OnDelete: "c"},
		{Table: "preview_card_trends", Column: "preview_card_id", ForeignTable: "preview_cards", OnDelete: "c"},
		{Table: "reports", Column: "action_taken_by_account_id", ForeignTable: "accounts", OnDelete: "n"},
		{Table: "reports", Column: "assigned_account_id", ForeignTable: "accounts", OnDelete: "n"},
		{Table: "reports", Column: "target_account_id", ForeignTable: "accounts", OnDelete: "c"},
		{Table: "session_activations", Column: "access_token_id", ForeignTable: "oauth_access_tokens", OnDelete: "c"},
		{Table: "status_edits", Column: "account_id", ForeignTable: "accounts", OnDelete: "n"},
		{Table: "status_pins", Column: "status_id", ForeignTable: "statuses", OnDelete: "c"},
		{Table: "status_stats", Column: "status_id", ForeignTable: "statuses", OnDelete: "c"},
		{Table: "status_trends", Column: "account_id", ForeignTable: "accounts", OnDelete: "c"},
		{Table: "status_trends", Column: "status_id", ForeignTable: "statuses", OnDelete: "c"},
		{Table: "statuses", Column: "reblog_of_id", ForeignTable: "statuses", OnDelete: "c"},
		{Table: "statuses", Column: "in_reply_to_id", ForeignTable: "statuses", OnDelete: "n"},
		{Table: "statuses_tags", Column: "tag_id", ForeignTable: "tags", OnDelete: "c"},
		{Table: "tag_follows", Column: "tag_id", ForeignTable: "tags", OnDelete: "c"},
		{Table: "user_invite_requests", Column: "user_id", ForeignTable: "users", OnDelete: "c"},
		{Table: "users", Column: "account_id", ForeignTable: "accounts", OnDelete: "c"},
		{Table: "users", Column: "invite_id", ForeignTable: "invites", OnDelete: "n"},
		{Table: "web_push_subscriptions", Column: "access_token_id", ForeignTable: "oauth_access_tokens", OnDelete: "c"},
		{Table: "web_settings", Column: "user_id", ForeignTable: "users", OnDelete: "c"},
		{Table: "webauthn_credentials", Column: "user_id", ForeignTable: "users", OnDelete: "c"},
	}
	for _, check := range checks {
		if !required[check.String()] {
			t.Fatalf("RequiredMastodonForeignKeys missing %s", check.String())
		}
	}
}

func TestSchemaAvailableChecksColumnsAfterTables(t *testing.T) {
	src, err := os.ReadFile("db.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	schemaAvailableBody := body[strings.Index(body, `func SchemaAvailable(database *gorm.DB) error {`):]
	for _, want := range []string{
		`RequiredMastodonTables()`,
		`mastodonRelationAvailable(database, relation)`,
		`to_regclass(?) IS NOT NULL`,
		`database schema is missing required Mastodon relations`,
		`RequiredMastodonColumns()`,
		`mastodonRelationColumns(database, table)`,
		`pg_attribute a`,
		`database schema is missing required Mastodon columns`,
		`RequiredMastodonIndexes()`,
		`mastodonIndexAvailable(database, index)`,
		`database schema is missing required Mastodon indexes`,
		`ForbiddenMastodonIndexes()`,
		`database schema still contains obsolete Mastodon indexes`,
		`RequiredMastodonUniqueIndexes()`,
		`pg_index i`,
		`database schema is missing required Mastodon unique indexes`,
		`RequiredMastodonIndexDefinitionFragments()`,
		`pg_get_indexdef(to_regclass(?))`,
		`database schema has incompatible Mastodon index definitions`,
		`RequiredMastodonPrimaryKeys()`,
		`index_info.indisprimary`,
		`database schema has incompatible Mastodon primary keys`,
		`RequiredMastodonRelationKinds()`,
		`to_regclass(?)`,
		`database schema has incompatible Mastodon relation kinds`,
		`RequiredMastodonExtensions()`,
		`SELECT EXISTS(SELECT 1 FROM pg_extension WHERE extname = ?)`,
		`database schema is missing required Mastodon extensions`,
		`RequiredMastodonFunctions()`,
		`to_regprocedure(?)`,
		`database schema is missing required Mastodon functions`,
		`RequiredMastodonSequences()`,
		`mastodonSequenceAvailable(database, sequence)`,
		`database schema is missing required Mastodon sequences`,
		`RequiredMastodonColumnDefinitions()`,
		`information_schema.columns`,
		`database schema has incompatible Mastodon column definitions`,
		`RequiredMastodonForeignKeys()`,
		`pg_constraint c`,
		`database schema is missing required Mastodon foreign keys`,
		`mastodonSchemaMigrationApplied(database, requiredMastodonSchemaVersion)`,
		`database schema_migrations is missing required Mastodon schema version`,
		`mastodonSchemaSHA1Applied(database, requiredMastodonSchemaSHA1)`,
		`database schema SHA-1`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("SchemaAvailable missing column check wiring %q", want)
		}
	}
	if strings.Contains(schemaAvailableBody, `HasTable(`) {
		t.Fatal("SchemaAvailable must not use GORM HasTable for Rails relations because it misses PostgreSQL views/materialized views")
	}
	if strings.Contains(schemaAvailableBody, `ColumnTypes(`) {
		t.Fatal("SchemaAvailable must not use GORM ColumnTypes for Rails relations because it misses PostgreSQL materialized view columns")
	}
	if strings.Contains(schemaAvailableBody, `HasIndex(`) {
		t.Fatal("SchemaAvailable must not use GORM HasIndex for Rails indexes because PostgreSQL relation checks are more precise")
	}
	if strings.Index(body, `mastodonRelationAvailable(database, relation)`) > strings.Index(body, `mastodonRelationColumns(database, table)`) {
		t.Fatal("SchemaAvailable must verify relation existence before inspecting columns")
	}
	if strings.Index(body, `RequiredMastodonRelationKinds()`) > strings.Index(body, `mastodonRelationColumns(database, table)`) {
		t.Fatal("SchemaAvailable must verify materialized-view/view kinds before inspecting columns")
	}
	if strings.Index(body, `RequiredMastodonExtensions()`) > strings.Index(body, `mastodonRelationColumns(database, table)`) {
		t.Fatal("SchemaAvailable must verify required database extensions before inspecting columns")
	}
	if strings.Index(body, `RequiredMastodonFunctions()`) > strings.Index(body, `mastodonRelationColumns(database, table)`) {
		t.Fatal("SchemaAvailable must verify required database functions before inspecting columns")
	}
	if strings.Index(body, `RequiredMastodonSequences()`) > strings.Index(body, `mastodonRelationColumns(database, table)`) {
		t.Fatal("SchemaAvailable must verify required database sequences before inspecting columns")
	}
	if strings.Index(schemaAvailableBody, `RequiredMastodonColumnDefinitions()`) < strings.Index(schemaAvailableBody, `mastodonRelationColumns(database, table)`) {
		t.Fatal("SchemaAvailable must verify column existence before inspecting column definitions")
	}
	if strings.Index(schemaAvailableBody, `RequiredMastodonColumnDefinitions()`) > strings.Index(schemaAvailableBody, `mastodonSchemaMigrationApplied(database, requiredMastodonSchemaVersion)`) {
		t.Fatal("SchemaAvailable must verify column definitions before accepting the Rails migration version")
	}
	if strings.Index(body, `RequiredMastodonForeignKeys()`) > strings.Index(body, `mastodonSchemaMigrationApplied(database, requiredMastodonSchemaVersion)`) {
		t.Fatal("SchemaAvailable must verify foreign keys before accepting the Rails migration version")
	}
	if strings.Index(body, `RequiredMastodonUniqueIndexes()`) > strings.Index(body, `mastodonSchemaMigrationApplied(database, requiredMastodonSchemaVersion)`) {
		t.Fatal("SchemaAvailable must verify unique indexes before accepting the Rails migration version")
	}
	if strings.Index(body, `RequiredMastodonIndexDefinitionFragments()`) > strings.Index(body, `mastodonSchemaMigrationApplied(database, requiredMastodonSchemaVersion)`) {
		t.Fatal("SchemaAvailable must verify index definitions before accepting the Rails migration version")
	}
	if strings.Index(body, `RequiredMastodonPrimaryKeys()`) > strings.Index(body, `mastodonSchemaMigrationApplied(database, requiredMastodonSchemaVersion)`) {
		t.Fatal("SchemaAvailable must verify primary keys before accepting the Rails migration version")
	}
	if strings.Index(body, `RequiredMastodonIndexes()`) > strings.Index(body, `mastodonSchemaMigrationApplied(database, requiredMastodonSchemaVersion)`) {
		t.Fatal("SchemaAvailable must verify indexes before accepting the Rails migration version")
	}
	if strings.Index(schemaAvailableBody, `mastodonSchemaMigrationApplied(database, requiredMastodonSchemaVersion)`) > strings.Index(schemaAvailableBody, `mastodonSchemaSHA1Applied(database, requiredMastodonSchemaSHA1)`) {
		t.Fatal("SchemaAvailable must verify the final migration marker before accepting the Rails schema SHA-1")
	}
}

func TestForbiddenMastodonColumnsCoversEvery43ContractDrop(t *testing.T) {
	forbidden := ForbiddenMastodonColumns()
	for table, columns := range map[string][]string{
		"account_relationship_severance_events": {"relationships_count"},
		"accounts":                              {"devices_url"},
		"notification_requests":                 {"dismissed"},
		"notification_policies":                 {"filter_not_followers", "filter_not_following", "filter_new_accounts", "filter_private_mentions"},
		"users":                                 {"admin", "moderator"},
	} {
		for _, column := range columns {
			if !slices.Contains(forbidden[table], column) {
				t.Fatalf("ForbiddenMastodonColumns missing %s.%s", table, column)
			}
		}
	}
}

func TestMastodon45FinalSchemaAdmissionContract(t *testing.T) {
	if got, want := RequiredMastodonSchemaVersion(), "20251023210145"; got != want {
		t.Fatalf("RequiredMastodonSchemaVersion() = %q, want %q", got, want)
	}
	if got, want := RequiredMastodonSchemaSHA1(), "801766beefdd9b1d55fe6f8bf3bed91392aebab1"; got != want {
		t.Fatalf("RequiredMastodonSchemaSHA1() = %q, want %q", got, want)
	}
	for _, index := range []string{
		"index_follows_on_target_account_id",
		"index_quotes_on_account_id_and_quoted_account_id",
		"index_quotes_on_quoted_status_id",
	} {
		if !slices.Contains(ForbiddenMastodonIndexes(), index) {
			t.Fatalf("ForbiddenMastodonIndexes() is missing v4.5 contract drop %q", index)
		}
	}
}

func TestSchemaAvailableExplainsSupportedDatabaseConfiguration(t *testing.T) {
	err := SchemaAvailable(nil)
	if err == nil {
		t.Fatal("SchemaAvailable(nil) error = nil")
	}
	if !strings.Contains(err.Error(), "DATABASE_URL") || !strings.Contains(err.Error(), "DB_NAME") {
		t.Fatalf("SchemaAvailable(nil) error = %q", err)
	}
}

func TestMastodonColumnDefaultContainsRailsArrayDefaultEquivalents(t *testing.T) {
	tests := []struct {
		name     string
		actual   string
		fragment string
		want     bool
	}{
		{
			name:     "varchar empty array literal matches Rails schema array default",
			actual:   "'{}'::character varying[]",
			fragment: "ARRAY[]::character varying[]",
			want:     true,
		},
		{
			name:     "bigint empty array accepts integer literal coerced by PostgreSQL",
			actual:   "'{}'::integer[]",
			fragment: "'{}'::bigint[]",
			want:     true,
		},
		{
			name:     "exact match remains valid",
			actual:   "false",
			fragment: "false",
			want:     true,
		},
		{
			name:     "unrelated defaults stay incompatible",
			actual:   "'{}'::text[]",
			fragment: "ARRAY[]::character varying[]",
			want:     false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := mastodonColumnDefaultContains(tt.actual, tt.fragment); got != tt.want {
				t.Fatalf("mastodonColumnDefaultContains(%q, %q) = %v, want %v", tt.actual, tt.fragment, got, tt.want)
			}
		})
	}
}

func TestMissingMastodonRelationErrorExplainsMigrationRecovery(t *testing.T) {
	src, err := os.ReadFile("db.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	for _, want := range []string{
		"paon-migrate",
		"task check-config:bin",
		"before starting paon",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing relation error must mention %q", want)
		}
	}
}

func tableColumnsFromSchema(schema string) map[string][]string {
	out := map[string][]string{}
	for _, match := range schemaTablePattern.FindAllStringSubmatch(schema, -1) {
		table := match[1]
		columns := []string{"id"}
		for _, line := range strings.Split(match[2], "\n") {
			column := schemaColumnName(line)
			if column != "" {
				columns = append(columns, column)
			}
		}
		out[table] = columns
	}
	for _, match := range schemaViewPattern.FindAllStringSubmatch(schema, -1) {
		table := match[1]
		columns := []string{}
		seen := map[string]bool{}
		for _, colMatch := range schemaViewColumnPattern.FindAllStringSubmatch(match[2], -1) {
			column := strings.TrimSpace(firstNonEmptyTestString(colMatch[2], colMatch[1]))
			if column == "" || seen[column] {
				continue
			}
			columns = append(columns, column)
			seen[column] = true
		}
		out[table] = columns
	}
	return out
}

func schemaIndexNamesByTable(schema string) map[string][]string {
	out := map[string][]string{}
	for _, match := range schemaTablePattern.FindAllStringSubmatch(schema, -1) {
		table := match[1]
		if schemaPrimaryKeyPattern.MatchString(match[0]) {
			out[table] = append(out[table], table+"_pkey")
		}
		for _, indexMatch := range schemaIndexNamePattern.FindAllStringSubmatch(match[2], -1) {
			out[table] = append(out[table], indexMatch[1])
		}
	}
	for _, match := range schemaAddIndexPattern.FindAllStringSubmatch(schema, -1) {
		out[match[1]] = append(out[match[1]], match[2])
	}
	return out
}

func schemaUniqueIndexNames(schema string) map[string]bool {
	out := map[string]bool{}
	for _, match := range schemaTablePattern.FindAllStringSubmatch(schema, -1) {
		for _, indexMatch := range schemaUniqueIndexPattern.FindAllStringSubmatch(match[2], -1) {
			out[indexMatch[1]] = true
		}
	}
	for _, match := range schemaUniqueAddIndexPattern.FindAllStringSubmatch(schema, -1) {
		out[match[1]] = true
	}
	return out
}

func schemaIndexDefinitions(schema string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(schema, "\n") {
		match := schemaAnyIndexLinePattern.FindStringSubmatch(line)
		if len(match) < 2 {
			continue
		}
		out[match[1]] = strings.TrimSpace(line)
	}
	return out
}

func schemaColumnDefinitions(schema string) map[string]string {
	out := map[string]string{}
	for _, match := range schemaTablePattern.FindAllStringSubmatch(schema, -1) {
		table := match[1]
		for _, line := range strings.Split(match[2], "\n") {
			column := schemaColumnName(line)
			if column == "" {
				continue
			}
			out[table+"."+column] = strings.TrimSpace(line)
		}
	}
	return out
}

func schemaRequiredColumnDefinitions(schema string) []MastodonColumnDefinition {
	out := []MastodonColumnDefinition{}
	requiredTables := requiredMastodonTablesByName()
	for _, match := range schemaTablePattern.FindAllStringSubmatch(schema, -1) {
		table := match[1]
		if !requiredTables[table] {
			continue
		}
		for _, line := range strings.Split(match[2], "\n") {
			column := schemaColumnName(line)
			if column == "" || column == "created_at" || column == "updated_at" {
				continue
			}
			definition := MastodonColumnDefinition{
				Table:   table,
				Column:  column,
				NotNull: strings.Contains(line, "null: false"),
			}
			if strings.Contains(line, "default:") {
				definition.DefaultFragments = []string{"schema.rb default"}
			}
			if !definition.NotNull && len(definition.DefaultFragments) == 0 {
				continue
			}
			out = append(out, definition)
		}
	}
	return out
}

func runtimeDefaultFragmentsContain(fragments []string, want string) bool {
	for _, fragment := range fragments {
		if strings.EqualFold(fragment, want) {
			return true
		}
	}
	return false
}

func schemaColumnDefinitionContainsDefault(line string, runtimeFragment string) bool {
	line = strings.ToLower(line)
	runtimeFragment = strings.ToLower(runtimeFragment)
	switch runtimeFragment {
	case "''::character varying", "''::text":
		return strings.Contains(line, `default: ""`)
	case "'{}'::bigint[]", "array[]::character varying[]":
		return strings.Contains(line, "default: []") && strings.Contains(line, "array: true")
	case "false", "true":
		return strings.Contains(line, "default: "+runtimeFragment)
	case "0":
		return strings.Contains(line, "default: 0")
	case "'0.0.0.0'::inet":
		return strings.Contains(line, `default: "0.0.0.0"`)
	default:
		return strings.Contains(line, runtimeFragment)
	}
}

func schemaIndexDefinitionContains(definition string, fragment string) bool {
	definition = strings.ToLower(definition)
	fragment = strings.ToLower(fragment)
	if strings.Contains(fragment, " text_pattern_ops") {
		column := strings.TrimSuffix(fragment, " text_pattern_ops")
		return strings.Contains(definition, column) && strings.Contains(definition, "text_pattern_ops")
	}
	if strings.HasPrefix(fragment, "where ") {
		return strings.Contains(definition, strings.TrimPrefix(fragment, "where "))
	}
	return strings.Contains(definition, fragment)
}

func schemaPrimaryKeysFromSchema(schema string) map[string][]string {
	out := map[string][]string{}
	for _, match := range schemaTablePattern.FindAllStringSubmatch(schema, -1) {
		headerEnd := strings.Index(match[0], " do |t|")
		if headerEnd < 0 {
			continue
		}
		header := match[0][:headerEnd]
		if strings.Contains(header, "id: false") || strings.Contains(header, "primary_key: [") {
			continue
		}
		out[match[1]] = []string{"id"}
	}
	for _, match := range schemaTablePrimaryKeyPattern.FindAllStringSubmatch(schema, -1) {
		columns := []string{}
		for _, columnMatch := range schemaQuotedValuePattern.FindAllStringSubmatch(match[2], -1) {
			columns = append(columns, columnMatch[1])
		}
		out[match[1]] = columns
	}
	return out
}

func schemaFragmentCoveredByRuntimeFragments(schemaFragment string, runtimeFragments []string) bool {
	schemaFragment = strings.ToLower(schemaFragment)
	for _, runtimeFragment := range runtimeFragments {
		runtimeFragment = strings.ToLower(runtimeFragment)
		if strings.Contains(schemaFragment, "opclass: :text_pattern_ops") && strings.Contains(runtimeFragment, "text_pattern_ops") {
			return true
		}
		if strings.HasPrefix(schemaFragment, "where: ") && strings.Contains(runtimeFragment, strings.TrimPrefix(schemaFragment, "where: ")) {
			return true
		}
		if strings.HasPrefix(schemaFragment, "where: ") {
			predicate := strings.Trim(strings.TrimPrefix(schemaFragment, "where: "), `"`)
			if strings.Contains(runtimeFragment, predicate) {
				return true
			}
		}
		if strings.Contains(runtimeFragment, schemaFragment) || strings.Contains(schemaFragment, runtimeFragment) {
			return true
		}
	}
	return false
}

func schemaRelationKinds(schema string) map[string]string {
	out := map[string]string{}
	for _, match := range schemaTablePattern.FindAllStringSubmatch(schema, -1) {
		out[match[1]] = "r"
	}
	for _, match := range schemaViewKindPattern.FindAllStringSubmatch(schema, -1) {
		kind := "v"
		if strings.Contains(match[2], "materialized: true") {
			kind = "m"
		}
		out[match[1]] = kind
	}
	return out
}

func schemaForeignKeysFromSchema(schema string) map[string]bool {
	out := map[string]bool{}
	for _, match := range schemaForeignKeyPattern.FindAllStringSubmatch(schema, -1) {
		foreignKey := MastodonForeignKey{
			Table:        match[1],
			Column:       "id",
			ForeignTable: match[2],
			OnDelete:     "a",
		}
		if column := schemaForeignKeyOption(match[3], "column"); column != "" {
			foreignKey.Column = column
		} else {
			foreignKey.Column = schemaForeignKeyDefaultColumn(foreignKey.ForeignTable)
		}
		switch schemaForeignKeyOption(match[3], "on_delete") {
		case "cascade":
			foreignKey.OnDelete = "c"
		case "nullify":
			foreignKey.OnDelete = "n"
		}
		out[foreignKey.String()] = true
	}
	return out
}

func schemaForeignKeyDefaultColumn(foreignTable string) string {
	switch foreignTable {
	case "statuses":
		return "status_id"
	case "oauth_access_tokens":
		return "access_token_id"
	case "oauth_access_grants":
		return "access_grant_id"
	case "oauth_applications":
		return "application_id"
	case "follow_requests":
		return "follow_request_id"
	case "account_warnings":
		return "account_warning_id"
	case "user_roles":
		return "role_id"
	default:
		if strings.HasSuffix(foreignTable, "statuses") {
			return strings.TrimSuffix(foreignTable, "es") + "_id"
		}
		if strings.HasSuffix(foreignTable, "emojis") {
			return strings.TrimSuffix(foreignTable, "s") + "_id"
		}
		return strings.TrimSuffix(foreignTable, "s") + "_id"
	}
}

func schemaForeignKeyOption(options string, name string) string {
	match := regexp.MustCompile(name + `:\s*:?"?([a-zA-Z_]+)"?`).FindStringSubmatch(options)
	if len(match) < 2 {
		return ""
	}
	return match[1]
}

func firstNonEmptyTestString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func requiredMastodonTablesByName() map[string]bool {
	out := map[string]bool{}
	for _, table := range RequiredMastodonTables() {
		out[table] = true
	}
	return out
}

func concreteModelColumnsByTable(src string) map[string][]string {
	out := map[string][]string{}
	typePattern := regexp.MustCompile(`(?ms)^type\s+(\w+)\s+struct\s+\{(.*?)^\}`)
	tablePattern := regexp.MustCompile(`func\s+\(\s*(\w+)\s*\)\s+TableName\(\)\s+string\s+\{\s*return\s+"([^"]+)"\s+\}`)
	typeTables := map[string]string{}
	for _, match := range tablePattern.FindAllStringSubmatch(src, -1) {
		typeTables[match[1]] = match[2]
	}
	for _, match := range typePattern.FindAllStringSubmatch(src, -1) {
		table := typeTables[match[1]]
		if table == "" {
			continue
		}
		seen := map[string]bool{}
		for _, columnMatch := range modelColumnTagPattern.FindAllStringSubmatch(match[2], -1) {
			column := columnMatch[1]
			if column == "" || column == "-" || seen[column] {
				continue
			}
			out[table] = append(out[table], column)
			seen[column] = true
		}
	}
	return out
}

func rawSQLReferenceRoots() []string {
	return []string{"..", "../../../cmd"}
}

func rawSQLRelationIgnored(relation string) bool {
	switch relation {
	case "ancestors",
		"domain_counts",
		"excluded",
		"existing_follows",
		"filter",
		"following_edge",
		"grouped_status_trends",
		"home_exclusive_lists",
		"keyword",
		"mine",
		"owner",
		"pghero_space_stats",
		"reply_members",
		"rows",
		"search_tree",
		"source",
		"source_follows",
		"status",
		"t0",
		"target",
		"voter":
		return true
	default:
		return strings.Contains(relation, "_accounts") ||
			strings.Contains(relation, "_blocks") ||
			strings.Contains(relation, "_domain_blocks") ||
			strings.Contains(relation, "_follows") ||
			strings.Contains(relation, "_mentions") ||
			strings.Contains(relation, "_mutes") ||
			strings.Contains(relation, "_status") ||
			strings.Contains(relation, "_statuses_tags") ||
			strings.Contains(relation, "_tags")
	}
}

func schemaColumnName(line string) string {
	match := schemaColumnPattern.FindStringSubmatch(line)
	if len(match) == 0 {
		return ""
	}
	return match[1]
}

var (
	schemaTablePattern           = regexp.MustCompile(`(?ms)create_table "([^"]+)".*? do \|t\|(.*?)\n  end`)
	schemaViewPattern            = regexp.MustCompile(`(?ms)create_view "([^"]+)",(?: materialized: true,)? sql_definition: <<-SQL\n(.*?)\n  SQL`)
	schemaViewKindPattern        = regexp.MustCompile(`(?ms)create_view "([^"]+)",(.*?)sql_definition: <<-SQL\n.*?\n  SQL`)
	schemaViewColumnPattern      = regexp.MustCompile(`(?mi)^\s*(?:SELECT\s+)?(?:(?:\w+\.)?(\w+)|.+?\s+AS\s+(\w+))\s*(?:,|$)`)
	schemaColumnPattern          = regexp.MustCompile(`^\s+t\.(?:bigint|binary|boolean|datetime|float|inet|integer|json|jsonb|string|text) "([^"]+)"`)
	schemaPrimaryKeyPattern      = regexp.MustCompile(`primary_key: \[[^\]]+\]`)
	schemaIndexNamePattern       = regexp.MustCompile(`name: "([^"]+)"`)
	schemaUniqueIndexPattern     = regexp.MustCompile(`t\.index .*?name: "([^"]+)".*?unique: true`)
	schemaAddIndexPattern        = regexp.MustCompile(`(?m)^\s*add_index "([^"]+)".*?name: "([^"]+)"`)
	schemaUniqueAddIndexPattern  = regexp.MustCompile(`(?m)^\s*add_index "[^"]+".*?name: "([^"]+)".*?unique: true`)
	schemaAnyIndexLinePattern    = regexp.MustCompile(`\b(?:t\.index|add_index)\b.*?name: "([^"]+)"`)
	schemaTablePrimaryKeyPattern = regexp.MustCompile(`(?m)^\s*create_table "([^"]+)", primary_key: \[([^\]]+)\]`)
	schemaQuotedValuePattern     = regexp.MustCompile(`"([^"]+)"`)
	schemaForeignKeyPattern      = regexp.MustCompile(`(?m)^\s*add_foreign_key "([^"]+)", "([^"]+)"(.*)$`)
	modelTableNamePattern        = regexp.MustCompile(`TableName\(\) string \{ return "([^"]+)" \}`)
	rawSQLRelationPattern        = regexp.MustCompile(`(?i)\b(?:FROM|JOIN|UPDATE|INSERT INTO|DELETE FROM)\s+([a-zA-Z_][a-zA-Z0-9_]*)`)
	gormTableRelationPattern     = regexp.MustCompile(`\.Table\("([a-zA-Z_][a-zA-Z0-9_]*)"\)`)
	modelColumnTagPattern        = regexp.MustCompile("`gorm:\"[^\"]*column:([^;\"`]+)")
)
