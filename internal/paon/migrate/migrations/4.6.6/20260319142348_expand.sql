-- Mastodon 4.6.6, upstream migration 20260319142348, phase expand.

CREATE TABLE tagged_objects (id bigserial PRIMARY KEY, status_id bigint NOT NULL, object_type character varying, object_id bigint, ap_type character varying NOT NULL, uri character varying, created_at timestamp(6) without time zone NOT NULL, updated_at timestamp(6) without time zone NOT NULL, CONSTRAINT fk_rails_087c1d32f7 FOREIGN KEY (status_id) REFERENCES statuses(id) ON DELETE CASCADE);

CREATE INDEX index_tagged_objects_on_object ON tagged_objects (object_type, object_id);

CREATE UNIQUE INDEX idx_on_status_id_object_type_object_id_d6ebe374bd ON tagged_objects (status_id, object_type, object_id) WHERE object_type IS NOT NULL AND object_id IS NOT NULL;

CREATE UNIQUE INDEX index_tagged_objects_on_status_id_and_uri ON tagged_objects (status_id, uri) WHERE uri IS NOT NULL;
