-- Mastodon 4.4.22, upstream migration 20241212153254, phase validate.

DELETE FROM custom_filters WHERE account_id IS NULL;

ALTER TABLE custom_filters ALTER COLUMN account_id SET NOT NULL;
