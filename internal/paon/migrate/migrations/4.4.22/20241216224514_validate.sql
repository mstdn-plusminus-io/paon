-- Mastodon 4.4.22, upstream migration 20241216224514, phase validate.

DELETE FROM polls WHERE account_id IS NULL;

ALTER TABLE polls VALIDATE CONSTRAINT polls_account_id_null;

ALTER TABLE polls ALTER COLUMN account_id SET NOT NULL;

ALTER TABLE polls DROP CONSTRAINT polls_account_id_null;
