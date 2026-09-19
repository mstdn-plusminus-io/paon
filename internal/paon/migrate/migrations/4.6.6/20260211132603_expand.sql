-- Mastodon 4.6.6, upstream migration 20260211132603, phase expand.

ALTER TABLE user_roles ADD COLUMN require_2fa boolean DEFAULT false NOT NULL;
