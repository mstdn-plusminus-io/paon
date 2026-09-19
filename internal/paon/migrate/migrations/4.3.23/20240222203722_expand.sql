-- Mastodon 4.3.23, upstream migration 20240222203722, phase expand.

CREATE TABLE notification_policies (id bigserial PRIMARY KEY, account_id bigint NOT NULL, filter_not_following boolean DEFAULT false NOT NULL, filter_not_followers boolean DEFAULT false NOT NULL, filter_new_accounts boolean DEFAULT false NOT NULL, filter_private_mentions boolean DEFAULT true NOT NULL, created_at timestamp(6) without time zone NOT NULL, updated_at timestamp(6) without time zone NOT NULL, CONSTRAINT fk_rails_506d62f0da FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE);

CREATE UNIQUE INDEX index_notification_policies_on_account_id ON notification_policies (account_id);
