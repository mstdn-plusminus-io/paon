-- Mastodon 4.4.22, upstream migration 20241216224825, phase validate.

DELETE FROM tombstones WHERE account_id IS NULL;

ALTER TABLE tombstones VALIDATE CONSTRAINT tombstones_account_id_null;

ALTER TABLE tombstones ALTER COLUMN account_id SET NOT NULL;

ALTER TABLE tombstones DROP CONSTRAINT tombstones_account_id_null;
