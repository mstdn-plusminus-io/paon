-- Mastodon 4.4.22, upstream migration 20241212152910, phase validate.

DELETE FROM admin_action_logs WHERE account_id IS NULL;

ALTER TABLE admin_action_logs ALTER COLUMN account_id SET NOT NULL;
