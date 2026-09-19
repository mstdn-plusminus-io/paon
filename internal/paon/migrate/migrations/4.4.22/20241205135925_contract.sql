-- Mastodon 4.4.22, upstream migration 20241205135925, phase contract.

DELETE FROM settings WHERE (thing_type IS NOT NULL AND thing_id IS NOT NULL) OR var IN ('notification_emails', 'interactions', 'boost_modal', 'auto_play_gif', 'delete_modal', 'system_font_ui', 'default_sensitive', 'unfollow_modal', 'reduce_motion', 'display_sensitive_media', 'hide_network', 'expand_spoilers', 'display_media', 'aggregate_reblogs', 'show_application', 'advanced_layout', 'use_blurhash', 'use_pending_items');

DROP INDEX IF EXISTS index_settings_on_thing_type_and_thing_id_and_var;

ALTER TABLE settings DROP COLUMN thing_type, DROP COLUMN thing_id;

CREATE UNIQUE INDEX index_settings_on_var ON settings (var);
