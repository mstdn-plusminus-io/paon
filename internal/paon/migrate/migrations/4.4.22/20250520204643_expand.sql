-- Mastodon 4.4.22, upstream migration 20250520204643, phase expand.

CREATE TABLE rule_translations (id bigserial PRIMARY KEY, text text DEFAULT '' NOT NULL, hint text DEFAULT '' NOT NULL, language character varying NOT NULL, rule_id bigint NOT NULL, created_at timestamp(6) without time zone NOT NULL, updated_at timestamp(6) without time zone NOT NULL, CONSTRAINT fk_rails_d5fd439dde FOREIGN KEY (rule_id) REFERENCES rules(id) ON DELETE CASCADE);

CREATE UNIQUE INDEX index_rule_translations_on_rule_id_and_language ON rule_translations (rule_id, language);
