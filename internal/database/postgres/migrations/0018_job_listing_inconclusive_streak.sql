-- +goose Up

-- Crawl-lane staleness counter (ADR-0035): consecutive Inconclusive refetch probes
-- (5xx / timeout / soft-404) accrued by a Job Listing. The staleness backstop closes a
-- listing once this reaches the threshold; it resets to 0 whenever the listing is seen
-- alive. ATS-lane listings leave it 0 (absence-from-board is authoritative on a complete
-- fetch), so 0 is the correct default for every existing and future row.
ALTER TABLE job_listing ADD COLUMN inconclusive_streak int NOT NULL DEFAULT 0;

-- Partial index over the Open subset keyed by career_page, supporting the per-board
-- Open-listing query (ListOpen) and the career_page-scoped absence-sweep (CloseAbsent).
CREATE INDEX job_listing_open_by_career_page
    ON job_listing (career_page_id)
    WHERE closed_at IS NULL;

-- +goose Down
DROP INDEX job_listing_open_by_career_page;
ALTER TABLE job_listing DROP COLUMN inconclusive_streak;
