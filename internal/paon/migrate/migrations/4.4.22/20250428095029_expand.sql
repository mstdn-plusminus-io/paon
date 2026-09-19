-- Mastodon 4.4.22, upstream migration 20250428095029, phase expand.

ALTER TABLE statuses ADD COLUMN quote_approval_policy integer DEFAULT 0 NOT NULL;
