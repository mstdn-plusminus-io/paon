-- Mastodon 4.4.22, upstream migration 20250103131909, phase expand.

CREATE TABLE fasp_backfill_requests (id bigserial PRIMARY KEY, category character varying NOT NULL, max_count integer DEFAULT 100 NOT NULL, cursor character varying, fulfilled boolean DEFAULT false NOT NULL, fasp_provider_id bigint NOT NULL, created_at timestamp(6) without time zone NOT NULL, updated_at timestamp(6) without time zone NOT NULL, CONSTRAINT fk_rails_760d761775 FOREIGN KEY (fasp_provider_id) REFERENCES fasp_providers(id));

CREATE INDEX index_fasp_backfill_requests_on_fasp_provider_id ON fasp_backfill_requests (fasp_provider_id);
