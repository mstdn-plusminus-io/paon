-- Mastodon 4.3.23, upstream migration 20231210154528, phase expand.

ALTER TABLE users ADD COLUMN otp_secret character varying;
