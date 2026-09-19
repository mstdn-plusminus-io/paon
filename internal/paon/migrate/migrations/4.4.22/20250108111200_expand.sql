-- Mastodon 4.4.22, upstream migration 20250108111200, phase expand.

ALTER TABLE web_push_subscriptions ADD COLUMN standard boolean DEFAULT false NOT NULL;
