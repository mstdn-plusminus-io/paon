-- Mastodon 4.6.6, upstream migration 20260423141611, phase expand.

CREATE INDEX index_collection_items_on_state ON collection_items (state) WHERE state IN (2, 3);
