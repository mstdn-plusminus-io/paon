-- Mastodon 4.4.22, upstream migration 20241216224507, phase expand.

ALTER TABLE polls ADD CONSTRAINT polls_account_id_null CHECK (account_id IS NOT NULL) NOT VALID;
