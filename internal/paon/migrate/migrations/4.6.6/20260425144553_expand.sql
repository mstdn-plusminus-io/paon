-- Mastodon 4.6.6, upstream migration 20260425144553, phase expand.

ALTER TABLE notification_policies ADD COLUMN for_bots integer DEFAULT 0 NOT NULL;
