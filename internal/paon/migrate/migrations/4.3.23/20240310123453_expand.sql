-- Mastodon 4.3.23, upstream migration 20240310123453, phase expand.

ALTER TABLE rules ADD COLUMN hint text DEFAULT '' NOT NULL;
