-- Mastodon 4.5.15, upstream migration 20250924170259, phase expand.

ALTER TABLE accounts ADD COLUMN id_scheme integer DEFAULT 0;
