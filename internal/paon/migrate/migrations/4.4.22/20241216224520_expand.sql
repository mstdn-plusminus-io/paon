-- Mastodon 4.4.22, upstream migration 20241216224520, phase expand.

ALTER TABLE polls ADD CONSTRAINT polls_status_id_null CHECK (status_id IS NOT NULL) NOT VALID;
