-- Mastodon 4.5.15, upstream migration 20250909100506, phase expand.

CREATE UNIQUE INDEX index_conversations_on_parent_status_id ON conversations (parent_status_id) WHERE parent_status_id IS NOT NULL;
