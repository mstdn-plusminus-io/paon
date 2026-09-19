-- Mastodon 4.3.23, upstream migration 20240322130318, phase contract.

ALTER TABLE account_relationship_severance_events DROP COLUMN relationships_count;
