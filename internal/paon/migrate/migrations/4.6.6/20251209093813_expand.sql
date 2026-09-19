-- Mastodon 4.6.6, upstream migration 20251209093813, phase expand.

ALTER TABLE collections ADD COLUMN item_count integer DEFAULT 0 NOT NULL;
