-- Mastodon 4.4.22, upstream migration 20250605110215, phase expand.

ALTER TABLE quotes ADD COLUMN legacy boolean DEFAULT false NOT NULL;
