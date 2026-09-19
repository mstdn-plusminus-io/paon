-- Mastodon 4.6.6, upstream migration 20251117023614, phase expand.

ALTER TABLE media_attachments ADD COLUMN thumbnail_storage_schema_version integer;
