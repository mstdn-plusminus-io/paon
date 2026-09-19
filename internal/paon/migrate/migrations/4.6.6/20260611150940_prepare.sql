-- Mastodon 4.6.6, upstream migration 20260611150940.
-- Additive SQL applied during expand without recording the final marker.

ALTER TABLE bulk_imports ADD COLUMN IF NOT EXISTS missing_status boolean DEFAULT false NOT NULL;
