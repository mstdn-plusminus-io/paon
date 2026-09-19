-- Mastodon 4.5.15, upstream migration 20250717003848, phase expand.

CREATE TABLE username_blocks (id bigserial PRIMARY KEY, username character varying NOT NULL, normalized_username character varying NOT NULL, exact boolean DEFAULT false NOT NULL, allow_with_approval boolean DEFAULT false NOT NULL, created_at timestamp(6) without time zone NOT NULL, updated_at timestamp(6) without time zone NOT NULL);

CREATE UNIQUE INDEX index_username_blocks_on_username_lower_btree ON username_blocks (lower((username)::text));

CREATE INDEX index_username_blocks_on_normalized_username ON username_blocks (normalized_username);
