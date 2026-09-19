-- Mastodon 4.5.15, upstream migration 20250805075010, phase expand.

ALTER TABLE fasp_providers ADD COLUMN delivery_last_failed_at timestamp(6) without time zone;
