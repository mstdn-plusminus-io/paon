-- Mastodon 4.3.23, upstream migration 20240909014637, phase contract.

ALTER TABLE accounts ADD COLUMN attribution_domains character varying[] DEFAULT '{}'::character varying[];
