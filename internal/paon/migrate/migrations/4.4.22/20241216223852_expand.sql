-- Mastodon 4.4.22, upstream migration 20241216223852, phase expand.

ALTER TABLE markers ADD CONSTRAINT markers_user_id_null CHECK (user_id IS NOT NULL) NOT VALID;
