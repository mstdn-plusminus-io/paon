-- Mastodon 4.3.23, upstream migration 20240312105620, phase expand.

CREATE TABLE severed_relationships (id bigserial PRIMARY KEY, relationship_severance_event_id bigint NOT NULL, local_account_id bigint NOT NULL, remote_account_id bigint NOT NULL, direction integer NOT NULL, show_reblogs boolean, notify boolean, languages character varying[], created_at timestamp(6) without time zone NOT NULL, updated_at timestamp(6) without time zone NOT NULL, CONSTRAINT fk_rails_5054494e1e FOREIGN KEY (relationship_severance_event_id) REFERENCES relationship_severance_events(id) ON DELETE CASCADE, CONSTRAINT fk_rails_98ff099d4c FOREIGN KEY (local_account_id) REFERENCES accounts(id) ON DELETE CASCADE, CONSTRAINT fk_rails_f7afd97ba4 FOREIGN KEY (remote_account_id) REFERENCES accounts(id) ON DELETE CASCADE);

CREATE UNIQUE INDEX index_severed_relationships_on_unique_tuples ON severed_relationships (relationship_severance_event_id, local_account_id, direction, remote_account_id);

CREATE INDEX index_severed_relationships_on_local_account_and_event ON severed_relationships (local_account_id, relationship_severance_event_id);

CREATE INDEX index_severed_relationships_on_remote_account_id ON severed_relationships (remote_account_id);
