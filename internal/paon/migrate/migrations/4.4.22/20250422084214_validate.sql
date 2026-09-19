-- Mastodon 4.4.22, upstream migration 20250422084214, phase validate.

DELETE FROM web_push_subscriptions WHERE user_id IS NULL;

ALTER TABLE web_push_subscriptions VALIDATE CONSTRAINT web_push_subscriptions_user_id_null;

ALTER TABLE web_push_subscriptions ALTER COLUMN user_id SET NOT NULL;

ALTER TABLE web_push_subscriptions DROP CONSTRAINT web_push_subscriptions_user_id_null;
