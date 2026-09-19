-- Mastodon 4.5.15, upstream migration 20250912082651, phase expand.

ALTER TABLE accounts ADD COLUMN following_url character varying DEFAULT '' NOT NULL;
