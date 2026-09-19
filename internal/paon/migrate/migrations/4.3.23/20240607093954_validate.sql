-- Mastodon 4.3.23, upstream migration 20240607093954, phase validate.

ALTER TABLE mentions VALIDATE CONSTRAINT mentions_status_id_null;

ALTER TABLE mentions ALTER COLUMN status_id SET NOT NULL;

ALTER TABLE mentions DROP CONSTRAINT mentions_status_id_null;
