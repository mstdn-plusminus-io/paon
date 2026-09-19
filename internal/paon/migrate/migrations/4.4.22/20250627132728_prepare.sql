-- Mastodon 4.4.22, upstream migration 20250627132728.
-- Additive SQL applied during expand without recording the final marker.

CREATE TABLE IF NOT EXISTS fasp_follow_recommendations (id bigserial PRIMARY KEY, requesting_account_id bigint NOT NULL, recommended_account_id bigint NOT NULL, created_at timestamp(6) without time zone NOT NULL, updated_at timestamp(6) without time zone NOT NULL, CONSTRAINT fk_rails_71623d7e2c FOREIGN KEY (requesting_account_id) REFERENCES accounts(id), CONSTRAINT fk_rails_5c63a5fd1b FOREIGN KEY (recommended_account_id) REFERENCES accounts(id));

CREATE INDEX IF NOT EXISTS index_fasp_follow_recommendations_on_requesting_account_id ON fasp_follow_recommendations (requesting_account_id);

CREATE INDEX IF NOT EXISTS index_fasp_follow_recommendations_on_recommended_account_id ON fasp_follow_recommendations (recommended_account_id);
