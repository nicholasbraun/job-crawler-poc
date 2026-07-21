-- +goose Up
-- country is the ISO 3166-1 alpha-2 Country a Job Listing resolves to (ADR-0029),
-- set at save on both lanes; empty ('') is the unresolved Country the Country
-- Constraint keeps (ADR-0028). NOT NULL DEFAULT '' mirrors the company_key /
-- work_arrangement idiom in this table so existing rows and unresolved listings
-- never surface SQL NULL to a string Scan. The DEFAULT is a constant, so on
-- Postgres 11+ the ADD COLUMN is metadata-only (no table rewrite). Purely
-- additive: no country backfill touches existing rows.
ALTER TABLE job_listing ADD COLUMN country text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE job_listing DROP COLUMN country;
