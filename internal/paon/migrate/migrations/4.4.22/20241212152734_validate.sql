-- Mastodon 4.4.22, upstream migration 20241212152734, phase validate.

DELETE FROM account_domain_blocks WHERE account_id IS NULL OR domain IS NULL;

ALTER TABLE account_domain_blocks ALTER COLUMN account_id SET NOT NULL, ALTER COLUMN domain SET NOT NULL;
