-- Mastodon 4.4.22, upstream migration 20241213130230, phase expand.

CREATE TABLE fasp_subscriptions (id bigserial PRIMARY KEY, category character varying NOT NULL, subscription_type character varying NOT NULL, max_batch_size integer NOT NULL, threshold_timeframe integer, threshold_shares integer, threshold_likes integer, threshold_replies integer, fasp_provider_id bigint NOT NULL, created_at timestamp(6) without time zone NOT NULL, updated_at timestamp(6) without time zone NOT NULL, CONSTRAINT fk_rails_4c021f5938 FOREIGN KEY (fasp_provider_id) REFERENCES fasp_providers(id));

CREATE INDEX index_fasp_subscriptions_on_fasp_provider_id ON fasp_subscriptions (fasp_provider_id);
