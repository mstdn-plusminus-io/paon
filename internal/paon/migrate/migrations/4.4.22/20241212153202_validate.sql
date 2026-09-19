-- Mastodon 4.4.22, upstream migration 20241212153202, phase validate.

DELETE FROM announcement_reactions WHERE account_id IS NULL OR announcement_id IS NULL;

ALTER TABLE announcement_reactions ALTER COLUMN account_id SET NOT NULL, ALTER COLUMN announcement_id SET NOT NULL;
