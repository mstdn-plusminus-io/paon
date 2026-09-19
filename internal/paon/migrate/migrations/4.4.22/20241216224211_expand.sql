-- Mastodon 4.4.22, upstream migration 20241216224211, phase expand.

ALTER TABLE poll_votes ADD CONSTRAINT poll_votes_account_id_null CHECK (account_id IS NOT NULL) NOT VALID;
