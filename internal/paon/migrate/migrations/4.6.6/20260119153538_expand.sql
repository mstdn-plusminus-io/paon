-- Mastodon 4.6.6, upstream migration 20260119153538, phase expand.

ALTER TABLE collections ADD COLUMN language character varying;
