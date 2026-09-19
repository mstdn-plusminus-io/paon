-- Mastodon 4.4.22, upstream migration 20250422085027, phase expand.

ALTER TABLE web_push_subscriptions ADD CONSTRAINT web_push_subscriptions_access_token_id_null CHECK (access_token_id IS NOT NULL) NOT VALID;
