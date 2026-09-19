-- Mastodon 4.4.22, upstream migration 20241123224956, phase expand.

CREATE TABLE terms_of_services (id bigserial PRIMARY KEY, text text DEFAULT '' NOT NULL, changelog text DEFAULT '' NOT NULL, published_at timestamp(6) without time zone, notification_sent_at timestamp(6) without time zone, created_at timestamp(6) without time zone NOT NULL, updated_at timestamp(6) without time zone NOT NULL);
