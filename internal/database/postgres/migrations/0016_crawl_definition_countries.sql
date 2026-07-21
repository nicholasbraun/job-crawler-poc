-- +goose Up
-- countries is the Country Constraint (ADR-0028): the set of target ISO 3166-1
-- alpha-2 codes a Keyword Crawl keeps Job Listings for. Empty ('{}') is the
-- "anywhere" constraint — today's behavior — so existing definitions keep it.
-- NOT NULL DEFAULT '{}' mirrors the seed_urls / keywords array idiom in this
-- table so a nil Go slice never surfaces SQL NULL to a []string Scan. The DEFAULT
-- is a constant, so on Postgres 11+ the ADD COLUMN is metadata-only (no table
-- rewrite). Purely additive: no backfill touches existing rows.
ALTER TABLE crawl_definition ADD COLUMN countries text[] NOT NULL DEFAULT '{}';

-- +goose Down
ALTER TABLE crawl_definition DROP COLUMN countries;
