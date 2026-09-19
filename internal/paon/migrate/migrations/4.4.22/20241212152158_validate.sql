-- Mastodon 4.4.22, upstream migration 20241212152158, phase validate.

DELETE FROM account_aliases WHERE account_id IS NULL;

ALTER TABLE account_aliases ALTER COLUMN account_id SET NOT NULL;
