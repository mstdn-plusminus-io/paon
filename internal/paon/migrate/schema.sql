-- paon:statement
CREATE EXTENSION IF NOT EXISTS plpgsql;

-- paon:statement
CREATE TABLE schema_migrations (version character varying NOT NULL PRIMARY KEY);

-- paon:statement
CREATE TABLE ar_internal_metadata (key character varying NOT NULL PRIMARY KEY, value character varying, created_at timestamp(6) without time zone NOT NULL, updated_at timestamp(6) without time zone NOT NULL);

-- paon:statement
CREATE OR REPLACE FUNCTION timestamp_id(table_name text)
RETURNS bigint AS $$
DECLARE
  time_part bigint;
  sequence_base bigint;
  tail bigint;
BEGIN
  time_part := (((date_part('epoch', now()) * 1000))::bigint << 16);
  sequence_base := ('x' || substr(md5(table_name || '__PAON_TIMESTAMP_ID_SALT__' || time_part::text), 1, 4))::bit(16)::bigint;
  tail := ((sequence_base + nextval(table_name || '_id_seq')) & 65535);
  RETURN time_part | tail;
END
$$ LANGUAGE plpgsql VOLATILE;

-- paon:statement
CREATE TABLE "account_aliases" (
  id bigserial PRIMARY KEY,
  "account_id" bigint NOT NULL,
  "acct" character varying DEFAULT '' NOT NULL,
  "uri" character varying DEFAULT '' NOT NULL,
  "created_at" timestamp without time zone NOT NULL,
  "updated_at" timestamp without time zone NOT NULL
);

-- paon:statement
CREATE UNIQUE INDEX "index_account_aliases_on_account_id_and_uri" ON "account_aliases" ("account_id", "uri");

-- paon:statement
CREATE TABLE "account_conversations" (
  id bigserial PRIMARY KEY,
  "account_id" bigint NOT NULL,
  "conversation_id" bigint NOT NULL,
  "participant_account_ids" bigint[] DEFAULT '{}'::bigint[] NOT NULL,
  "status_ids" bigint[] DEFAULT '{}'::bigint[] NOT NULL,
  "last_status_id" bigint,
  "lock_version" integer DEFAULT 0 NOT NULL,
  "unread" boolean DEFAULT false NOT NULL
);

-- paon:statement
CREATE UNIQUE INDEX "index_unique_conversations" ON "account_conversations" ("account_id", "conversation_id", "participant_account_ids");

-- paon:statement
CREATE INDEX "index_account_conversations_on_conversation_id" ON "account_conversations" ("conversation_id");

-- paon:statement
CREATE TABLE "account_deletion_requests" (
  id bigserial PRIMARY KEY,
  "account_id" bigint NOT NULL,
  "created_at" timestamp without time zone NOT NULL,
  "updated_at" timestamp without time zone NOT NULL
);

-- paon:statement
CREATE INDEX "index_account_deletion_requests_on_account_id" ON "account_deletion_requests" ("account_id");

-- paon:statement
CREATE TABLE "account_domain_blocks" (
  id bigserial PRIMARY KEY,
  "domain" character varying NOT NULL,
  "created_at" timestamp without time zone NOT NULL,
  "updated_at" timestamp without time zone NOT NULL,
  "account_id" bigint NOT NULL
);

-- paon:statement
CREATE UNIQUE INDEX "index_account_domain_blocks_on_account_id_and_domain" ON "account_domain_blocks" ("account_id", "domain");

-- paon:statement
CREATE TABLE "account_migrations" (
  id bigserial PRIMARY KEY,
  "account_id" bigint,
  "acct" character varying DEFAULT '' NOT NULL,
  "followers_count" bigint DEFAULT 0 NOT NULL,
  "target_account_id" bigint,
  "created_at" timestamp without time zone NOT NULL,
  "updated_at" timestamp without time zone NOT NULL
);

-- paon:statement
CREATE INDEX "index_account_migrations_on_account_id" ON "account_migrations" ("account_id");

-- paon:statement
CREATE INDEX "index_account_migrations_on_target_account_id" ON "account_migrations" ("target_account_id") WHERE (target_account_id IS NOT NULL);

-- paon:statement
CREATE TABLE "account_moderation_notes" (
  id bigserial PRIMARY KEY,
  "content" text NOT NULL,
  "account_id" bigint NOT NULL,
  "target_account_id" bigint NOT NULL,
  "created_at" timestamp without time zone NOT NULL,
  "updated_at" timestamp without time zone NOT NULL
);

-- paon:statement
CREATE INDEX "index_account_moderation_notes_on_account_id" ON "account_moderation_notes" ("account_id");

-- paon:statement
CREATE INDEX "index_account_moderation_notes_on_target_account_id" ON "account_moderation_notes" ("target_account_id");

-- paon:statement
CREATE TABLE "account_notes" (
  id bigserial PRIMARY KEY,
  "account_id" bigint NOT NULL,
  "target_account_id" bigint NOT NULL,
  "comment" text NOT NULL,
  "created_at" timestamp without time zone NOT NULL,
  "updated_at" timestamp without time zone NOT NULL
);

-- paon:statement
CREATE UNIQUE INDEX "index_account_notes_on_account_id_and_target_account_id" ON "account_notes" ("account_id", "target_account_id");

-- paon:statement
CREATE INDEX "index_account_notes_on_target_account_id" ON "account_notes" ("target_account_id");

-- paon:statement
CREATE TABLE "account_pins" (
  id bigserial PRIMARY KEY,
  "account_id" bigint NOT NULL,
  "target_account_id" bigint NOT NULL,
  "created_at" timestamp without time zone NOT NULL,
  "updated_at" timestamp without time zone NOT NULL
);

-- paon:statement
CREATE UNIQUE INDEX "index_account_pins_on_account_id_and_target_account_id" ON "account_pins" ("account_id", "target_account_id");

-- paon:statement
CREATE INDEX "index_account_pins_on_target_account_id" ON "account_pins" ("target_account_id");

-- paon:statement
CREATE TABLE "account_relationship_severance_events" (
  id bigserial PRIMARY KEY,
  "account_id" bigint NOT NULL,
  "relationship_severance_event_id" bigint NOT NULL,
  "created_at" timestamp(6) without time zone NOT NULL,
  "updated_at" timestamp(6) without time zone NOT NULL,
  "followers_count" integer DEFAULT 0 NOT NULL,
  "following_count" integer DEFAULT 0 NOT NULL
);

-- paon:statement
CREATE UNIQUE INDEX "idx_on_account_id_relationship_severance_event_id_7bd82bf20e" ON "account_relationship_severance_events" ("account_id", "relationship_severance_event_id");

-- paon:statement
CREATE INDEX "idx_on_relationship_severance_event_id_403f53e707" ON "account_relationship_severance_events" ("relationship_severance_event_id");

-- paon:statement
CREATE TABLE "account_stats" (
  id bigserial PRIMARY KEY,
  "account_id" bigint NOT NULL,
  "statuses_count" bigint DEFAULT 0 NOT NULL,
  "following_count" bigint DEFAULT 0 NOT NULL,
  "followers_count" bigint DEFAULT 0 NOT NULL,
  "created_at" timestamp without time zone NOT NULL,
  "updated_at" timestamp without time zone NOT NULL,
  "last_status_at" timestamp without time zone
);

-- paon:statement
CREATE UNIQUE INDEX "index_account_stats_on_account_id" ON "account_stats" ("account_id");

-- paon:statement
CREATE INDEX "index_account_stats_on_last_status_at_and_account_id" ON "account_stats" ("last_status_at" DESC NULLS LAST, "account_id");

-- paon:statement
CREATE TABLE "account_statuses_cleanup_policies" (
  id bigserial PRIMARY KEY,
  "account_id" bigint NOT NULL,
  "enabled" boolean DEFAULT true NOT NULL,
  "min_status_age" integer DEFAULT 1209600 NOT NULL,
  "keep_direct" boolean DEFAULT true NOT NULL,
  "keep_pinned" boolean DEFAULT true NOT NULL,
  "keep_polls" boolean DEFAULT false NOT NULL,
  "keep_media" boolean DEFAULT false NOT NULL,
  "keep_self_fav" boolean DEFAULT true NOT NULL,
  "keep_self_bookmark" boolean DEFAULT true NOT NULL,
  "min_favs" integer,
  "min_reblogs" integer,
  "created_at" timestamp(6) without time zone NOT NULL,
  "updated_at" timestamp(6) without time zone NOT NULL
);

-- paon:statement
CREATE INDEX "index_account_statuses_cleanup_policies_on_account_id" ON "account_statuses_cleanup_policies" ("account_id");

-- paon:statement
CREATE TABLE "account_warning_presets" (
  id bigserial PRIMARY KEY,
  "text" text DEFAULT '' NOT NULL,
  "created_at" timestamp without time zone NOT NULL,
  "updated_at" timestamp without time zone NOT NULL,
  "title" character varying DEFAULT '' NOT NULL
);

-- paon:statement
CREATE TABLE "account_warnings" (
  id bigserial PRIMARY KEY,
  "account_id" bigint,
  "target_account_id" bigint,
  "action" integer DEFAULT 0 NOT NULL,
  "text" text DEFAULT '' NOT NULL,
  "created_at" timestamp without time zone NOT NULL,
  "updated_at" timestamp without time zone NOT NULL,
  "report_id" bigint,
  "status_ids" character varying[],
  "overruled_at" timestamp without time zone
);

-- paon:statement
CREATE INDEX "index_account_warnings_on_account_id" ON "account_warnings" ("account_id");

-- paon:statement
CREATE INDEX "index_account_warnings_on_target_account_id" ON "account_warnings" ("target_account_id");

-- paon:statement
CREATE SEQUENCE "accounts_id_seq";

-- paon:statement
CREATE TABLE "accounts" (
  id bigint DEFAULT timestamp_id('accounts') NOT NULL PRIMARY KEY,
  "username" character varying DEFAULT '' NOT NULL,
  "domain" character varying,
  "private_key" text,
  "public_key" text DEFAULT '' NOT NULL,
  "created_at" timestamp without time zone NOT NULL,
  "updated_at" timestamp without time zone NOT NULL,
  "note" text DEFAULT '' NOT NULL,
  "display_name" character varying DEFAULT '' NOT NULL,
  "uri" character varying DEFAULT '' NOT NULL,
  "url" character varying,
  "avatar_file_name" character varying,
  "avatar_content_type" character varying,
  "avatar_file_size" integer,
  "avatar_updated_at" timestamp without time zone,
  "header_file_name" character varying,
  "header_content_type" character varying,
  "header_file_size" integer,
  "header_updated_at" timestamp without time zone,
  "avatar_remote_url" character varying,
  "locked" boolean DEFAULT false NOT NULL,
  "header_remote_url" character varying DEFAULT '' NOT NULL,
  "last_webfingered_at" timestamp without time zone,
  "inbox_url" character varying DEFAULT '' NOT NULL,
  "outbox_url" character varying DEFAULT '' NOT NULL,
  "shared_inbox_url" character varying DEFAULT '' NOT NULL,
  "followers_url" character varying DEFAULT '' NOT NULL,
  "protocol" integer DEFAULT 0 NOT NULL,
  "memorial" boolean DEFAULT false NOT NULL,
  "moved_to_account_id" bigint,
  "featured_collection_url" character varying,
  "fields" jsonb,
  "actor_type" character varying,
  "discoverable" boolean,
  "also_known_as" character varying[],
  "silenced_at" timestamp without time zone,
  "suspended_at" timestamp without time zone,
  "hide_collections" boolean,
  "avatar_storage_schema_version" integer,
  "header_storage_schema_version" integer,
  "sensitized_at" timestamp without time zone,
  "suspension_origin" integer,
  "trendable" boolean,
  "reviewed_at" timestamp without time zone,
  "requested_review_at" timestamp without time zone,
  "indexable" boolean DEFAULT false NOT NULL,
  "attribution_domains" character varying[] DEFAULT '{}'::character varying[]
);

-- paon:statement
CREATE INDEX "search_index" ON "accounts" USING gin ((((setweight(to_tsvector('simple'::regconfig, (display_name)::text), 'A'::"char") || setweight(to_tsvector('simple'::regconfig, (username)::text), 'B'::"char")) || setweight(to_tsvector('simple'::regconfig, (COALESCE(domain, ''::character varying))::text), 'C'::"char"))));

-- paon:statement
CREATE UNIQUE INDEX "index_accounts_on_username_and_domain_lower" ON "accounts" (lower((username)::text), COALESCE(lower((domain)::text), ''::text));

-- paon:statement
CREATE INDEX "index_accounts_on_domain_and_id" ON "accounts" ("domain", "id");

-- paon:statement
CREATE INDEX "index_accounts_on_moved_to_account_id" ON "accounts" ("moved_to_account_id") WHERE (moved_to_account_id IS NOT NULL);

-- paon:statement
CREATE INDEX "index_accounts_on_uri" ON "accounts" ("uri");

-- paon:statement
CREATE INDEX "index_accounts_on_url" ON "accounts" ("url" text_pattern_ops) WHERE (url IS NOT NULL);

-- paon:statement
CREATE TABLE "accounts_tags" (
  "account_id" bigint NOT NULL,
  "tag_id" bigint NOT NULL,
  PRIMARY KEY ("tag_id", "account_id")
);

-- paon:statement
CREATE INDEX "index_accounts_tags_on_account_id_and_tag_id" ON "accounts_tags" ("account_id", "tag_id");

-- paon:statement
CREATE TABLE "admin_action_logs" (
  id bigserial PRIMARY KEY,
  "account_id" bigint NOT NULL,
  "action" character varying DEFAULT '' NOT NULL,
  "target_type" character varying,
  "target_id" bigint,
  "created_at" timestamp without time zone NOT NULL,
  "updated_at" timestamp without time zone NOT NULL,
  "human_identifier" character varying,
  "route_param" character varying,
  "permalink" character varying
);

-- paon:statement
CREATE INDEX "index_admin_action_logs_on_account_id" ON "admin_action_logs" ("account_id");

-- paon:statement
CREATE INDEX "index_admin_action_logs_on_target_type_and_target_id" ON "admin_action_logs" ("target_type", "target_id");

-- paon:statement
CREATE TABLE "announcement_mutes" (
  id bigserial PRIMARY KEY,
  "account_id" bigint NOT NULL,
  "announcement_id" bigint NOT NULL,
  "created_at" timestamp without time zone NOT NULL,
  "updated_at" timestamp without time zone NOT NULL
);

-- paon:statement
CREATE UNIQUE INDEX "index_announcement_mutes_on_account_id_and_announcement_id" ON "announcement_mutes" ("account_id", "announcement_id");

-- paon:statement
CREATE INDEX "index_announcement_mutes_on_announcement_id" ON "announcement_mutes" ("announcement_id");

-- paon:statement
CREATE TABLE "announcement_reactions" (
  id bigserial PRIMARY KEY,
  "account_id" bigint NOT NULL,
  "announcement_id" bigint NOT NULL,
  "name" character varying DEFAULT '' NOT NULL,
  "custom_emoji_id" bigint,
  "created_at" timestamp without time zone NOT NULL,
  "updated_at" timestamp without time zone NOT NULL
);

-- paon:statement
CREATE UNIQUE INDEX "index_announcement_reactions_on_account_id_and_announcement_id" ON "announcement_reactions" ("account_id", "announcement_id", "name");

-- paon:statement
CREATE INDEX "index_announcement_reactions_on_announcement_id" ON "announcement_reactions" ("announcement_id");

-- paon:statement
CREATE INDEX "index_announcement_reactions_on_custom_emoji_id" ON "announcement_reactions" ("custom_emoji_id") WHERE (custom_emoji_id IS NOT NULL);

-- paon:statement
CREATE TABLE "announcements" (
  id bigserial PRIMARY KEY,
  "text" text DEFAULT '' NOT NULL,
  "published" boolean DEFAULT false NOT NULL,
  "all_day" boolean DEFAULT false NOT NULL,
  "scheduled_at" timestamp without time zone,
  "starts_at" timestamp without time zone,
  "ends_at" timestamp without time zone,
  "created_at" timestamp without time zone NOT NULL,
  "updated_at" timestamp without time zone NOT NULL,
  "published_at" timestamp without time zone,
  "status_ids" bigint[],
  "notification_sent_at" timestamp without time zone
);

-- paon:statement
CREATE TABLE "appeals" (
  id bigserial PRIMARY KEY,
  "account_id" bigint NOT NULL,
  "account_warning_id" bigint NOT NULL,
  "text" text DEFAULT '' NOT NULL,
  "approved_at" timestamp without time zone,
  "approved_by_account_id" bigint,
  "rejected_at" timestamp without time zone,
  "rejected_by_account_id" bigint,
  "created_at" timestamp(6) without time zone NOT NULL,
  "updated_at" timestamp(6) without time zone NOT NULL
);

-- paon:statement
CREATE INDEX "index_appeals_on_account_id" ON "appeals" ("account_id");

-- paon:statement
CREATE UNIQUE INDEX "index_appeals_on_account_warning_id" ON "appeals" ("account_warning_id");

-- paon:statement
CREATE INDEX "index_appeals_on_approved_by_account_id" ON "appeals" ("approved_by_account_id") WHERE (approved_by_account_id IS NOT NULL);

-- paon:statement
CREATE INDEX "index_appeals_on_rejected_by_account_id" ON "appeals" ("rejected_by_account_id") WHERE (rejected_by_account_id IS NOT NULL);

-- paon:statement
CREATE TABLE "backups" (
  id bigserial PRIMARY KEY,
  "user_id" bigint,
  "dump_file_name" character varying,
  "dump_content_type" character varying,
  "dump_updated_at" timestamp without time zone,
  "processed" boolean DEFAULT false NOT NULL,
  "created_at" timestamp without time zone NOT NULL,
  "updated_at" timestamp without time zone NOT NULL,
  "dump_file_size" bigint
);

-- paon:statement
CREATE INDEX "index_backups_on_user_id" ON "backups" ("user_id");

-- paon:statement
CREATE TABLE "blocks" (
  id bigserial PRIMARY KEY,
  "created_at" timestamp without time zone NOT NULL,
  "updated_at" timestamp without time zone NOT NULL,
  "account_id" bigint NOT NULL,
  "target_account_id" bigint NOT NULL,
  "uri" character varying
);

-- paon:statement
CREATE UNIQUE INDEX "index_blocks_on_account_id_and_target_account_id" ON "blocks" ("account_id", "target_account_id");

-- paon:statement
CREATE INDEX "index_blocks_on_target_account_id" ON "blocks" ("target_account_id");

-- paon:statement
CREATE TABLE "bookmarks" (
  id bigserial PRIMARY KEY,
  "account_id" bigint NOT NULL,
  "status_id" bigint NOT NULL,
  "created_at" timestamp without time zone NOT NULL,
  "updated_at" timestamp without time zone NOT NULL
);

-- paon:statement
CREATE UNIQUE INDEX "index_bookmarks_on_account_id_and_status_id" ON "bookmarks" ("account_id", "status_id");

-- paon:statement
CREATE INDEX "index_bookmarks_on_status_id" ON "bookmarks" ("status_id");

-- paon:statement
CREATE TABLE "bulk_import_rows" (
  id bigserial PRIMARY KEY,
  "bulk_import_id" bigint NOT NULL,
  "data" jsonb,
  "created_at" timestamp(6) without time zone NOT NULL,
  "updated_at" timestamp(6) without time zone NOT NULL
);

-- paon:statement
CREATE INDEX "index_bulk_import_rows_on_bulk_import_id" ON "bulk_import_rows" ("bulk_import_id");

-- paon:statement
CREATE TABLE "bulk_imports" (
  id bigserial PRIMARY KEY,
  "type" integer NOT NULL,
  "state" integer NOT NULL,
  "total_items" integer DEFAULT 0 NOT NULL,
  "imported_items" integer DEFAULT 0 NOT NULL,
  "processed_items" integer DEFAULT 0 NOT NULL,
  "finished_at" timestamp without time zone,
  "overwrite" boolean DEFAULT false NOT NULL,
  "likely_mismatched" boolean DEFAULT false NOT NULL,
  "original_filename" character varying DEFAULT '' NOT NULL,
  "account_id" bigint NOT NULL,
  "created_at" timestamp(6) without time zone NOT NULL,
  "updated_at" timestamp(6) without time zone NOT NULL
);

-- paon:statement
CREATE INDEX "index_bulk_imports_on_account_id" ON "bulk_imports" ("account_id");

-- paon:statement
CREATE INDEX "index_bulk_imports_unconfirmed" ON "bulk_imports" ("id") WHERE (state = 0);

-- paon:statement
CREATE TABLE "canonical_email_blocks" (
  id bigserial PRIMARY KEY,
  "canonical_email_hash" character varying DEFAULT '' NOT NULL,
  "reference_account_id" bigint,
  "created_at" timestamp(6) without time zone NOT NULL,
  "updated_at" timestamp(6) without time zone NOT NULL
);

-- paon:statement
CREATE UNIQUE INDEX "index_canonical_email_blocks_on_canonical_email_hash" ON "canonical_email_blocks" ("canonical_email_hash");

-- paon:statement
CREATE INDEX "index_canonical_email_blocks_on_reference_account_id" ON "canonical_email_blocks" ("reference_account_id");

-- paon:statement
CREATE TABLE "conversation_mutes" (
  id bigserial PRIMARY KEY,
  "conversation_id" bigint NOT NULL,
  "account_id" bigint NOT NULL
);

-- paon:statement
CREATE UNIQUE INDEX "index_conversation_mutes_on_account_id_and_conversation_id" ON "conversation_mutes" ("account_id", "conversation_id");

-- paon:statement
CREATE TABLE "conversations" (
  id bigserial PRIMARY KEY,
  "uri" character varying,
  "created_at" timestamp without time zone NOT NULL,
  "updated_at" timestamp without time zone NOT NULL
);

-- paon:statement
CREATE UNIQUE INDEX "index_conversations_on_uri" ON "conversations" ("uri" text_pattern_ops) WHERE (uri IS NOT NULL);

-- paon:statement
CREATE TABLE "custom_emoji_categories" (
  id bigserial PRIMARY KEY,
  "name" character varying,
  "created_at" timestamp without time zone NOT NULL,
  "updated_at" timestamp without time zone NOT NULL
);

-- paon:statement
CREATE UNIQUE INDEX "index_custom_emoji_categories_on_name" ON "custom_emoji_categories" ("name");

-- paon:statement
CREATE TABLE "custom_emojis" (
  id bigserial PRIMARY KEY,
  "shortcode" character varying DEFAULT '' NOT NULL,
  "domain" character varying,
  "image_file_name" character varying,
  "image_content_type" character varying,
  "image_file_size" integer,
  "image_updated_at" timestamp without time zone,
  "created_at" timestamp without time zone NOT NULL,
  "updated_at" timestamp without time zone NOT NULL,
  "disabled" boolean DEFAULT false NOT NULL,
  "uri" character varying,
  "image_remote_url" character varying,
  "visible_in_picker" boolean DEFAULT true NOT NULL,
  "category_id" bigint,
  "image_storage_schema_version" integer
);

-- paon:statement
CREATE UNIQUE INDEX "index_custom_emojis_on_shortcode_and_domain" ON "custom_emojis" ("shortcode", "domain");

-- paon:statement
CREATE TABLE "custom_filter_keywords" (
  id bigserial PRIMARY KEY,
  "custom_filter_id" bigint NOT NULL,
  "keyword" text DEFAULT '' NOT NULL,
  "whole_word" boolean DEFAULT true NOT NULL,
  "created_at" timestamp(6) without time zone NOT NULL,
  "updated_at" timestamp(6) without time zone NOT NULL
);

-- paon:statement
CREATE INDEX "index_custom_filter_keywords_on_custom_filter_id" ON "custom_filter_keywords" ("custom_filter_id");

-- paon:statement
CREATE TABLE "custom_filter_statuses" (
  id bigserial PRIMARY KEY,
  "custom_filter_id" bigint NOT NULL,
  "status_id" bigint NOT NULL,
  "created_at" timestamp(6) without time zone NOT NULL,
  "updated_at" timestamp(6) without time zone NOT NULL
);

-- paon:statement
CREATE INDEX "index_custom_filter_statuses_on_custom_filter_id" ON "custom_filter_statuses" ("custom_filter_id");

-- paon:statement
CREATE UNIQUE INDEX "index_custom_filter_statuses_on_status_id_and_custom_filter_id" ON "custom_filter_statuses" ("status_id", "custom_filter_id");

-- paon:statement
CREATE TABLE "custom_filters" (
  id bigserial PRIMARY KEY,
  "account_id" bigint NOT NULL,
  "expires_at" timestamp without time zone,
  "phrase" text DEFAULT '' NOT NULL,
  "context" character varying[] DEFAULT '{}'::character varying[] NOT NULL,
  "created_at" timestamp without time zone NOT NULL,
  "updated_at" timestamp without time zone NOT NULL,
  "action" integer DEFAULT 0 NOT NULL
);

-- paon:statement
CREATE INDEX "index_custom_filters_on_account_id" ON "custom_filters" ("account_id");

-- paon:statement
CREATE TABLE "domain_allows" (
  id bigserial PRIMARY KEY,
  "domain" character varying DEFAULT '' NOT NULL,
  "created_at" timestamp without time zone NOT NULL,
  "updated_at" timestamp without time zone NOT NULL
);

-- paon:statement
CREATE UNIQUE INDEX "index_domain_allows_on_domain" ON "domain_allows" ("domain");

-- paon:statement
CREATE TABLE "domain_blocks" (
  id bigserial PRIMARY KEY,
  "domain" character varying DEFAULT '' NOT NULL,
  "created_at" timestamp without time zone NOT NULL,
  "updated_at" timestamp without time zone NOT NULL,
  "severity" integer DEFAULT 0,
  "reject_media" boolean DEFAULT false NOT NULL,
  "reject_reports" boolean DEFAULT false NOT NULL,
  "private_comment" text,
  "public_comment" text,
  "obfuscate" boolean DEFAULT false NOT NULL
);

-- paon:statement
CREATE UNIQUE INDEX "index_domain_blocks_on_domain" ON "domain_blocks" ("domain");

-- paon:statement
CREATE TABLE "email_domain_blocks" (
  id bigserial PRIMARY KEY,
  "domain" character varying DEFAULT '' NOT NULL,
  "created_at" timestamp without time zone NOT NULL,
  "updated_at" timestamp without time zone NOT NULL,
  "parent_id" bigint,
  "allow_with_approval" boolean DEFAULT false NOT NULL
);

-- paon:statement
CREATE UNIQUE INDEX "index_email_domain_blocks_on_domain" ON "email_domain_blocks" ("domain");

-- paon:statement
CREATE TABLE "favourites" (
  id bigserial PRIMARY KEY,
  "created_at" timestamp without time zone NOT NULL,
  "updated_at" timestamp without time zone NOT NULL,
  "account_id" bigint NOT NULL,
  "status_id" bigint NOT NULL
);

-- paon:statement
CREATE INDEX "index_favourites_on_account_id_and_id" ON "favourites" ("account_id", "id");

-- paon:statement
CREATE UNIQUE INDEX "index_favourites_on_account_id_and_status_id" ON "favourites" ("account_id", "status_id");

-- paon:statement
CREATE INDEX "index_favourites_on_status_id" ON "favourites" ("status_id");

-- paon:statement
CREATE TABLE "featured_tags" (
  id bigserial PRIMARY KEY,
  "account_id" bigint NOT NULL,
  "tag_id" bigint NOT NULL,
  "statuses_count" bigint DEFAULT 0 NOT NULL,
  "last_status_at" timestamp without time zone,
  "created_at" timestamp without time zone NOT NULL,
  "updated_at" timestamp without time zone NOT NULL,
  "name" character varying
);

-- paon:statement
CREATE UNIQUE INDEX "index_featured_tags_on_account_id_and_tag_id" ON "featured_tags" ("account_id", "tag_id");

-- paon:statement
CREATE INDEX "index_featured_tags_on_tag_id" ON "featured_tags" ("tag_id");

-- paon:statement
CREATE TABLE "follow_recommendation_mutes" (
  id bigserial PRIMARY KEY,
  "account_id" bigint NOT NULL,
  "target_account_id" bigint NOT NULL,
  "created_at" timestamp(6) without time zone NOT NULL,
  "updated_at" timestamp(6) without time zone NOT NULL
);

-- paon:statement
CREATE UNIQUE INDEX "idx_on_account_id_target_account_id_a8c8ddf44e" ON "follow_recommendation_mutes" ("account_id", "target_account_id");

-- paon:statement
CREATE INDEX "index_follow_recommendation_mutes_on_target_account_id" ON "follow_recommendation_mutes" ("target_account_id");

-- paon:statement
CREATE TABLE "follow_recommendation_suppressions" (
  id bigserial PRIMARY KEY,
  "account_id" bigint NOT NULL,
  "created_at" timestamp(6) without time zone NOT NULL,
  "updated_at" timestamp(6) without time zone NOT NULL
);

-- paon:statement
CREATE UNIQUE INDEX "index_follow_recommendation_suppressions_on_account_id" ON "follow_recommendation_suppressions" ("account_id");

-- paon:statement
CREATE TABLE "follow_requests" (
  id bigserial PRIMARY KEY,
  "created_at" timestamp without time zone NOT NULL,
  "updated_at" timestamp without time zone NOT NULL,
  "account_id" bigint NOT NULL,
  "target_account_id" bigint NOT NULL,
  "show_reblogs" boolean DEFAULT true NOT NULL,
  "uri" character varying,
  "notify" boolean DEFAULT false NOT NULL,
  "languages" character varying[]
);

-- paon:statement
CREATE UNIQUE INDEX "index_follow_requests_on_account_id_and_target_account_id" ON "follow_requests" ("account_id", "target_account_id");

-- paon:statement
CREATE TABLE "follows" (
  id bigserial PRIMARY KEY,
  "created_at" timestamp without time zone NOT NULL,
  "updated_at" timestamp without time zone NOT NULL,
  "account_id" bigint NOT NULL,
  "target_account_id" bigint NOT NULL,
  "show_reblogs" boolean DEFAULT true NOT NULL,
  "uri" character varying,
  "notify" boolean DEFAULT false NOT NULL,
  "languages" character varying[]
);

-- paon:statement
CREATE UNIQUE INDEX "index_follows_on_account_id_and_target_account_id" ON "follows" ("account_id", "target_account_id");

-- paon:statement
CREATE INDEX "index_follows_on_target_account_id" ON "follows" ("target_account_id");

-- paon:statement
CREATE TABLE "generated_annual_reports" (
  id bigserial PRIMARY KEY,
  "account_id" bigint NOT NULL,
  "year" integer NOT NULL,
  "data" jsonb NOT NULL,
  "schema_version" integer NOT NULL,
  "viewed_at" timestamp(6) without time zone,
  "created_at" timestamp(6) without time zone NOT NULL,
  "updated_at" timestamp(6) without time zone NOT NULL
);

-- paon:statement
CREATE UNIQUE INDEX "index_generated_annual_reports_on_account_id_and_year" ON "generated_annual_reports" ("account_id", "year");

-- paon:statement
CREATE TABLE "identities" (
  id bigserial PRIMARY KEY,
  "provider" character varying DEFAULT '' NOT NULL,
  "uid" character varying DEFAULT '' NOT NULL,
  "created_at" timestamp without time zone NOT NULL,
  "updated_at" timestamp without time zone NOT NULL,
  "user_id" bigint
);

-- paon:statement
CREATE UNIQUE INDEX "index_identities_on_uid_and_provider" ON "identities" ("uid", "provider");

-- paon:statement
CREATE INDEX "index_identities_on_user_id" ON "identities" ("user_id");

-- paon:statement
CREATE TABLE "invites" (
  id bigserial PRIMARY KEY,
  "user_id" bigint NOT NULL,
  "code" character varying DEFAULT '' NOT NULL,
  "expires_at" timestamp without time zone,
  "max_uses" integer,
  "uses" integer DEFAULT 0 NOT NULL,
  "created_at" timestamp without time zone NOT NULL,
  "updated_at" timestamp without time zone NOT NULL,
  "autofollow" boolean DEFAULT false NOT NULL,
  "comment" text
);

-- paon:statement
CREATE UNIQUE INDEX "index_invites_on_code" ON "invites" ("code");

-- paon:statement
CREATE INDEX "index_invites_on_user_id" ON "invites" ("user_id");

-- paon:statement
CREATE TABLE "ip_blocks" (
  id bigserial PRIMARY KEY,
  "created_at" timestamp without time zone NOT NULL,
  "updated_at" timestamp without time zone NOT NULL,
  "expires_at" timestamp without time zone,
  "ip" inet DEFAULT '0.0.0.0' NOT NULL,
  "severity" integer DEFAULT 0 NOT NULL,
  "comment" text DEFAULT '' NOT NULL
);

-- paon:statement
CREATE UNIQUE INDEX "index_ip_blocks_on_ip" ON "ip_blocks" ("ip");

-- paon:statement
CREATE TABLE "list_accounts" (
  id bigserial PRIMARY KEY,
  "list_id" bigint NOT NULL,
  "account_id" bigint NOT NULL,
  "follow_id" bigint,
  "follow_request_id" bigint
);

-- paon:statement
CREATE UNIQUE INDEX "index_list_accounts_on_account_id_and_list_id" ON "list_accounts" ("account_id", "list_id");

-- paon:statement
CREATE INDEX "index_list_accounts_on_follow_id" ON "list_accounts" ("follow_id") WHERE (follow_id IS NOT NULL);

-- paon:statement
CREATE INDEX "index_list_accounts_on_follow_request_id" ON "list_accounts" ("follow_request_id") WHERE (follow_request_id IS NOT NULL);

-- paon:statement
CREATE INDEX "index_list_accounts_on_list_id_and_account_id" ON "list_accounts" ("list_id", "account_id");

-- paon:statement
CREATE TABLE "lists" (
  id bigserial PRIMARY KEY,
  "account_id" bigint NOT NULL,
  "title" character varying DEFAULT '' NOT NULL,
  "created_at" timestamp without time zone NOT NULL,
  "updated_at" timestamp without time zone NOT NULL,
  "replies_policy" integer DEFAULT 0 NOT NULL,
  "exclusive" boolean DEFAULT false NOT NULL
);

-- paon:statement
CREATE INDEX "index_lists_on_account_id" ON "lists" ("account_id");

-- paon:statement
CREATE TABLE "login_activities" (
  id bigserial PRIMARY KEY,
  "user_id" bigint NOT NULL,
  "authentication_method" character varying,
  "provider" character varying,
  "success" boolean,
  "failure_reason" character varying,
  "ip" inet,
  "user_agent" character varying,
  "created_at" timestamp without time zone
);

-- paon:statement
CREATE INDEX "index_login_activities_on_user_id" ON "login_activities" ("user_id");

-- paon:statement
CREATE TABLE "markers" (
  id bigserial PRIMARY KEY,
  "user_id" bigint NOT NULL,
  "timeline" character varying DEFAULT '' NOT NULL,
  "last_read_id" bigint DEFAULT 0 NOT NULL,
  "lock_version" integer DEFAULT 0 NOT NULL,
  "created_at" timestamp without time zone NOT NULL,
  "updated_at" timestamp without time zone NOT NULL
);

-- paon:statement
CREATE UNIQUE INDEX "index_markers_on_user_id_and_timeline" ON "markers" ("user_id", "timeline");

-- paon:statement
CREATE SEQUENCE "media_attachments_id_seq";

-- paon:statement
CREATE TABLE "media_attachments" (
  id bigint DEFAULT timestamp_id('media_attachments') NOT NULL PRIMARY KEY,
  "status_id" bigint,
  "file_file_name" character varying,
  "file_content_type" character varying,
  "file_file_size" integer,
  "file_updated_at" timestamp without time zone,
  "remote_url" character varying DEFAULT '' NOT NULL,
  "created_at" timestamp without time zone NOT NULL,
  "updated_at" timestamp without time zone NOT NULL,
  "shortcode" character varying,
  "type" integer DEFAULT 0 NOT NULL,
  "file_meta" json,
  "account_id" bigint,
  "description" text,
  "scheduled_status_id" bigint,
  "blurhash" character varying,
  "processing" integer,
  "file_storage_schema_version" integer,
  "thumbnail_file_name" character varying,
  "thumbnail_content_type" character varying,
  "thumbnail_file_size" integer,
  "thumbnail_updated_at" timestamp without time zone,
  "thumbnail_remote_url" character varying
);

-- paon:statement
CREATE INDEX "index_media_attachments_on_account_id_and_status_id" ON "media_attachments" ("account_id", "status_id" DESC);

-- paon:statement
CREATE INDEX "index_media_attachments_on_scheduled_status_id" ON "media_attachments" ("scheduled_status_id") WHERE (scheduled_status_id IS NOT NULL);

-- paon:statement
CREATE UNIQUE INDEX "index_media_attachments_on_shortcode" ON "media_attachments" ("shortcode" text_pattern_ops) WHERE (shortcode IS NOT NULL);

-- paon:statement
CREATE INDEX "index_media_attachments_on_status_id" ON "media_attachments" ("status_id");

-- paon:statement
CREATE TABLE "mentions" (
  id bigserial PRIMARY KEY,
  "status_id" bigint NOT NULL,
  "created_at" timestamp without time zone NOT NULL,
  "updated_at" timestamp without time zone NOT NULL,
  "account_id" bigint NOT NULL,
  "silent" boolean DEFAULT false NOT NULL
);

-- paon:statement
CREATE UNIQUE INDEX "index_mentions_on_account_id_and_status_id" ON "mentions" ("account_id", "status_id");

-- paon:statement
CREATE INDEX "index_mentions_on_status_id" ON "mentions" ("status_id");

-- paon:statement
CREATE TABLE "mutes" (
  id bigserial PRIMARY KEY,
  "created_at" timestamp without time zone NOT NULL,
  "updated_at" timestamp without time zone NOT NULL,
  "hide_notifications" boolean DEFAULT true NOT NULL,
  "account_id" bigint NOT NULL,
  "target_account_id" bigint NOT NULL,
  "expires_at" timestamp without time zone
);

-- paon:statement
CREATE UNIQUE INDEX "index_mutes_on_account_id_and_target_account_id" ON "mutes" ("account_id", "target_account_id");

-- paon:statement
CREATE INDEX "index_mutes_on_target_account_id" ON "mutes" ("target_account_id");

-- paon:statement
CREATE TABLE "notification_permissions" (
  id bigserial PRIMARY KEY,
  "account_id" bigint NOT NULL,
  "from_account_id" bigint NOT NULL,
  "created_at" timestamp(6) without time zone NOT NULL,
  "updated_at" timestamp(6) without time zone NOT NULL
);

-- paon:statement
CREATE INDEX "index_notification_permissions_on_account_id" ON "notification_permissions" ("account_id");

-- paon:statement
CREATE INDEX "index_notification_permissions_on_from_account_id" ON "notification_permissions" ("from_account_id");

-- paon:statement
CREATE TABLE "notification_policies" (
  id bigserial PRIMARY KEY,
  "account_id" bigint NOT NULL,
  "created_at" timestamp(6) without time zone NOT NULL,
  "updated_at" timestamp(6) without time zone NOT NULL,
  "for_not_following" integer DEFAULT 0 NOT NULL,
  "for_not_followers" integer DEFAULT 0 NOT NULL,
  "for_new_accounts" integer DEFAULT 0 NOT NULL,
  "for_private_mentions" integer DEFAULT 1 NOT NULL,
  "for_limited_accounts" integer DEFAULT 1 NOT NULL
);

-- paon:statement
CREATE UNIQUE INDEX "index_notification_policies_on_account_id" ON "notification_policies" ("account_id");

-- paon:statement
CREATE SEQUENCE "notification_requests_id_seq";

-- paon:statement
CREATE TABLE "notification_requests" (
  id bigint DEFAULT timestamp_id('notification_requests') NOT NULL PRIMARY KEY,
  "account_id" bigint NOT NULL,
  "from_account_id" bigint NOT NULL,
  "last_status_id" bigint,
  "notifications_count" bigint DEFAULT 0 NOT NULL,
  "created_at" timestamp(6) without time zone NOT NULL,
  "updated_at" timestamp(6) without time zone NOT NULL
);

-- paon:statement
CREATE UNIQUE INDEX "index_notification_requests_on_account_id_and_from_account_id" ON "notification_requests" ("account_id", "from_account_id");

-- paon:statement
CREATE INDEX "index_notification_requests_on_from_account_id" ON "notification_requests" ("from_account_id");

-- paon:statement
CREATE INDEX "index_notification_requests_on_last_status_id" ON "notification_requests" ("last_status_id");

-- paon:statement
CREATE TABLE "notifications" (
  id bigserial PRIMARY KEY,
  "activity_id" bigint NOT NULL,
  "activity_type" character varying NOT NULL,
  "created_at" timestamp without time zone NOT NULL,
  "updated_at" timestamp without time zone NOT NULL,
  "account_id" bigint NOT NULL,
  "from_account_id" bigint NOT NULL,
  "type" character varying,
  "filtered" boolean DEFAULT false NOT NULL,
  "group_key" character varying
);

-- paon:statement
CREATE INDEX "index_notifications_on_account_id_and_group_key" ON "notifications" ("account_id", "group_key") WHERE (group_key IS NOT NULL);

-- paon:statement
CREATE INDEX "index_notifications_on_account_id_and_id_and_type" ON "notifications" ("account_id", "id" DESC, "type");

-- paon:statement
CREATE INDEX "index_notifications_on_filtered" ON "notifications" ("account_id", "id" DESC, "type") WHERE (filtered = false);

-- paon:statement
CREATE INDEX "index_notifications_on_activity_id_and_activity_type" ON "notifications" ("activity_id", "activity_type");

-- paon:statement
CREATE INDEX "index_notifications_on_from_account_id" ON "notifications" ("from_account_id");

-- paon:statement
CREATE TABLE "oauth_access_grants" (
  id bigserial PRIMARY KEY,
  "token" character varying NOT NULL,
  "expires_in" integer NOT NULL,
  "redirect_uri" text NOT NULL,
  "created_at" timestamp without time zone NOT NULL,
  "revoked_at" timestamp without time zone,
  "scopes" character varying,
  "application_id" bigint NOT NULL,
  "resource_owner_id" bigint NOT NULL,
  "code_challenge" character varying,
  "code_challenge_method" character varying
);

-- paon:statement
CREATE INDEX "index_oauth_access_grants_on_resource_owner_id" ON "oauth_access_grants" ("resource_owner_id");

-- paon:statement
CREATE UNIQUE INDEX "index_oauth_access_grants_on_token" ON "oauth_access_grants" ("token");

-- paon:statement
CREATE TABLE "oauth_access_tokens" (
  id bigserial PRIMARY KEY,
  "token" character varying NOT NULL,
  "refresh_token" character varying,
  "expires_in" integer,
  "revoked_at" timestamp without time zone,
  "created_at" timestamp without time zone NOT NULL,
  "scopes" character varying,
  "application_id" bigint,
  "resource_owner_id" bigint,
  "last_used_at" timestamp without time zone,
  "last_used_ip" inet
);

-- paon:statement
CREATE UNIQUE INDEX "index_oauth_access_tokens_on_refresh_token" ON "oauth_access_tokens" ("refresh_token" text_pattern_ops) WHERE (refresh_token IS NOT NULL);

-- paon:statement
CREATE INDEX "index_oauth_access_tokens_on_resource_owner_id" ON "oauth_access_tokens" ("resource_owner_id") WHERE (resource_owner_id IS NOT NULL);

-- paon:statement
CREATE UNIQUE INDEX "index_oauth_access_tokens_on_token" ON "oauth_access_tokens" ("token");

-- paon:statement
CREATE TABLE "oauth_applications" (
  id bigserial PRIMARY KEY,
  "name" character varying NOT NULL,
  "uid" character varying NOT NULL,
  "secret" character varying NOT NULL,
  "redirect_uri" text NOT NULL,
  "scopes" character varying DEFAULT '' NOT NULL,
  "created_at" timestamp without time zone,
  "updated_at" timestamp without time zone,
  "superapp" boolean DEFAULT false NOT NULL,
  "website" character varying,
  "owner_type" character varying,
  "owner_id" bigint,
  "confidential" boolean DEFAULT true NOT NULL
);

-- paon:statement
CREATE INDEX "index_oauth_applications_on_owner_id_and_owner_type" ON "oauth_applications" ("owner_id", "owner_type");

-- paon:statement
CREATE INDEX "index_oauth_applications_on_superapp" ON "oauth_applications" ("superapp") WHERE (superapp = true);

-- paon:statement
CREATE UNIQUE INDEX "index_oauth_applications_on_uid" ON "oauth_applications" ("uid");

-- paon:statement
CREATE TABLE "pghero_space_stats" (
  id bigserial PRIMARY KEY,
  "database" text,
  "schema" text,
  "relation" text,
  "size" bigint,
  "captured_at" timestamp without time zone
);

-- paon:statement
CREATE INDEX "index_pghero_space_stats_on_database_and_captured_at" ON "pghero_space_stats" ("database", "captured_at");

-- paon:statement
CREATE TABLE "poll_votes" (
  id bigserial PRIMARY KEY,
  "account_id" bigint NOT NULL,
  "poll_id" bigint NOT NULL,
  "choice" integer DEFAULT 0 NOT NULL,
  "created_at" timestamp without time zone NOT NULL,
  "updated_at" timestamp without time zone NOT NULL,
  "uri" character varying
);

-- paon:statement
CREATE INDEX "index_poll_votes_on_account_id" ON "poll_votes" ("account_id");

-- paon:statement
CREATE INDEX "index_poll_votes_on_poll_id" ON "poll_votes" ("poll_id");

-- paon:statement
CREATE TABLE "polls" (
  id bigserial PRIMARY KEY,
  "account_id" bigint NOT NULL,
  "status_id" bigint NOT NULL,
  "expires_at" timestamp without time zone,
  "options" character varying[] DEFAULT '{}'::character varying[] NOT NULL,
  "cached_tallies" bigint[] DEFAULT '{}'::bigint[] NOT NULL,
  "multiple" boolean DEFAULT false NOT NULL,
  "hide_totals" boolean DEFAULT false NOT NULL,
  "votes_count" bigint DEFAULT 0 NOT NULL,
  "last_fetched_at" timestamp without time zone,
  "created_at" timestamp without time zone NOT NULL,
  "updated_at" timestamp without time zone NOT NULL,
  "lock_version" integer DEFAULT 0 NOT NULL,
  "voters_count" bigint
);

-- paon:statement
CREATE INDEX "index_polls_on_account_id" ON "polls" ("account_id");

-- paon:statement
CREATE INDEX "index_polls_on_status_id" ON "polls" ("status_id");

-- paon:statement
CREATE TABLE "preview_card_providers" (
  id bigserial PRIMARY KEY,
  "domain" character varying DEFAULT '' NOT NULL,
  "icon_file_name" character varying,
  "icon_content_type" character varying,
  "icon_file_size" bigint,
  "icon_updated_at" timestamp without time zone,
  "trendable" boolean,
  "reviewed_at" timestamp without time zone,
  "requested_review_at" timestamp without time zone,
  "created_at" timestamp(6) without time zone NOT NULL,
  "updated_at" timestamp(6) without time zone NOT NULL
);

-- paon:statement
CREATE UNIQUE INDEX "index_preview_card_providers_on_domain" ON "preview_card_providers" ("domain");

-- paon:statement
CREATE TABLE "preview_card_trends" (
  id bigserial PRIMARY KEY,
  "preview_card_id" bigint NOT NULL,
  "score" double precision DEFAULT 0.0 NOT NULL,
  "rank" integer DEFAULT 0 NOT NULL,
  "allowed" boolean DEFAULT false NOT NULL,
  "language" character varying
);

-- paon:statement
CREATE UNIQUE INDEX "index_preview_card_trends_on_preview_card_id" ON "preview_card_trends" ("preview_card_id");

-- paon:statement
CREATE TABLE "preview_cards" (
  id bigserial PRIMARY KEY,
  "url" character varying DEFAULT '' NOT NULL,
  "title" character varying DEFAULT '' NOT NULL,
  "description" character varying DEFAULT '' NOT NULL,
  "image_file_name" character varying,
  "image_content_type" character varying,
  "image_file_size" integer,
  "image_updated_at" timestamp without time zone,
  "type" integer DEFAULT 0 NOT NULL,
  "html" text DEFAULT '' NOT NULL,
  "author_name" character varying DEFAULT '' NOT NULL,
  "author_url" character varying DEFAULT '' NOT NULL,
  "provider_name" character varying DEFAULT '' NOT NULL,
  "provider_url" character varying DEFAULT '' NOT NULL,
  "width" integer DEFAULT 0 NOT NULL,
  "height" integer DEFAULT 0 NOT NULL,
  "created_at" timestamp without time zone NOT NULL,
  "updated_at" timestamp without time zone NOT NULL,
  "embed_url" character varying DEFAULT '' NOT NULL,
  "image_storage_schema_version" integer,
  "blurhash" character varying,
  "language" character varying,
  "max_score" double precision,
  "max_score_at" timestamp without time zone,
  "trendable" boolean,
  "link_type" integer,
  "published_at" timestamp(6) without time zone,
  "image_description" character varying DEFAULT '' NOT NULL,
  "author_account_id" bigint
);

-- paon:statement
CREATE INDEX "index_preview_cards_on_author_account_id" ON "preview_cards" ("author_account_id") WHERE (author_account_id IS NOT NULL);

-- paon:statement
CREATE UNIQUE INDEX "index_preview_cards_on_url" ON "preview_cards" ("url");

-- paon:statement
CREATE TABLE "preview_cards_statuses" (
  "preview_card_id" bigint NOT NULL,
  "status_id" bigint NOT NULL,
  "url" character varying,
  PRIMARY KEY ("status_id", "preview_card_id")
);

-- paon:statement
CREATE TABLE "relationship_severance_events" (
  id bigserial PRIMARY KEY,
  "type" integer NOT NULL,
  "target_name" character varying NOT NULL,
  "purged" boolean DEFAULT false NOT NULL,
  "created_at" timestamp(6) without time zone NOT NULL,
  "updated_at" timestamp(6) without time zone NOT NULL
);

-- paon:statement
CREATE INDEX "index_relationship_severance_events_on_type_and_target_name" ON "relationship_severance_events" ("type", "target_name");

-- paon:statement
CREATE TABLE "relays" (
  id bigserial PRIMARY KEY,
  "inbox_url" character varying DEFAULT '' NOT NULL,
  "follow_activity_id" character varying,
  "created_at" timestamp without time zone NOT NULL,
  "updated_at" timestamp without time zone NOT NULL,
  "state" integer DEFAULT 0 NOT NULL
);

-- paon:statement
CREATE TABLE "report_notes" (
  id bigserial PRIMARY KEY,
  "content" text NOT NULL,
  "report_id" bigint NOT NULL,
  "account_id" bigint NOT NULL,
  "created_at" timestamp without time zone NOT NULL,
  "updated_at" timestamp without time zone NOT NULL
);

-- paon:statement
CREATE INDEX "index_report_notes_on_account_id" ON "report_notes" ("account_id");

-- paon:statement
CREATE INDEX "index_report_notes_on_report_id" ON "report_notes" ("report_id");

-- paon:statement
CREATE TABLE "reports" (
  id bigserial PRIMARY KEY,
  "status_ids" bigint[] DEFAULT '{}'::bigint[] NOT NULL,
  "comment" text DEFAULT '' NOT NULL,
  "created_at" timestamp without time zone NOT NULL,
  "updated_at" timestamp without time zone NOT NULL,
  "account_id" bigint NOT NULL,
  "action_taken_by_account_id" bigint,
  "target_account_id" bigint NOT NULL,
  "assigned_account_id" bigint,
  "uri" character varying,
  "forwarded" boolean,
  "category" integer DEFAULT 0 NOT NULL,
  "action_taken_at" timestamp without time zone,
  "rule_ids" bigint[],
  "application_id" bigint
);

-- paon:statement
CREATE INDEX "index_reports_on_account_id" ON "reports" ("account_id");

-- paon:statement
CREATE INDEX "index_reports_on_action_taken_by_account_id" ON "reports" ("action_taken_by_account_id") WHERE (action_taken_by_account_id IS NOT NULL);

-- paon:statement
CREATE INDEX "index_reports_on_assigned_account_id" ON "reports" ("assigned_account_id") WHERE (assigned_account_id IS NOT NULL);

-- paon:statement
CREATE INDEX "index_reports_on_target_account_id" ON "reports" ("target_account_id");

-- paon:statement
CREATE TABLE "rules" (
  id bigserial PRIMARY KEY,
  "priority" integer DEFAULT 0 NOT NULL,
  "deleted_at" timestamp without time zone,
  "text" text DEFAULT '' NOT NULL,
  "created_at" timestamp without time zone NOT NULL,
  "updated_at" timestamp without time zone NOT NULL,
  "hint" text DEFAULT '' NOT NULL
);

-- paon:statement
CREATE TABLE "scheduled_statuses" (
  id bigserial PRIMARY KEY,
  "account_id" bigint NOT NULL,
  "scheduled_at" timestamp without time zone,
  "params" jsonb
);

-- paon:statement
CREATE INDEX "index_scheduled_statuses_on_account_id" ON "scheduled_statuses" ("account_id");

-- paon:statement
CREATE INDEX "index_scheduled_statuses_on_scheduled_at" ON "scheduled_statuses" ("scheduled_at");

-- paon:statement
CREATE TABLE "session_activations" (
  id bigserial PRIMARY KEY,
  "session_id" character varying NOT NULL,
  "created_at" timestamp without time zone NOT NULL,
  "updated_at" timestamp without time zone NOT NULL,
  "user_agent" character varying DEFAULT '' NOT NULL,
  "ip" inet,
  "access_token_id" bigint,
  "user_id" bigint NOT NULL,
  "web_push_subscription_id" bigint
);

-- paon:statement
CREATE INDEX "index_session_activations_on_access_token_id" ON "session_activations" ("access_token_id");

-- paon:statement
CREATE UNIQUE INDEX "index_session_activations_on_session_id" ON "session_activations" ("session_id");

-- paon:statement
CREATE INDEX "index_session_activations_on_user_id" ON "session_activations" ("user_id");

-- paon:statement
CREATE TABLE "settings" (
  id bigserial PRIMARY KEY,
  "var" character varying NOT NULL,
  "value" text,
  "created_at" timestamp without time zone,
  "updated_at" timestamp without time zone
);

-- paon:statement
CREATE UNIQUE INDEX "index_settings_on_var" ON "settings" ("var");

-- paon:statement
CREATE TABLE "severed_relationships" (
  id bigserial PRIMARY KEY,
  "relationship_severance_event_id" bigint NOT NULL,
  "local_account_id" bigint NOT NULL,
  "remote_account_id" bigint NOT NULL,
  "direction" integer NOT NULL,
  "show_reblogs" boolean,
  "notify" boolean,
  "languages" character varying[],
  "created_at" timestamp(6) without time zone NOT NULL,
  "updated_at" timestamp(6) without time zone NOT NULL
);

-- paon:statement
CREATE INDEX "index_severed_relationships_on_local_account_and_event" ON "severed_relationships" ("local_account_id", "relationship_severance_event_id");

-- paon:statement
CREATE UNIQUE INDEX "index_severed_relationships_on_unique_tuples" ON "severed_relationships" ("relationship_severance_event_id", "local_account_id", "direction", "remote_account_id");

-- paon:statement
CREATE INDEX "index_severed_relationships_on_remote_account_id" ON "severed_relationships" ("remote_account_id");

-- paon:statement
CREATE TABLE "site_uploads" (
  id bigserial PRIMARY KEY,
  "var" character varying DEFAULT '' NOT NULL,
  "file_file_name" character varying,
  "file_content_type" character varying,
  "file_file_size" integer,
  "file_updated_at" timestamp without time zone,
  "meta" json,
  "created_at" timestamp without time zone NOT NULL,
  "updated_at" timestamp without time zone NOT NULL,
  "blurhash" character varying
);

-- paon:statement
CREATE UNIQUE INDEX "index_site_uploads_on_var" ON "site_uploads" ("var");

-- paon:statement
CREATE TABLE "software_updates" (
  id bigserial PRIMARY KEY,
  "version" character varying NOT NULL,
  "urgent" boolean DEFAULT false NOT NULL,
  "type" integer DEFAULT 0 NOT NULL,
  "release_notes" character varying DEFAULT '' NOT NULL,
  "created_at" timestamp(6) without time zone NOT NULL,
  "updated_at" timestamp(6) without time zone NOT NULL
);

-- paon:statement
CREATE UNIQUE INDEX "index_software_updates_on_version" ON "software_updates" ("version");

-- paon:statement
CREATE TABLE "status_edits" (
  id bigserial PRIMARY KEY,
  "status_id" bigint NOT NULL,
  "account_id" bigint,
  "text" text DEFAULT '' NOT NULL,
  "spoiler_text" text DEFAULT '' NOT NULL,
  "created_at" timestamp(6) without time zone NOT NULL,
  "updated_at" timestamp(6) without time zone NOT NULL,
  "ordered_media_attachment_ids" bigint[],
  "media_descriptions" text[],
  "poll_options" character varying[],
  "sensitive" boolean,
  "quote_id" bigint
);

-- paon:statement
CREATE INDEX "index_status_edits_on_account_id" ON "status_edits" ("account_id");

-- paon:statement
CREATE INDEX "index_status_edits_on_status_id" ON "status_edits" ("status_id");

-- paon:statement
CREATE TABLE "status_pins" (
  id bigserial PRIMARY KEY,
  "account_id" bigint NOT NULL,
  "status_id" bigint NOT NULL,
  "created_at" timestamp without time zone NOT NULL,
  "updated_at" timestamp without time zone NOT NULL
);

-- paon:statement
CREATE UNIQUE INDEX "index_status_pins_on_account_id_and_status_id" ON "status_pins" ("account_id", "status_id");

-- paon:statement
CREATE INDEX "index_status_pins_on_status_id" ON "status_pins" ("status_id");

-- paon:statement
CREATE TABLE "status_stats" (
  id bigserial PRIMARY KEY,
  "status_id" bigint NOT NULL,
  "replies_count" bigint DEFAULT 0 NOT NULL,
  "reblogs_count" bigint DEFAULT 0 NOT NULL,
  "favourites_count" bigint DEFAULT 0 NOT NULL,
  "created_at" timestamp without time zone NOT NULL,
  "updated_at" timestamp without time zone NOT NULL,
  "untrusted_favourites_count" bigint,
  "untrusted_reblogs_count" bigint
);

-- paon:statement
CREATE UNIQUE INDEX "index_status_stats_on_status_id" ON "status_stats" ("status_id");

-- paon:statement
CREATE TABLE "status_trends" (
  id bigserial PRIMARY KEY,
  "status_id" bigint NOT NULL,
  "account_id" bigint NOT NULL,
  "score" double precision DEFAULT 0.0 NOT NULL,
  "rank" integer DEFAULT 0 NOT NULL,
  "allowed" boolean DEFAULT false NOT NULL,
  "language" character varying
);

-- paon:statement
CREATE INDEX "index_status_trends_on_account_id" ON "status_trends" ("account_id");

-- paon:statement
CREATE UNIQUE INDEX "index_status_trends_on_status_id" ON "status_trends" ("status_id");

-- paon:statement
CREATE SEQUENCE "statuses_id_seq";

-- paon:statement
CREATE TABLE "statuses" (
  id bigint DEFAULT timestamp_id('statuses') NOT NULL PRIMARY KEY,
  "uri" character varying,
  "text" text DEFAULT '' NOT NULL,
  "created_at" timestamp without time zone NOT NULL,
  "updated_at" timestamp without time zone NOT NULL,
  "in_reply_to_id" bigint,
  "reblog_of_id" bigint,
  "url" character varying,
  "sensitive" boolean DEFAULT false NOT NULL,
  "visibility" integer DEFAULT 0 NOT NULL,
  "spoiler_text" text DEFAULT '' NOT NULL,
  "reply" boolean DEFAULT false NOT NULL,
  "language" character varying,
  "conversation_id" bigint,
  "local" boolean,
  "account_id" bigint NOT NULL,
  "application_id" bigint,
  "in_reply_to_account_id" bigint,
  "poll_id" bigint,
  "deleted_at" timestamp without time zone,
  "edited_at" timestamp without time zone,
  "trendable" boolean,
  "ordered_media_attachment_ids" bigint[],
  "fetched_replies_at" timestamp without time zone,
  "quote_approval_policy" integer DEFAULT 0 NOT NULL
);

-- paon:statement
CREATE INDEX "index_statuses_20190820" ON "statuses" ("account_id", "id" DESC, "visibility", "updated_at") WHERE (deleted_at IS NULL);

-- paon:statement
CREATE INDEX "index_statuses_on_account_id" ON "statuses" ("account_id");

-- paon:statement
CREATE INDEX "index_statuses_on_deleted_at" ON "statuses" ("deleted_at") WHERE (deleted_at IS NOT NULL);

-- paon:statement
CREATE INDEX "index_statuses_local_20190824" ON "statuses" ("id" DESC, "account_id") WHERE ((local OR (uri IS NULL)) AND (deleted_at IS NULL) AND (visibility = 0) AND (reblog_of_id IS NULL) AND ((NOT reply) OR (in_reply_to_account_id = account_id)));

-- paon:statement
CREATE INDEX "index_statuses_public_20250129" ON "statuses" ("id" DESC, "language", "account_id") WHERE ((deleted_at IS NULL) AND (visibility = 0) AND (reblog_of_id IS NULL) AND ((NOT reply) OR (in_reply_to_account_id = account_id)));

-- paon:statement
CREATE INDEX "index_statuses_on_in_reply_to_account_id" ON "statuses" ("in_reply_to_account_id") WHERE (in_reply_to_account_id IS NOT NULL);

-- paon:statement
CREATE INDEX "index_statuses_on_in_reply_to_id" ON "statuses" ("in_reply_to_id") WHERE (in_reply_to_id IS NOT NULL);

-- paon:statement
CREATE INDEX "index_statuses_on_reblog_of_id_and_account_id" ON "statuses" ("reblog_of_id", "account_id");

-- paon:statement
CREATE UNIQUE INDEX "index_statuses_on_uri" ON "statuses" ("uri" text_pattern_ops) WHERE (uri IS NOT NULL);

-- paon:statement
CREATE TABLE "statuses_tags" (
  "status_id" bigint NOT NULL,
  "tag_id" bigint NOT NULL,
  PRIMARY KEY ("tag_id", "status_id")
);

-- paon:statement
CREATE INDEX "index_statuses_tags_on_status_id" ON "statuses_tags" ("status_id");

-- paon:statement
CREATE TABLE "tag_follows" (
  id bigserial PRIMARY KEY,
  "tag_id" bigint NOT NULL,
  "account_id" bigint NOT NULL,
  "created_at" timestamp(6) without time zone NOT NULL,
  "updated_at" timestamp(6) without time zone NOT NULL
);

-- paon:statement
CREATE UNIQUE INDEX "index_tag_follows_on_account_id_and_tag_id" ON "tag_follows" ("account_id", "tag_id");

-- paon:statement
CREATE INDEX "index_tag_follows_on_tag_id" ON "tag_follows" ("tag_id");

-- paon:statement
CREATE TABLE "tags" (
  id bigserial PRIMARY KEY,
  "name" character varying DEFAULT '' NOT NULL,
  "created_at" timestamp without time zone NOT NULL,
  "updated_at" timestamp without time zone NOT NULL,
  "usable" boolean,
  "trendable" boolean,
  "listable" boolean,
  "reviewed_at" timestamp without time zone,
  "requested_review_at" timestamp without time zone,
  "last_status_at" timestamp without time zone,
  "max_score" double precision,
  "max_score_at" timestamp without time zone,
  "display_name" character varying
);

-- paon:statement
CREATE UNIQUE INDEX "index_tags_on_name_lower_btree" ON "tags" (lower((name)::text) text_pattern_ops);

-- paon:statement
CREATE TABLE "tombstones" (
  id bigserial PRIMARY KEY,
  "account_id" bigint NOT NULL,
  "uri" character varying NOT NULL,
  "created_at" timestamp without time zone NOT NULL,
  "updated_at" timestamp without time zone NOT NULL,
  "by_moderator" boolean
);

-- paon:statement
CREATE INDEX "index_tombstones_on_account_id" ON "tombstones" ("account_id");

-- paon:statement
CREATE INDEX "index_tombstones_on_uri" ON "tombstones" ("uri");

-- paon:statement
CREATE TABLE "unavailable_domains" (
  id bigserial PRIMARY KEY,
  "domain" character varying DEFAULT '' NOT NULL,
  "created_at" timestamp without time zone NOT NULL,
  "updated_at" timestamp without time zone NOT NULL
);

-- paon:statement
CREATE UNIQUE INDEX "index_unavailable_domains_on_domain" ON "unavailable_domains" ("domain");

-- paon:statement
CREATE TABLE "user_invite_requests" (
  id bigserial PRIMARY KEY,
  "user_id" bigint NOT NULL,
  "text" text,
  "created_at" timestamp without time zone NOT NULL,
  "updated_at" timestamp without time zone NOT NULL
);

-- paon:statement
CREATE INDEX "index_user_invite_requests_on_user_id" ON "user_invite_requests" ("user_id");

-- paon:statement
CREATE TABLE "user_roles" (
  id bigserial PRIMARY KEY,
  "name" character varying DEFAULT '' NOT NULL,
  "color" character varying DEFAULT '' NOT NULL,
  "position" integer DEFAULT 0 NOT NULL,
  "permissions" bigint DEFAULT 0 NOT NULL,
  "highlighted" boolean DEFAULT false NOT NULL,
  "created_at" timestamp(6) without time zone NOT NULL,
  "updated_at" timestamp(6) without time zone NOT NULL
);

-- paon:statement
CREATE TABLE "users" (
  id bigserial PRIMARY KEY,
  "email" character varying DEFAULT '' NOT NULL,
  "created_at" timestamp without time zone NOT NULL,
  "updated_at" timestamp without time zone NOT NULL,
  "encrypted_password" character varying DEFAULT '' NOT NULL,
  "reset_password_token" character varying,
  "reset_password_sent_at" timestamp without time zone,
  "sign_in_count" integer DEFAULT 0 NOT NULL,
  "current_sign_in_at" timestamp without time zone,
  "last_sign_in_at" timestamp without time zone,
  "confirmation_token" character varying,
  "confirmed_at" timestamp without time zone,
  "confirmation_sent_at" timestamp without time zone,
  "unconfirmed_email" character varying,
  "locale" character varying,
  "consumed_timestep" integer,
  "otp_required_for_login" boolean DEFAULT false NOT NULL,
  "last_emailed_at" timestamp without time zone,
  "otp_backup_codes" character varying[],
  "account_id" bigint NOT NULL,
  "disabled" boolean DEFAULT false NOT NULL,
  "invite_id" bigint,
  "chosen_languages" character varying[],
  "created_by_application_id" bigint,
  "approved" boolean DEFAULT true NOT NULL,
  "sign_in_token" character varying,
  "sign_in_token_sent_at" timestamp without time zone,
  "webauthn_id" character varying,
  "sign_up_ip" inet,
  "skip_sign_in_token" boolean,
  "role_id" bigint,
  "settings" text,
  "time_zone" character varying,
  "otp_secret" character varying,
  "age_verified_at" timestamp without time zone,
  "require_tos_interstitial" boolean DEFAULT false NOT NULL
);

-- paon:statement
CREATE INDEX "index_users_on_account_id" ON "users" ("account_id");

-- paon:statement
CREATE UNIQUE INDEX "index_users_on_confirmation_token" ON "users" ("confirmation_token");

-- paon:statement
CREATE INDEX "index_users_on_created_by_application_id" ON "users" ("created_by_application_id") WHERE (created_by_application_id IS NOT NULL);

-- paon:statement
CREATE UNIQUE INDEX "index_users_on_email" ON "users" ("email");

-- paon:statement
CREATE UNIQUE INDEX "index_users_on_reset_password_token" ON "users" ("reset_password_token" text_pattern_ops) WHERE (reset_password_token IS NOT NULL);

-- paon:statement
CREATE INDEX "index_users_on_role_id" ON "users" ("role_id") WHERE (role_id IS NOT NULL);

-- paon:statement
CREATE INDEX "index_users_on_unconfirmed_email" ON "users" ("unconfirmed_email") WHERE (unconfirmed_email IS NOT NULL);

-- paon:statement
CREATE TABLE "web_push_subscriptions" (
  id bigserial PRIMARY KEY,
  "endpoint" character varying NOT NULL,
  "key_p256dh" character varying NOT NULL,
  "key_auth" character varying NOT NULL,
  "data" json,
  "created_at" timestamp without time zone NOT NULL,
  "updated_at" timestamp without time zone NOT NULL,
  "access_token_id" bigint NOT NULL,
  "user_id" bigint NOT NULL,
  "standard" boolean DEFAULT false NOT NULL
);

-- paon:statement
CREATE INDEX "index_web_push_subscriptions_on_access_token_id" ON "web_push_subscriptions" ("access_token_id") WHERE (access_token_id IS NOT NULL);

-- paon:statement
CREATE INDEX "index_web_push_subscriptions_on_user_id" ON "web_push_subscriptions" ("user_id");

-- paon:statement
CREATE TABLE "web_settings" (
  id bigserial PRIMARY KEY,
  "data" json,
  "created_at" timestamp without time zone NOT NULL,
  "updated_at" timestamp without time zone NOT NULL,
  "user_id" bigint NOT NULL
);

-- paon:statement
CREATE UNIQUE INDEX "index_web_settings_on_user_id" ON "web_settings" ("user_id");

-- paon:statement
CREATE TABLE "webauthn_credentials" (
  id bigserial PRIMARY KEY,
  "external_id" character varying NOT NULL,
  "public_key" character varying NOT NULL,
  "nickname" character varying NOT NULL,
  "sign_count" bigint DEFAULT 0 NOT NULL,
  "user_id" bigint,
  "created_at" timestamp without time zone NOT NULL,
  "updated_at" timestamp without time zone NOT NULL
);

-- paon:statement
CREATE UNIQUE INDEX "index_webauthn_credentials_on_external_id" ON "webauthn_credentials" ("external_id");

-- paon:statement
CREATE UNIQUE INDEX "index_webauthn_credentials_on_user_id_and_nickname" ON "webauthn_credentials" ("user_id", "nickname");

-- paon:statement
CREATE TABLE "webhooks" (
  id bigserial PRIMARY KEY,
  "url" character varying NOT NULL,
  "events" character varying[] DEFAULT '{}'::character varying[] NOT NULL,
  "secret" character varying DEFAULT '' NOT NULL,
  "enabled" boolean DEFAULT true NOT NULL,
  "created_at" timestamp(6) without time zone NOT NULL,
  "updated_at" timestamp(6) without time zone NOT NULL,
  "template" text
);

-- paon:statement
CREATE UNIQUE INDEX "index_webhooks_on_url" ON "webhooks" ("url");

-- paon:statement
CREATE TABLE "annual_report_statuses_per_account_counts" (
  id bigserial PRIMARY KEY,
  "year" integer NOT NULL,
  "account_id" bigint NOT NULL,
  "statuses_count" bigint NOT NULL
);

-- paon:statement
CREATE UNIQUE INDEX "idx_on_year_account_id_ff3e167cef" ON "annual_report_statuses_per_account_counts" ("year", "account_id");

-- paon:statement
CREATE TABLE "tag_trends" (
  id bigserial PRIMARY KEY,
  "tag_id" bigint NOT NULL,
  "score" double precision DEFAULT 0.0 NOT NULL,
  "rank" integer DEFAULT 0 NOT NULL,
  "allowed" boolean DEFAULT false NOT NULL,
  "language" character varying DEFAULT '' NOT NULL
);

-- paon:statement
CREATE UNIQUE INDEX "index_tag_trends_on_tag_id_and_language" ON "tag_trends" ("tag_id", "language");

-- paon:statement
CREATE TABLE "terms_of_services" (
  id bigserial PRIMARY KEY,
  "text" text DEFAULT '' NOT NULL,
  "changelog" text DEFAULT '' NOT NULL,
  "published_at" timestamp without time zone,
  "notification_sent_at" timestamp without time zone,
  "created_at" timestamp without time zone NOT NULL,
  "updated_at" timestamp without time zone NOT NULL,
  "effective_date" date
);

-- paon:statement
CREATE UNIQUE INDEX "index_terms_of_services_on_effective_date" ON "terms_of_services" ("effective_date") WHERE (effective_date IS NOT NULL);

-- paon:statement
CREATE TABLE "fasp_providers" (
  id bigserial PRIMARY KEY,
  "confirmed" boolean DEFAULT false NOT NULL,
  "name" character varying NOT NULL,
  "base_url" character varying NOT NULL,
  "sign_in_url" character varying,
  "remote_identifier" character varying NOT NULL,
  "provider_public_key_pem" character varying NOT NULL,
  "server_private_key_pem" character varying NOT NULL,
  "capabilities" jsonb DEFAULT '[]'::jsonb NOT NULL,
  "privacy_policy" jsonb,
  "contact_email" character varying,
  "fediverse_account" character varying,
  "created_at" timestamp without time zone NOT NULL,
  "updated_at" timestamp without time zone NOT NULL
);

-- paon:statement
CREATE UNIQUE INDEX "index_fasp_providers_on_base_url" ON "fasp_providers" ("base_url");

-- paon:statement
CREATE TABLE "fasp_debug_callbacks" (
  id bigserial PRIMARY KEY,
  "fasp_provider_id" bigint NOT NULL,
  "ip" character varying NOT NULL,
  "request_body" text NOT NULL,
  "created_at" timestamp without time zone NOT NULL,
  "updated_at" timestamp without time zone NOT NULL
);

-- paon:statement
CREATE INDEX "index_fasp_debug_callbacks_on_fasp_provider_id" ON "fasp_debug_callbacks" ("fasp_provider_id");

-- paon:statement
CREATE TABLE "fasp_subscriptions" (
  id bigserial PRIMARY KEY,
  "category" character varying NOT NULL,
  "subscription_type" character varying NOT NULL,
  "max_batch_size" integer NOT NULL,
  "threshold_timeframe" integer,
  "threshold_shares" integer,
  "threshold_likes" integer,
  "threshold_replies" integer,
  "fasp_provider_id" bigint NOT NULL,
  "created_at" timestamp without time zone NOT NULL,
  "updated_at" timestamp without time zone NOT NULL
);

-- paon:statement
CREATE INDEX "index_fasp_subscriptions_on_fasp_provider_id" ON "fasp_subscriptions" ("fasp_provider_id");

-- paon:statement
CREATE TABLE "fasp_backfill_requests" (
  id bigserial PRIMARY KEY,
  "category" character varying NOT NULL,
  "max_count" integer DEFAULT 100 NOT NULL,
  "cursor" character varying,
  "fulfilled" boolean DEFAULT false NOT NULL,
  "fasp_provider_id" bigint NOT NULL,
  "created_at" timestamp without time zone NOT NULL,
  "updated_at" timestamp without time zone NOT NULL
);

-- paon:statement
CREATE INDEX "index_fasp_backfill_requests_on_fasp_provider_id" ON "fasp_backfill_requests" ("fasp_provider_id");

-- paon:statement
CREATE TABLE "fasp_follow_recommendations" (
  id bigserial PRIMARY KEY,
  "requesting_account_id" bigint NOT NULL,
  "recommended_account_id" bigint NOT NULL,
  "created_at" timestamp without time zone NOT NULL,
  "updated_at" timestamp without time zone NOT NULL
);

-- paon:statement
CREATE INDEX "index_fasp_follow_recommendations_on_requesting_account_id" ON "fasp_follow_recommendations" ("requesting_account_id");

-- paon:statement
CREATE INDEX "index_fasp_follow_recommendations_on_recommended_account_id" ON "fasp_follow_recommendations" ("recommended_account_id");

-- paon:statement
CREATE TABLE "instance_moderation_notes" (
  id bigserial PRIMARY KEY,
  "domain" character varying NOT NULL,
  "account_id" bigint NOT NULL,
  "content" text,
  "created_at" timestamp without time zone NOT NULL,
  "updated_at" timestamp without time zone NOT NULL
);

-- paon:statement
CREATE INDEX "index_instance_moderation_notes_on_domain" ON "instance_moderation_notes" ("domain");

-- paon:statement
CREATE SEQUENCE "quotes_id_seq";

-- paon:statement
CREATE TABLE "quotes" (
  id bigint DEFAULT timestamp_id('quotes') NOT NULL PRIMARY KEY,
  "account_id" bigint NOT NULL,
  "status_id" bigint NOT NULL,
  "quoted_status_id" bigint,
  "quoted_account_id" bigint,
  "state" integer DEFAULT 0 NOT NULL,
  "approval_uri" character varying,
  "activity_uri" character varying,
  "created_at" timestamp without time zone NOT NULL,
  "updated_at" timestamp without time zone NOT NULL,
  "legacy" boolean DEFAULT false NOT NULL
);

-- paon:statement
CREATE INDEX "index_quotes_on_account_id_and_quoted_account_id" ON "quotes" ("account_id", "quoted_account_id");

-- paon:statement
CREATE UNIQUE INDEX "index_quotes_on_activity_uri" ON "quotes" ("activity_uri") WHERE (activity_uri IS NOT NULL);

-- paon:statement
CREATE INDEX "index_quotes_on_approval_uri" ON "quotes" ("approval_uri") WHERE (approval_uri IS NOT NULL);

-- paon:statement
CREATE INDEX "index_quotes_on_quoted_account_id" ON "quotes" ("quoted_account_id");

-- paon:statement
CREATE INDEX "index_quotes_on_quoted_status_id" ON "quotes" ("quoted_status_id");

-- paon:statement
CREATE UNIQUE INDEX "index_quotes_on_status_id" ON "quotes" ("status_id");

-- paon:statement
CREATE TABLE "rule_translations" (
  id bigserial PRIMARY KEY,
  "text" text DEFAULT '' NOT NULL,
  "hint" text DEFAULT '' NOT NULL,
  "language" character varying NOT NULL,
  "rule_id" bigint NOT NULL,
  "created_at" timestamp without time zone NOT NULL,
  "updated_at" timestamp without time zone NOT NULL
);

-- paon:statement
CREATE UNIQUE INDEX "index_rule_translations_on_rule_id_and_language" ON "rule_translations" ("rule_id", "language");

-- paon:statement
ALTER TABLE "account_aliases" ADD FOREIGN KEY ("account_id") REFERENCES "accounts" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "account_conversations" ADD FOREIGN KEY ("account_id") REFERENCES "accounts" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "account_conversations" ADD FOREIGN KEY ("conversation_id") REFERENCES "conversations" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "account_deletion_requests" ADD FOREIGN KEY ("account_id") REFERENCES "accounts" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "account_domain_blocks" ADD FOREIGN KEY ("account_id") REFERENCES "accounts" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "account_migrations" ADD FOREIGN KEY ("target_account_id") REFERENCES "accounts" (id) ON DELETE SET NULL;

-- paon:statement
ALTER TABLE "account_migrations" ADD FOREIGN KEY ("account_id") REFERENCES "accounts" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "account_moderation_notes" ADD FOREIGN KEY ("account_id") REFERENCES "accounts" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "account_moderation_notes" ADD FOREIGN KEY ("target_account_id") REFERENCES "accounts" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "account_notes" ADD FOREIGN KEY ("target_account_id") REFERENCES "accounts" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "account_notes" ADD FOREIGN KEY ("account_id") REFERENCES "accounts" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "account_pins" ADD FOREIGN KEY ("target_account_id") REFERENCES "accounts" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "account_pins" ADD FOREIGN KEY ("account_id") REFERENCES "accounts" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "account_relationship_severance_events" ADD FOREIGN KEY ("account_id") REFERENCES "accounts" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "account_relationship_severance_events" ADD FOREIGN KEY ("relationship_severance_event_id") REFERENCES "relationship_severance_events" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "account_stats" ADD FOREIGN KEY ("account_id") REFERENCES "accounts" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "account_statuses_cleanup_policies" ADD FOREIGN KEY ("account_id") REFERENCES "accounts" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "account_warnings" ADD FOREIGN KEY ("target_account_id") REFERENCES "accounts" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "account_warnings" ADD FOREIGN KEY ("account_id") REFERENCES "accounts" (id) ON DELETE SET NULL;

-- paon:statement
ALTER TABLE "account_warnings" ADD FOREIGN KEY ("report_id") REFERENCES "reports" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "accounts" ADD FOREIGN KEY ("moved_to_account_id") REFERENCES "accounts" (id) ON DELETE SET NULL;

-- paon:statement
ALTER TABLE "admin_action_logs" ADD FOREIGN KEY ("account_id") REFERENCES "accounts" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "announcement_mutes" ADD FOREIGN KEY ("account_id") REFERENCES "accounts" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "announcement_mutes" ADD FOREIGN KEY ("announcement_id") REFERENCES "announcements" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "announcement_reactions" ADD FOREIGN KEY ("account_id") REFERENCES "accounts" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "announcement_reactions" ADD FOREIGN KEY ("announcement_id") REFERENCES "announcements" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "announcement_reactions" ADD FOREIGN KEY ("custom_emoji_id") REFERENCES "custom_emojis" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "appeals" ADD FOREIGN KEY ("account_warning_id") REFERENCES "account_warnings" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "appeals" ADD FOREIGN KEY ("approved_by_account_id") REFERENCES "accounts" (id) ON DELETE SET NULL;

-- paon:statement
ALTER TABLE "appeals" ADD FOREIGN KEY ("rejected_by_account_id") REFERENCES "accounts" (id) ON DELETE SET NULL;

-- paon:statement
ALTER TABLE "appeals" ADD FOREIGN KEY ("account_id") REFERENCES "accounts" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "backups" ADD FOREIGN KEY ("user_id") REFERENCES "users" (id) ON DELETE SET NULL;

-- paon:statement
ALTER TABLE "blocks" ADD FOREIGN KEY ("target_account_id") REFERENCES "accounts" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "blocks" ADD FOREIGN KEY ("account_id") REFERENCES "accounts" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "bookmarks" ADD FOREIGN KEY ("account_id") REFERENCES "accounts" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "bookmarks" ADD FOREIGN KEY ("status_id") REFERENCES "statuses" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "bulk_import_rows" ADD FOREIGN KEY ("bulk_import_id") REFERENCES "bulk_imports" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "bulk_imports" ADD FOREIGN KEY ("account_id") REFERENCES "accounts" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "canonical_email_blocks" ADD FOREIGN KEY ("reference_account_id") REFERENCES "accounts" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "conversation_mutes" ADD FOREIGN KEY ("account_id") REFERENCES "accounts" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "conversation_mutes" ADD FOREIGN KEY ("conversation_id") REFERENCES "conversations" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "custom_filter_keywords" ADD FOREIGN KEY ("custom_filter_id") REFERENCES "custom_filters" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "custom_filter_statuses" ADD FOREIGN KEY ("custom_filter_id") REFERENCES "custom_filters" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "custom_filter_statuses" ADD FOREIGN KEY ("status_id") REFERENCES "statuses" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "custom_filters" ADD FOREIGN KEY ("account_id") REFERENCES "accounts" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "email_domain_blocks" ADD FOREIGN KEY ("parent_id") REFERENCES "email_domain_blocks" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "favourites" ADD FOREIGN KEY ("account_id") REFERENCES "accounts" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "favourites" ADD FOREIGN KEY ("status_id") REFERENCES "statuses" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "featured_tags" ADD FOREIGN KEY ("account_id") REFERENCES "accounts" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "featured_tags" ADD FOREIGN KEY ("tag_id") REFERENCES "tags" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "follow_recommendation_mutes" ADD FOREIGN KEY ("target_account_id") REFERENCES "accounts" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "follow_recommendation_mutes" ADD FOREIGN KEY ("account_id") REFERENCES "accounts" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "follow_recommendation_suppressions" ADD FOREIGN KEY ("account_id") REFERENCES "accounts" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "follow_requests" ADD FOREIGN KEY ("target_account_id") REFERENCES "accounts" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "follow_requests" ADD FOREIGN KEY ("account_id") REFERENCES "accounts" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "follows" ADD FOREIGN KEY ("target_account_id") REFERENCES "accounts" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "follows" ADD FOREIGN KEY ("account_id") REFERENCES "accounts" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "generated_annual_reports" ADD FOREIGN KEY ("account_id") REFERENCES "accounts" (id);

-- paon:statement
ALTER TABLE "identities" ADD FOREIGN KEY ("user_id") REFERENCES "users" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "invites" ADD FOREIGN KEY ("user_id") REFERENCES "users" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "list_accounts" ADD FOREIGN KEY ("account_id") REFERENCES "accounts" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "list_accounts" ADD FOREIGN KEY ("follow_request_id") REFERENCES "follow_requests" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "list_accounts" ADD FOREIGN KEY ("follow_id") REFERENCES "follows" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "list_accounts" ADD FOREIGN KEY ("list_id") REFERENCES "lists" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "lists" ADD FOREIGN KEY ("account_id") REFERENCES "accounts" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "login_activities" ADD FOREIGN KEY ("user_id") REFERENCES "users" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "markers" ADD FOREIGN KEY ("user_id") REFERENCES "users" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "media_attachments" ADD FOREIGN KEY ("account_id") REFERENCES "accounts" (id) ON DELETE SET NULL;

-- paon:statement
ALTER TABLE "media_attachments" ADD FOREIGN KEY ("scheduled_status_id") REFERENCES "scheduled_statuses" (id) ON DELETE SET NULL;

-- paon:statement
ALTER TABLE "media_attachments" ADD FOREIGN KEY ("status_id") REFERENCES "statuses" (id) ON DELETE SET NULL;

-- paon:statement
ALTER TABLE "mentions" ADD FOREIGN KEY ("account_id") REFERENCES "accounts" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "mentions" ADD FOREIGN KEY ("status_id") REFERENCES "statuses" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "mutes" ADD FOREIGN KEY ("target_account_id") REFERENCES "accounts" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "mutes" ADD FOREIGN KEY ("account_id") REFERENCES "accounts" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "notification_permissions" ADD FOREIGN KEY ("from_account_id") REFERENCES "accounts" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "notification_permissions" ADD FOREIGN KEY ("account_id") REFERENCES "accounts" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "notification_policies" ADD FOREIGN KEY ("account_id") REFERENCES "accounts" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "notification_requests" ADD FOREIGN KEY ("from_account_id") REFERENCES "accounts" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "notification_requests" ADD FOREIGN KEY ("account_id") REFERENCES "accounts" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "notification_requests" ADD FOREIGN KEY ("last_status_id") REFERENCES "statuses" (id) ON DELETE SET NULL;

-- paon:statement
ALTER TABLE "notifications" ADD FOREIGN KEY ("from_account_id") REFERENCES "accounts" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "notifications" ADD FOREIGN KEY ("account_id") REFERENCES "accounts" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "oauth_access_grants" ADD FOREIGN KEY ("application_id") REFERENCES "oauth_applications" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "oauth_access_grants" ADD FOREIGN KEY ("resource_owner_id") REFERENCES "users" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "oauth_access_tokens" ADD FOREIGN KEY ("application_id") REFERENCES "oauth_applications" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "oauth_access_tokens" ADD FOREIGN KEY ("resource_owner_id") REFERENCES "users" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "oauth_applications" ADD FOREIGN KEY ("owner_id") REFERENCES "users" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "poll_votes" ADD FOREIGN KEY ("account_id") REFERENCES "accounts" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "poll_votes" ADD FOREIGN KEY ("poll_id") REFERENCES "polls" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "polls" ADD FOREIGN KEY ("account_id") REFERENCES "accounts" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "polls" ADD FOREIGN KEY ("status_id") REFERENCES "statuses" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "preview_card_trends" ADD FOREIGN KEY ("preview_card_id") REFERENCES "preview_cards" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "preview_cards" ADD FOREIGN KEY ("author_account_id") REFERENCES "accounts" (id) ON DELETE SET NULL;

-- paon:statement
ALTER TABLE "report_notes" ADD FOREIGN KEY ("account_id") REFERENCES "accounts" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "report_notes" ADD FOREIGN KEY ("report_id") REFERENCES "reports" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "reports" ADD FOREIGN KEY ("action_taken_by_account_id") REFERENCES "accounts" (id) ON DELETE SET NULL;

-- paon:statement
ALTER TABLE "reports" ADD FOREIGN KEY ("assigned_account_id") REFERENCES "accounts" (id) ON DELETE SET NULL;

-- paon:statement
ALTER TABLE "reports" ADD FOREIGN KEY ("target_account_id") REFERENCES "accounts" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "reports" ADD FOREIGN KEY ("account_id") REFERENCES "accounts" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "reports" ADD FOREIGN KEY ("application_id") REFERENCES "oauth_applications" (id) ON DELETE SET NULL;

-- paon:statement
ALTER TABLE "scheduled_statuses" ADD FOREIGN KEY ("account_id") REFERENCES "accounts" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "session_activations" ADD FOREIGN KEY ("access_token_id") REFERENCES "oauth_access_tokens" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "session_activations" ADD FOREIGN KEY ("user_id") REFERENCES "users" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "severed_relationships" ADD FOREIGN KEY ("local_account_id") REFERENCES "accounts" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "severed_relationships" ADD FOREIGN KEY ("remote_account_id") REFERENCES "accounts" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "severed_relationships" ADD FOREIGN KEY ("relationship_severance_event_id") REFERENCES "relationship_severance_events" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "status_edits" ADD FOREIGN KEY ("account_id") REFERENCES "accounts" (id) ON DELETE SET NULL;

-- paon:statement
ALTER TABLE "status_edits" ADD FOREIGN KEY ("status_id") REFERENCES "statuses" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "status_pins" ADD FOREIGN KEY ("account_id") REFERENCES "accounts" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "status_pins" ADD FOREIGN KEY ("status_id") REFERENCES "statuses" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "status_stats" ADD FOREIGN KEY ("status_id") REFERENCES "statuses" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "status_trends" ADD FOREIGN KEY ("account_id") REFERENCES "accounts" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "status_trends" ADD FOREIGN KEY ("status_id") REFERENCES "statuses" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "statuses" ADD FOREIGN KEY ("in_reply_to_account_id") REFERENCES "accounts" (id) ON DELETE SET NULL;

-- paon:statement
ALTER TABLE "statuses" ADD FOREIGN KEY ("account_id") REFERENCES "accounts" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "statuses" ADD FOREIGN KEY ("in_reply_to_id") REFERENCES "statuses" (id) ON DELETE SET NULL;

-- paon:statement
ALTER TABLE "statuses" ADD FOREIGN KEY ("reblog_of_id") REFERENCES "statuses" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "statuses_tags" ADD FOREIGN KEY ("status_id") REFERENCES "statuses" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "statuses_tags" ADD FOREIGN KEY ("tag_id") REFERENCES "tags" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "tag_follows" ADD FOREIGN KEY ("account_id") REFERENCES "accounts" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "tag_follows" ADD FOREIGN KEY ("tag_id") REFERENCES "tags" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "tombstones" ADD FOREIGN KEY ("account_id") REFERENCES "accounts" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "user_invite_requests" ADD FOREIGN KEY ("user_id") REFERENCES "users" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "users" ADD FOREIGN KEY ("account_id") REFERENCES "accounts" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "users" ADD FOREIGN KEY ("invite_id") REFERENCES "invites" (id) ON DELETE SET NULL;

-- paon:statement
ALTER TABLE "users" ADD FOREIGN KEY ("created_by_application_id") REFERENCES "oauth_applications" (id) ON DELETE SET NULL;

-- paon:statement
ALTER TABLE "users" ADD FOREIGN KEY ("role_id") REFERENCES "user_roles" (id) ON DELETE SET NULL;

-- paon:statement
ALTER TABLE "web_push_subscriptions" ADD FOREIGN KEY ("access_token_id") REFERENCES "oauth_access_tokens" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "web_push_subscriptions" ADD FOREIGN KEY ("user_id") REFERENCES "users" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "web_settings" ADD FOREIGN KEY ("user_id") REFERENCES "users" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "webauthn_credentials" ADD FOREIGN KEY ("user_id") REFERENCES "users" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "fasp_backfill_requests" ADD FOREIGN KEY ("fasp_provider_id") REFERENCES "fasp_providers" (id);

-- paon:statement
ALTER TABLE "fasp_debug_callbacks" ADD FOREIGN KEY ("fasp_provider_id") REFERENCES "fasp_providers" (id);

-- paon:statement
ALTER TABLE "fasp_subscriptions" ADD FOREIGN KEY ("fasp_provider_id") REFERENCES "fasp_providers" (id);

-- paon:statement
ALTER TABLE "fasp_follow_recommendations" ADD FOREIGN KEY ("requesting_account_id") REFERENCES "accounts" (id);

-- paon:statement
ALTER TABLE "fasp_follow_recommendations" ADD FOREIGN KEY ("recommended_account_id") REFERENCES "accounts" (id);

-- paon:statement
ALTER TABLE "instance_moderation_notes" ADD FOREIGN KEY ("account_id") REFERENCES "accounts" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "quotes" ADD FOREIGN KEY ("account_id") REFERENCES "accounts" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "quotes" ADD FOREIGN KEY ("status_id") REFERENCES "statuses" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "quotes" ADD FOREIGN KEY ("quoted_status_id") REFERENCES "statuses" (id) ON DELETE SET NULL;

-- paon:statement
ALTER TABLE "quotes" ADD FOREIGN KEY ("quoted_account_id") REFERENCES "accounts" (id) ON DELETE SET NULL;

-- paon:statement
ALTER TABLE "rule_translations" ADD FOREIGN KEY ("rule_id") REFERENCES "rules" (id) ON DELETE CASCADE;

-- paon:statement
ALTER TABLE "tag_trends" ADD FOREIGN KEY ("tag_id") REFERENCES "tags" (id) ON DELETE CASCADE;

-- paon:statement
CREATE MATERIALIZED VIEW "instances" AS
WITH domain_counts(domain, accounts_count) AS (
           SELECT accounts.domain,
              count(*) AS accounts_count
             FROM accounts
            WHERE (accounts.domain IS NOT NULL)
            GROUP BY accounts.domain
          )
   SELECT domain_counts.domain,
      domain_counts.accounts_count
     FROM domain_counts
  UNION
   SELECT domain_blocks.domain,
      COALESCE(domain_counts.accounts_count, (0)::bigint) AS accounts_count
     FROM (domain_blocks
       LEFT JOIN domain_counts ON (((domain_counts.domain)::text = (domain_blocks.domain)::text)))
  UNION
   SELECT domain_allows.domain,
      COALESCE(domain_counts.accounts_count, (0)::bigint) AS accounts_count
     FROM (domain_allows
       LEFT JOIN domain_counts ON (((domain_counts.domain)::text = (domain_allows.domain)::text)));;

-- paon:statement
CREATE VIEW "user_ips" AS
SELECT t0.user_id,
      t0.ip,
      max(t0.used_at) AS used_at
     FROM ( SELECT users.id AS user_id,
              users.sign_up_ip AS ip,
              users.created_at AS used_at
             FROM users
            WHERE (users.sign_up_ip IS NOT NULL)
          UNION ALL
           SELECT session_activations.user_id,
              session_activations.ip,
              session_activations.updated_at
             FROM session_activations
          UNION ALL
           SELECT login_activities.user_id,
              login_activities.ip,
              login_activities.created_at
             FROM login_activities
            WHERE (login_activities.success = true)) t0
    GROUP BY t0.user_id, t0.ip;;

-- paon:statement
CREATE MATERIALIZED VIEW "account_summaries" AS
SELECT accounts.id AS account_id,
      mode() WITHIN GROUP (ORDER BY t0.language) AS language,
      mode() WITHIN GROUP (ORDER BY t0.sensitive) AS sensitive
     FROM (accounts
       CROSS JOIN LATERAL ( SELECT statuses.account_id,
              statuses.language,
              statuses.sensitive
             FROM statuses
            WHERE ((statuses.account_id = accounts.id) AND (statuses.deleted_at IS NULL) AND (statuses.reblog_of_id IS NULL))
            ORDER BY statuses.id DESC
           LIMIT 20) t0)
    WHERE ((accounts.suspended_at IS NULL) AND (accounts.silenced_at IS NULL) AND (accounts.moved_to_account_id IS NULL) AND (accounts.discoverable = true) AND (accounts.locked = false))
    GROUP BY accounts.id;;

-- paon:statement
CREATE MATERIALIZED VIEW "global_follow_recommendations" AS
SELECT t0.account_id,
      sum(t0.rank) AS rank,
      array_agg(t0.reason) AS reason
     FROM ( SELECT account_summaries.account_id,
              ((count(follows.id))::numeric / (1.0 + (count(follows.id))::numeric)) AS rank,
              'most_followed'::text AS reason
             FROM ((follows
               JOIN account_summaries ON ((account_summaries.account_id = follows.target_account_id)))
               JOIN users ON ((users.account_id = follows.account_id)))
            WHERE ((users.current_sign_in_at >= (now() - 'P30D'::interval)) AND (account_summaries.sensitive = false) AND (NOT (EXISTS ( SELECT 1
                     FROM follow_recommendation_suppressions
                    WHERE (follow_recommendation_suppressions.account_id = follows.target_account_id)))))
            GROUP BY account_summaries.account_id
           HAVING (count(follows.id) >= 5)
          UNION ALL
           SELECT account_summaries.account_id,
              (sum((status_stats.reblogs_count + status_stats.favourites_count)) / (1.0 + sum((status_stats.reblogs_count + status_stats.favourites_count)))) AS rank,
              'most_interactions'::text AS reason
             FROM ((status_stats
               JOIN statuses ON ((statuses.id = status_stats.status_id)))
               JOIN account_summaries ON ((account_summaries.account_id = statuses.account_id)))
            WHERE ((statuses.id >= (((date_part('epoch'::text, (now() - 'P30D'::interval)) * (1000)::double precision))::bigint << 16)) AND (account_summaries.sensitive = false) AND (NOT (EXISTS ( SELECT 1
                     FROM follow_recommendation_suppressions
                    WHERE (follow_recommendation_suppressions.account_id = statuses.account_id)))))
            GROUP BY account_summaries.account_id
           HAVING (sum((status_stats.reblogs_count + status_stats.favourites_count)) >= (5)::numeric)) t0
    GROUP BY t0.account_id
    ORDER BY (sum(t0.rank)) DESC;;

-- paon:statement
CREATE INDEX "index_instances_on_reverse_domain" ON "instances" (reverse(('.'::text || (domain)::text)), domain);

-- paon:statement
CREATE UNIQUE INDEX "index_instances_on_domain" ON "instances" ("domain");

-- paon:statement
CREATE UNIQUE INDEX "index_account_summaries_on_account_id" ON "account_summaries" ("account_id");

-- paon:statement
CREATE INDEX "idx_on_account_id_language_sensitive_250461e1eb" ON "account_summaries" ("account_id", "language", "sensitive");

-- paon:statement
CREATE UNIQUE INDEX "index_global_follow_recommendations_on_account_id" ON "global_follow_recommendations" ("account_id");

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20160220174730');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20160220211917');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20160221003140');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20160221003621');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20160222122600');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20160222143943');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20160223162837');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20160223164502');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20160223165723');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20160223165855');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20160223171800');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20160224223247');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20160227230233');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20160305115639');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20160306172223');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20160312193225');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20160314164231');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20160316103650');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20160322193748');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20160325130944');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20160826155805');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20160905150353');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20160919221059');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20160920003904');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20160926213048');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20161003142332');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20161003145426');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20161006213403');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20161009120834');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20161027172456');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20161104173623');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20161105130633');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20161116162355');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20161119211120');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20161122163057');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20161123093447');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20161128103007');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20161130142058');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20161130185319');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20161202132159');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20161203164520');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20161205214545');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20161221152630');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20161222201034');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20161222204147');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20170105224407');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20170109120109');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20170112154826');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20170114194937');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20170114203041');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20170119214911');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20170123162658');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20170123203248');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20170125145934');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20170127165745');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20170205175257');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20170209184350');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20170214110202');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20170217012631');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20170301222600');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20170303212857');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20170304202101');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20170317193015');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20170318214217');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20170322021028');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20170322143850');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20170322162804');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20170330021336');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20170330163835');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20170330164118');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20170403172249');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20170405112956');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20170406215816');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20170409170753');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20170414080609');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20170414132105');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20170418160728');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20170423005413');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20170424003227');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20170424112722');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20170425131920');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20170425202925');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20170427011934');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20170506235850');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20170507000211');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20170507141759');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20170508230434');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20170516072309');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20170520145338');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20170601210557');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20170604144747');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20170606113804');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20170609145826');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20170610000000');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20170623152212');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20170624134742');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20170625140443');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20170711225116');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20170713112503');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20170713175513');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20170713190709');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20170714184731');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20170716191202');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20170718211102');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20170720000000');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20170823162448');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20170824103029');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20170829215220');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20170901141119');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20170901142658');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20170905044538');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20170905165803');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20170913000752');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20170917153509');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20170918125918');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20170920024819');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20170920032311');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20170924022025');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20170927215609');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20170928082043');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20171005102658');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20171005171936');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20171006142024');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20171010023049');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20171010025614');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20171020084748');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20171028221157');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20171107143332');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20171107143624');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20171109012327');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20171114080328');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20171114231651');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20171116161857');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20171118012443');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20171119172437');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20171122120436');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20171125024930');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20171125031751');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20171125185353');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20171125190735');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20171129172043');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20171130000000');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20171201000000');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20171212195226');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20171226094803');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20180106000232');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20180109143959');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20180204034416');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20180206000000');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20180211015820');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20180304013859');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20180310000000');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20180402031200');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20180402040909');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20180410204633');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20180416210259');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20180506221944');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20180510214435');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20180510230049');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20180514130000');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20180514140000');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20180528141303');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20180608213548');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20180609104432');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20180615122121');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20180616192031');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20180617162849');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20180628181026');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20180707154237');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20180711152640');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20180808175627');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20180812123222');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20180812162710');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20180812173710');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20180814171349');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20180820232245');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20180831171112');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20180929222014');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20181007025445');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20181010141500');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20181017170937');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20181018205649');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20181024224956');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20181026034033');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20181116165755');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20181116173541');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20181127130500');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20181127165847');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20181203003808');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20181203021853');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20181204193439');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20181204215309');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20181207011115');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20181213184704');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20181213185533');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20181219235220');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20181226021420');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20190103124649');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20190103124754');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20190117114553');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20190201012802');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20190203180359');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20190225031541');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20190225031625');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20190226003449');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20190304152020');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20190306145741');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20190307234537');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20190314181829');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20190316190352');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20190317135723');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20190403141604');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20190409054914');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20190420025523');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20190509164208');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20190511134027');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20190529143559');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20190627222225');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20190627222826');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20190701022101');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20190705002136');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20190715164535');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20190726175042');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20190729185330');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20190805123746');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20190807135426');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20190815225426');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20190819134503');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20190820003045');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20190823221802');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20190901035623');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20190904222339');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20190914202517');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20190915194355');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20190917213523');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20190927232842');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20191001213028');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20191007013357');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20191031163205');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20191212003415');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20191212163405');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20191218153258');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20200113125135');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20200114113335');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20200119112504');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20200126203551');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20200306035625');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20200309150742');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20200312144258');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20200312162302');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20200312185443');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20200317021758');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20200407201300');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20200407202420');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20200417125749');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20200508212852');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20200510110808');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20200510181721');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20200516180352');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20200516183822');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20200518083523');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20200521180606');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20200529214050');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20200601222558');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20200605155027');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20200608113046');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20200614002136');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20200620164023');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20200622213645');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20200627125810');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20200628133322');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20200630190240');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20200630190544');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20200908193330');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20200917192924');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20200917193034');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20200917222316');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20201008202037');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20201008220312');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20201017233919');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20201206004238');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20201218054746');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20210221045109');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20210306164523');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20210322164601');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20210323114347');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20210324171613');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20210416200740');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20210421121431');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20210425135952');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20210505174616');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20210609202149');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20210616214526');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20210621221010');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20210630000137');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20210722120340');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20210904215403');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20210908220918');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20211031031021');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20211112011713');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20211115032527');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20211123212714');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20211213040746');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20211231080958');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20220105163928');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20220115125126');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20220115125341');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20220116202951');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20220124141035');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20220202200743');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20220202200926');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20220210153119');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20220224010024');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20220227041951');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20220302232632');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20220303000827');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20220304195405');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20220307094650');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20220309213005');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20220316233212');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20220428112511');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20220428112727');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20220428114454');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20220428114902');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20220606044941');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20220611210335');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20220611212541');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20220613110628');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20220613110711');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20220613110834');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20220710102457');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20220714171049');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20220808101323');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20220824164433');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20220824233535');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20220827195229');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20220829192633');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20220829192658');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20221006061337');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20221012181003');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20221021055441');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20221025171544');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20221104133904');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20230129023109');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20230215074327');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20230215074423');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20230330135507');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20230330140036');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20230330155710');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20230524190515');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20230524192812');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20230524194155');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20230531153942');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20230531154811');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20230605085710');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20230605085711');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20230630145300');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20230702131023');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20230702151753');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20230724160715');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20230725213448');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20230814223300');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20230818141056');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20230822081029');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES ('20230907150100');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES
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
  ('20230904134623');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES
  ('20231006183200'),
  ('20231018192110'),
  ('20231018193209'),
  ('20231018193355'),
  ('20231018193659'),
  ('20231210154528'),
  ('20231211234923'),
  ('20231212073317'),
  ('20231222100226'),
  ('20240109103012'),
  ('20240111033014'),
  ('20240217171534'),
  ('20240221195424'),
  ('20240221195828'),
  ('20240221211359'),
  ('20240222193403'),
  ('20240222203722'),
  ('20240227191620'),
  ('20240304090449'),
  ('20240307180905'),
  ('20240310123453'),
  ('20240312100644'),
  ('20240312105620'),
  ('20240320140159'),
  ('20240320163441'),
  ('20240321160706'),
  ('20240322125607'),
  ('20240322130318'),
  ('20240322161611'),
  ('20240510192043'),
  ('20240513095755'),
  ('20240513123807'),
  ('20240522041528'),
  ('20240603195202'),
  ('20240607093446'),
  ('20240607093954'),
  ('20240607094603'),
  ('20240607094856'),
  ('20240712064044'),
  ('20240713171841'),
  ('20240713171909'),
  ('20240720140205'),
  ('20240724181224'),
  ('20240808114841'),
  ('20240808124338'),
  ('20240808124339'),
  ('20240808125420'),
  ('20240909014637'),
  ('20240916190140'),
  ('20241007071624');

-- paon:statement
INSERT INTO schema_migrations (version) VALUES
  ('20240918233930'),
  ('20241014010506'),
  ('20241022214312'),
  ('20241104082851'),
  ('20241111141355'),
  ('20241123160722'),
  ('20241123224956'),
  ('20241205103523'),
  ('20241205135901'),
  ('20241205135925'),
  ('20241205162640'),
  ('20241205163118'),
  ('20241206131513'),
  ('20241210140838'),
  ('20241212152158'),
  ('20241212152618'),
  ('20241212152734'),
  ('20241212152910'),
  ('20241212153054'),
  ('20241212153202'),
  ('20241212153254'),
  ('20241212154231'),
  ('20241212154346'),
  ('20241213130230'),
  ('20241213170027'),
  ('20241213170036'),
  ('20241213170043'),
  ('20241213170053'),
  ('20241216223425'),
  ('20241216223433'),
  ('20241216223446'),
  ('20241216223452'),
  ('20241216223852'),
  ('20241216223859'),
  ('20241216224211'),
  ('20241216224218'),
  ('20241216224229'),
  ('20241216224237'),
  ('20241216224507'),
  ('20241216224514'),
  ('20241216224520'),
  ('20241216224530'),
  ('20241216224813'),
  ('20241216224825'),
  ('20250103131909'),
  ('20250108111200'),
  ('20250129144440'),
  ('20250129144813'),
  ('20250221143646'),
  ('20250224144617'),
  ('20250305074104'),
  ('20250313123400'),
  ('20250328153843'),
  ('20250410144908'),
  ('20250411094808'),
  ('20250411095859'),
  ('20250422083912'),
  ('20250422084214'),
  ('20250422085027'),
  ('20250422085303'),
  ('20250425134308'),
  ('20250428095029'),
  ('20250428104538'),
  ('20250520192024'),
  ('20250520204643'),
  ('20250605110215'),
  ('20250627132728');

-- paon:statement
INSERT INTO ar_internal_metadata (key, value, created_at, updated_at) VALUES ('environment', 'production', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP);

-- paon:statement
INSERT INTO ar_internal_metadata (key, value, created_at, updated_at) VALUES ('schema_sha1', 'b53e3b8de778cd1b53158326b97afa9368f3237e', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP);
