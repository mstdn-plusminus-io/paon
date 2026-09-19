-- Mastodon 4.4.22, upstream migration 20241206131513, phase expand.

CREATE TABLE fasp_debug_callbacks (id bigserial PRIMARY KEY, fasp_provider_id bigint NOT NULL, ip character varying NOT NULL, request_body text NOT NULL, created_at timestamp(6) without time zone NOT NULL, updated_at timestamp(6) without time zone NOT NULL, CONSTRAINT fk_rails_c1650087cd FOREIGN KEY (fasp_provider_id) REFERENCES fasp_providers(id));

CREATE INDEX index_fasp_debug_callbacks_on_fasp_provider_id ON fasp_debug_callbacks (fasp_provider_id);
