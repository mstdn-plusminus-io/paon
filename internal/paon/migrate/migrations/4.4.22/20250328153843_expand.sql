-- Mastodon 4.4.22, upstream migration 20250328153843, phase expand.

CREATE TABLE instance_moderation_notes (id bigserial PRIMARY KEY, domain character varying NOT NULL, account_id bigint NOT NULL, content text, created_at timestamp(6) without time zone NOT NULL, updated_at timestamp(6) without time zone NOT NULL, CONSTRAINT fk_rails_62f919e09b FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE);

CREATE INDEX index_instance_moderation_notes_on_domain ON instance_moderation_notes (domain);
