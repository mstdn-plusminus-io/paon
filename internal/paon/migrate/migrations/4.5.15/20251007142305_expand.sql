-- Mastodon 4.5.15, upstream migration 20251007142305, phase expand.

ALTER TABLE accounts ALTER COLUMN id_scheme SET DEFAULT 1;
