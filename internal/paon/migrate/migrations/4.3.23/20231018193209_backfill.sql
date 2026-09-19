-- Mastodon 4.3.23, upstream migration 20231018193209, phase backfill.

DELETE FROM account_aliases WHERE id IN (SELECT id FROM (SELECT id, ROW_NUMBER() OVER (PARTITION BY account_id, uri ORDER BY id ASC) AS duplicate_rank FROM account_aliases) ranked WHERE duplicate_rank > 1 ORDER BY id ASC LIMIT ?);

CREATE UNIQUE INDEX index_account_aliases_on_account_id_and_uri ON account_aliases (account_id, uri);
