-- Mastodon 4.4.22, upstream migration 20241111141355, phase expand.

CREATE TABLE tag_trends (id bigserial PRIMARY KEY, tag_id bigint NOT NULL, score double precision DEFAULT 0.0 NOT NULL, rank integer DEFAULT 0 NOT NULL, allowed boolean DEFAULT false NOT NULL, language character varying DEFAULT '' NOT NULL, CONSTRAINT fk_rails_3033046460 FOREIGN KEY (tag_id) REFERENCES tags(id) ON DELETE CASCADE);

CREATE UNIQUE INDEX index_tag_trends_on_tag_id_and_language ON tag_trends (tag_id, language);
