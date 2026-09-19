-- Mastodon 4.3.23, upstream migration 20240808114841, phase expand.

ALTER TABLE notification_policies ADD COLUMN for_not_following integer DEFAULT 0 NOT NULL;

ALTER TABLE notification_policies ADD COLUMN for_not_followers integer DEFAULT 0 NOT NULL;

ALTER TABLE notification_policies ADD COLUMN for_new_accounts integer DEFAULT 0 NOT NULL;

ALTER TABLE notification_policies ADD COLUMN for_private_mentions integer DEFAULT 1 NOT NULL;

ALTER TABLE notification_policies ADD COLUMN for_limited_accounts integer DEFAULT 1 NOT NULL;
