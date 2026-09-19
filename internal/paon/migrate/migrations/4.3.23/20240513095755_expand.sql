-- Mastodon 4.3.23, upstream migration 20240513095755, phase expand.

ALTER TABLE notifications ADD COLUMN group_key character varying;
