-- Mastodon 4.6.6, upstream migration 20260115153219, phase expand.

ALTER TABLE collections ALTER COLUMN id SET DEFAULT timestamp_id('collections');

ALTER TABLE collection_items ALTER COLUMN id SET DEFAULT timestamp_id('collection_items');
