-- Mastodon 4.4.22, upstream migration 20241205163118, phase contract.

ALTER TABLE account_moderation_notes DROP CONSTRAINT IF EXISTS fk_rails_3f8b75089b, DROP CONSTRAINT IF EXISTS account_moderation_notes_account_id_fkey;

ALTER TABLE account_moderation_notes ADD CONSTRAINT fk_rails_3f8b75089b FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE;

ALTER TABLE account_moderation_notes DROP CONSTRAINT IF EXISTS fk_rails_dd62ed5ac3, DROP CONSTRAINT IF EXISTS account_moderation_notes_target_account_id_fkey;

ALTER TABLE account_moderation_notes ADD CONSTRAINT fk_rails_dd62ed5ac3 FOREIGN KEY (target_account_id) REFERENCES accounts(id) ON DELETE CASCADE;
