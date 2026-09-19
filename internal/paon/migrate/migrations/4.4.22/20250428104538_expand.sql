-- Mastodon 4.4.22, upstream migration 20250428104538, phase expand.

ALTER TABLE users ADD COLUMN require_tos_interstitial boolean DEFAULT false NOT NULL;
