-- +goose Up

-- Corpus full-text search (ADR-0037). Text-search config is deliberately `simple`
-- (no stemming, no stopwords): the Corpus mixes German and English postings and an
-- English stemmer mangles German. Do NOT "fix" this to `english` — per-language
-- stemming is a later upgrade behind the same SearchListings port.
CREATE EXTENSION IF NOT EXISTS pg_trgm;

-- Weighted, generated tsvector: title A / description B / company C (ADR-0037).
-- GENERATED ALWAYS keeps it in lockstep with the row without a trigger. coalesce()
-- guards the NULLable description/company columns. STORED requires a table rewrite,
-- which is free here: job_listing was truncated at the corpus re-key (0017) and the
-- first Collection Cycle repopulates it, so the table is empty at migration time.
ALTER TABLE job_listing
    ADD COLUMN search_tsv tsvector GENERATED ALWAYS AS (
        setweight(to_tsvector('simple', coalesce(title, '')), 'A') ||
        setweight(to_tsvector('simple', coalesce(description, '')), 'B') ||
        setweight(to_tsvector('simple', coalesce(company, '')), 'C')
    ) STORED;

CREATE INDEX job_listing_search_tsv_idx ON job_listing USING gin (search_tsv);

-- Trigram indexes power the pg_trgm fuzzy tail. gin_trgm_ops serves the word-similarity
-- operator (title %> kw / company %> kw), so SearchListings uses these via a BitmapOr with
-- the tsvector index above — provided the fuzzy branch is written as the %> operator, not
-- the word_similarity(...) >= const function form, which is opaque to the planner and
-- forces a seqscan. The operator tests pg_trgm.word_similarity_threshold, which
-- SearchListings pins per query with SET LOCAL. Only title (strongest signal) and company
-- are indexed; description is intentionally excluded (large text, low fuzzy value,
-- oversized index).
CREATE INDEX job_listing_title_trgm_idx   ON job_listing USING gin (title gin_trgm_ops);
CREATE INDEX job_listing_company_trgm_idx ON job_listing USING gin (company gin_trgm_ops);

-- +goose Down
DROP INDEX job_listing_company_trgm_idx;
DROP INDEX job_listing_title_trgm_idx;
DROP INDEX job_listing_search_tsv_idx;
ALTER TABLE job_listing DROP COLUMN search_tsv;
DROP EXTENSION IF EXISTS pg_trgm;
