-- Mastodon 4.3.23, upstream migration 20240808125420, phase contract.

ALTER TABLE notification_policies DROP COLUMN filter_not_following;

ALTER TABLE notification_policies DROP COLUMN filter_not_followers;

ALTER TABLE notification_policies DROP COLUMN filter_new_accounts;

ALTER TABLE notification_policies DROP COLUMN filter_private_mentions;
