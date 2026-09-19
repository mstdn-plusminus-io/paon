-- Mastodon 4.5.15, upstream migration 20250819100545.
-- Additive SQL applied during expand without recording the final marker.

CREATE INDEX IF NOT EXISTS index_quotes_on_account_id_and_quoted_account_id_and_id ON quotes (account_id, quoted_account_id, id);

CREATE INDEX IF NOT EXISTS index_quotes_on_quoted_status_id_and_id ON quotes (quoted_status_id, id);
