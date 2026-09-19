-- Mastodon 4.4.22, upstream migration 20241212154346, phase validate.

DELETE FROM user_invite_requests WHERE user_id IS NULL;

ALTER TABLE user_invite_requests ALTER COLUMN user_id SET NOT NULL;
