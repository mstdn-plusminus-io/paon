-- Mastodon 4.3.23, upstream migration 20231211234923, phase expand.

CREATE TABLE follow_recommendation_mutes (id bigserial PRIMARY KEY, account_id bigint NOT NULL, target_account_id bigint NOT NULL, created_at timestamp(6) without time zone NOT NULL, updated_at timestamp(6) without time zone NOT NULL, CONSTRAINT fk_rails_d36abd69ea FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE, CONSTRAINT fk_rails_a9f09ec9a8 FOREIGN KEY (target_account_id) REFERENCES accounts(id) ON DELETE CASCADE);

CREATE UNIQUE INDEX idx_on_account_id_target_account_id_a8c8ddf44e ON follow_recommendation_mutes (account_id, target_account_id);

CREATE INDEX index_follow_recommendation_mutes_on_target_account_id ON follow_recommendation_mutes (target_account_id);
