-- Mastodon 4.3.23, upstream migration 20240712064044, phase contract.

DELETE FROM notification_requests WHERE dismissed;

ALTER TABLE notification_requests DROP COLUMN dismissed;
