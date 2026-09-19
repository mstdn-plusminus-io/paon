-- Mastodon 4.3.23, upstream migration 20231006183200, phase expand.

ALTER TABLE preview_cards_statuses ADD COLUMN url character varying;
