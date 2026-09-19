-- Mastodon 4.4.22, upstream migration 20241210140838, phase validate.

DELETE FROM account_pins WHERE account_id IS NULL OR target_account_id IS NULL;

ALTER TABLE account_pins ALTER COLUMN account_id SET NOT NULL, ALTER COLUMN target_account_id SET NOT NULL;
