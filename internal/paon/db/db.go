package db

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	paonmodels "github.com/mstdn-plusminus-io/paon/internal/paon/models"
	paonschema "github.com/mstdn-plusminus-io/paon/internal/paon/schema"
	"github.com/mstdn-plusminus-io/paon/internal/paon/telemetry"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/plugin/dbresolver"
)

const (
	requiredMastodonSchemaVersion = paonschema.Mastodon4422Version
	minimumPostgreSQLVersionNum   = 130000
)

func Open(cfg config.Config) (*gorm.DB, error) {
	if cfg.DatabaseURL == "" {
		return nil, nil
	}

	primaryDSN := databaseDSNWithLockTimeout(cfg.DatabaseURL, cfg.DatabaseLockTimeout)
	database, err := gorm.Open(postgres.New(postgres.Config{
		DSN:                  primaryDSN,
		PreferSimpleProtocol: !cfg.DatabasePreparedStatements,
	}), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
		Logger:                                   newGORMLogger(os.Stdout, gormLoggerLevel(cfg.RailsLogLevel)),
	})
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(cfg.ReplicaDatabaseURL) != "" {
		replicaDSN := databaseDSNWithLockTimeout(cfg.ReplicaDatabaseURL, cfg.DatabaseLockTimeout)
		if err := database.Use(dbresolver.Register(dbresolver.Config{
			Replicas: []gorm.Dialector{postgres.New(postgres.Config{
				DSN:                  replicaDSN,
				PreferSimpleProtocol: !cfg.ReplicaPreparedStatements,
			})},
		})); err != nil {
			return nil, err
		}
	}
	if err := telemetry.InstrumentGORM(database); err != nil {
		return nil, fmt.Errorf("instrument database: %w", err)
	}

	sqlDB, err := database.DB()
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxOpenConns(cfg.DatabaseMaxOpenConns)
	sqlDB.SetMaxIdleConns(cfg.DatabaseMaxIdleConns)

	return database, nil
}

func databaseDSNWithLockTimeout(raw string, timeout time.Duration) string {
	if timeout <= 0 {
		return raw
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	query := parsed.Query()
	if query.Has("lock_timeout") {
		return raw
	}
	milliseconds := (timeout + time.Millisecond - 1) / time.Millisecond
	query.Set("lock_timeout", strconv.FormatInt(int64(milliseconds), 10))
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func OpenPgHeroStats(cfg config.Config) (*gorm.DB, error) {
	if strings.TrimSpace(cfg.PgHeroStatsDatabaseURL) == "" {
		return nil, nil
	}
	statsCfg := cfg
	statsCfg.DatabaseURL = cfg.PgHeroStatsDatabaseURL
	statsCfg.ReplicaDatabaseURL = ""
	return Open(statsCfg)
}

func OpenPgHeroOther(cfg config.Config) (*gorm.DB, error) {
	if strings.TrimSpace(cfg.PgHeroOtherDatabaseURL) == "" {
		return nil, nil
	}
	otherCfg := cfg
	otherCfg.DatabaseURL = cfg.PgHeroOtherDatabaseURL
	otherCfg.ReplicaDatabaseURL = ""
	return Open(otherCfg)
}

func newGORMLogger(output io.Writer, level logger.LogLevel) logger.Interface {
	return logger.New(log.New(output, "\r\n", log.LstdFlags), logger.Config{
		SlowThreshold:             200 * time.Millisecond,
		LogLevel:                  level,
		IgnoreRecordNotFoundError: true,
		ParameterizedQueries:      true,
		Colorful:                  false,
	})
}

func gormLoggerLevel(railsLogLevel string) logger.LogLevel {
	switch strings.ToLower(strings.TrimSpace(railsLogLevel)) {
	case "debug":
		return logger.Info
	case "info", "warn":
		return logger.Warn
	case "error", "fatal":
		return logger.Error
	case "unknown":
		return logger.Silent
	default:
		return logger.Warn
	}
}

func Available(database *gorm.DB) error {
	if database == nil {
		return errors.New("database connection is not configured; set DATABASE_URL or DB_NAME/DB_HOST")
	}
	sqlDB, err := database.DB()
	if err != nil {
		return err
	}
	return sqlDB.Ping()
}

// RequireSupportedVersion rejects PostgreSQL releases that Mastodon 4.4 no
// longer supports. Keep this separate from Available so callers can report a
// connectivity failure independently from an unsupported server version.
func RequireSupportedVersion(database *gorm.DB) error {
	if database == nil {
		return errors.New("database connection is not configured; set DATABASE_URL or DB_NAME/DB_HOST")
	}
	var version int
	if err := database.Raw("SHOW server_version_num").Scan(&version).Error; err != nil {
		return fmt.Errorf("read PostgreSQL server version: %w", err)
	}
	return validatePostgreSQLVersionNum(version)
}

func validatePostgreSQLVersionNum(version int) error {
	if version < minimumPostgreSQLVersionNum {
		return fmt.Errorf("PostgreSQL 13.0 or newer is required (server_version_num=%d)", version)
	}
	return nil
}

func RequiredMastodonTables() []string {
	return []string{
		"accounts",
		"account_aliases",
		"account_deletion_requests",
		"account_migrations",
		"account_moderation_notes",
		"account_notes",
		"account_stats",
		"account_pins",
		"account_relationship_severance_events",
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
		"follow_recommendation_mutes",
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
		"conversation_mutes",
		"notifications",
		"notification_permissions",
		"notification_policies",
		"notification_requests",
		"markers",
		"tags",
		"statuses_tags",
		"tag_follows",
		"featured_tags",
		"tag_trends",
		"polls",
		"poll_votes",
		"preview_cards",
		"preview_card_providers",
		"preview_card_trends",
		"preview_cards_statuses",
		"relationship_severance_events",
		"scheduled_statuses",
		"announcements",
		"annual_report_statuses_per_account_counts",
		"announcement_mutes",
		"announcement_reactions",
		"appeals",
		"custom_emojis",
		"custom_emoji_categories",
		"custom_filters",
		"custom_filter_keywords",
		"custom_filter_statuses",
		"domain_blocks",
		"domain_allows",
		"account_domain_blocks",
		"email_domain_blocks",
		"canonical_email_blocks",
		"ip_blocks",
		"unavailable_domains",
		"reports",
		"generated_annual_reports",
		"report_notes",
		"rules",
		"invites",
		"backups",
		"bulk_imports",
		"bulk_import_rows",
		"identities",
		"instance_moderation_notes",
		"instances",
		"account_summaries",
		"global_follow_recommendations",
		"login_activities",
		"user_ips",
		"session_activations",
		"severed_relationships",
		"web_settings",
		"web_push_subscriptions",
		"webauthn_credentials",
		"relays",
		"webhooks",
		"site_uploads",
		"software_updates",
		"admin_action_logs",
		"oauth_applications",
		"oauth_access_tokens",
		"oauth_access_grants",
		"settings",
		"terms_of_services",
		"quotes",
		"rule_translations",
		"fasp_providers",
		"fasp_debug_callbacks",
		"fasp_subscriptions",
		"fasp_backfill_requests",
		"fasp_follow_recommendations",
	}
}

func ForbiddenMastodonRelations() []string {
	return []string{"devices", "encrypted_messages", "one_time_keys", "system_keys", "imports"}
}

func ForbiddenMastodonColumns() map[string][]string {
	return map[string][]string{
		"account_relationship_severance_events": {"relationships_count"},
		"accounts":                              {"devices_url"},
		"notification_requests":                 {"dismissed"},
		"notification_policies":                 {"filter_not_followers", "filter_not_following", "filter_new_accounts", "filter_private_mentions"},
		"settings":                              {"thing_type", "thing_id"},
		"users":                                 {"admin", "moderator", "encrypted_otp_secret", "encrypted_otp_secret_iv", "encrypted_otp_secret_salt"},
	}
}

func RequiredMastodonColumns() map[string][]string {
	required := map[string][]string{
		"accounts": {
			"id",
			"username",
			"domain",
			"private_key",
			"public_key",
			"display_name",
			"note",
			"uri",
			"url",
			"created_at",
			"updated_at",
			"attribution_domains",
		},
		"account_aliases": {
			"id",
			"account_id",
			"acct",
			"uri",
		},
		"account_deletion_requests": {
			"id",
			"account_id",
			"created_at",
			"updated_at",
		},
		"account_migrations": {
			"id",
			"account_id",
			"acct",
			"followers_count",
			"target_account_id",
		},
		"account_moderation_notes": {
			"id",
			"account_id",
			"target_account_id",
			"content",
		},
		"account_notes": {
			"id",
			"account_id",
			"target_account_id",
			"comment",
		},
		"account_stats": {
			"account_id",
			"statuses_count",
			"followers_count",
			"following_count",
		},
		"account_pins": {
			"id",
			"account_id",
			"target_account_id",
		},
		"account_relationship_severance_events": {
			"id",
			"account_id",
			"relationship_severance_event_id",
			"followers_count",
			"following_count",
			"created_at",
			"updated_at",
		},
		"account_statuses_cleanup_policies": {
			"id",
			"account_id",
			"enabled",
			"min_status_age",
			"keep_direct",
			"keep_pinned",
			"keep_polls",
			"keep_media",
			"keep_self_fav",
			"keep_self_bookmark",
			"min_favs",
			"min_reblogs",
		},
		"account_warning_presets": {
			"id",
			"text",
			"title",
		},
		"account_warnings": {
			"id",
			"account_id",
			"target_account_id",
			"action",
			"text",
			"status_ids",
		},
		"accounts_tags": {
			"account_id",
			"tag_id",
		},
		"users": {
			"id",
			"account_id",
			"email",
			"encrypted_password",
			"confirmed_at",
			"approved",
			"disabled",
			"locale",
			"settings",
			"role_id",
			"otp_secret",
		},
		"user_roles": {
			"id",
			"name",
			"permissions",
			"highlighted",
		},
		"user_invite_requests": {
			"id",
			"user_id",
			"text",
		},
		"statuses": {
			"id",
			"account_id",
			"text",
			"visibility",
			"created_at",
			"updated_at",
			"deleted_at",
			"reblog_of_id",
			"in_reply_to_id",
			"ordered_media_attachment_ids",
		},
		"status_stats": {
			"status_id",
			"replies_count",
			"reblogs_count",
			"favourites_count",
		},
		"status_edits": {
			"id",
			"status_id",
			"account_id",
			"text",
			"spoiler_text",
			"ordered_media_attachment_ids",
			"media_descriptions",
			"poll_options",
			"sensitive",
		},
		"status_pins": {
			"id",
			"account_id",
			"status_id",
		},
		"status_trends": {
			"id",
			"status_id",
			"account_id",
			"score",
			"rank",
			"allowed",
			"language",
		},
		"tombstones": {
			"id",
			"account_id",
			"uri",
			"by_moderator",
		},
		"mentions": {
			"id",
			"account_id",
			"status_id",
		},
		"media_attachments": {
			"id",
			"account_id",
			"status_id",
			"file_file_name",
			"file_content_type",
			"file_file_size",
			"file_meta",
			"thumbnail_file_name",
			"type",
		},
		"follows": {
			"id",
			"account_id",
			"target_account_id",
			"show_reblogs",
			"notify",
			"languages",
		},
		"follow_recommendation_suppressions": {
			"id",
			"account_id",
		},
		"follow_recommendation_mutes": {
			"id",
			"account_id",
			"target_account_id",
			"created_at",
			"updated_at",
		},
		"follow_requests": {
			"id",
			"account_id",
			"target_account_id",
			"show_reblogs",
			"notify",
			"languages",
		},
		"blocks": {
			"id",
			"account_id",
			"target_account_id",
		},
		"mutes": {
			"id",
			"account_id",
			"target_account_id",
			"hide_notifications",
			"expires_at",
		},
		"favourites": {
			"id",
			"account_id",
			"status_id",
			"created_at",
		},
		"bookmarks": {
			"id",
			"account_id",
			"status_id",
			"created_at",
		},
		"lists": {
			"id",
			"account_id",
			"title",
			"replies_policy",
			"exclusive",
		},
		"list_accounts": {
			"id",
			"list_id",
			"account_id",
			"follow_id",
			"follow_request_id",
		},
		"conversations": {
			"id",
			"uri",
			"created_at",
			"updated_at",
		},
		"account_conversations": {
			"id",
			"account_id",
			"conversation_id",
			"participant_account_ids",
			"status_ids",
			"last_status_id",
			"lock_version",
		},
		"conversation_mutes": {
			"id",
			"account_id",
			"conversation_id",
		},
		"notifications": {
			"id",
			"account_id",
			"from_account_id",
			"activity_id",
			"activity_type",
			"type",
			"filtered",
			"group_key",
		},
		"notification_permissions": {
			"id",
			"account_id",
			"from_account_id",
			"created_at",
			"updated_at",
		},
		"notification_policies": {
			"id",
			"account_id",
			"for_not_following",
			"for_not_followers",
			"for_new_accounts",
			"for_private_mentions",
			"for_limited_accounts",
			"created_at",
			"updated_at",
		},
		"notification_requests": {
			"id",
			"account_id",
			"from_account_id",
			"last_status_id",
			"notifications_count",
			"created_at",
			"updated_at",
		},
		"markers": {
			"id",
			"user_id",
			"timeline",
			"last_read_id",
			"lock_version",
		},
		"tags": {
			"id",
			"name",
			"reviewed_at",
			"last_status_at",
			"display_name",
		},
		"statuses_tags": {
			"status_id",
			"tag_id",
		},
		"tag_follows": {
			"id",
			"tag_id",
			"account_id",
		},
		"featured_tags": {
			"id",
			"account_id",
			"tag_id",
			"statuses_count",
			"name",
		},
		"polls": {
			"id",
			"account_id",
			"status_id",
			"expires_at",
			"options",
			"cached_tallies",
			"multiple",
			"hide_totals",
			"votes_count",
		},
		"poll_votes": {
			"id",
			"account_id",
			"poll_id",
			"choice",
			"uri",
		},
		"preview_cards": {
			"id",
			"url",
			"title",
			"description",
			"type",
			"html",
			"provider_name",
			"provider_url",
			"image_file_name",
			"blurhash",
			"author_account_id",
		},
		"preview_card_providers": {
			"id",
			"domain",
			"trendable",
			"reviewed_at",
			"requested_review_at",
		},
		"preview_card_trends": {
			"id",
			"preview_card_id",
			"score",
			"rank",
			"allowed",
			"language",
		},
		"preview_cards_statuses": {
			"status_id",
			"preview_card_id",
			"url",
		},
		"relationship_severance_events": {
			"id",
			"type",
			"target_name",
			"purged",
			"created_at",
			"updated_at",
		},
		"scheduled_statuses": {
			"id",
			"account_id",
			"scheduled_at",
			"params",
		},
		"announcements": {
			"id",
			"text",
			"published",
			"published_at",
			"scheduled_at",
			"starts_at",
			"ends_at",
		},
		"announcement_mutes": {
			"id",
			"account_id",
			"announcement_id",
		},
		"announcement_reactions": {
			"id",
			"account_id",
			"announcement_id",
			"name",
		},
		"appeals": {
			"id",
			"account_id",
			"account_warning_id",
			"text",
			"approved_at",
			"approved_by_account_id",
			"rejected_at",
			"rejected_by_account_id",
		},
		"custom_emojis": {
			"id",
			"shortcode",
			"domain",
			"image_file_name",
			"image_content_type",
			"image_file_size",
			"disabled",
			"visible_in_picker",
			"category_id",
		},
		"custom_emoji_categories": {
			"id",
			"name",
		},
		"custom_filters": {
			"id",
			"account_id",
			"phrase",
			"context",
			"expires_at",
			"action",
		},
		"custom_filter_keywords": {
			"id",
			"custom_filter_id",
			"keyword",
			"whole_word",
		},
		"custom_filter_statuses": {
			"id",
			"custom_filter_id",
			"status_id",
		},
		"domain_blocks": {
			"id",
			"domain",
			"severity",
			"reject_media",
			"reject_reports",
			"obfuscate",
		},
		"domain_allows": {
			"id",
			"domain",
			"created_at",
			"updated_at",
		},
		"account_domain_blocks": {
			"id",
			"account_id",
			"domain",
		},
		"email_domain_blocks": {
			"id",
			"domain",
			"parent_id",
			"allow_with_approval",
		},
		"canonical_email_blocks": {
			"id",
			"canonical_email_hash",
			"reference_account_id",
		},
		"ip_blocks": {
			"id",
			"ip",
			"severity",
			"expires_at",
			"comment",
		},
		"unavailable_domains": {
			"id",
			"domain",
			"created_at",
			"updated_at",
		},
		"reports": {
			"id",
			"account_id",
			"target_account_id",
			"assigned_account_id",
			"action_taken_at",
			"action_taken_by_account_id",
			"category",
			"status_ids",
			"rule_ids",
			"application_id",
		},
		"report_notes": {
			"id",
			"account_id",
			"report_id",
			"content",
		},
		"rules": {
			"id",
			"text",
			"priority",
			"deleted_at",
			"hint",
		},
		"generated_annual_reports": {
			"id",
			"account_id",
			"year",
			"data",
			"schema_version",
			"viewed_at",
			"created_at",
			"updated_at",
		},
		"invites": {
			"id",
			"user_id",
			"code",
			"expires_at",
			"max_uses",
			"uses",
			"autofollow",
		},
		"backups": {
			"id",
			"user_id",
			"dump_file_name",
			"dump_content_type",
			"dump_file_size",
			"dump_updated_at",
			"processed",
		},
		"bulk_imports": {
			"id",
			"account_id",
			"type",
			"state",
			"total_items",
			"imported_items",
			"processed_items",
			"overwrite",
			"likely_mismatched",
			"original_filename",
		},
		"bulk_import_rows": {
			"id",
			"bulk_import_id",
			"data",
		},
		"identities": {
			"id",
			"user_id",
			"provider",
			"uid",
		},
		"instances": {
			"domain",
			"accounts_count",
		},
		"account_summaries": {
			"account_id",
			"language",
			"sensitive",
		},
		"global_follow_recommendations": {
			"account_id",
			"rank",
			"reason",
		},
		"login_activities": {
			"id",
			"user_id",
			"authentication_method",
			"provider",
			"success",
			"failure_reason",
			"ip",
			"user_agent",
			"created_at",
		},
		"user_ips": {
			"user_id",
			"ip",
			"used_at",
		},
		"session_activations": {
			"id",
			"user_id",
			"session_id",
			"access_token_id",
			"web_push_subscription_id",
			"ip",
			"user_agent",
		},
		"severed_relationships": {
			"id",
			"relationship_severance_event_id",
			"local_account_id",
			"remote_account_id",
			"direction",
			"show_reblogs",
			"notify",
			"languages",
			"created_at",
			"updated_at",
		},
		"web_settings": {
			"id",
			"user_id",
			"data",
		},
		"web_push_subscriptions": {
			"id",
			"endpoint",
			"key_p256dh",
			"key_auth",
			"data",
			"access_token_id",
			"user_id",
		},
		"webauthn_credentials": {
			"id",
			"user_id",
			"external_id",
			"public_key",
			"nickname",
			"sign_count",
		},
		"relays": {
			"id",
			"inbox_url",
			"follow_activity_id",
			"state",
		},
		"webhooks": {
			"id",
			"url",
			"events",
			"secret",
			"enabled",
			"template",
		},
		"site_uploads": {
			"id",
			"var",
			"file_file_name",
			"file_content_type",
			"file_file_size",
			"file_updated_at",
			"meta",
			"blurhash",
		},
		"software_updates": {
			"id",
			"version",
			"urgent",
			"type",
			"release_notes",
		},
		"admin_action_logs": {
			"id",
			"account_id",
			"action",
			"target_type",
			"target_id",
			"human_identifier",
			"route_param",
			"permalink",
		},
		"oauth_applications": {
			"id",
			"uid",
			"secret",
			"redirect_uri",
			"scopes",
		},
		"oauth_access_tokens": {
			"id",
			"token",
			"resource_owner_id",
			"application_id",
			"scopes",
			"revoked_at",
		},
		"oauth_access_grants": {
			"id",
			"token",
			"resource_owner_id",
			"application_id",
			"redirect_uri",
			"scopes",
			"revoked_at",
			"code_challenge",
			"code_challenge_method",
		},
		"settings": {
			"id",
			"var",
			"value",
		},
	}
	for table, columns := range modelBackedMastodonColumns() {
		existing := map[string]struct{}{}
		for _, column := range required[table] {
			existing[column] = struct{}{}
		}
		for _, column := range columns {
			if _, ok := existing[column]; !ok {
				required[table] = append(required[table], column)
				existing[column] = struct{}{}
			}
		}
	}
	return required
}

func RequiredMastodonIndexes() map[string][]string {
	return map[string][]string{
		"accounts": {
			"search_index",
			"index_accounts_on_domain_and_id",
			"index_accounts_on_moved_to_account_id",
			"index_accounts_on_username_and_domain_lower",
			"index_accounts_on_uri",
			"index_accounts_on_url",
		},
		"accounts_tags": {
			"accounts_tags_pkey",
			"index_accounts_tags_on_account_id_and_tag_id",
		},
		"account_aliases": {
			"index_account_aliases_on_account_id_and_uri",
		},
		"account_conversations": {
			"index_unique_conversations",
			"index_account_conversations_on_conversation_id",
		},
		"account_deletion_requests": {
			"index_account_deletion_requests_on_account_id",
		},
		"account_domain_blocks": {
			"index_account_domain_blocks_on_account_id_and_domain",
		},
		"account_migrations": {
			"index_account_migrations_on_account_id",
			"index_account_migrations_on_target_account_id",
		},
		"account_moderation_notes": {
			"index_account_moderation_notes_on_account_id",
			"index_account_moderation_notes_on_target_account_id",
		},
		"account_notes": {
			"index_account_notes_on_account_id_and_target_account_id",
			"index_account_notes_on_target_account_id",
		},
		"account_pins": {
			"index_account_pins_on_account_id_and_target_account_id",
			"index_account_pins_on_target_account_id",
		},
		"account_relationship_severance_events": {
			"idx_on_account_id_relationship_severance_event_id_7bd82bf20e",
			"idx_on_relationship_severance_event_id_403f53e707",
		},
		"account_statuses_cleanup_policies": {
			"index_account_statuses_cleanup_policies_on_account_id",
		},
		"account_stats": {
			"index_account_stats_on_account_id",
			"index_account_stats_on_last_status_at_and_account_id",
		},
		"account_warnings": {
			"index_account_warnings_on_account_id",
			"index_account_warnings_on_target_account_id",
		},
		"announcement_mutes": {
			"index_announcement_mutes_on_account_id_and_announcement_id",
			"index_announcement_mutes_on_announcement_id",
		},
		"announcement_reactions": {
			"index_announcement_reactions_on_account_id_and_announcement_id",
			"index_announcement_reactions_on_announcement_id",
			"index_announcement_reactions_on_custom_emoji_id",
		},
		"admin_action_logs": {
			"index_admin_action_logs_on_account_id",
			"index_admin_action_logs_on_target_type_and_target_id",
		},
		"appeals": {
			"index_appeals_on_account_id",
			"index_appeals_on_account_warning_id",
			"index_appeals_on_approved_by_account_id",
			"index_appeals_on_rejected_by_account_id",
		},
		"bookmarks": {
			"index_bookmarks_on_account_id_and_status_id",
			"index_bookmarks_on_status_id",
		},
		"blocks": {
			"index_blocks_on_account_id_and_target_account_id",
			"index_blocks_on_target_account_id",
		},
		"bulk_import_rows": {
			"index_bulk_import_rows_on_bulk_import_id",
		},
		"bulk_imports": {
			"index_bulk_imports_on_account_id",
			"index_bulk_imports_unconfirmed",
		},
		"canonical_email_blocks": {
			"index_canonical_email_blocks_on_canonical_email_hash",
			"index_canonical_email_blocks_on_reference_account_id",
		},
		"custom_emojis": {
			"index_custom_emojis_on_shortcode_and_domain",
		},
		"custom_emoji_categories": {
			"index_custom_emoji_categories_on_name",
		},
		"custom_filter_keywords": {
			"index_custom_filter_keywords_on_custom_filter_id",
		},
		"custom_filter_statuses": {
			"index_custom_filter_statuses_on_custom_filter_id",
			"index_custom_filter_statuses_on_status_id_and_custom_filter_id",
		},
		"custom_filters": {
			"index_custom_filters_on_account_id",
		},
		"conversation_mutes": {
			"index_conversation_mutes_on_account_id_and_conversation_id",
		},
		"conversations": {
			"index_conversations_on_uri",
		},
		"domain_allows": {
			"index_domain_allows_on_domain",
		},
		"domain_blocks": {
			"index_domain_blocks_on_domain",
		},
		"email_domain_blocks": {
			"index_email_domain_blocks_on_domain",
		},
		"favourites": {
			"index_favourites_on_account_id_and_id",
			"index_favourites_on_account_id_and_status_id",
			"index_favourites_on_status_id",
		},
		"follows": {
			"index_follows_on_account_id_and_target_account_id",
			"index_follows_on_target_account_id",
		},
		"follow_requests": {
			"index_follow_requests_on_account_id_and_target_account_id",
		},
		"featured_tags": {
			"index_featured_tags_on_account_id_and_tag_id",
			"index_featured_tags_on_tag_id",
		},
		"follow_recommendation_suppressions": {
			"index_follow_recommendation_suppressions_on_account_id",
		},
		"follow_recommendation_mutes": {
			"idx_on_account_id_target_account_id_a8c8ddf44e",
			"index_follow_recommendation_mutes_on_target_account_id",
		},
		"generated_annual_reports": {
			"index_generated_annual_reports_on_account_id_and_year",
		},
		"backups": {
			"index_backups_on_user_id",
		},
		"instances": {
			"index_instances_on_domain",
			"index_instances_on_reverse_domain",
		},
		"invites": {
			"index_invites_on_code",
			"index_invites_on_user_id",
		},
		"ip_blocks": {
			"index_ip_blocks_on_ip",
		},
		"list_accounts": {
			"index_list_accounts_on_account_id_and_list_id",
			"index_list_accounts_on_follow_id",
			"index_list_accounts_on_follow_request_id",
			"index_list_accounts_on_list_id_and_account_id",
		},
		"lists": {
			"index_lists_on_account_id",
		},
		"account_summaries": {
			"index_account_summaries_on_account_id",
			"idx_on_account_id_language_sensitive_250461e1eb",
		},
		"global_follow_recommendations": {
			"index_global_follow_recommendations_on_account_id",
		},
		"identities": {
			"index_identities_on_uid_and_provider",
			"index_identities_on_user_id",
		},
		"login_activities": {
			"index_login_activities_on_user_id",
		},
		"markers": {
			"index_markers_on_user_id_and_timeline",
		},
		"media_attachments": {
			"index_media_attachments_on_account_id_and_status_id",
			"index_media_attachments_on_scheduled_status_id",
			"index_media_attachments_on_shortcode",
			"index_media_attachments_on_status_id",
		},
		"mentions": {
			"index_mentions_on_account_id_and_status_id",
			"index_mentions_on_status_id",
		},
		"mutes": {
			"index_mutes_on_account_id_and_target_account_id",
			"index_mutes_on_target_account_id",
		},
		"notifications": {
			"index_notifications_on_account_id_and_group_key",
			"index_notifications_on_account_id_and_id_and_type",
			"index_notifications_on_filtered",
			"index_notifications_on_activity_id_and_activity_type",
			"index_notifications_on_from_account_id",
		},
		"notification_permissions": {
			"index_notification_permissions_on_account_id",
			"index_notification_permissions_on_from_account_id",
		},
		"notification_policies": {
			"index_notification_policies_on_account_id",
		},
		"notification_requests": {
			"index_notification_requests_on_account_id_and_from_account_id",
			"index_notification_requests_on_from_account_id",
			"index_notification_requests_on_last_status_id",
		},
		"oauth_access_grants": {
			"index_oauth_access_grants_on_resource_owner_id",
			"index_oauth_access_grants_on_token",
		},
		"oauth_access_tokens": {
			"index_oauth_access_tokens_on_refresh_token",
			"index_oauth_access_tokens_on_token",
			"index_oauth_access_tokens_on_resource_owner_id",
		},
		"oauth_applications": {
			"index_oauth_applications_on_owner_id_and_owner_type",
			"index_oauth_applications_on_superapp",
			"index_oauth_applications_on_uid",
		},
		"poll_votes": {
			"index_poll_votes_on_account_id",
			"index_poll_votes_on_poll_id",
		},
		"polls": {
			"index_polls_on_account_id",
			"index_polls_on_status_id",
		},
		"preview_card_providers": {
			"index_preview_card_providers_on_domain",
		},
		"preview_card_trends": {
			"index_preview_card_trends_on_preview_card_id",
		},
		"preview_cards": {
			"index_preview_cards_on_author_account_id",
			"index_preview_cards_on_url",
		},
		"preview_cards_statuses": {
			"preview_cards_statuses_pkey",
		},
		"relationship_severance_events": {
			"index_relationship_severance_events_on_type_and_target_name",
		},
		"reports": {
			"index_reports_on_account_id",
			"index_reports_on_action_taken_by_account_id",
			"index_reports_on_assigned_account_id",
			"index_reports_on_target_account_id",
		},
		"report_notes": {
			"index_report_notes_on_account_id",
			"index_report_notes_on_report_id",
		},
		"scheduled_statuses": {
			"index_scheduled_statuses_on_account_id",
			"index_scheduled_statuses_on_scheduled_at",
		},
		"session_activations": {
			"index_session_activations_on_access_token_id",
			"index_session_activations_on_session_id",
			"index_session_activations_on_user_id",
		},
		"severed_relationships": {
			"index_severed_relationships_on_local_account_and_event",
			"index_severed_relationships_on_unique_tuples",
			"index_severed_relationships_on_remote_account_id",
		},
		"settings": {
			"index_settings_on_var",
		},
		"site_uploads": {
			"index_site_uploads_on_var",
		},
		"software_updates": {
			"index_software_updates_on_version",
		},
		"status_pins": {
			"index_status_pins_on_account_id_and_status_id",
			"index_status_pins_on_status_id",
		},
		"status_stats": {
			"index_status_stats_on_status_id",
		},
		"status_trends": {
			"index_status_trends_on_account_id",
			"index_status_trends_on_status_id",
		},
		"status_edits": {
			"index_status_edits_on_account_id",
			"index_status_edits_on_status_id",
		},
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
		"statuses_tags": {
			"statuses_tags_pkey",
			"index_statuses_tags_on_status_id",
		},
		"tag_follows": {
			"index_tag_follows_on_account_id_and_tag_id",
			"index_tag_follows_on_tag_id",
		},
		"tags": {
			"index_tags_on_name_lower_btree",
		},
		"tombstones": {
			"index_tombstones_on_account_id",
			"index_tombstones_on_uri",
		},
		"unavailable_domains": {
			"index_unavailable_domains_on_domain",
		},
		"user_invite_requests": {
			"index_user_invite_requests_on_user_id",
		},
		"users": {
			"index_users_on_account_id",
			"index_users_on_confirmation_token",
			"index_users_on_created_by_application_id",
			"index_users_on_email",
			"index_users_on_reset_password_token",
			"index_users_on_role_id",
			"index_users_on_unconfirmed_email",
		},
		"web_push_subscriptions": {
			"index_web_push_subscriptions_on_access_token_id",
			"index_web_push_subscriptions_on_user_id",
		},
		"web_settings": {
			"index_web_settings_on_user_id",
		},
		"webhooks": {
			"index_webhooks_on_url",
		},
		"webauthn_credentials": {
			"index_webauthn_credentials_on_external_id",
			"index_webauthn_credentials_on_user_id_and_nickname",
		},
		"annual_report_statuses_per_account_counts": {"idx_on_year_account_id_ff3e167cef"},
		"tag_trends":                  {"index_tag_trends_on_tag_id_and_language"},
		"terms_of_services":           {"index_terms_of_services_on_effective_date"},
		"fasp_providers":              {"index_fasp_providers_on_base_url"},
		"fasp_debug_callbacks":        {"index_fasp_debug_callbacks_on_fasp_provider_id"},
		"fasp_subscriptions":          {"index_fasp_subscriptions_on_fasp_provider_id"},
		"fasp_backfill_requests":      {"index_fasp_backfill_requests_on_fasp_provider_id"},
		"fasp_follow_recommendations": {"index_fasp_follow_recommendations_on_requesting_account_id", "index_fasp_follow_recommendations_on_recommended_account_id"},
		"instance_moderation_notes":   {"index_instance_moderation_notes_on_domain"},
		"quotes":                      {"index_quotes_on_account_id_and_quoted_account_id", "index_quotes_on_activity_uri", "index_quotes_on_approval_uri", "index_quotes_on_quoted_account_id", "index_quotes_on_quoted_status_id", "index_quotes_on_status_id"},
		"rule_translations":           {"index_rule_translations_on_rule_id_and_language"},
	}
}

func RequiredMastodonUniqueIndexes() []string {
	return []string{
		"index_unique_conversations",
		"index_account_aliases_on_account_id_and_uri",
		"idx_on_account_id_relationship_severance_event_id_7bd82bf20e",
		"index_account_domain_blocks_on_account_id_and_domain",
		"index_account_notes_on_account_id_and_target_account_id",
		"index_account_pins_on_account_id_and_target_account_id",
		"index_account_stats_on_account_id",
		"index_accounts_on_username_and_domain_lower",
		"index_announcement_mutes_on_account_id_and_announcement_id",
		"index_announcement_reactions_on_account_id_and_announcement_id",
		"index_appeals_on_account_warning_id",
		"index_blocks_on_account_id_and_target_account_id",
		"index_bookmarks_on_account_id_and_status_id",
		"index_canonical_email_blocks_on_canonical_email_hash",
		"index_conversation_mutes_on_account_id_and_conversation_id",
		"index_conversations_on_uri",
		"index_custom_emoji_categories_on_name",
		"index_custom_emojis_on_shortcode_and_domain",
		"index_custom_filter_statuses_on_status_id_and_custom_filter_id",
		"index_domain_allows_on_domain",
		"index_domain_blocks_on_domain",
		"index_email_domain_blocks_on_domain",
		"index_favourites_on_account_id_and_status_id",
		"index_featured_tags_on_account_id_and_tag_id",
		"idx_on_account_id_target_account_id_a8c8ddf44e",
		"index_follow_recommendation_suppressions_on_account_id",
		"index_follow_requests_on_account_id_and_target_account_id",
		"index_follows_on_account_id_and_target_account_id",
		"index_invites_on_code",
		"index_generated_annual_reports_on_account_id_and_year",
		"index_identities_on_uid_and_provider",
		"index_ip_blocks_on_ip",
		"index_list_accounts_on_account_id_and_list_id",
		"index_markers_on_user_id_and_timeline",
		"index_media_attachments_on_shortcode",
		"index_mentions_on_account_id_and_status_id",
		"index_mutes_on_account_id_and_target_account_id",
		"index_notification_policies_on_account_id",
		"index_notification_requests_on_account_id_and_from_account_id",
		"index_oauth_access_grants_on_token",
		"index_oauth_access_tokens_on_refresh_token",
		"index_oauth_access_tokens_on_token",
		"index_oauth_applications_on_uid",
		"index_preview_card_providers_on_domain",
		"index_preview_card_trends_on_preview_card_id",
		"index_preview_cards_on_url",
		"index_severed_relationships_on_unique_tuples",
		"index_session_activations_on_session_id",
		"index_settings_on_var",
		"index_site_uploads_on_var",
		"index_software_updates_on_version",
		"index_status_pins_on_account_id_and_status_id",
		"index_status_stats_on_status_id",
		"index_status_trends_on_status_id",
		"index_statuses_on_uri",
		"index_tag_follows_on_account_id_and_tag_id",
		"index_tags_on_name_lower_btree",
		"index_unavailable_domains_on_domain",
		"index_users_on_confirmation_token",
		"index_users_on_email",
		"index_users_on_reset_password_token",
		"index_web_settings_on_user_id",
		"index_webauthn_credentials_on_external_id",
		"index_webauthn_credentials_on_user_id_and_nickname",
		"index_webhooks_on_url",
		"index_instances_on_domain",
		"index_account_summaries_on_account_id",
		"index_global_follow_recommendations_on_account_id",
		"idx_on_year_account_id_ff3e167cef",
		"index_tag_trends_on_tag_id_and_language",
		"index_terms_of_services_on_effective_date",
		"index_fasp_providers_on_base_url",
		"index_quotes_on_activity_uri",
		"index_quotes_on_status_id",
		"index_rule_translations_on_rule_id_and_language",
	}
}

func RequiredMastodonIndexDefinitionFragments() map[string][]string {
	return map[string][]string{
		"index_announcement_reactions_on_custom_emoji_id": {
			"custom_emoji_id",
			"WHERE (custom_emoji_id IS NOT NULL)",
		},
		"index_account_migrations_on_target_account_id": {
			"target_account_id",
			"WHERE (target_account_id IS NOT NULL)",
		},
		"index_account_stats_on_last_status_at_and_account_id": {
			"last_status_at",
			"DESC NULLS LAST",
			"account_id",
		},
		"search_index": {
			"gin",
			"to_tsvector",
			"display_name",
			"username",
			"domain",
		},
		"index_accounts_on_moved_to_account_id": {
			"moved_to_account_id",
			"WHERE (moved_to_account_id IS NOT NULL)",
		},
		"index_accounts_on_username_and_domain_lower": {
			"lower((username)::text)",
			"COALESCE(lower((domain)::text), ''::text)",
		},
		"index_appeals_on_approved_by_account_id": {
			"approved_by_account_id",
			"WHERE (approved_by_account_id IS NOT NULL)",
		},
		"index_appeals_on_rejected_by_account_id": {
			"rejected_by_account_id",
			"WHERE (rejected_by_account_id IS NOT NULL)",
		},
		"index_bulk_imports_unconfirmed": {
			"id",
			"WHERE (state = 0)",
		},
		"index_list_accounts_on_follow_id": {
			"follow_id",
			"WHERE (follow_id IS NOT NULL)",
		},
		"index_list_accounts_on_follow_request_id": {
			"follow_request_id",
			"WHERE (follow_request_id IS NOT NULL)",
		},
		"index_conversations_on_uri": {
			"uri text_pattern_ops",
			"WHERE (uri IS NOT NULL)",
		},
		"index_media_attachments_on_shortcode": {
			"shortcode text_pattern_ops",
			"WHERE (shortcode IS NOT NULL)",
		},
		"index_oauth_access_tokens_on_refresh_token": {
			"refresh_token text_pattern_ops",
			"WHERE (refresh_token IS NOT NULL)",
		},
		"index_oauth_access_tokens_on_resource_owner_id": {
			"resource_owner_id",
			"WHERE (resource_owner_id IS NOT NULL)",
		},
		"index_oauth_applications_on_superapp": {
			"superapp",
			"WHERE (superapp = true)",
		},
		"index_notifications_on_account_id_and_group_key": {
			"account_id",
			"group_key",
			"WHERE (group_key IS NOT NULL)",
		},
		"index_notifications_on_filtered": {
			"account_id",
			"id DESC",
			"type",
			"WHERE (filtered = false)",
		},
		"index_preview_cards_on_author_account_id": {
			"author_account_id",
			"WHERE (author_account_id IS NOT NULL)",
		},
		"index_reports_on_action_taken_by_account_id": {
			"action_taken_by_account_id",
			"WHERE (action_taken_by_account_id IS NOT NULL)",
		},
		"index_statuses_20190820": {
			"account_id",
			"id",
			"visibility",
			"updated_at",
			"WHERE (deleted_at IS NULL)",
		},
		"index_statuses_on_deleted_at": {
			"deleted_at",
			"WHERE (deleted_at IS NOT NULL)",
		},
		"index_statuses_on_in_reply_to_id": {
			"in_reply_to_id",
			"WHERE (in_reply_to_id IS NOT NULL)",
		},
		"index_statuses_local_20190824": {
			"id",
			"account_id",
			"local",
			"deleted_at IS NULL",
			"visibility = 0",
			"reblog_of_id IS NULL",
		},
		"index_statuses_public_20250129": {
			"id DESC",
			"language",
			"account_id",
			"deleted_at IS NULL",
			"visibility = 0",
			"reblog_of_id IS NULL",
		},
		"index_statuses_on_uri": {
			"uri text_pattern_ops",
			"WHERE (uri IS NOT NULL)",
		},
		"index_tags_on_name_lower_btree": {
			"lower((name)::text)",
			"text_pattern_ops",
		},
		"index_users_on_reset_password_token": {
			"reset_password_token text_pattern_ops",
			"WHERE (reset_password_token IS NOT NULL)",
		},
		"index_users_on_created_by_application_id": {
			"created_by_application_id",
			"WHERE (created_by_application_id IS NOT NULL)",
		},
		"index_users_on_role_id": {
			"role_id",
			"WHERE (role_id IS NOT NULL)",
		},
		"index_users_on_unconfirmed_email": {
			"unconfirmed_email",
			"WHERE (unconfirmed_email IS NOT NULL)",
		},
		"index_web_push_subscriptions_on_access_token_id": {
			"access_token_id",
			"WHERE (access_token_id IS NOT NULL)",
		},
		"index_terms_of_services_on_effective_date": {
			"effective_date",
			"WHERE (effective_date IS NOT NULL)",
		},
		"index_quotes_on_activity_uri": {
			"activity_uri",
			"WHERE (activity_uri IS NOT NULL)",
		},
		"index_quotes_on_approval_uri": {
			"approval_uri",
			"WHERE (approval_uri IS NOT NULL)",
		},
	}
}

func RequiredMastodonPrimaryKeys() map[string][]string {
	return map[string][]string{
		"account_aliases":                       {"id"},
		"account_conversations":                 {"id"},
		"account_deletion_requests":             {"id"},
		"account_domain_blocks":                 {"id"},
		"account_migrations":                    {"id"},
		"account_moderation_notes":              {"id"},
		"account_notes":                         {"id"},
		"account_pins":                          {"id"},
		"account_relationship_severance_events": {"id"},
		"account_stats":                         {"id"},
		"account_statuses_cleanup_policies":     {"id"},
		"account_warning_presets":               {"id"},
		"account_warnings":                      {"id"},
		"accounts":                              {"id"},
		"accounts_tags":                         {"tag_id", "account_id"},
		"announcement_mutes":                    {"id"},
		"announcement_reactions":                {"id"},
		"announcements":                         {"id"},
		"appeals":                               {"id"},
		"backups":                               {"id"},
		"blocks":                                {"id"},
		"bookmarks":                             {"id"},
		"bulk_import_rows":                      {"id"},
		"bulk_imports":                          {"id"},
		"canonical_email_blocks":                {"id"},
		"conversation_mutes":                    {"id"},
		"conversations":                         {"id"},
		"custom_emoji_categories":               {"id"},
		"custom_emojis":                         {"id"},
		"custom_filter_keywords":                {"id"},
		"custom_filter_statuses":                {"id"},
		"custom_filters":                        {"id"},
		"domain_allows":                         {"id"},
		"domain_blocks":                         {"id"},
		"email_domain_blocks":                   {"id"},
		"favourites":                            {"id"},
		"featured_tags":                         {"id"},
		"follow_recommendation_mutes":           {"id"},
		"follow_recommendation_suppressions":    {"id"},
		"follow_requests":                       {"id"},
		"follows":                               {"id"},
		"generated_annual_reports":              {"id"},
		"identities":                            {"id"},
		"invites":                               {"id"},
		"ip_blocks":                             {"id"},
		"list_accounts":                         {"id"},
		"lists":                                 {"id"},
		"login_activities":                      {"id"},
		"markers":                               {"id"},
		"media_attachments":                     {"id"},
		"mentions":                              {"id"},
		"mutes":                                 {"id"},
		"notification_permissions":              {"id"},
		"notification_policies":                 {"id"},
		"notification_requests":                 {"id"},
		"notifications":                         {"id"},
		"oauth_access_grants":                   {"id"},
		"oauth_access_tokens":                   {"id"},
		"oauth_applications":                    {"id"},
		"poll_votes":                            {"id"},
		"polls":                                 {"id"},
		"preview_card_providers":                {"id"},
		"preview_card_trends":                   {"id"},
		"preview_cards":                         {"id"},
		"preview_cards_statuses":                {"status_id", "preview_card_id"},
		"relationship_severance_events":         {"id"},
		"report_notes":                          {"id"},
		"reports":                               {"id"},
		"rules":                                 {"id"},
		"scheduled_statuses":                    {"id"},
		"session_activations":                   {"id"},
		"severed_relationships":                 {"id"},
		"settings":                              {"id"},
		"site_uploads":                          {"id"},
		"software_updates":                      {"id"},
		"status_edits":                          {"id"},
		"status_pins":                           {"id"},
		"status_stats":                          {"id"},
		"status_trends":                         {"id"},
		"statuses":                              {"id"},
		"statuses_tags":                         {"tag_id", "status_id"},
		"tag_follows":                           {"id"},
		"tags":                                  {"id"},
		"tombstones":                            {"id"},
		"unavailable_domains":                   {"id"},
		"user_invite_requests":                  {"id"},
		"user_roles":                            {"id"},
		"users":                                 {"id"},
		"web_push_subscriptions":                {"id"},
		"web_settings":                          {"id"},
		"webauthn_credentials":                  {"id"},
		"webhooks":                              {"id"},
		"annual_report_statuses_per_account_counts": {"id"},
		"tag_trends":                  {"id"},
		"terms_of_services":           {"id"},
		"fasp_providers":              {"id"},
		"fasp_debug_callbacks":        {"id"},
		"fasp_subscriptions":          {"id"},
		"fasp_backfill_requests":      {"id"},
		"fasp_follow_recommendations": {"id"},
		"instance_moderation_notes":   {"id"},
		"quotes":                      {"id"},
		"rule_translations":           {"id"},
	}
}

type MastodonColumnDefinition struct {
	Table             string
	Column            string
	NotNull           bool
	MustBeNullable    bool
	DefaultMustBeNull bool
	DataType          string
	DefaultFragments  []string
}

func RequiredMastodonColumnDefinitions() []MastodonColumnDefinition {
	return []MastodonColumnDefinition{
		{Table: "accounts", Column: "username", NotNull: true, DefaultFragments: []string{"''::character varying"}},
		{Table: "accounts", Column: "public_key", NotNull: true, DefaultFragments: []string{"''::text"}},
		{Table: "accounts", Column: "note", NotNull: true, DefaultFragments: []string{"''::text"}},
		{Table: "accounts", Column: "display_name", NotNull: true, DefaultFragments: []string{"''::character varying"}},
		{Table: "accounts", Column: "uri", NotNull: true, DefaultFragments: []string{"''::character varying"}},
		{Table: "accounts", Column: "header_remote_url", NotNull: true, DefaultFragments: []string{"''::character varying"}},
		{Table: "accounts", Column: "inbox_url", NotNull: true, DefaultFragments: []string{"''::character varying"}},
		{Table: "accounts", Column: "outbox_url", NotNull: true, DefaultFragments: []string{"''::character varying"}},
		{Table: "accounts", Column: "shared_inbox_url", NotNull: true, DefaultFragments: []string{"''::character varying"}},
		{Table: "accounts", Column: "followers_url", NotNull: true, DefaultFragments: []string{"''::character varying"}},
		{Table: "accounts", Column: "locked", NotNull: true, DefaultFragments: []string{"false"}},
		{Table: "accounts", Column: "protocol", NotNull: true, DefaultFragments: []string{"0"}},
		{Table: "accounts", Column: "memorial", NotNull: true, DefaultFragments: []string{"false"}},
		{Table: "accounts", Column: "indexable", NotNull: true, DefaultFragments: []string{"false"}},
		{Table: "accounts", Column: "attribution_domains", MustBeNullable: true, DataType: "_varchar", DefaultFragments: []string{"'{}'::character varying[]"}},
		{Table: "accounts_tags", Column: "account_id", NotNull: true},
		{Table: "accounts_tags", Column: "tag_id", NotNull: true},
		{Table: "account_aliases", Column: "account_id", NotNull: true},
		{Table: "account_aliases", Column: "acct", NotNull: true, DefaultFragments: []string{"''::character varying"}},
		{Table: "account_aliases", Column: "uri", NotNull: true, DefaultFragments: []string{"''::character varying"}},
		{Table: "account_conversations", Column: "account_id", NotNull: true},
		{Table: "account_conversations", Column: "conversation_id", NotNull: true},
		{Table: "account_deletion_requests", Column: "account_id", NotNull: true},
		{Table: "account_domain_blocks", Column: "account_id", NotNull: true},
		{Table: "account_domain_blocks", Column: "domain", NotNull: true},
		{Table: "account_migrations", Column: "acct", NotNull: true, DefaultFragments: []string{"''::character varying"}},
		{Table: "account_migrations", Column: "followers_count", NotNull: true, DefaultFragments: []string{"0"}},
		{Table: "account_moderation_notes", Column: "content", NotNull: true},
		{Table: "account_moderation_notes", Column: "account_id", NotNull: true},
		{Table: "account_moderation_notes", Column: "target_account_id", NotNull: true},
		{Table: "account_stats", Column: "account_id", NotNull: true},
		{Table: "account_stats", Column: "statuses_count", NotNull: true, DefaultFragments: []string{"0"}},
		{Table: "account_stats", Column: "followers_count", NotNull: true, DefaultFragments: []string{"0"}},
		{Table: "account_stats", Column: "following_count", NotNull: true, DefaultFragments: []string{"0"}},
		{Table: "account_statuses_cleanup_policies", Column: "account_id", NotNull: true},
		{Table: "account_statuses_cleanup_policies", Column: "enabled", NotNull: true, DefaultFragments: []string{"true"}},
		{Table: "account_statuses_cleanup_policies", Column: "min_status_age", NotNull: true, DefaultFragments: []string{"1209600"}},
		{Table: "account_statuses_cleanup_policies", Column: "keep_direct", NotNull: true, DefaultFragments: []string{"true"}},
		{Table: "account_statuses_cleanup_policies", Column: "keep_pinned", NotNull: true, DefaultFragments: []string{"true"}},
		{Table: "account_statuses_cleanup_policies", Column: "keep_polls", NotNull: true, DefaultFragments: []string{"false"}},
		{Table: "account_statuses_cleanup_policies", Column: "keep_media", NotNull: true, DefaultFragments: []string{"false"}},
		{Table: "account_statuses_cleanup_policies", Column: "keep_self_fav", NotNull: true, DefaultFragments: []string{"true"}},
		{Table: "account_statuses_cleanup_policies", Column: "keep_self_bookmark", NotNull: true, DefaultFragments: []string{"true"}},
		{Table: "account_notes", Column: "account_id", NotNull: true},
		{Table: "account_notes", Column: "target_account_id", NotNull: true},
		{Table: "account_notes", Column: "comment", NotNull: true},
		{Table: "account_pins", Column: "account_id", NotNull: true},
		{Table: "account_pins", Column: "target_account_id", NotNull: true},
		{Table: "account_relationship_severance_events", Column: "account_id", NotNull: true},
		{Table: "account_relationship_severance_events", Column: "relationship_severance_event_id", NotNull: true},
		{Table: "account_relationship_severance_events", Column: "followers_count", NotNull: true, DefaultFragments: []string{"0"}},
		{Table: "account_relationship_severance_events", Column: "following_count", NotNull: true, DefaultFragments: []string{"0"}},
		{Table: "account_warning_presets", Column: "text", NotNull: true, DefaultFragments: []string{"''::text"}},
		{Table: "account_warning_presets", Column: "title", NotNull: true, DefaultFragments: []string{"''::character varying"}},
		{Table: "account_warnings", Column: "action", NotNull: true, DefaultFragments: []string{"0"}},
		{Table: "account_warnings", Column: "text", NotNull: true, DefaultFragments: []string{"''::text"}},
		{Table: "admin_action_logs", Column: "account_id", NotNull: true},
		{Table: "admin_action_logs", Column: "action", NotNull: true, DefaultFragments: []string{"''::character varying"}},
		{Table: "account_conversations", Column: "participant_account_ids", NotNull: true, DefaultFragments: []string{"'{}'::bigint[]"}},
		{Table: "account_conversations", Column: "status_ids", NotNull: true, DefaultFragments: []string{"'{}'::bigint[]"}},
		{Table: "account_conversations", Column: "lock_version", NotNull: true, DefaultFragments: []string{"0"}},
		{Table: "account_conversations", Column: "unread", NotNull: true, DefaultFragments: []string{"false"}},
		{Table: "announcement_mutes", Column: "account_id", NotNull: true},
		{Table: "announcement_mutes", Column: "announcement_id", NotNull: true},
		{Table: "announcement_reactions", Column: "account_id", NotNull: true},
		{Table: "announcement_reactions", Column: "announcement_id", NotNull: true},
		{Table: "announcement_reactions", Column: "name", NotNull: true, DefaultFragments: []string{"''::character varying"}},
		{Table: "announcements", Column: "text", NotNull: true, DefaultFragments: []string{"''::text"}},
		{Table: "announcements", Column: "published", NotNull: true, DefaultFragments: []string{"false"}},
		{Table: "announcements", Column: "all_day", NotNull: true, DefaultFragments: []string{"false"}},
		{Table: "announcements", Column: "notification_sent_at", MustBeNullable: true, DataType: "timestamp"},
		{Table: "annual_report_statuses_per_account_counts", Column: "year", NotNull: true, DataType: "int4"},
		{Table: "annual_report_statuses_per_account_counts", Column: "account_id", NotNull: true, DataType: "int8"},
		{Table: "annual_report_statuses_per_account_counts", Column: "statuses_count", NotNull: true, DataType: "int8"},
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
		{Table: "custom_filters", Column: "account_id", NotNull: true},
		{Table: "custom_filters", Column: "phrase", NotNull: true, DefaultFragments: []string{"''::text"}},
		{Table: "custom_filters", Column: "context", NotNull: true, DefaultFragments: []string{"ARRAY[]::character varying[]"}},
		{Table: "custom_filters", Column: "action", NotNull: true, DefaultFragments: []string{"0"}},
		{Table: "custom_emojis", Column: "shortcode", NotNull: true, DefaultFragments: []string{"''::character varying"}},
		{Table: "custom_emojis", Column: "disabled", NotNull: true, DefaultFragments: []string{"false"}},
		{Table: "custom_emojis", Column: "visible_in_picker", NotNull: true, DefaultFragments: []string{"true"}},
		{Table: "conversation_mutes", Column: "conversation_id", NotNull: true},
		{Table: "conversation_mutes", Column: "account_id", NotNull: true},
		{Table: "domain_allows", Column: "domain", NotNull: true, DefaultFragments: []string{"''::character varying"}},
		{Table: "domain_blocks", Column: "domain", NotNull: true, DefaultFragments: []string{"''::character varying"}},
		{Table: "domain_blocks", Column: "severity", DefaultFragments: []string{"0"}},
		{Table: "domain_blocks", Column: "reject_media", NotNull: true, DefaultFragments: []string{"false"}},
		{Table: "domain_blocks", Column: "reject_reports", NotNull: true, DefaultFragments: []string{"false"}},
		{Table: "domain_blocks", Column: "obfuscate", NotNull: true, DefaultFragments: []string{"false"}},
		{Table: "email_domain_blocks", Column: "domain", NotNull: true, DefaultFragments: []string{"''::character varying"}},
		{Table: "email_domain_blocks", Column: "allow_with_approval", NotNull: true, DataType: "bool", DefaultFragments: []string{"false"}},
		{Table: "favourites", Column: "account_id", NotNull: true},
		{Table: "favourites", Column: "status_id", NotNull: true},
		{Table: "featured_tags", Column: "account_id", NotNull: true},
		{Table: "featured_tags", Column: "tag_id", NotNull: true},
		{Table: "featured_tags", Column: "statuses_count", NotNull: true, DefaultFragments: []string{"0"}},
		{Table: "follow_recommendation_suppressions", Column: "account_id", NotNull: true},
		{Table: "follow_recommendation_mutes", Column: "account_id", NotNull: true, DataType: "int8"},
		{Table: "follow_recommendation_mutes", Column: "target_account_id", NotNull: true, DataType: "int8"},
		{Table: "generated_annual_reports", Column: "account_id", NotNull: true, DataType: "int8"},
		{Table: "generated_annual_reports", Column: "year", NotNull: true, DataType: "int4"},
		{Table: "generated_annual_reports", Column: "data", NotNull: true, DataType: "jsonb"},
		{Table: "generated_annual_reports", Column: "schema_version", NotNull: true, DataType: "int4"},
		{Table: "generated_annual_reports", Column: "viewed_at", MustBeNullable: true, DataType: "timestamp"},
		{Table: "fasp_providers", Column: "confirmed", NotNull: true, DataType: "bool", DefaultFragments: []string{"false"}},
		{Table: "fasp_providers", Column: "name", NotNull: true, DataType: "varchar"},
		{Table: "fasp_providers", Column: "base_url", NotNull: true, DataType: "varchar"},
		{Table: "fasp_providers", Column: "sign_in_url", MustBeNullable: true, DataType: "varchar"},
		{Table: "fasp_providers", Column: "remote_identifier", NotNull: true, DataType: "varchar"},
		{Table: "fasp_providers", Column: "provider_public_key_pem", NotNull: true, DataType: "varchar"},
		{Table: "fasp_providers", Column: "server_private_key_pem", NotNull: true, DataType: "varchar"},
		{Table: "fasp_providers", Column: "capabilities", NotNull: true, DataType: "jsonb", DefaultFragments: []string{"'[]'::jsonb"}},
		{Table: "fasp_providers", Column: "privacy_policy", MustBeNullable: true, DataType: "jsonb"},
		{Table: "fasp_providers", Column: "contact_email", MustBeNullable: true, DataType: "varchar"},
		{Table: "fasp_providers", Column: "fediverse_account", MustBeNullable: true, DataType: "varchar"},
		{Table: "fasp_providers", Column: "created_at", NotNull: true, DataType: "timestamp", DefaultMustBeNull: true},
		{Table: "fasp_providers", Column: "updated_at", NotNull: true, DataType: "timestamp", DefaultMustBeNull: true},
		{Table: "fasp_debug_callbacks", Column: "fasp_provider_id", NotNull: true, DataType: "int8"},
		{Table: "fasp_debug_callbacks", Column: "ip", NotNull: true, DataType: "varchar"},
		{Table: "fasp_debug_callbacks", Column: "request_body", NotNull: true, DataType: "text"},
		{Table: "fasp_debug_callbacks", Column: "created_at", NotNull: true, DataType: "timestamp", DefaultMustBeNull: true},
		{Table: "fasp_debug_callbacks", Column: "updated_at", NotNull: true, DataType: "timestamp", DefaultMustBeNull: true},
		{Table: "fasp_subscriptions", Column: "category", NotNull: true, DataType: "varchar"},
		{Table: "fasp_subscriptions", Column: "subscription_type", NotNull: true, DataType: "varchar"},
		{Table: "fasp_subscriptions", Column: "max_batch_size", NotNull: true, DataType: "int4"},
		{Table: "fasp_subscriptions", Column: "threshold_timeframe", MustBeNullable: true, DataType: "int4"},
		{Table: "fasp_subscriptions", Column: "threshold_shares", MustBeNullable: true, DataType: "int4"},
		{Table: "fasp_subscriptions", Column: "threshold_likes", MustBeNullable: true, DataType: "int4"},
		{Table: "fasp_subscriptions", Column: "threshold_replies", MustBeNullable: true, DataType: "int4"},
		{Table: "fasp_subscriptions", Column: "fasp_provider_id", NotNull: true, DataType: "int8"},
		{Table: "fasp_subscriptions", Column: "created_at", NotNull: true, DataType: "timestamp", DefaultMustBeNull: true},
		{Table: "fasp_subscriptions", Column: "updated_at", NotNull: true, DataType: "timestamp", DefaultMustBeNull: true},
		{Table: "fasp_backfill_requests", Column: "category", NotNull: true, DataType: "varchar"},
		{Table: "fasp_backfill_requests", Column: "max_count", NotNull: true, DataType: "int4", DefaultFragments: []string{"100"}},
		{Table: "fasp_backfill_requests", Column: "cursor", MustBeNullable: true, DataType: "varchar"},
		{Table: "fasp_backfill_requests", Column: "fulfilled", NotNull: true, DataType: "bool", DefaultFragments: []string{"false"}},
		{Table: "fasp_backfill_requests", Column: "fasp_provider_id", NotNull: true, DataType: "int8"},
		{Table: "fasp_backfill_requests", Column: "created_at", NotNull: true, DataType: "timestamp", DefaultMustBeNull: true},
		{Table: "fasp_backfill_requests", Column: "updated_at", NotNull: true, DataType: "timestamp", DefaultMustBeNull: true},
		{Table: "fasp_follow_recommendations", Column: "requesting_account_id", NotNull: true, DataType: "int8"},
		{Table: "fasp_follow_recommendations", Column: "recommended_account_id", NotNull: true, DataType: "int8"},
		{Table: "fasp_follow_recommendations", Column: "created_at", NotNull: true, DataType: "timestamp", DefaultMustBeNull: true},
		{Table: "fasp_follow_recommendations", Column: "updated_at", NotNull: true, DataType: "timestamp", DefaultMustBeNull: true},
		{Table: "follow_requests", Column: "account_id", NotNull: true},
		{Table: "follow_requests", Column: "target_account_id", NotNull: true},
		{Table: "follow_requests", Column: "show_reblogs", NotNull: true, DefaultFragments: []string{"true"}},
		{Table: "follow_requests", Column: "notify", NotNull: true, DefaultFragments: []string{"false"}},
		{Table: "follows", Column: "account_id", NotNull: true},
		{Table: "follows", Column: "target_account_id", NotNull: true},
		{Table: "follows", Column: "show_reblogs", NotNull: true, DefaultFragments: []string{"true"}},
		{Table: "follows", Column: "notify", NotNull: true, DefaultFragments: []string{"false"}},
		{Table: "identities", Column: "provider", NotNull: true, DefaultFragments: []string{"''::character varying"}},
		{Table: "identities", Column: "uid", NotNull: true, DefaultFragments: []string{"''::character varying"}},
		{Table: "instance_moderation_notes", Column: "domain", NotNull: true, DataType: "varchar"},
		{Table: "instance_moderation_notes", Column: "account_id", NotNull: true, DataType: "int8"},
		{Table: "instance_moderation_notes", Column: "content", MustBeNullable: true, DataType: "text"},
		{Table: "instance_moderation_notes", Column: "created_at", NotNull: true, DataType: "timestamp", DefaultMustBeNull: true},
		{Table: "instance_moderation_notes", Column: "updated_at", NotNull: true, DataType: "timestamp", DefaultMustBeNull: true},
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
		{Table: "markers", Column: "user_id", NotNull: true},
		{Table: "markers", Column: "timeline", NotNull: true, DefaultFragments: []string{"''::character varying"}},
		{Table: "markers", Column: "last_read_id", NotNull: true, DefaultFragments: []string{"0"}},
		{Table: "markers", Column: "lock_version", NotNull: true, DefaultFragments: []string{"0"}},
		{Table: "media_attachments", Column: "remote_url", NotNull: true, DefaultFragments: []string{"''::character varying"}},
		{Table: "media_attachments", Column: "type", NotNull: true, DefaultFragments: []string{"0"}},
		{Table: "mentions", Column: "silent", NotNull: true, DefaultFragments: []string{"false"}},
		{Table: "mentions", Column: "status_id", NotNull: true, DataType: "int8"},
		{Table: "mentions", Column: "account_id", NotNull: true, DataType: "int8"},
		{Table: "mutes", Column: "account_id", NotNull: true},
		{Table: "mutes", Column: "target_account_id", NotNull: true},
		{Table: "mutes", Column: "hide_notifications", NotNull: true, DefaultFragments: []string{"true"}},
		{Table: "notifications", Column: "activity_id", NotNull: true},
		{Table: "notifications", Column: "activity_type", NotNull: true},
		{Table: "notifications", Column: "account_id", NotNull: true},
		{Table: "notifications", Column: "from_account_id", NotNull: true},
		{Table: "notifications", Column: "filtered", NotNull: true, DataType: "bool", DefaultFragments: []string{"false"}},
		{Table: "notifications", Column: "group_key", MustBeNullable: true, DataType: "varchar"},
		{Table: "notification_permissions", Column: "account_id", NotNull: true, DataType: "int8"},
		{Table: "notification_permissions", Column: "from_account_id", NotNull: true, DataType: "int8"},
		{Table: "notification_policies", Column: "account_id", NotNull: true, DataType: "int8"},
		{Table: "notification_policies", Column: "for_not_following", NotNull: true, DataType: "int4", DefaultFragments: []string{"0"}},
		{Table: "notification_policies", Column: "for_not_followers", NotNull: true, DataType: "int4", DefaultFragments: []string{"0"}},
		{Table: "notification_policies", Column: "for_new_accounts", NotNull: true, DataType: "int4", DefaultFragments: []string{"0"}},
		{Table: "notification_policies", Column: "for_private_mentions", NotNull: true, DataType: "int4", DefaultFragments: []string{"1"}},
		{Table: "notification_policies", Column: "for_limited_accounts", NotNull: true, DataType: "int4", DefaultFragments: []string{"1"}},
		{Table: "notification_requests", Column: "id", NotNull: true, DataType: "int8", DefaultFragments: []string{"timestamp_id('notification_requests'"}},
		{Table: "notification_requests", Column: "account_id", NotNull: true, DataType: "int8"},
		{Table: "notification_requests", Column: "from_account_id", NotNull: true, DataType: "int8"},
		{Table: "notification_requests", Column: "last_status_id", MustBeNullable: true, DataType: "int8"},
		{Table: "notification_requests", Column: "notifications_count", NotNull: true, DataType: "int8", DefaultFragments: []string{"0"}},
		{Table: "oauth_access_grants", Column: "token", NotNull: true},
		{Table: "oauth_access_grants", Column: "expires_in", NotNull: true},
		{Table: "oauth_access_grants", Column: "redirect_uri", NotNull: true},
		{Table: "oauth_access_grants", Column: "application_id", NotNull: true},
		{Table: "oauth_access_grants", Column: "resource_owner_id", NotNull: true},
		{Table: "oauth_access_grants", Column: "code_challenge", MustBeNullable: true, DataType: "varchar"},
		{Table: "oauth_access_grants", Column: "code_challenge_method", MustBeNullable: true, DataType: "varchar"},
		{Table: "oauth_access_tokens", Column: "token", NotNull: true},
		{Table: "oauth_applications", Column: "name", NotNull: true},
		{Table: "oauth_applications", Column: "uid", NotNull: true},
		{Table: "oauth_applications", Column: "secret", NotNull: true},
		{Table: "oauth_applications", Column: "redirect_uri", NotNull: true},
		{Table: "oauth_applications", Column: "scopes", NotNull: true, DefaultFragments: []string{"''::character varying"}},
		{Table: "oauth_applications", Column: "superapp", NotNull: true, DefaultFragments: []string{"false"}},
		{Table: "oauth_applications", Column: "confidential", NotNull: true, DefaultFragments: []string{"true"}},
		{Table: "poll_votes", Column: "account_id", NotNull: true},
		{Table: "poll_votes", Column: "poll_id", NotNull: true},
		{Table: "poll_votes", Column: "choice", NotNull: true, DefaultFragments: []string{"0"}},
		{Table: "polls", Column: "account_id", NotNull: true},
		{Table: "polls", Column: "status_id", NotNull: true},
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
		{Table: "preview_cards", Column: "author_account_id", MustBeNullable: true, DataType: "int8"},
		{Table: "preview_cards_statuses", Column: "preview_card_id", NotNull: true},
		{Table: "preview_cards_statuses", Column: "status_id", NotNull: true},
		{Table: "preview_cards_statuses", Column: "url", MustBeNullable: true, DataType: "varchar"},
		{Table: "relationship_severance_events", Column: "type", NotNull: true, DataType: "int4"},
		{Table: "relationship_severance_events", Column: "target_name", NotNull: true, DataType: "varchar"},
		{Table: "relationship_severance_events", Column: "purged", NotNull: true, DataType: "bool", DefaultFragments: []string{"false"}},
		{Table: "quotes", Column: "id", NotNull: true, DataType: "int8", DefaultFragments: []string{"timestamp_id('quotes'"}},
		{Table: "quotes", Column: "account_id", NotNull: true, DataType: "int8"},
		{Table: "quotes", Column: "status_id", NotNull: true, DataType: "int8"},
		{Table: "quotes", Column: "quoted_status_id", MustBeNullable: true, DataType: "int8"},
		{Table: "quotes", Column: "quoted_account_id", MustBeNullable: true, DataType: "int8"},
		{Table: "quotes", Column: "state", NotNull: true, DataType: "int4", DefaultFragments: []string{"0"}},
		{Table: "quotes", Column: "approval_uri", MustBeNullable: true, DataType: "varchar"},
		{Table: "quotes", Column: "activity_uri", MustBeNullable: true, DataType: "varchar"},
		{Table: "quotes", Column: "created_at", NotNull: true, DataType: "timestamp", DefaultMustBeNull: true},
		{Table: "quotes", Column: "updated_at", NotNull: true, DataType: "timestamp", DefaultMustBeNull: true},
		{Table: "quotes", Column: "legacy", NotNull: true, DataType: "bool", DefaultFragments: []string{"false"}},
		{Table: "reports", Column: "status_ids", NotNull: true, DefaultFragments: []string{"'{}'::bigint[]"}},
		{Table: "reports", Column: "comment", NotNull: true, DefaultFragments: []string{"''::text"}},
		{Table: "reports", Column: "account_id", NotNull: true},
		{Table: "reports", Column: "target_account_id", NotNull: true},
		{Table: "reports", Column: "category", NotNull: true, DefaultFragments: []string{"0"}},
		{Table: "reports", Column: "application_id", MustBeNullable: true, DataType: "int8"},
		{Table: "report_notes", Column: "content", NotNull: true},
		{Table: "report_notes", Column: "report_id", NotNull: true},
		{Table: "report_notes", Column: "account_id", NotNull: true},
		{Table: "relays", Column: "inbox_url", NotNull: true, DefaultFragments: []string{"''::character varying"}},
		{Table: "relays", Column: "state", NotNull: true, DefaultFragments: []string{"0"}},
		{Table: "rules", Column: "priority", NotNull: true, DefaultFragments: []string{"0"}},
		{Table: "rules", Column: "text", NotNull: true, DefaultFragments: []string{"''::text"}},
		{Table: "rules", Column: "hint", NotNull: true, DataType: "text", DefaultFragments: []string{"''::text"}},
		{Table: "rule_translations", Column: "text", NotNull: true, DataType: "text", DefaultFragments: []string{"''::text"}},
		{Table: "rule_translations", Column: "hint", NotNull: true, DataType: "text", DefaultFragments: []string{"''::text"}},
		{Table: "rule_translations", Column: "language", NotNull: true, DataType: "varchar"},
		{Table: "rule_translations", Column: "rule_id", NotNull: true, DataType: "int8"},
		{Table: "rule_translations", Column: "created_at", NotNull: true, DataType: "timestamp", DefaultMustBeNull: true},
		{Table: "rule_translations", Column: "updated_at", NotNull: true, DataType: "timestamp", DefaultMustBeNull: true},
		{Table: "scheduled_statuses", Column: "account_id", NotNull: true},
		{Table: "session_activations", Column: "session_id", NotNull: true},
		{Table: "session_activations", Column: "user_agent", NotNull: true, DefaultFragments: []string{"''::character varying"}},
		{Table: "session_activations", Column: "user_id", NotNull: true},
		{Table: "severed_relationships", Column: "relationship_severance_event_id", NotNull: true},
		{Table: "severed_relationships", Column: "local_account_id", NotNull: true},
		{Table: "severed_relationships", Column: "remote_account_id", NotNull: true},
		{Table: "severed_relationships", Column: "direction", NotNull: true, DataType: "int4"},
		{Table: "severed_relationships", Column: "show_reblogs", MustBeNullable: true, DataType: "bool"},
		{Table: "severed_relationships", Column: "notify", MustBeNullable: true, DataType: "bool"},
		{Table: "severed_relationships", Column: "languages", MustBeNullable: true, DataType: "_varchar"},
		{Table: "settings", Column: "var", NotNull: true},
		{Table: "site_uploads", Column: "var", NotNull: true, DefaultFragments: []string{"''::character varying"}},
		{Table: "software_updates", Column: "version", NotNull: true},
		{Table: "software_updates", Column: "urgent", NotNull: true, DefaultFragments: []string{"false"}},
		{Table: "software_updates", Column: "type", NotNull: true, DefaultFragments: []string{"0"}},
		{Table: "software_updates", Column: "release_notes", NotNull: true, DefaultFragments: []string{"''::character varying"}},
		{Table: "status_edits", Column: "status_id", NotNull: true},
		{Table: "status_edits", Column: "text", NotNull: true, DefaultFragments: []string{"''::text"}},
		{Table: "status_edits", Column: "spoiler_text", NotNull: true, DefaultFragments: []string{"''::text"}},
		{Table: "status_edits", Column: "quote_id", MustBeNullable: true, DataType: "int8"},
		{Table: "status_pins", Column: "account_id", NotNull: true},
		{Table: "status_pins", Column: "status_id", NotNull: true},
		{Table: "status_pins", Column: "created_at", NotNull: true, DefaultMustBeNull: true},
		{Table: "status_pins", Column: "updated_at", NotNull: true, DefaultMustBeNull: true},
		{Table: "status_stats", Column: "status_id", NotNull: true},
		{Table: "status_stats", Column: "replies_count", NotNull: true, DefaultFragments: []string{"0"}},
		{Table: "status_stats", Column: "reblogs_count", NotNull: true, DefaultFragments: []string{"0"}},
		{Table: "status_stats", Column: "favourites_count", NotNull: true, DefaultFragments: []string{"0"}},
		{Table: "status_stats", Column: "untrusted_favourites_count", MustBeNullable: true, DataType: "int8"},
		{Table: "status_stats", Column: "untrusted_reblogs_count", MustBeNullable: true, DataType: "int8"},
		{Table: "status_trends", Column: "status_id", NotNull: true},
		{Table: "status_trends", Column: "account_id", NotNull: true},
		{Table: "status_trends", Column: "score", NotNull: true, DefaultFragments: []string{"0"}},
		{Table: "status_trends", Column: "rank", NotNull: true, DefaultFragments: []string{"0"}},
		{Table: "status_trends", Column: "allowed", NotNull: true, DefaultFragments: []string{"false"}},
		{Table: "statuses", Column: "text", NotNull: true, DefaultFragments: []string{"''::text"}},
		{Table: "statuses", Column: "sensitive", NotNull: true, DefaultFragments: []string{"false"}},
		{Table: "statuses", Column: "visibility", NotNull: true, DefaultFragments: []string{"0"}},
		{Table: "statuses", Column: "spoiler_text", NotNull: true, DefaultFragments: []string{"''::text"}},
		{Table: "statuses", Column: "reply", NotNull: true, DefaultFragments: []string{"false"}},
		{Table: "statuses", Column: "account_id", NotNull: true},
		{Table: "statuses", Column: "fetched_replies_at", MustBeNullable: true, DataType: "timestamp"},
		{Table: "statuses", Column: "quote_approval_policy", NotNull: true, DataType: "int4", DefaultFragments: []string{"0"}},
		{Table: "statuses_tags", Column: "status_id", NotNull: true},
		{Table: "statuses_tags", Column: "tag_id", NotNull: true},
		{Table: "tag_follows", Column: "tag_id", NotNull: true},
		{Table: "tag_follows", Column: "account_id", NotNull: true},
		{Table: "tag_trends", Column: "tag_id", NotNull: true, DataType: "int8"},
		{Table: "tag_trends", Column: "score", NotNull: true, DataType: "float8", DefaultFragments: []string{"0"}},
		{Table: "tag_trends", Column: "rank", NotNull: true, DataType: "int4", DefaultFragments: []string{"0"}},
		{Table: "tag_trends", Column: "allowed", NotNull: true, DataType: "bool", DefaultFragments: []string{"false"}},
		{Table: "tag_trends", Column: "language", NotNull: true, DataType: "varchar", DefaultFragments: []string{"''::character varying"}},
		{Table: "tags", Column: "name", NotNull: true, DefaultFragments: []string{"''::character varying"}},
		{Table: "terms_of_services", Column: "text", NotNull: true, DataType: "text", DefaultFragments: []string{"''::text"}},
		{Table: "terms_of_services", Column: "changelog", NotNull: true, DataType: "text", DefaultFragments: []string{"''::text"}},
		{Table: "terms_of_services", Column: "published_at", MustBeNullable: true, DataType: "timestamp"},
		{Table: "terms_of_services", Column: "notification_sent_at", MustBeNullable: true, DataType: "timestamp"},
		{Table: "terms_of_services", Column: "created_at", NotNull: true, DataType: "timestamp", DefaultMustBeNull: true},
		{Table: "terms_of_services", Column: "updated_at", NotNull: true, DataType: "timestamp", DefaultMustBeNull: true},
		{Table: "terms_of_services", Column: "effective_date", MustBeNullable: true, DataType: "date"},
		{Table: "tombstones", Column: "account_id", NotNull: true},
		{Table: "tombstones", Column: "uri", NotNull: true},
		{Table: "unavailable_domains", Column: "domain", NotNull: true, DefaultFragments: []string{"''::character varying"}},
		{Table: "user_roles", Column: "name", NotNull: true, DefaultFragments: []string{"''::character varying"}},
		{Table: "user_roles", Column: "color", NotNull: true, DefaultFragments: []string{"''::character varying"}},
		{Table: "user_roles", Column: "position", NotNull: true, DefaultFragments: []string{"0"}},
		{Table: "user_roles", Column: "permissions", NotNull: true, DefaultFragments: []string{"0"}},
		{Table: "user_roles", Column: "highlighted", NotNull: true, DefaultFragments: []string{"false"}},
		{Table: "user_invite_requests", Column: "user_id", NotNull: true},
		{Table: "users", Column: "email", NotNull: true, DefaultFragments: []string{"''::character varying"}},
		{Table: "users", Column: "encrypted_password", NotNull: true, DefaultFragments: []string{"''::character varying"}},
		{Table: "users", Column: "sign_in_count", NotNull: true, DefaultFragments: []string{"0"}},
		{Table: "users", Column: "otp_required_for_login", NotNull: true, DefaultFragments: []string{"false"}},
		{Table: "users", Column: "otp_secret", MustBeNullable: true, DataType: "varchar"},
		{Table: "users", Column: "account_id", NotNull: true},
		{Table: "users", Column: "disabled", NotNull: true, DefaultFragments: []string{"false"}},
		{Table: "users", Column: "approved", NotNull: true, DefaultFragments: []string{"true"}},
		{Table: "users", Column: "age_verified_at", MustBeNullable: true, DataType: "timestamp"},
		{Table: "users", Column: "require_tos_interstitial", NotNull: true, DataType: "bool", DefaultFragments: []string{"false"}},
		{Table: "web_push_subscriptions", Column: "endpoint", NotNull: true},
		{Table: "web_push_subscriptions", Column: "key_p256dh", NotNull: true},
		{Table: "web_push_subscriptions", Column: "key_auth", NotNull: true},
		{Table: "web_push_subscriptions", Column: "access_token_id", NotNull: true, DataType: "int8"},
		{Table: "web_push_subscriptions", Column: "user_id", NotNull: true, DataType: "int8"},
		{Table: "web_push_subscriptions", Column: "standard", NotNull: true, DataType: "bool", DefaultFragments: []string{"false"}},
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
}

type MastodonRelationKind struct {
	Name        string
	Kinds       []string
	Description string
}

type MastodonForeignKey struct {
	Table        string
	Column       string
	ForeignTable string
	OnDelete     string
	Name         string
}

func RequiredMastodonRelationKinds() []MastodonRelationKind {
	return []MastodonRelationKind{
		{
			Name:        "instances",
			Kinds:       []string{"m"},
			Description: "materialized view required by the embedded schema snapshot",
		},
		{
			Name:        "account_summaries",
			Kinds:       []string{"m"},
			Description: "materialized view required by the embedded schema snapshot",
		},
		{
			Name:        "global_follow_recommendations",
			Kinds:       []string{"m"},
			Description: "materialized view required by the embedded schema snapshot",
		},
		{
			Name:        "user_ips",
			Kinds:       []string{"v"},
			Description: "view required by the embedded schema snapshot",
		},
	}
}

func requiredMastodonRelationDescriptions() map[string]string {
	out := map[string]string{}
	for _, relation := range RequiredMastodonRelationKinds() {
		out[relation.Name] = relation.Description
	}
	return out
}

func formatMissingMastodonRelations(names []string) string {
	descriptions := requiredMastodonRelationDescriptions()
	out := make([]string, 0, len(names))
	for _, name := range names {
		if description := descriptions[name]; description != "" {
			out = append(out, name+" ("+description+")")
			continue
		}
		out = append(out, name)
	}
	return strings.Join(out, ", ")
}

func RequiredMastodonForeignKeys() []MastodonForeignKey {
	return []MastodonForeignKey{
		{Table: "account_aliases", Column: "account_id", ForeignTable: "accounts", OnDelete: "c", Name: "fk_rails_fc91575d08"},
		{Table: "account_conversations", Column: "account_id", ForeignTable: "accounts", OnDelete: "c", Name: "fk_rails_6f5278b6e9"},
		{Table: "account_conversations", Column: "conversation_id", ForeignTable: "conversations", OnDelete: "c", Name: "fk_rails_1491654f9f"},
		{Table: "account_deletion_requests", Column: "account_id", ForeignTable: "accounts", OnDelete: "c", Name: "fk_rails_45bf2626b9"},
		{Table: "account_domain_blocks", Column: "account_id", ForeignTable: "accounts", OnDelete: "c", Name: "fk_206c6029bd"},
		{Table: "account_migrations", Column: "account_id", ForeignTable: "accounts", OnDelete: "c", Name: "fk_rails_c9f701caaf"},
		{Table: "account_migrations", Column: "target_account_id", ForeignTable: "accounts", OnDelete: "n", Name: "fk_rails_d9a8dad070"},
		{Table: "account_moderation_notes", Column: "account_id", ForeignTable: "accounts", OnDelete: "c", Name: "fk_rails_3f8b75089b"},
		{Table: "account_moderation_notes", Column: "target_account_id", ForeignTable: "accounts", OnDelete: "c", Name: "fk_rails_dd62ed5ac3"},
		{Table: "account_notes", Column: "account_id", ForeignTable: "accounts", OnDelete: "c", Name: "fk_rails_4ee4503c69"},
		{Table: "account_notes", Column: "target_account_id", ForeignTable: "accounts", OnDelete: "c", Name: "fk_rails_2801b48f1a"},
		{Table: "account_pins", Column: "account_id", ForeignTable: "accounts", OnDelete: "c", Name: "fk_rails_d44979e5dd"},
		{Table: "account_pins", Column: "target_account_id", ForeignTable: "accounts", OnDelete: "c", Name: "fk_rails_a176e26c37"},
		{Table: "account_relationship_severance_events", Column: "account_id", ForeignTable: "accounts", OnDelete: "c", Name: "fk_rails_030c916965"},
		{Table: "account_relationship_severance_events", Column: "relationship_severance_event_id", ForeignTable: "relationship_severance_events", OnDelete: "c", Name: "fk_rails_8a34c3a361"},
		{Table: "account_stats", Column: "account_id", ForeignTable: "accounts", OnDelete: "c", Name: "fk_rails_215bb31ff1"},
		{Table: "account_statuses_cleanup_policies", Column: "account_id", ForeignTable: "accounts", OnDelete: "c", Name: "fk_rails_23d5f73cfe"},
		{Table: "account_warnings", Column: "account_id", ForeignTable: "accounts", OnDelete: "n", Name: "fk_rails_a65a1bf71b"},
		{Table: "account_warnings", Column: "report_id", ForeignTable: "reports", OnDelete: "c", Name: "fk_rails_8f2bab4b16"},
		{Table: "account_warnings", Column: "target_account_id", ForeignTable: "accounts", OnDelete: "c", Name: "fk_rails_a7ebbb1e37"},
		{Table: "accounts", Column: "moved_to_account_id", ForeignTable: "accounts", OnDelete: "n", Name: "fk_rails_2320833084"},
		{Table: "admin_action_logs", Column: "account_id", ForeignTable: "accounts", OnDelete: "c", Name: "fk_rails_a7667297fa"},
		{Table: "announcement_mutes", Column: "account_id", ForeignTable: "accounts", OnDelete: "c", Name: "fk_rails_9c99f8e835"},
		{Table: "announcement_mutes", Column: "announcement_id", ForeignTable: "announcements", OnDelete: "c", Name: "fk_rails_e35401adf1"},
		{Table: "announcement_reactions", Column: "account_id", ForeignTable: "accounts", OnDelete: "c", Name: "fk_rails_7444ad831f"},
		{Table: "announcement_reactions", Column: "announcement_id", ForeignTable: "announcements", OnDelete: "c", Name: "fk_rails_a1226eaa5c"},
		{Table: "announcement_reactions", Column: "custom_emoji_id", ForeignTable: "custom_emojis", OnDelete: "c", Name: "fk_rails_b742c91c0e"},
		{Table: "appeals", Column: "account_id", ForeignTable: "accounts", OnDelete: "c", Name: "fk_rails_ea84881569"},
		{Table: "appeals", Column: "account_warning_id", ForeignTable: "account_warnings", OnDelete: "c", Name: "fk_rails_a99f14546e"},
		{Table: "appeals", Column: "approved_by_account_id", ForeignTable: "accounts", OnDelete: "n", Name: "fk_rails_9deb2f63ad"},
		{Table: "appeals", Column: "rejected_by_account_id", ForeignTable: "accounts", OnDelete: "n", Name: "fk_rails_501c3a6e13"},
		{Table: "backups", Column: "user_id", ForeignTable: "users", OnDelete: "n", Name: "fk_rails_096669d221"},
		{Table: "blocks", Column: "account_id", ForeignTable: "accounts", OnDelete: "c", Name: "fk_4269e03e65"},
		{Table: "blocks", Column: "target_account_id", ForeignTable: "accounts", OnDelete: "c", Name: "fk_9571bfabc1"},
		{Table: "bookmarks", Column: "account_id", ForeignTable: "accounts", OnDelete: "c", Name: "fk_rails_9f6ac182a6"},
		{Table: "bookmarks", Column: "status_id", ForeignTable: "statuses", OnDelete: "c", Name: "fk_rails_11207ffcfd"},
		{Table: "bulk_import_rows", Column: "bulk_import_id", ForeignTable: "bulk_imports", OnDelete: "c", Name: "fk_rails_d39af34335"},
		{Table: "bulk_imports", Column: "account_id", ForeignTable: "accounts", OnDelete: "c", Name: "fk_rails_1d89c0f8b2"},
		{Table: "canonical_email_blocks", Column: "reference_account_id", ForeignTable: "accounts", OnDelete: "c", Name: "fk_rails_1ecb262096"},
		{Table: "conversation_mutes", Column: "account_id", ForeignTable: "accounts", OnDelete: "c", Name: "fk_225b4212bb"},
		{Table: "conversation_mutes", Column: "conversation_id", ForeignTable: "conversations", OnDelete: "c", Name: "fk_rails_5ab139311f"},
		{Table: "custom_filter_keywords", Column: "custom_filter_id", ForeignTable: "custom_filters", OnDelete: "c", Name: "fk_rails_5a49a74012"},
		{Table: "custom_filter_statuses", Column: "custom_filter_id", ForeignTable: "custom_filters", OnDelete: "c", Name: "fk_rails_e2ddaf5b14"},
		{Table: "custom_filter_statuses", Column: "status_id", ForeignTable: "statuses", OnDelete: "c", Name: "fk_rails_2f6d20c0cf"},
		{Table: "custom_filters", Column: "account_id", ForeignTable: "accounts", OnDelete: "c", Name: "fk_rails_8b8d786993"},
		{Table: "email_domain_blocks", Column: "parent_id", ForeignTable: "email_domain_blocks", OnDelete: "c", Name: "fk_rails_408efe0a15"},
		{Table: "favourites", Column: "account_id", ForeignTable: "accounts", OnDelete: "c", Name: "fk_5eb6c2b873"},
		{Table: "favourites", Column: "status_id", ForeignTable: "statuses", OnDelete: "c", Name: "fk_b0e856845e"},
		{Table: "featured_tags", Column: "account_id", ForeignTable: "accounts", OnDelete: "c", Name: "fk_rails_174efcf15f"},
		{Table: "featured_tags", Column: "tag_id", ForeignTable: "tags", OnDelete: "c", Name: "fk_rails_23a9055c7c"},
		{Table: "follow_recommendation_mutes", Column: "account_id", ForeignTable: "accounts", OnDelete: "c", Name: "fk_rails_d36abd69ea"},
		{Table: "follow_recommendation_mutes", Column: "target_account_id", ForeignTable: "accounts", OnDelete: "c", Name: "fk_rails_a9f09ec9a8"},
		{Table: "follow_recommendation_suppressions", Column: "account_id", ForeignTable: "accounts", OnDelete: "c", Name: "fk_rails_dfb9a1dbe2"},
		{Table: "follow_requests", Column: "account_id", ForeignTable: "accounts", OnDelete: "c", Name: "fk_76d644b0e7"},
		{Table: "follow_requests", Column: "target_account_id", ForeignTable: "accounts", OnDelete: "c", Name: "fk_9291ec025d"},
		{Table: "follows", Column: "account_id", ForeignTable: "accounts", OnDelete: "c", Name: "fk_32ed1b5560"},
		{Table: "follows", Column: "target_account_id", ForeignTable: "accounts", OnDelete: "c", Name: "fk_745ca29eac"},
		{Table: "fasp_backfill_requests", Column: "fasp_provider_id", ForeignTable: "fasp_providers", OnDelete: "a", Name: "fk_rails_760d761775"},
		{Table: "fasp_debug_callbacks", Column: "fasp_provider_id", ForeignTable: "fasp_providers", OnDelete: "a", Name: "fk_rails_c1650087cd"},
		{Table: "fasp_follow_recommendations", Column: "requesting_account_id", ForeignTable: "accounts", OnDelete: "a", Name: "fk_rails_71623d7e2c"},
		{Table: "fasp_follow_recommendations", Column: "recommended_account_id", ForeignTable: "accounts", OnDelete: "a", Name: "fk_rails_5c63a5fd1b"},
		{Table: "fasp_subscriptions", Column: "fasp_provider_id", ForeignTable: "fasp_providers", OnDelete: "a", Name: "fk_rails_4c021f5938"},
		{Table: "generated_annual_reports", Column: "account_id", ForeignTable: "accounts", OnDelete: "a", Name: "fk_rails_4ca37f035c"},
		{Table: "identities", Column: "user_id", ForeignTable: "users", OnDelete: "c", Name: "fk_bea040f377"},
		{Table: "instance_moderation_notes", Column: "account_id", ForeignTable: "accounts", OnDelete: "c", Name: "fk_rails_62f919e09b"},
		{Table: "invites", Column: "user_id", ForeignTable: "users", OnDelete: "c", Name: "fk_rails_ff69dbb2ac"},
		{Table: "list_accounts", Column: "account_id", ForeignTable: "accounts", OnDelete: "c", Name: "fk_rails_85fee9d6ab"},
		{Table: "list_accounts", Column: "follow_id", ForeignTable: "follows", OnDelete: "c", Name: "fk_rails_40f9cc29f1"},
		{Table: "list_accounts", Column: "follow_request_id", ForeignTable: "follow_requests", OnDelete: "c", Name: "fk_rails_f11f9d1fcc"},
		{Table: "list_accounts", Column: "list_id", ForeignTable: "lists", OnDelete: "c", Name: "fk_rails_e54e356c88"},
		{Table: "lists", Column: "account_id", ForeignTable: "accounts", OnDelete: "c", Name: "fk_rails_3853b78dac"},
		{Table: "login_activities", Column: "user_id", ForeignTable: "users", OnDelete: "c", Name: "fk_rails_e4b6396b41"},
		{Table: "markers", Column: "user_id", ForeignTable: "users", OnDelete: "c", Name: "fk_rails_a7009bc2b6"},
		{Table: "media_attachments", Column: "account_id", ForeignTable: "accounts", OnDelete: "n", Name: "fk_96dd81e81b"},
		{Table: "media_attachments", Column: "scheduled_status_id", ForeignTable: "scheduled_statuses", OnDelete: "n", Name: "fk_rails_31fc5aeef1"},
		{Table: "media_attachments", Column: "status_id", ForeignTable: "statuses", OnDelete: "n", Name: "fk_rails_3ec0cfdd70"},
		{Table: "mentions", Column: "account_id", ForeignTable: "accounts", OnDelete: "c", Name: "fk_970d43f9d1"},
		{Table: "mentions", Column: "status_id", ForeignTable: "statuses", OnDelete: "c", Name: "fk_rails_59edbe2887"},
		{Table: "mutes", Column: "account_id", ForeignTable: "accounts", OnDelete: "c", Name: "fk_b8d8daf315"},
		{Table: "mutes", Column: "target_account_id", ForeignTable: "accounts", OnDelete: "c", Name: "fk_eecff219ea"},
		{Table: "notification_permissions", Column: "account_id", ForeignTable: "accounts", OnDelete: "c", Name: "fk_rails_7c0bed08df"},
		{Table: "notification_permissions", Column: "from_account_id", ForeignTable: "accounts", OnDelete: "c", Name: "fk_rails_e3e0aaad70"},
		{Table: "notification_policies", Column: "account_id", ForeignTable: "accounts", OnDelete: "c", Name: "fk_rails_506d62f0da"},
		{Table: "notification_requests", Column: "account_id", ForeignTable: "accounts", OnDelete: "c", Name: "fk_rails_881c7f71c4"},
		{Table: "notification_requests", Column: "from_account_id", ForeignTable: "accounts", OnDelete: "c", Name: "fk_rails_5632f121b4"},
		{Table: "notification_requests", Column: "last_status_id", ForeignTable: "statuses", OnDelete: "n", Name: "fk_rails_61c7aa9c1f"},
		{Table: "notifications", Column: "account_id", ForeignTable: "accounts", OnDelete: "c", Name: "fk_c141c8ee55"},
		{Table: "notifications", Column: "from_account_id", ForeignTable: "accounts", OnDelete: "c", Name: "fk_fbd6b0bf9e"},
		{Table: "oauth_access_grants", Column: "application_id", ForeignTable: "oauth_applications", OnDelete: "c", Name: "fk_34d54b0a33"},
		{Table: "oauth_access_grants", Column: "resource_owner_id", ForeignTable: "users", OnDelete: "c", Name: "fk_63b044929b"},
		{Table: "oauth_access_tokens", Column: "application_id", ForeignTable: "oauth_applications", OnDelete: "c", Name: "fk_f5fc4c1ee3"},
		{Table: "oauth_access_tokens", Column: "resource_owner_id", ForeignTable: "users", OnDelete: "c", Name: "fk_e84df68546"},
		{Table: "oauth_applications", Column: "owner_id", ForeignTable: "users", OnDelete: "c", Name: "fk_b0988c7c0a"},
		{Table: "poll_votes", Column: "account_id", ForeignTable: "accounts", OnDelete: "c", Name: "fk_rails_b6c18cf44a"},
		{Table: "poll_votes", Column: "poll_id", ForeignTable: "polls", OnDelete: "c", Name: "fk_rails_a6e6974b7e"},
		{Table: "polls", Column: "account_id", ForeignTable: "accounts", OnDelete: "c", Name: "fk_rails_5b19a0c011"},
		{Table: "polls", Column: "status_id", ForeignTable: "statuses", OnDelete: "c", Name: "fk_rails_3e0d9f1115"},
		{Table: "preview_card_trends", Column: "preview_card_id", ForeignTable: "preview_cards", OnDelete: "c", Name: "fk_rails_371593db34"},
		{Table: "preview_cards", Column: "author_account_id", ForeignTable: "accounts", OnDelete: "n", Name: "fk_rails_dca4905b94"},
		{Table: "quotes", Column: "account_id", ForeignTable: "accounts", OnDelete: "c", Name: "fk_rails_36d54169fc"},
		{Table: "quotes", Column: "status_id", ForeignTable: "statuses", OnDelete: "c", Name: "fk_rails_bd3ab4462c"},
		{Table: "quotes", Column: "quoted_account_id", ForeignTable: "accounts", OnDelete: "n", Name: "fk_rails_bfc5276b70"},
		{Table: "quotes", Column: "quoted_status_id", ForeignTable: "statuses", OnDelete: "n", Name: "fk_rails_38068caa0e"},
		{Table: "report_notes", Column: "account_id", ForeignTable: "accounts", OnDelete: "c", Name: "fk_rails_cae66353f3"},
		{Table: "report_notes", Column: "report_id", ForeignTable: "reports", OnDelete: "c", Name: "fk_rails_7fa83a61eb"},
		{Table: "reports", Column: "action_taken_by_account_id", ForeignTable: "accounts", OnDelete: "n", Name: "fk_bca45b75fd"},
		{Table: "reports", Column: "account_id", ForeignTable: "accounts", OnDelete: "c", Name: "fk_4b81f7522c"},
		{Table: "reports", Column: "assigned_account_id", ForeignTable: "accounts", OnDelete: "n", Name: "fk_rails_4e7a498fb4"},
		{Table: "reports", Column: "target_account_id", ForeignTable: "accounts", OnDelete: "c", Name: "fk_eb37af34f0"},
		{Table: "reports", Column: "application_id", ForeignTable: "oauth_applications", OnDelete: "n", Name: "fk_rails_3deb8c7acb"},
		{Table: "rule_translations", Column: "rule_id", ForeignTable: "rules", OnDelete: "c", Name: "fk_rails_d5fd439dde"},
		{Table: "scheduled_statuses", Column: "account_id", ForeignTable: "accounts", OnDelete: "c", Name: "fk_rails_23bd9018f9"},
		{Table: "session_activations", Column: "access_token_id", ForeignTable: "oauth_access_tokens", OnDelete: "c", Name: "fk_957e5bda89"},
		{Table: "session_activations", Column: "user_id", ForeignTable: "users", OnDelete: "c", Name: "fk_e5fda67334"},
		{Table: "severed_relationships", Column: "local_account_id", ForeignTable: "accounts", OnDelete: "c", Name: "fk_rails_98ff099d4c"},
		{Table: "severed_relationships", Column: "remote_account_id", ForeignTable: "accounts", OnDelete: "c", Name: "fk_rails_f7afd97ba4"},
		{Table: "severed_relationships", Column: "relationship_severance_event_id", ForeignTable: "relationship_severance_events", OnDelete: "c", Name: "fk_rails_5054494e1e"},
		{Table: "status_edits", Column: "account_id", ForeignTable: "accounts", OnDelete: "n", Name: "fk_rails_dc8988c545"},
		{Table: "status_edits", Column: "status_id", ForeignTable: "statuses", OnDelete: "c", Name: "fk_rails_a960f234a0"},
		{Table: "status_pins", Column: "account_id", ForeignTable: "accounts", OnDelete: "c", Name: "fk_d4cb435b62"},
		{Table: "status_pins", Column: "status_id", ForeignTable: "statuses", OnDelete: "c", Name: "fk_rails_65c05552f1"},
		{Table: "status_stats", Column: "status_id", ForeignTable: "statuses", OnDelete: "c", Name: "fk_rails_4a247aac42"},
		{Table: "status_trends", Column: "account_id", ForeignTable: "accounts", OnDelete: "c", Name: "fk_rails_a6b527ea49"},
		{Table: "status_trends", Column: "status_id", ForeignTable: "statuses", OnDelete: "c", Name: "fk_rails_68c610dc1a"},
		{Table: "statuses", Column: "account_id", ForeignTable: "accounts", OnDelete: "c", Name: "fk_9bda1543f7"},
		{Table: "statuses", Column: "in_reply_to_account_id", ForeignTable: "accounts", OnDelete: "n", Name: "fk_c7fa917661"},
		{Table: "statuses", Column: "in_reply_to_id", ForeignTable: "statuses", OnDelete: "n", Name: "fk_rails_94a6f70399"},
		{Table: "statuses", Column: "reblog_of_id", ForeignTable: "statuses", OnDelete: "c", Name: "fk_rails_256483a9ab"},
		{Table: "statuses_tags", Column: "status_id", ForeignTable: "statuses", OnDelete: "c", Name: "fk_rails_df0fe11427"},
		{Table: "statuses_tags", Column: "tag_id", ForeignTable: "tags", OnDelete: "c", Name: "fk_3081861e21"},
		{Table: "tag_follows", Column: "account_id", ForeignTable: "accounts", OnDelete: "c", Name: "fk_rails_091e831473"},
		{Table: "tag_follows", Column: "tag_id", ForeignTable: "tags", OnDelete: "c", Name: "fk_rails_0deefe597f"},
		{Table: "tag_trends", Column: "tag_id", ForeignTable: "tags", OnDelete: "c", Name: "fk_rails_3033046460"},
		{Table: "tombstones", Column: "account_id", ForeignTable: "accounts", OnDelete: "c", Name: "fk_rails_f95b861449"},
		{Table: "user_invite_requests", Column: "user_id", ForeignTable: "users", OnDelete: "c", Name: "fk_rails_3773f15361"},
		{Table: "users", Column: "account_id", ForeignTable: "accounts", OnDelete: "c", Name: "fk_50500f500d"},
		{Table: "users", Column: "invite_id", ForeignTable: "invites", OnDelete: "n", Name: "fk_rails_8fb2a43e88"},
		{Table: "users", Column: "created_by_application_id", ForeignTable: "oauth_applications", OnDelete: "n", Name: "fk_rails_ecc9536e7c"},
		{Table: "users", Column: "role_id", ForeignTable: "user_roles", OnDelete: "n", Name: "fk_rails_642f17018b"},
		{Table: "web_push_subscriptions", Column: "access_token_id", ForeignTable: "oauth_access_tokens", OnDelete: "c", Name: "fk_rails_751a9f390b"},
		{Table: "web_push_subscriptions", Column: "user_id", ForeignTable: "users", OnDelete: "c", Name: "fk_rails_b006f28dac"},
		{Table: "web_settings", Column: "user_id", ForeignTable: "users", OnDelete: "c", Name: "fk_11910667b2"},
		{Table: "webauthn_credentials", Column: "user_id", ForeignTable: "users", OnDelete: "c", Name: "fk_rails_a4355aef77"},
	}
}

func RequiredMastodonExtensions() []string {
	return []string{
		"plpgsql",
	}
}

func RequiredMastodonFunctions() []string {
	return []string{
		"timestamp_id(text)",
	}
}

func RequiredMastodonSequences() []string {
	return []string{
		"accounts_id_seq",
		"media_attachments_id_seq",
		"notification_requests_id_seq",
		"quotes_id_seq",
		"statuses_id_seq",
	}
}

func modelBackedMastodonColumns() map[string][]string {
	requiredTables := map[string]struct{}{}
	for _, table := range RequiredMastodonTables() {
		requiredTables[table] = struct{}{}
	}
	forbiddenColumns := map[string]map[string]struct{}{}
	for table, columns := range ForbiddenMastodonColumns() {
		forbiddenColumns[table] = map[string]struct{}{}
		for _, column := range columns {
			forbiddenColumns[table][column] = struct{}{}
		}
	}
	out := map[string][]string{}
	for _, model := range requiredMastodonColumnModels() {
		typ := reflect.TypeOf(model)
		if typ.Kind() == reflect.Pointer {
			typ = typ.Elem()
		}
		tableNamer, ok := reflect.New(typ).Interface().(interface{ TableName() string })
		if !ok {
			continue
		}
		table := tableNamer.TableName()
		if _, ok := requiredTables[table]; !ok {
			continue
		}
		seen := map[string]struct{}{}
		for _, column := range out[table] {
			seen[column] = struct{}{}
		}
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			for _, part := range strings.Split(field.Tag.Get("gorm"), ";") {
				if !strings.HasPrefix(part, "column:") {
					continue
				}
				column := strings.TrimPrefix(part, "column:")
				if _, forbidden := forbiddenColumns[table][column]; forbidden {
					continue
				}
				if _, ok := seen[column]; ok {
					continue
				}
				out[table] = append(out[table], column)
				seen[column] = struct{}{}
			}
		}
	}
	return out
}

func requiredMastodonColumnModels() []any {
	return []any{
		paonmodels.Account{},
		paonmodels.AccountAlias{},
		paonmodels.AccountConversation{},
		paonmodels.AccountDeletionRequest{},
		paonmodels.AccountDomainBlock{},
		paonmodels.AccountMigration{},
		paonmodels.AccountModerationNote{},
		paonmodels.AccountNote{},
		paonmodels.AccountPin{},
		paonmodels.AccountRelationshipSeveranceEvent{},
		paonmodels.AccountStat{},
		paonmodels.AccountStatusesCleanupPolicy{},
		paonmodels.AccountTag{},
		paonmodels.AccountWarning{},
		paonmodels.AccountWarningPreset{},
		paonmodels.AdminActionLog{},
		paonmodels.Announcement{},
		paonmodels.AnnualReportStatusesPerAccountCount{},
		paonmodels.AnnouncementMute{},
		paonmodels.AnnouncementReaction{},
		paonmodels.Appeal{},
		paonmodels.Backup{},
		paonmodels.Block{},
		paonmodels.Bookmark{},
		paonmodels.BulkImport{},
		paonmodels.BulkImportRow{},
		paonmodels.CanonicalEmailBlock{},
		paonmodels.Conversation{},
		paonmodels.ConversationMute{},
		paonmodels.CustomEmoji{},
		paonmodels.CustomEmojiCategory{},
		paonmodels.CustomFilter{},
		paonmodels.CustomFilterKeyword{},
		paonmodels.CustomFilterStatus{},
		paonmodels.DomainAllow{},
		paonmodels.DomainBlock{},
		paonmodels.EmailDomainBlock{},
		paonmodels.Favourite{},
		paonmodels.FaspBackfillRequest{},
		paonmodels.FaspDebugCallback{},
		paonmodels.FaspFollowRecommendation{},
		paonmodels.FaspProvider{},
		paonmodels.FaspSubscription{},
		paonmodels.FeaturedTag{},
		paonmodels.Follow{},
		paonmodels.FollowRecommendationMute{},
		paonmodels.FollowRecommendationSuppression{},
		paonmodels.FollowRequest{},
		paonmodels.GeneratedAnnualReport{},
		paonmodels.Identity{},
		paonmodels.Instance{},
		paonmodels.InstanceModerationNote{},
		paonmodels.Invite{},
		paonmodels.IPBlock{},
		paonmodels.List{},
		paonmodels.ListAccount{},
		paonmodels.LoginActivity{},
		paonmodels.Marker{},
		paonmodels.MediaAttachment{},
		paonmodels.Mention{},
		paonmodels.Mute{},
		paonmodels.Notification{},
		paonmodels.NotificationPermission{},
		paonmodels.NotificationPolicy{},
		paonmodels.NotificationRequest{},
		paonmodels.OAuthAccessGrant{},
		paonmodels.OAuthAccessToken{},
		paonmodels.OAuthApplication{},
		paonmodels.Poll{},
		paonmodels.PollVote{},
		paonmodels.PreviewCard{},
		paonmodels.PreviewCardProvider{},
		paonmodels.PreviewCardStatus{},
		paonmodels.PreviewCardTrend{},
		paonmodels.RelationshipSeveranceEvent{},
		paonmodels.Relay{},
		paonmodels.Report{},
		paonmodels.ReportNote{},
		paonmodels.Rule{},
		paonmodels.RuleTranslation{},
		paonmodels.ScheduledStatus{},
		paonmodels.SessionActivation{},
		paonmodels.SeveredRelationship{},
		paonmodels.Setting{},
		paonmodels.SiteUpload{},
		paonmodels.SoftwareUpdate{},
		paonmodels.Status{},
		paonmodels.StatusEdit{},
		paonmodels.StatusPin{},
		paonmodels.StatusStat{},
		paonmodels.StatusTag{},
		paonmodels.StatusTrend{},
		paonmodels.Tag{},
		paonmodels.TagFollow{},
		paonmodels.TagTrend{},
		paonmodels.TermsOfService{},
		paonmodels.Tombstone{},
		paonmodels.UnavailableDomain{},
		paonmodels.User{},
		paonmodels.UserInviteRequest{},
		paonmodels.UserRole{},
		paonmodels.WebauthnCredential{},
		paonmodels.Webhook{},
		paonmodels.WebPushSubscription{},
		paonmodels.WebSetting{},
		paonmodels.Quote{},
	}
}

func SchemaAvailable(database *gorm.DB) error {
	if database == nil {
		return errors.New("database connection is not configured; set DATABASE_URL or DB_NAME/DB_HOST")
	}
	missing := make([]string, 0)
	for _, relation := range RequiredMastodonTables() {
		available, err := mastodonRelationAvailable(database, relation)
		if err != nil {
			return fmt.Errorf("inspect database schema relation %s: %w", relation, err)
		}
		if !available {
			missing = append(missing, relation)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("database schema is missing required Mastodon relations: %s; run paon-migrate against this database, then rerun task check-config:bin before starting paon", formatMissingMastodonRelations(missing))
	}
	obsoleteRelations := make([]string, 0)
	for _, relation := range ForbiddenMastodonRelations() {
		available, err := mastodonRelationAvailable(database, relation)
		if err != nil {
			return fmt.Errorf("inspect obsolete database schema relation %s: %w", relation, err)
		}
		if available {
			obsoleteRelations = append(obsoleteRelations, relation)
		}
	}
	if len(obsoleteRelations) > 0 {
		return fmt.Errorf("database schema still contains obsolete Mastodon relations: %s; complete the acknowledged migration contract through Mastodon 4.4 before starting paon", strings.Join(obsoleteRelations, ", "))
	}
	wrongRelationKinds := make([]string, 0)
	for _, relation := range RequiredMastodonRelationKinds() {
		kind, err := mastodonRelationKind(database, relation.Name)
		if err != nil {
			return fmt.Errorf("inspect database schema relation %s: %w", relation.Name, err)
		}
		if !relationKindAllowed(kind, relation.Kinds) {
			wrongRelationKinds = append(wrongRelationKinds, fmt.Sprintf("%s must be %s", relation.Name, relation.Description))
		}
	}
	if len(wrongRelationKinds) > 0 {
		return fmt.Errorf("database schema has incompatible Mastodon relation kinds: %s", strings.Join(wrongRelationKinds, ", "))
	}
	missingExtensions := make([]string, 0)
	for _, extension := range RequiredMastodonExtensions() {
		ok, err := mastodonExtensionAvailable(database, extension)
		if err != nil {
			return fmt.Errorf("inspect database schema extension %s: %w", extension, err)
		}
		if !ok {
			missingExtensions = append(missingExtensions, extension)
		}
	}
	if len(missingExtensions) > 0 {
		return fmt.Errorf("database schema is missing required Mastodon extensions: %s", strings.Join(missingExtensions, ", "))
	}
	missingFunctions := make([]string, 0)
	for _, function := range RequiredMastodonFunctions() {
		ok, err := mastodonFunctionAvailable(database, function)
		if err != nil {
			return fmt.Errorf("inspect database schema function %s: %w", function, err)
		}
		if !ok {
			missingFunctions = append(missingFunctions, function)
		}
	}
	if len(missingFunctions) > 0 {
		return fmt.Errorf("database schema is missing required Mastodon functions: %s", strings.Join(missingFunctions, ", "))
	}
	missingSequences := make([]string, 0)
	for _, sequence := range RequiredMastodonSequences() {
		ok, err := mastodonSequenceAvailable(database, sequence)
		if err != nil {
			return fmt.Errorf("inspect database schema sequence %s: %w", sequence, err)
		}
		if !ok {
			missingSequences = append(missingSequences, sequence)
		}
	}
	if len(missingSequences) > 0 {
		return fmt.Errorf("database schema is missing required Mastodon sequences: %s", strings.Join(missingSequences, ", "))
	}
	missingColumns := make([]string, 0)
	requiredColumns := RequiredMastodonColumns()
	for _, table := range RequiredMastodonTables() {
		columns, err := mastodonRelationColumns(database, table)
		if err != nil {
			return fmt.Errorf("inspect database schema table %s: %w", table, err)
		}
		available := map[string]struct{}{}
		for _, column := range columns {
			available[strings.ToLower(column)] = struct{}{}
		}
		for _, column := range requiredColumns[table] {
			if _, ok := available[strings.ToLower(column)]; !ok {
				missingColumns = append(missingColumns, table+"."+column)
			}
		}
	}
	if len(missingColumns) > 0 {
		return fmt.Errorf("database schema is missing required Mastodon columns: %s", strings.Join(missingColumns, ", "))
	}
	obsoleteColumns := make([]string, 0)
	for table, forbidden := range ForbiddenMastodonColumns() {
		columns, err := mastodonRelationColumns(database, table)
		if err != nil {
			return fmt.Errorf("inspect obsolete database schema columns for %s: %w", table, err)
		}
		available := map[string]struct{}{}
		for _, column := range columns {
			available[strings.ToLower(column)] = struct{}{}
		}
		for _, column := range forbidden {
			if _, ok := available[strings.ToLower(column)]; ok {
				obsoleteColumns = append(obsoleteColumns, table+"."+column)
			}
		}
	}
	if len(obsoleteColumns) > 0 {
		return fmt.Errorf("database schema still contains obsolete Mastodon columns: %s; complete the acknowledged migration contract through Mastodon 4.4 before starting paon", strings.Join(obsoleteColumns, ", "))
	}
	wrongColumnDefinitions := make([]string, 0)
	for _, definition := range RequiredMastodonColumnDefinitions() {
		ok, err := mastodonColumnDefinitionMatches(database, definition)
		if err != nil {
			return fmt.Errorf("inspect database schema column definition %s.%s: %w", definition.Table, definition.Column, err)
		}
		if !ok {
			wrongColumnDefinitions = append(wrongColumnDefinitions, definition.String())
		}
	}
	if len(wrongColumnDefinitions) > 0 {
		return fmt.Errorf("database schema has incompatible Mastodon column definitions: %s", strings.Join(wrongColumnDefinitions, ", "))
	}
	missingIndexes := make([]string, 0)
	for table, indexes := range RequiredMastodonIndexes() {
		for _, index := range indexes {
			ok, err := mastodonIndexAvailable(database, index)
			if err != nil {
				return fmt.Errorf("inspect database schema index %s.%s: %w", table, index, err)
			}
			if !ok {
				missingIndexes = append(missingIndexes, table+"."+index)
			}
		}
	}
	if len(missingIndexes) > 0 {
		return fmt.Errorf("database schema is missing required Mastodon indexes: %s", strings.Join(missingIndexes, ", "))
	}
	nonUniqueIndexes := make([]string, 0)
	for _, index := range RequiredMastodonUniqueIndexes() {
		ok, err := mastodonUniqueIndexAvailable(database, index)
		if err != nil {
			return fmt.Errorf("inspect database schema unique index %s: %w", index, err)
		}
		if !ok {
			nonUniqueIndexes = append(nonUniqueIndexes, index)
		}
	}
	if len(nonUniqueIndexes) > 0 {
		return fmt.Errorf("database schema is missing required Mastodon unique indexes: %s", strings.Join(nonUniqueIndexes, ", "))
	}
	wrongIndexDefinitions := make([]string, 0)
	for index, fragments := range RequiredMastodonIndexDefinitionFragments() {
		ok, err := mastodonIndexDefinitionContains(database, index, fragments)
		if err != nil {
			return fmt.Errorf("inspect database schema index definition %s: %w", index, err)
		}
		if !ok {
			wrongIndexDefinitions = append(wrongIndexDefinitions, index)
		}
	}
	if len(wrongIndexDefinitions) > 0 {
		return fmt.Errorf("database schema has incompatible Mastodon index definitions: %s", strings.Join(wrongIndexDefinitions, ", "))
	}
	wrongPrimaryKeys := make([]string, 0)
	for table, columns := range RequiredMastodonPrimaryKeys() {
		ok, err := mastodonPrimaryKeyMatches(database, table, columns)
		if err != nil {
			return fmt.Errorf("inspect database schema primary key %s: %w", table, err)
		}
		if !ok {
			wrongPrimaryKeys = append(wrongPrimaryKeys, table+"("+strings.Join(columns, ",")+")")
		}
	}
	if len(wrongPrimaryKeys) > 0 {
		return fmt.Errorf("database schema has incompatible Mastodon primary keys: %s", strings.Join(wrongPrimaryKeys, ", "))
	}
	missingForeignKeys := make([]string, 0)
	for _, foreignKey := range RequiredMastodonForeignKeys() {
		ok, err := mastodonForeignKeyAvailable(database, foreignKey)
		if err != nil {
			return fmt.Errorf("inspect database schema foreign key %s.%s: %w", foreignKey.Table, foreignKey.Column, err)
		}
		if !ok {
			missingForeignKeys = append(missingForeignKeys, foreignKey.String())
		}
	}
	if len(missingForeignKeys) > 0 {
		return fmt.Errorf("database schema is missing required Mastodon foreign keys: %s", strings.Join(missingForeignKeys, ", "))
	}
	if err := mastodonSchemaMigrationApplied(database, requiredMastodonSchemaVersion); err != nil {
		return err
	}
	return nil
}

func RequiredMastodonSchemaVersion() string {
	return requiredMastodonSchemaVersion
}

func mastodonSchemaMigrationApplied(database *gorm.DB, version string) error {
	if version == "" {
		return nil
	}
	var latest sql.NullString
	if err := database.Raw(`SELECT MAX(version) FROM schema_migrations`).Scan(&latest).Error; err != nil {
		return fmt.Errorf("inspect latest database schema_migrations version: %w", err)
	}
	if latest.Valid && latest.String > version {
		return fmt.Errorf("database schema version %s is newer than supported Mastodon schema version %s", latest.String, version)
	}
	var upgradeVersions []string
	if err := database.Raw(`SELECT version FROM schema_migrations WHERE version > ? AND version <= ? ORDER BY version`, paonschema.Mastodon4219Version, version).Scan(&upgradeVersions).Error; err != nil {
		return fmt.Errorf("inspect Mastodon 4.3/4.4 database schema_migrations versions: %w", err)
	}
	var mastodon43Count int
	var mastodon44Count int
	for _, upgradeVersion := range upgradeVersions {
		switch {
		case paonschema.Mastodon43UpgradeVersionKnown(upgradeVersion):
			mastodon43Count++
		case paonschema.Mastodon44UpgradeVersionKnown(upgradeVersion):
			mastodon44Count++
		default:
			return fmt.Errorf("database schema contains unsupported migration marker %s between Mastodon 4.2 and 4.4", upgradeVersion)
		}
	}
	if mastodon43Count != paonschema.Mastodon43UpgradeVersionCount() {
		return fmt.Errorf("database schema has final marker %s but only %d of %d reviewed Mastodon 4.3 migration markers", version, mastodon43Count, paonschema.Mastodon43UpgradeVersionCount())
	}
	if mastodon44Count != paonschema.Mastodon44UpgradeVersionCount() {
		return fmt.Errorf("database schema has final marker %s but only %d of %d reviewed Mastodon 4.4 migration markers", version, mastodon44Count, paonschema.Mastodon44UpgradeVersionCount())
	}
	var found string
	err := database.Raw("SELECT version FROM schema_migrations WHERE version = ? LIMIT 1", version).Row().Scan(&found)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("database schema_migrations is missing required Mastodon schema version %s", version)
		}
		return fmt.Errorf("inspect database schema_migrations: %w", err)
	}
	if found != version {
		return fmt.Errorf("database schema_migrations is missing required Mastodon schema version %s", version)
	}
	return nil
}

func mastodonRelationKind(database *gorm.DB, relation string) (string, error) {
	var kind string
	err := database.Raw(
		`SELECT c.relkind
		   FROM pg_class c
		  WHERE c.oid = to_regclass(?)`,
		relation,
	).Row().Scan(&kind)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return kind, nil
}

func mastodonRelationAvailable(database *gorm.DB, relation string) (bool, error) {
	var available bool
	err := database.Raw(`SELECT to_regclass(?) IS NOT NULL`, relation).Row().Scan(&available)
	if err != nil {
		return false, err
	}
	return available, nil
}

func mastodonRelationColumns(database *gorm.DB, relation string) ([]string, error) {
	rows, err := database.Raw(
		`SELECT a.attname
		   FROM pg_attribute a
		   JOIN pg_class c ON c.oid = a.attrelid
		  WHERE c.oid = to_regclass(?)
		    AND a.attnum > 0
		    AND NOT a.attisdropped
		  ORDER BY a.attnum`,
		relation,
	).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns := make([]string, 0)
	for rows.Next() {
		var column string
		if err := rows.Scan(&column); err != nil {
			return nil, err
		}
		columns = append(columns, column)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return columns, nil
}

func mastodonExtensionAvailable(database *gorm.DB, extension string) (bool, error) {
	var available bool
	err := database.Raw(`SELECT EXISTS(SELECT 1 FROM pg_extension WHERE extname = ?)`, extension).Row().Scan(&available)
	if err != nil {
		return false, err
	}
	return available, nil
}

func mastodonFunctionAvailable(database *gorm.DB, function string) (bool, error) {
	var available bool
	err := database.Raw(`SELECT to_regprocedure(?) IS NOT NULL`, function).Row().Scan(&available)
	if err != nil {
		return false, err
	}
	return available, nil
}

func mastodonSequenceAvailable(database *gorm.DB, sequence string) (bool, error) {
	kind, err := mastodonRelationKind(database, sequence)
	if err != nil {
		return false, err
	}
	return kind == "S", nil
}

func mastodonColumnDefinitionMatches(database *gorm.DB, definition MastodonColumnDefinition) (bool, error) {
	var nullable string
	var defaultValue sql.NullString
	var dataType string
	err := database.Raw(
		`SELECT is_nullable, column_default, udt_name
		   FROM information_schema.columns
		  WHERE table_schema = ANY(current_schemas(false))
		    AND table_name = ?
		    AND column_name = ?
		  LIMIT 1`,
		definition.Table,
		definition.Column,
	).Row().Scan(&nullable, &defaultValue, &dataType)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if definition.NotNull && nullable != "NO" {
		return false, nil
	}
	if definition.MustBeNullable && nullable != "YES" {
		return false, nil
	}
	if definition.DataType != "" && dataType != definition.DataType {
		return false, nil
	}
	if definition.DefaultMustBeNull && defaultValue.Valid {
		return false, nil
	}
	defaultText := ""
	if defaultValue.Valid {
		defaultText = strings.ToLower(defaultValue.String)
	}
	for _, fragment := range definition.DefaultFragments {
		if !mastodonColumnDefaultContains(defaultText, fragment) {
			return false, nil
		}
	}
	return true, nil
}

func mastodonColumnDefaultContains(defaultText string, fragment string) bool {
	defaultText = strings.ToLower(defaultText)
	fragment = strings.ToLower(fragment)
	if strings.Contains(defaultText, fragment) {
		return true
	}
	for _, equivalent := range mastodonColumnDefaultEquivalents(fragment) {
		if strings.Contains(defaultText, equivalent) {
			return true
		}
	}
	return false
}

func mastodonColumnDefaultEquivalents(fragment string) []string {
	switch strings.ToLower(fragment) {
	case "array[]::character varying[]":
		return []string{"'{}'::character varying[]"}
	case "'{}'::bigint[]":
		return []string{"'{}'::integer[]"}
	default:
		return nil
	}
}

func mastodonIndexAvailable(database *gorm.DB, index string) (bool, error) {
	var available bool
	err := database.Raw(`SELECT to_regclass(?) IS NOT NULL`, index).Row().Scan(&available)
	if err != nil {
		return false, err
	}
	return available, nil
}

func mastodonUniqueIndexAvailable(database *gorm.DB, index string) (bool, error) {
	var available bool
	err := database.Raw(
		`SELECT EXISTS(
		   SELECT 1
		     FROM pg_index i
		     JOIN pg_class idx ON idx.oid = i.indexrelid
		    WHERE idx.relname = ?
		      AND i.indisunique
		 )`,
		index,
	).Row().Scan(&available)
	if err != nil {
		return false, err
	}
	return available, nil
}

func mastodonIndexDefinitionContains(database *gorm.DB, index string, fragments []string) (bool, error) {
	var definition string
	err := database.Raw(`SELECT COALESCE(pg_get_indexdef(to_regclass(?)), '')`, index).Row().Scan(&definition)
	if err != nil {
		return false, err
	}
	if definition == "" {
		return false, nil
	}
	definition = strings.ToLower(definition)
	for _, fragment := range fragments {
		if !strings.Contains(definition, strings.ToLower(fragment)) {
			return false, nil
		}
	}
	return true, nil
}

func mastodonPrimaryKeyMatches(database *gorm.DB, table string, columns []string) (bool, error) {
	var actual string
	err := database.Raw(
		`SELECT COALESCE(string_agg(column_attribute.attname, ',' ORDER BY key_column.ordinality), '')
		   FROM pg_index index_info
		   JOIN pg_class table_class ON table_class.oid = index_info.indrelid
		   JOIN unnest(index_info.indkey) WITH ORDINALITY AS key_column(attnum, ordinality) ON true
		   JOIN pg_attribute column_attribute
		     ON column_attribute.attrelid = table_class.oid
		    AND column_attribute.attnum = key_column.attnum
		  WHERE table_class.relname = ?
		    AND index_info.indisprimary`,
		table,
	).Row().Scan(&actual)
	if err != nil {
		return false, err
	}
	return actual == strings.Join(columns, ","), nil
}

func mastodonForeignKeyAvailable(database *gorm.DB, foreignKey MastodonForeignKey) (bool, error) {
	var available bool
	query := `SELECT EXISTS(
			   SELECT 1
			     FROM pg_constraint c
			     JOIN pg_class source_table ON source_table.oid = c.conrelid
			     JOIN pg_namespace source_namespace ON source_namespace.oid = source_table.relnamespace
			     JOIN pg_class target_table ON target_table.oid = c.confrelid
			     JOIN pg_namespace target_namespace ON target_namespace.oid = target_table.relnamespace
			     JOIN pg_attribute source_column
		       ON source_column.attrelid = source_table.oid
		      AND source_column.attnum = ANY(c.conkey)
			    WHERE c.contype = 'f'
			      AND source_namespace.nspname = current_schema()
			      AND target_namespace.nspname = current_schema()
			      AND source_table.relname = ?
			      AND source_column.attname = ?
			      AND target_table.relname = ?
			      AND c.confdeltype = ?
	`
	arguments := []any{
		foreignKey.Table,
		foreignKey.Column,
		foreignKey.ForeignTable,
		foreignKey.OnDelete,
	}
	if foreignKey.Name != "" {
		query += ` AND c.conname = ?`
		arguments = append(arguments, foreignKey.Name)
	}
	query += `)`
	err := database.Raw(query, arguments...).Row().Scan(&available)
	if err != nil {
		return false, err
	}
	return available, nil
}

func (foreignKey MastodonForeignKey) String() string {
	return foreignKey.Table + "." + foreignKey.Column + "->" + foreignKey.ForeignTable + " on_delete=" + foreignKey.OnDelete
}

func (definition MastodonColumnDefinition) String() string {
	return definition.Table + "." + definition.Column
}

func relationKindAllowed(got string, allowed []string) bool {
	for _, kind := range allowed {
		if got == kind {
			return true
		}
	}
	return false
}
