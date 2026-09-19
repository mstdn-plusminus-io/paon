-- Mastodon 4.4.22, upstream migration 20250221143646, phase expand.

ALTER TABLE announcements ADD COLUMN notification_sent_at timestamp(6) without time zone;
