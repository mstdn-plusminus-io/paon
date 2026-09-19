-- Mastodon 4.4.22, upstream migration 20241216224229, phase expand.

ALTER TABLE poll_votes ADD CONSTRAINT poll_votes_poll_id_null CHECK (poll_id IS NOT NULL) NOT VALID;
