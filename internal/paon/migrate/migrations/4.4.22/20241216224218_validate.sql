-- Mastodon 4.4.22, upstream migration 20241216224218, phase validate.

DELETE FROM poll_votes WHERE account_id IS NULL;

ALTER TABLE poll_votes VALIDATE CONSTRAINT poll_votes_account_id_null;

ALTER TABLE poll_votes ALTER COLUMN account_id SET NOT NULL;

ALTER TABLE poll_votes DROP CONSTRAINT poll_votes_account_id_null;
