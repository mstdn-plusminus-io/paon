-- Mastodon 4.4.22, upstream migration 20241213170027, phase expand.

ALTER TABLE account_conversations ADD CONSTRAINT account_conversations_account_id_null CHECK (account_id IS NOT NULL) NOT VALID;
