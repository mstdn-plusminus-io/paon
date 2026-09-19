-- Mastodon 4.5.15, upstream migration 20250902221600, phase expand.

CREATE INDEX index_statuses_on_conversation_id ON statuses (conversation_id);
