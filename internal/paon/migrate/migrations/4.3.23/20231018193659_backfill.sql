-- Mastodon 4.3.23, upstream migration 20231018193659, phase backfill.

DELETE FROM identities WHERE id IN (SELECT id FROM (SELECT id, ROW_NUMBER() OVER (PARTITION BY uid, provider ORDER BY id ASC) AS duplicate_rank FROM identities) ranked WHERE duplicate_rank > 1 ORDER BY id ASC LIMIT ?);

CREATE UNIQUE INDEX index_identities_on_uid_and_provider ON identities (uid, provider);
