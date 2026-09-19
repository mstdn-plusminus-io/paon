-- Mastodon 4.6.6, upstream migration 20251202140424, phase expand.

ALTER TABLE generated_annual_reports ADD COLUMN share_key character varying;
