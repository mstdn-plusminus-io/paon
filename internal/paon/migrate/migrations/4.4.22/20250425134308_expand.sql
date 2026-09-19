-- Mastodon 4.4.22, upstream migration 20250425134308, phase expand.

ALTER TABLE quotes ALTER COLUMN id SET DEFAULT timestamp_id('quotes');
