-- Mastodon 4.4.22, upstream migration 20241022214312, phase expand.

ALTER TABLE status_stats ADD COLUMN untrusted_favourites_count bigint;

ALTER TABLE status_stats ADD COLUMN untrusted_reblogs_count bigint;
