-- Mastodon 4.6.6, upstream migration 20260311152331, phase expand.

ALTER TABLE accounts ADD COLUMN collections_url character varying;
