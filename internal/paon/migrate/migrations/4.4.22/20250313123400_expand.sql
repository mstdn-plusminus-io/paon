-- Mastodon 4.4.22, upstream migration 20250313123400, phase expand.

ALTER TABLE users ADD COLUMN age_verified_at timestamp(6) without time zone;
