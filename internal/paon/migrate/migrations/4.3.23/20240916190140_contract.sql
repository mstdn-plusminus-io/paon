-- Mastodon 4.3.23, upstream migration 20240916190140, phase contract.

UPDATE oauth_applications SET scopes = TRIM(REPLACE(scopes, 'crypto', '')) WHERE scopes LIKE '%crypto%';

UPDATE oauth_access_tokens SET scopes = TRIM(REPLACE(scopes, 'crypto', '')) WHERE scopes LIKE '%crypto%';
