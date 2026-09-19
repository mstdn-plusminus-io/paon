-- Mastodon 4.6.6, upstream migration 20260212113020, phase expand.

ALTER TABLE collection_items ADD COLUMN uri character varying;
