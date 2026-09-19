-- Mastodon 4.3.23, upstream migration 20240221211359, phase expand.

ALTER TABLE notification_requests ALTER COLUMN id SET DEFAULT timestamp_id('notification_requests');
