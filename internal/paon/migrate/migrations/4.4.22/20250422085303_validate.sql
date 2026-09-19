-- Mastodon 4.4.22, upstream migration 20250422085303, phase validate.

DELETE FROM web_push_subscriptions WHERE access_token_id IS NULL;

ALTER TABLE web_push_subscriptions VALIDATE CONSTRAINT web_push_subscriptions_access_token_id_null;

ALTER TABLE web_push_subscriptions ALTER COLUMN access_token_id SET NOT NULL;

ALTER TABLE web_push_subscriptions DROP CONSTRAINT web_push_subscriptions_access_token_id_null;
