-- Mastodon 4.3.23, upstream migration 20240322161611, phase contract.

ALTER TABLE users DROP COLUMN admin;

ALTER TABLE users DROP COLUMN moderator;
