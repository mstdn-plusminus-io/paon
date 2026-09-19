-- Mastodon 4.6.6, upstream migration 20260325151755, phase expand.

CREATE UNIQUE INDEX index_collections_on_uri ON collections (uri) WHERE uri IS NOT NULL;

CREATE UNIQUE INDEX index_collection_items_on_uri ON collection_items (uri) WHERE uri IS NOT NULL;
