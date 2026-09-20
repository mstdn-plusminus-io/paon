-- Mastodon 4.3.23, upstream migration 20240607093446, phase validate.

ALTER TABLE mentions ADD CONSTRAINT mentions_status_id_null CHECK (status_id IS NOT NULL) NOT VALID;
