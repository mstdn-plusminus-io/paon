-- Mastodon 4.4.22, upstream migration 20250411095859, phase expand.

ALTER TABLE status_edits ADD COLUMN quote_id bigint;
