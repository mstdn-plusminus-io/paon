-- Mastodon 4.3.23, upstream migration 20240720140205, phase contract.

DROP TABLE system_keys;

DROP TABLE one_time_keys;

DROP TABLE encrypted_messages;

DROP TABLE devices;

ALTER TABLE accounts DROP COLUMN devices_url;
