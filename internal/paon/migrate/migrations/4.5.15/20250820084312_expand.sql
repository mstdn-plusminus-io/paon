-- Mastodon 4.5.15, upstream migration 20250820084312, phase expand.

ALTER TABLE status_stats ADD COLUMN quotes_count bigint DEFAULT 0 NOT NULL;
