-- +goose Up

-- Description Source (ADR-0041): where each Job Listing's stored description came
-- from — the closed set crawler.DescriptionSource holds. It exists so the Corpus can
-- be audited (how much of it is real posting text) and so the refetch lane can find
-- the rows still carrying a legacy, model-authored summary and heal them from the
-- page it already fetched.
--
-- Deliberately NOT part of search_tsv. Migration 0025 had to DROP and re-ADD the
-- STORED generated column to change its expression, rewriting job_listing under an
-- ACCESS EXCLUSIVE lock; a plain added column avoids that entirely, and nothing here
-- changes what is indexed — search stays title (A) / description (B).
--
-- The DEFAULT is a constant, so on Postgres 11+ the ADD COLUMN is metadata-only, no
-- table rewrite (the backfill test pins this by asserting pg_relation_filenode is
-- unchanged). 'llm_summary' is the truthful value for every row that exists at
-- migration time — the crawl lane has stored the extractor's summary since ADR-0023 —
-- and it is also the safe value for any future insert that omits the column: legacy
-- is the one marker that invites re-derivation from the page instead of asserting a
-- provenance the row does not have.
ALTER TABLE job_listing ADD COLUMN description_source text NOT NULL DEFAULT 'llm_summary';

-- Truthful backfill, not a placeholder. An ATS-lane row already carries the board
-- API's own text (ADR-0022/0023), so mark it as such: the refetch lane iterates EVERY
-- Open listing under a crawl seed with no source-lane filter, so an ATS-lane listing
-- hanging off an ATS Embed page does reach the heal path — a placeholder marker there
-- would let scraped page furniture overwrite good board text. This is a row-level
-- UPDATE (ROW EXCLUSIVE, no rewrite); it recomputes each touched row's generated
-- search_tsv from the unchanged title/description, so stored text is byte-identical.
UPDATE job_listing SET description_source = 'ats_board' WHERE source = 'ats';

-- Closed set with teeth, matching the source column's CHECK in migration 0017: a lane
-- that forgets to stamp a marker fails its Save loudly instead of writing a silent
-- third state the heal would skip and the audit would not count. Added AFTER the
-- backfill, as its own statement: adding a CHECK scans the table to validate, it never
-- rewrites it.
ALTER TABLE job_listing ADD CONSTRAINT job_listing_description_source_check
    CHECK (description_source IN ('structured_data', 'page_content', 'ats_board', 'llm_summary'));

-- Operator audit — how much of the Corpus is real posting text, and how far the heal
-- has drained the legacy rows — is one query:
--   SELECT description_source, count(*) FROM job_listing
--    WHERE closed_at IS NULL GROUP BY description_source ORDER BY 2 DESC;

-- +goose Down
ALTER TABLE job_listing DROP CONSTRAINT job_listing_description_source_check;
ALTER TABLE job_listing DROP COLUMN description_source;
