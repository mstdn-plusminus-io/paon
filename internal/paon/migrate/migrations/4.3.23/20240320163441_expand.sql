-- Mastodon 4.3.23, upstream migration 20240320163441, phase expand.

ALTER TABLE notification_requests ALTER COLUMN last_status_id DROP NOT NULL;
