-- Mastodon 4.3.23, upstream migration 20240320140159, phase expand.

CREATE TABLE account_relationship_severance_events (id bigserial PRIMARY KEY, account_id bigint NOT NULL, relationship_severance_event_id bigint NOT NULL, relationships_count integer DEFAULT 0 NOT NULL, created_at timestamp(6) without time zone NOT NULL, updated_at timestamp(6) without time zone NOT NULL, CONSTRAINT fk_rails_030c916965 FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE, CONSTRAINT fk_rails_8a34c3a361 FOREIGN KEY (relationship_severance_event_id) REFERENCES relationship_severance_events(id) ON DELETE CASCADE);

CREATE UNIQUE INDEX idx_on_account_id_relationship_severance_event_id_7bd82bf20e ON account_relationship_severance_events (account_id, relationship_severance_event_id);

CREATE INDEX index_account_relationship_severance_events_on_account_id ON account_relationship_severance_events (account_id);

CREATE INDEX idx_on_relationship_severance_event_id_403f53e707 ON account_relationship_severance_events (relationship_severance_event_id);
