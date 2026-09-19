-- Mastodon 4.4.22, upstream migration 20250305074104, phase expand.

CREATE UNIQUE INDEX index_terms_of_services_on_effective_date ON terms_of_services (effective_date) WHERE effective_date IS NOT NULL;
