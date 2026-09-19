-- Mastodon 4.4.22, upstream migration 20241205103523, phase expand.

CREATE TABLE fasp_providers (id bigserial PRIMARY KEY, confirmed boolean DEFAULT false NOT NULL, name character varying NOT NULL, base_url character varying NOT NULL, sign_in_url character varying, remote_identifier character varying NOT NULL, provider_public_key_pem character varying NOT NULL, server_private_key_pem character varying NOT NULL, capabilities jsonb DEFAULT '[]'::jsonb NOT NULL, privacy_policy jsonb, contact_email character varying, fediverse_account character varying, created_at timestamp(6) without time zone NOT NULL, updated_at timestamp(6) without time zone NOT NULL);

CREATE UNIQUE INDEX index_fasp_providers_on_base_url ON fasp_providers (base_url);
