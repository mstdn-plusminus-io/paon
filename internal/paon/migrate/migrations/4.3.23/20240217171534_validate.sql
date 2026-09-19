-- Mastodon 4.3.23, upstream migration 20240217171534, phase validate.

ALTER TABLE status_pins ALTER COLUMN created_at DROP DEFAULT;

ALTER TABLE status_pins ALTER COLUMN updated_at DROP DEFAULT;
