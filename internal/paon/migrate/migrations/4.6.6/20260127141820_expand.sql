-- Mastodon 4.6.6, upstream migration 20260127141820, phase expand.

ALTER TABLE accounts ADD COLUMN header_description character varying DEFAULT '' NOT NULL;
