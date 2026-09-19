-- Mastodon 4.6.6, upstream migration 20251118115657, phase expand.

CREATE TABLE collections (id bigserial PRIMARY KEY, account_id bigint NOT NULL, name character varying NOT NULL, description text NOT NULL, uri character varying, local boolean NOT NULL, sensitive boolean NOT NULL, discoverable boolean NOT NULL, tag_id bigint, original_number_of_items integer, created_at timestamp(6) without time zone NOT NULL, updated_at timestamp(6) without time zone NOT NULL, CONSTRAINT fk_rails_544f142936 FOREIGN KEY (account_id) REFERENCES accounts(id), CONSTRAINT fk_rails_70f13aad15 FOREIGN KEY (tag_id) REFERENCES tags(id));

CREATE INDEX index_collections_on_account_id ON collections (account_id);

CREATE INDEX index_collections_on_tag_id ON collections (tag_id);
