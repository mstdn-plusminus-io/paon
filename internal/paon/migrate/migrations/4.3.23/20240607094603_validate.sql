-- Mastodon 4.3.23, upstream migration 20240607094603, phase validate.

ALTER TABLE mentions ADD CONSTRAINT mentions_account_id_null CHECK (account_id IS NOT NULL) NOT VALID;
