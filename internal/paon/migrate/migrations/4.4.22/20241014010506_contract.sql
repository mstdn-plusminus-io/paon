-- Mastodon 4.4.22, upstream migration 20241014010506, phase contract.

DROP INDEX IF EXISTS index_account_aliases_on_account_id;

DROP INDEX IF EXISTS index_account_relationship_severance_events_on_account_id;

DROP INDEX IF EXISTS index_custom_filter_statuses_on_status_id;

DROP INDEX IF EXISTS index_webauthn_credentials_on_user_id;
