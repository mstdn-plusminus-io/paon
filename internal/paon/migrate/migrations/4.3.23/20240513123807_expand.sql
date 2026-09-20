-- Mastodon 4.3.23, upstream migration 20240513123807, phase expand.

CREATE INDEX index_notifications_on_account_id_and_group_key ON notifications (account_id, group_key) WHERE group_key IS NOT NULL;
