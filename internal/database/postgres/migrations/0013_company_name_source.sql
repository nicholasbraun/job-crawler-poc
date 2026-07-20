-- +goose Up
-- name_source records which Name Ladder rung produced a Company's name (ADR-0025),
-- so the Catalog records how far to trust it. Nullable: NULL = legacy/unknown (a
-- row catalogued before the ladder, or an imported row). No backfill -- a row
-- acquires a Source on re-crawl. The empty string maps to SQL NULL (the
-- ats_provider idiom).
ALTER TABLE company ADD COLUMN name_source text;

-- +goose Down
ALTER TABLE company DROP COLUMN name_source;
