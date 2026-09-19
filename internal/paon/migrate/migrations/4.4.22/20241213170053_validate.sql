-- Mastodon 4.4.22, upstream migration 20241213170053, phase validate.

DELETE FROM account_conversations WHERE conversation_id IS NULL;

ALTER TABLE account_conversations VALIDATE CONSTRAINT account_conversations_conversation_id_null;

ALTER TABLE account_conversations ALTER COLUMN conversation_id SET NOT NULL;

ALTER TABLE account_conversations DROP CONSTRAINT account_conversations_conversation_id_null;
