-- +goose Up

-- Keyword search must match on title and description only, never the company name.
-- A mis-derived company (a list-heading like "neuesten Angebote" that the Name Ladder's
-- title rung mistook for an employer name) was dragging many unrelated postings into a
-- keyword search two ways: as weight C in search_tsv it matched exactly, and via the
-- company trigram index it matched fuzzily. Drop the company (weight C) term from
-- search_tsv so the exact FTS covers only title (A) / description (B), and drop the
-- company trigram index -- SearchListings now fuzzes title alone.
--
-- search_tsv is a STORED generated column and its expression cannot be altered in place
-- across all supported Postgres versions, so it is dropped and re-added (which drops its
-- dependent GIN index; recreated below). This rewrites job_listing under an ACCESS
-- EXCLUSIVE lock -- acceptable here: the tsvector is derived, so no data is lost, and the
-- rewrite only recomputes it from title/description already on the row.
DROP INDEX job_listing_company_trgm_idx;
ALTER TABLE job_listing DROP COLUMN search_tsv;
ALTER TABLE job_listing
    ADD COLUMN search_tsv tsvector GENERATED ALWAYS AS (
        setweight(to_tsvector('simple', coalesce(title, '')), 'A') ||
        setweight(to_tsvector('simple', coalesce(description, '')), 'B')
    ) STORED;
CREATE INDEX job_listing_search_tsv_idx ON job_listing USING gin (search_tsv);

-- +goose Down
DROP INDEX job_listing_search_tsv_idx;
ALTER TABLE job_listing DROP COLUMN search_tsv;
ALTER TABLE job_listing
    ADD COLUMN search_tsv tsvector GENERATED ALWAYS AS (
        setweight(to_tsvector('simple', coalesce(title, '')), 'A') ||
        setweight(to_tsvector('simple', coalesce(description, '')), 'B') ||
        setweight(to_tsvector('simple', coalesce(company, '')), 'C')
    ) STORED;
CREATE INDEX job_listing_search_tsv_idx ON job_listing USING gin (search_tsv);
CREATE INDEX job_listing_company_trgm_idx ON job_listing USING gin (company gin_trgm_ops);
