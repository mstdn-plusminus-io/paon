-- Mastodon 4.4.22, upstream migration 20241213170043, phase expand.

ALTER TABLE account_conversations ADD CONSTRAINT account_conversations_conversation_id_null CHECK (conversation_id IS NOT NULL) NOT VALID;
