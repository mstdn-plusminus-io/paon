-- Mastodon 4.6.6, upstream migration 20251217091936, phase expand.

ALTER TABLE accounts ADD COLUMN feature_approval_policy integer DEFAULT 0 NOT NULL;
