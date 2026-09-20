-- Mastodon 4.3.23, upstream migration 20231018192110, phase backfill.

DELETE FROM webauthn_credentials WHERE id IN (SELECT id FROM (SELECT id, ROW_NUMBER() OVER (PARTITION BY user_id, nickname ORDER BY id ASC) AS duplicate_rank FROM webauthn_credentials) ranked WHERE duplicate_rank > 1 ORDER BY id ASC LIMIT ?);

CREATE UNIQUE INDEX index_webauthn_credentials_on_user_id_and_nickname ON webauthn_credentials (user_id, nickname);
