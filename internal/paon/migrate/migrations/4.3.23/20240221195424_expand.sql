-- Mastodon 4.3.23, upstream migration 20240221195424, phase expand.

ALTER TABLE notifications ADD COLUMN filtered boolean DEFAULT false NOT NULL;
