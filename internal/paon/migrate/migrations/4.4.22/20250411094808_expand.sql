-- Mastodon 4.4.22, upstream migration 20250411094808, phase expand.

CREATE TABLE quotes (id bigserial PRIMARY KEY, account_id bigint NOT NULL, status_id bigint NOT NULL, quoted_status_id bigint, quoted_account_id bigint, state integer DEFAULT 0 NOT NULL, approval_uri character varying, activity_uri character varying, created_at timestamp(6) without time zone NOT NULL, updated_at timestamp(6) without time zone NOT NULL, CONSTRAINT fk_rails_36d54169fc FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE, CONSTRAINT fk_rails_bd3ab4462c FOREIGN KEY (status_id) REFERENCES statuses(id) ON DELETE CASCADE, CONSTRAINT fk_rails_38068caa0e FOREIGN KEY (quoted_status_id) REFERENCES statuses(id) ON DELETE SET NULL, CONSTRAINT fk_rails_bfc5276b70 FOREIGN KEY (quoted_account_id) REFERENCES accounts(id) ON DELETE SET NULL);

CREATE UNIQUE INDEX index_quotes_on_status_id ON quotes (status_id);

CREATE INDEX index_quotes_on_quoted_status_id ON quotes (quoted_status_id);

CREATE INDEX index_quotes_on_quoted_account_id ON quotes (quoted_account_id);

CREATE INDEX index_quotes_on_account_id_and_quoted_account_id ON quotes (account_id, quoted_account_id);

CREATE INDEX index_quotes_on_approval_uri ON quotes (approval_uri) WHERE approval_uri IS NOT NULL;

CREATE UNIQUE INDEX index_quotes_on_activity_uri ON quotes (activity_uri) WHERE activity_uri IS NOT NULL;
