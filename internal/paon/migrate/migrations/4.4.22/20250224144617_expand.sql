-- Mastodon 4.4.22, upstream migration 20250224144617, phase expand.

ALTER TABLE terms_of_services ADD COLUMN effective_date date;
