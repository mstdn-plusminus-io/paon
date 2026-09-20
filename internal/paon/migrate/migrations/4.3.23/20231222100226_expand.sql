-- Mastodon 4.3.23, upstream migration 20231222100226, phase expand.

ALTER TABLE email_domain_blocks ADD COLUMN allow_with_approval boolean DEFAULT false NOT NULL;
