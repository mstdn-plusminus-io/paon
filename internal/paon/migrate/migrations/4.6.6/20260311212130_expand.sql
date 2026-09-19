-- Mastodon 4.6.6, upstream migration 20260311212130, phase expand.

CREATE TABLE email_subscriptions (id bigserial PRIMARY KEY, account_id bigint NOT NULL, email character varying NOT NULL, locale character varying NOT NULL, confirmation_token character varying, confirmed_at timestamp(6) without time zone, created_at timestamp(6) without time zone NOT NULL, updated_at timestamp(6) without time zone NOT NULL, CONSTRAINT fk_rails_282940e759 FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE);

CREATE INDEX index_email_subscriptions_on_account_id ON email_subscriptions (account_id);

CREATE UNIQUE INDEX index_email_subscriptions_on_confirmation_token ON email_subscriptions (confirmation_token) WHERE confirmation_token IS NOT NULL;

CREATE UNIQUE INDEX index_email_subscriptions_on_account_id_and_email ON email_subscriptions (account_id, email);
