-- Mastodon 4.6.6, upstream migration 20260303144409, phase expand.

ALTER TABLE preview_cards ADD COLUMN unverified_author_account_id bigint;

ALTER TABLE preview_cards ADD CONSTRAINT fk_rails_6fb2119894 FOREIGN KEY (unverified_author_account_id) REFERENCES accounts(id) ON DELETE SET NULL;

CREATE INDEX index_preview_cards_on_unverified_author_account_id_and_id ON preview_cards (unverified_author_account_id, id) WHERE unverified_author_account_id IS NOT NULL;
