-- Mastodon 4.6.6, upstream migration 20260310095021, phase expand.

ALTER TABLE collections ADD COLUMN description_html text;

ALTER TABLE collections ALTER COLUMN description DROP NOT NULL;
