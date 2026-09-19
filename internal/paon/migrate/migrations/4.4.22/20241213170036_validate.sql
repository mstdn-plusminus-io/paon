-- Mastodon 4.4.22, upstream migration 20241213170036, phase validate.

DELETE FROM account_conversations WHERE account_id IS NULL;

ALTER TABLE account_conversations VALIDATE CONSTRAINT account_conversations_account_id_null;

ALTER TABLE account_conversations ALTER COLUMN account_id SET NOT NULL;

ALTER TABLE account_conversations DROP CONSTRAINT account_conversations_account_id_null;
