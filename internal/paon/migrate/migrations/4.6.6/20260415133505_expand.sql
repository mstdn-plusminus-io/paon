-- Mastodon 4.6.6, upstream migration 20260415133505, phase expand.

ALTER TABLE collections ADD COLUMN url character varying;
