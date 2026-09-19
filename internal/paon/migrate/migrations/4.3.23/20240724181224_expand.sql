-- Mastodon 4.3.23, upstream migration 20240724181224, phase expand.

ALTER TABLE oauth_access_grants ADD COLUMN code_challenge character varying;

ALTER TABLE oauth_access_grants ADD COLUMN code_challenge_method character varying;
