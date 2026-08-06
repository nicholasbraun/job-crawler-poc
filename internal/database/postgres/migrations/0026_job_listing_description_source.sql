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
--
-- LOCK DURATION, since "no rewrite" is not the same as "no wait": goose runs a
-- migration file in ONE transaction, and ADD COLUMN takes ACCESS EXCLUSIVE on
-- job_listing immediately and holds it to COMMIT. The backfill UPDATE and the CHECK's
-- validation scan below therefore both run with the whole table locked against readers
-- and writers. That is startup latency here — docker compose replaces the single
-- container, so nothing is serving during it — but on a Corpus large enough to care,
-- split it with goose's NO TRANSACTION annotation: ADD COLUMN alone, then the backfill
-- in batches, then ADD CONSTRAINT ... NOT VALID followed by VALIDATE CONSTRAINT. NOT
-- VALID buys nothing while the statements share one transaction. (The annotation is
-- spelled out rather than quoted here on purpose: goose parses its marker anywhere in a
-- comment line, so writing it literally would break this migration's own parse.)
ALTER TABLE job_listing ADD COLUMN description_source text NOT NULL DEFAULT 'llm_summary';

-- Truthful backfill, not a placeholder. An ATS-lane row already carries the board API's
-- own text (ADR-0022/0023), so mark it as such rather than letting it default to legacy,
-- which is what the heal treats as "rewrite me from the page".
--
-- No ATS-lane row can actually reach the heal today — an embed board is fetched with a
-- Nil CareerPageID so its listings are invisible to ListOpen's career_page_id filter, an
-- ATS-routed seed never enters refetchPages at all (collection.RouteSeeds), and an ATS
-- row's empty source_hash could never match a sha256 digest anyway. This backfill is
-- defence in depth against that stack changing, and it is what makes the operator audit
-- below mean anything. This is a row-level UPDATE (ROW EXCLUSIVE, no rewrite); it
-- recomputes each touched row's generated search_tsv from the unchanged
-- title/description, so stored text is byte-identical.
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
