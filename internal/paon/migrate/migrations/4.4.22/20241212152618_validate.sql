-- Mastodon 4.4.22, upstream migration 20241212152618, phase validate.

DELETE FROM account_deletion_requests WHERE account_id IS NULL;

ALTER TABLE account_deletion_requests ALTER COLUMN account_id SET NOT NULL;
