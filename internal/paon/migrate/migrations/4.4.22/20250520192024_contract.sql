-- Mastodon 4.4.22, upstream migration 20250520192024, phase contract.

ALTER TABLE users DROP COLUMN encrypted_otp_secret, DROP COLUMN encrypted_otp_secret_iv, DROP COLUMN encrypted_otp_secret_salt;
