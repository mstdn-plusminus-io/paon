-- Mastodon 4.3.23, upstream migration 20240322125607, phase expand.

ALTER TABLE account_relationship_severance_events ADD COLUMN followers_count integer DEFAULT 0 NOT NULL;

ALTER TABLE account_relationship_severance_events ADD COLUMN following_count integer DEFAULT 0 NOT NULL;
