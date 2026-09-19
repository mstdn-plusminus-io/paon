-- Mastodon 4.6.6, upstream migration 20260127141459, phase expand.

ALTER TABLE accounts ADD COLUMN avatar_description character varying DEFAULT '' NOT NULL;
