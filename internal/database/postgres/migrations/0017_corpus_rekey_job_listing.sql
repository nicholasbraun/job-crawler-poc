-- +goose Up

-- Corpus re-key of the listing table (ADR-0034). Clean cutover, no backfill
-- (ADR-0038): drop every existing listing row — they carry neither
-- canonicalization nor lifecycle, and the first Collection Cycle repopulates.
TRUNCATE TABLE job_listing;

-- Drop the old (definition_id, url) identity. Dropping the column cascades to
-- the UNIQUE(definition_id, url) constraint and the FK to crawl_definition.
ALTER TABLE job_listing DROP COLUMN definition_id;

-- New Corpus identity + Source Lane + lifecycle columns.
ALTER TABLE job_listing ADD COLUMN canonical_url  text NOT NULL;
ALTER TABLE job_listing ADD CONSTRAINT job_listing_canonical_url_key UNIQUE (canonical_url);
ALTER TABLE job_listing ADD COLUMN source         text NOT NULL CHECK (source IN ('ats', 'crawl'));
ALTER TABLE job_listing ADD COLUMN source_id      text NOT NULL DEFAULT '';
ALTER TABLE job_listing ADD COLUMN source_hash    text NOT NULL DEFAULT '';
-- career_page_id is provenance, not identity, and is already nullable (pageless
-- companies leave it NULL). ON DELETE SET NULL so deleting a Career Page orphans
-- its Corpus listings rather than FK-violating; the listings survive the page.
ALTER TABLE job_listing ADD COLUMN career_page_id uuid REFERENCES career_page (id) ON DELETE SET NULL;
ALTER TABLE job_listing ADD COLUMN closed_at      timestamptz;

-- source_hash replaces the write-only output content_hash (ADR-0035).
ALTER TABLE job_listing DROP COLUMN content_hash;

-- Retire the keyword crawl lane's persisted state (ADR-0038): remove every
-- non-discovery (keyword) CrawlDefinition and its runs, then drop the now-unused
-- keyword/country columns. Runs are deleted first (crawl_run FKs crawl_definition).
DELETE FROM crawl_run
    WHERE definition_id IN (SELECT id FROM crawl_definition WHERE kind <> 'discovery');
DELETE FROM crawl_definition WHERE kind <> 'discovery';

ALTER TABLE crawl_definition DROP COLUMN keywords;
ALTER TABLE crawl_definition DROP COLUMN countries;

-- +goose Down
-- Structural reverse only — dropped listing rows and keyword definitions are not
-- restored (the Up is a one-way clean cutover, ADR-0038).
ALTER TABLE crawl_definition ADD COLUMN countries text[] NOT NULL DEFAULT '{}';
ALTER TABLE crawl_definition ADD COLUMN keywords  text[] NOT NULL DEFAULT '{}';

ALTER TABLE job_listing ADD COLUMN content_hash text;
ALTER TABLE job_listing DROP COLUMN closed_at;
ALTER TABLE job_listing DROP COLUMN career_page_id;
ALTER TABLE job_listing DROP COLUMN source_hash;
ALTER TABLE job_listing DROP COLUMN source_id;
ALTER TABLE job_listing DROP COLUMN source;
ALTER TABLE job_listing DROP CONSTRAINT job_listing_canonical_url_key;
ALTER TABLE job_listing DROP COLUMN canonical_url;
-- definition_id cannot be restored NOT NULL (no source rows); re-add nullable so
-- the rollback is runnable. The old UNIQUE(definition_id, url) is not recreated.
ALTER TABLE job_listing ADD COLUMN definition_id uuid REFERENCES crawl_definition (id);
