-- +goose Up
-- ats_provider records the ATS host family a company was discovered on
-- ("greenhouse", "lever", …) or NULL for self-hosted career pages. It is a
-- separately queryable column; company_key stays provider-qualified so the
-- existing UNIQUE(company_key) cannot collapse same-named tenants across
-- different ATS providers (ADR-0001).
ALTER TABLE company ADD COLUMN ats_provider text;

-- A career page is uniquely identified by its owning company and URL, so a
-- re-crawl upserts in place instead of inserting a duplicate row.
ALTER TABLE career_page ADD CONSTRAINT career_page_company_url_key UNIQUE (company_id, url);

-- +goose Down
ALTER TABLE career_page DROP CONSTRAINT career_page_company_url_key;
ALTER TABLE company DROP COLUMN ats_provider;
