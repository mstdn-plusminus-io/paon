-- Mastodon 4.4.22, upstream migration 20241216223446, phase expand.

ALTER TABLE account_notes ADD CONSTRAINT account_notes_target_account_id_null CHECK (target_account_id IS NOT NULL) NOT VALID;
