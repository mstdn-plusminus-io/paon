-- Mastodon 4.4.22, upstream migration 20250129144440, phase expand.

CREATE INDEX index_statuses_public_20250129 ON statuses (id DESC, language, account_id) WHERE deleted_at IS NULL AND visibility = 0 AND reblog_of_id IS NULL AND ((NOT reply) OR (in_reply_to_account_id = account_id));
