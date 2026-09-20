-- Mastodon 4.3.23, upstream migration 20240607094856, phase validate.

ALTER TABLE mentions VALIDATE CONSTRAINT mentions_account_id_null;

ALTER TABLE mentions ALTER COLUMN account_id SET NOT NULL;

ALTER TABLE mentions DROP CONSTRAINT mentions_account_id_null;
