-- Mastodon 4.6.6, upstream migration 20260217154542, phase expand.

ALTER TABLE accounts ADD COLUMN show_media boolean DEFAULT true NOT NULL, ADD COLUMN show_media_replies boolean DEFAULT true NOT NULL, ADD COLUMN show_featured boolean DEFAULT true NOT NULL;
