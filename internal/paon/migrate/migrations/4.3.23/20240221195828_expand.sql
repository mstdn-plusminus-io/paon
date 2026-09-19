-- Mastodon 4.3.23, upstream migration 20240221195828, phase expand.

CREATE SEQUENCE notification_requests_id_seq;

CREATE TABLE notification_requests (id bigint DEFAULT nextval('notification_requests_id_seq') NOT NULL PRIMARY KEY, account_id bigint NOT NULL, from_account_id bigint NOT NULL, last_status_id bigint NOT NULL, notifications_count bigint DEFAULT 0 NOT NULL, dismissed boolean DEFAULT false NOT NULL, created_at timestamp(6) without time zone NOT NULL, updated_at timestamp(6) without time zone NOT NULL, CONSTRAINT fk_rails_881c7f71c4 FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE, CONSTRAINT fk_rails_5632f121b4 FOREIGN KEY (from_account_id) REFERENCES accounts(id) ON DELETE CASCADE, CONSTRAINT fk_rails_61c7aa9c1f FOREIGN KEY (last_status_id) REFERENCES statuses(id) ON DELETE SET NULL);

ALTER SEQUENCE notification_requests_id_seq OWNED BY notification_requests.id;

CREATE UNIQUE INDEX index_notification_requests_on_account_id_and_from_account_id ON notification_requests (account_id, from_account_id);

CREATE INDEX index_notification_requests_on_from_account_id ON notification_requests (from_account_id);

CREATE INDEX index_notification_requests_on_last_status_id ON notification_requests (last_status_id);

CREATE INDEX index_notification_requests_on_account_id_and_id ON notification_requests (account_id, id DESC) WHERE dismissed = false;
