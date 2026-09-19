-- Mastodon 4.3.23, upstream migration 20240222193403, phase expand.

CREATE TABLE notification_permissions (id bigserial PRIMARY KEY, account_id bigint NOT NULL, from_account_id bigint NOT NULL, created_at timestamp(6) without time zone NOT NULL, updated_at timestamp(6) without time zone NOT NULL, CONSTRAINT fk_rails_7c0bed08df FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE, CONSTRAINT fk_rails_e3e0aaad70 FOREIGN KEY (from_account_id) REFERENCES accounts(id) ON DELETE CASCADE);

CREATE INDEX index_notification_permissions_on_account_id ON notification_permissions (account_id);

CREATE INDEX index_notification_permissions_on_from_account_id ON notification_permissions (from_account_id);
