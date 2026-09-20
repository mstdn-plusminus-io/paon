-- Mastodon 4.3.23, upstream migration 20240522041528, phase expand.

ALTER TABLE preview_cards ADD COLUMN author_account_id bigint;

ALTER TABLE preview_cards ADD CONSTRAINT fk_rails_dca4905b94 FOREIGN KEY (author_account_id) REFERENCES accounts(id) ON DELETE SET NULL;

CREATE INDEX index_preview_cards_on_author_account_id ON preview_cards (author_account_id) WHERE author_account_id IS NOT NULL;
