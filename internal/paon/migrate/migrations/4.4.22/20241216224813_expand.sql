-- Mastodon 4.4.22, upstream migration 20241216224813, phase expand.

ALTER TABLE tombstones ADD CONSTRAINT tombstones_account_id_null CHECK (account_id IS NOT NULL) NOT VALID;
