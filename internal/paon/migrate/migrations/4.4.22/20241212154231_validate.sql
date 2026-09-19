-- Mastodon 4.4.22, upstream migration 20241212154231, phase validate.

DELETE FROM scheduled_statuses WHERE account_id IS NULL;

ALTER TABLE scheduled_statuses ALTER COLUMN account_id SET NOT NULL;
