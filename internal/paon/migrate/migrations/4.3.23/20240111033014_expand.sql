-- Mastodon 4.3.23, upstream migration 20240111033014, phase expand.

CREATE TABLE generated_annual_reports (id bigserial PRIMARY KEY, account_id bigint NOT NULL, year integer NOT NULL, data jsonb NOT NULL, schema_version integer NOT NULL, viewed_at timestamp(6) without time zone, created_at timestamp(6) without time zone NOT NULL, updated_at timestamp(6) without time zone NOT NULL, CONSTRAINT fk_rails_4ca37f035c FOREIGN KEY (account_id) REFERENCES accounts(id));

CREATE UNIQUE INDEX index_generated_annual_reports_on_account_id_and_year ON generated_annual_reports (account_id, year);
