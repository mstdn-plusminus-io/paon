-- Mastodon 4.4.22, upstream migration 20250422083912, phase expand.

ALTER TABLE web_push_subscriptions ADD CONSTRAINT web_push_subscriptions_user_id_null CHECK (user_id IS NOT NULL) NOT VALID;
