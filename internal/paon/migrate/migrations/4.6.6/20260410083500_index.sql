-- Mastodon 4.6.6, upstream migration 20260410083500.
-- Applied after the Go duplicate backfill; the old index is removed in contract.

CREATE UNIQUE INDEX IF NOT EXISTS index_collection_items_on_account_id_and_collection_id ON collection_items (account_id, collection_id);
