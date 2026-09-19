-- Mastodon 4.3.23, upstream migration 20240713171841, phase expand.

ALTER TABLE reports ADD COLUMN application_id bigint;

ALTER TABLE reports ADD CONSTRAINT fk_rails_3deb8c7acb FOREIGN KEY (application_id) REFERENCES oauth_applications(id) ON DELETE SET NULL NOT VALID;
