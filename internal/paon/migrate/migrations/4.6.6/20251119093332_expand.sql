-- Mastodon 4.6.6, upstream migration 20251119093332, phase expand.

CREATE TABLE collection_items (id bigserial PRIMARY KEY, collection_id bigint NOT NULL, account_id bigint, position integer DEFAULT 1 NOT NULL, object_uri character varying, approval_uri character varying, activity_uri character varying, approval_last_verified_at timestamp(6) without time zone, state integer DEFAULT 0 NOT NULL, created_at timestamp(6) without time zone NOT NULL, updated_at timestamp(6) without time zone NOT NULL, CONSTRAINT fk_rails_b1a778644b FOREIGN KEY (collection_id) REFERENCES collections(id) ON DELETE CASCADE, CONSTRAINT fk_rails_2eb992658d FOREIGN KEY (account_id) REFERENCES accounts(id));

CREATE INDEX index_collection_items_on_collection_id ON collection_items (collection_id);

CREATE INDEX index_collection_items_on_account_id ON collection_items (account_id);

CREATE UNIQUE INDEX index_collection_items_on_object_uri ON collection_items (object_uri) WHERE activity_uri IS NOT NULL;

CREATE UNIQUE INDEX index_collection_items_on_approval_uri ON collection_items (approval_uri) WHERE approval_uri IS NOT NULL;
