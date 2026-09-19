-- Mastodon 4.5.15, upstream migration 20251007100627, phase expand.

CREATE INDEX index_follows_on_target_account_id_and_account_id ON follows (target_account_id, account_id);
