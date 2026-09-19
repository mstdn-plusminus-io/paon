-- Mastodon 4.4.22, upstream migration 20241212153054, phase validate.

DELETE FROM announcement_mutes WHERE account_id IS NULL OR announcement_id IS NULL;

ALTER TABLE announcement_mutes ALTER COLUMN account_id SET NOT NULL, ALTER COLUMN announcement_id SET NOT NULL;
