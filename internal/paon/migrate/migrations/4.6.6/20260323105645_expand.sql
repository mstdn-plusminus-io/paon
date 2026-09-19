-- Mastodon 4.6.6, upstream migration 20260323105645, phase expand.

CREATE TABLE keypairs (id bigserial PRIMARY KEY, account_id bigint NOT NULL, uri character varying NOT NULL, type integer NOT NULL, public_key character varying NOT NULL, private_key character varying, expires_at timestamp(6) without time zone, revoked boolean DEFAULT false NOT NULL, created_at timestamp(6) without time zone NOT NULL, updated_at timestamp(6) without time zone NOT NULL, CONSTRAINT fk_rails_f5ea7ac36a FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE);

CREATE INDEX index_keypairs_on_account_id ON keypairs (account_id);

CREATE UNIQUE INDEX index_keypairs_on_uri ON keypairs (uri);
