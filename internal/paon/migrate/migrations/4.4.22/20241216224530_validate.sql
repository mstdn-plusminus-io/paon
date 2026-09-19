-- Mastodon 4.4.22, upstream migration 20241216224530, phase validate.

DELETE FROM polls WHERE status_id IS NULL;

ALTER TABLE polls VALIDATE CONSTRAINT polls_status_id_null;

ALTER TABLE polls ALTER COLUMN status_id SET NOT NULL;

ALTER TABLE polls DROP CONSTRAINT polls_status_id_null;
