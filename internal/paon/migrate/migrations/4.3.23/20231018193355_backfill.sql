-- Mastodon 4.3.23, upstream migration 20231018193355, phase backfill.

DELETE FROM custom_filter_statuses WHERE id IN (SELECT id FROM (SELECT id, ROW_NUMBER() OVER (PARTITION BY status_id, custom_filter_id ORDER BY id ASC) AS duplicate_rank FROM custom_filter_statuses) ranked WHERE duplicate_rank > 1 ORDER BY id ASC LIMIT ?);

CREATE UNIQUE INDEX index_custom_filter_statuses_on_status_id_and_custom_filter_id ON custom_filter_statuses (status_id, custom_filter_id);
