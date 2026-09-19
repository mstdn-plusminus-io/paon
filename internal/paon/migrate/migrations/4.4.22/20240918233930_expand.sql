-- Mastodon 4.4.22, upstream migration 20240918233930, phase expand.

ALTER TABLE statuses ADD COLUMN fetched_replies_at timestamp(6) without time zone;
