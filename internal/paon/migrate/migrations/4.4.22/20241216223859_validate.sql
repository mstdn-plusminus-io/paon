-- Mastodon 4.4.22, upstream migration 20241216223859, phase validate.

DELETE FROM markers WHERE user_id IS NULL;

ALTER TABLE markers VALIDATE CONSTRAINT markers_user_id_null;

ALTER TABLE markers ALTER COLUMN user_id SET NOT NULL;

ALTER TABLE markers DROP CONSTRAINT markers_user_id_null;
