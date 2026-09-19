-- Mastodon 4.3.23, upstream migration 20231212073317, phase expand.

CREATE INDEX idx_on_account_id_language_sensitive_250461e1eb ON account_summaries (account_id, language, sensitive);
