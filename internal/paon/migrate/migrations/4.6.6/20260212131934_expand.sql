-- Mastodon 4.6.6, upstream migration 20260212131934, phase expand.

CREATE TABLE collection_reports (id bigserial PRIMARY KEY, collection_id bigint NOT NULL, report_id bigint NOT NULL, created_at timestamp(6) without time zone NOT NULL, updated_at timestamp(6) without time zone NOT NULL, CONSTRAINT fk_rails_0720c1a3d6 FOREIGN KEY (collection_id) REFERENCES collections(id) ON DELETE CASCADE, CONSTRAINT fk_rails_4a504bd5e6 FOREIGN KEY (report_id) REFERENCES reports(id) ON DELETE CASCADE);

CREATE INDEX index_collection_reports_on_collection_id ON collection_reports (collection_id);

CREATE INDEX index_collection_reports_on_report_id ON collection_reports (report_id);
