-- Mastodon 4.6.6, upstream migration 20260420124030, phase expand.

ALTER TABLE user_roles ADD COLUMN collection_limit integer DEFAULT 10 NOT NULL;
