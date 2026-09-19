-- Mastodon 4.4.22, upstream migration 20241205162640, phase contract.

ALTER TABLE webauthn_credentials DROP CONSTRAINT IF EXISTS fk_rails_a4355aef77, DROP CONSTRAINT IF EXISTS webauthn_credentials_user_id_fkey;

ALTER TABLE webauthn_credentials ADD CONSTRAINT fk_rails_a4355aef77 FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;
