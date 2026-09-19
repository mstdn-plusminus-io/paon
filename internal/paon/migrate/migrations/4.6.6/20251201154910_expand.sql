-- Mastodon 4.6.6, upstream migration 20251201154910, phase expand.

ALTER TABLE custom_emoji_categories ADD COLUMN featured_emoji_id bigint;

ALTER TABLE custom_emoji_categories ADD CONSTRAINT fk_rails_ad7840c8cf FOREIGN KEY (featured_emoji_id) REFERENCES custom_emojis(id) ON DELETE SET NULL NOT VALID;
