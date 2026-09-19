-- Mastodon 4.5.15, upstream migration 20250828222741, phase expand.

ALTER TABLE conversations ADD COLUMN parent_status_id bigint, ADD COLUMN parent_account_id bigint;
