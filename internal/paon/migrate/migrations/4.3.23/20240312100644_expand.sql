-- Mastodon 4.3.23, upstream migration 20240312100644, phase expand.

CREATE TABLE relationship_severance_events (id bigserial PRIMARY KEY, type integer NOT NULL, target_name character varying NOT NULL, purged boolean DEFAULT false NOT NULL, created_at timestamp(6) without time zone NOT NULL, updated_at timestamp(6) without time zone NOT NULL);

CREATE INDEX index_relationship_severance_events_on_type_and_target_name ON relationship_severance_events (type, target_name);
