-- Mastodon 4.4.22, upstream migration 20241216223425, phase expand.

ALTER TABLE account_notes ADD CONSTRAINT account_notes_account_id_null CHECK (account_id IS NOT NULL) NOT VALID;
