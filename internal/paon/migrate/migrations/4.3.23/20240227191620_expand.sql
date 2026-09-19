-- Mastodon 4.3.23, upstream migration 20240227191620, phase expand.

CREATE INDEX index_notifications_on_filtered ON notifications (account_id, id DESC, type) WHERE filtered = false;
