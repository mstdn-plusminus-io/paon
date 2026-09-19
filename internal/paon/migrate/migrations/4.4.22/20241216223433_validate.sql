-- Mastodon 4.4.22, upstream migration 20241216223433, phase validate.

DELETE FROM account_notes WHERE account_id IS NULL;

ALTER TABLE account_notes VALIDATE CONSTRAINT account_notes_account_id_null;

ALTER TABLE account_notes ALTER COLUMN account_id SET NOT NULL;

ALTER TABLE account_notes DROP CONSTRAINT account_notes_account_id_null;
