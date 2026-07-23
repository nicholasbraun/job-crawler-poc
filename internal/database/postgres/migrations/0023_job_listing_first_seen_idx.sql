-- +goose Up

-- Back the Overview's "recently found" feed (GET /api/listings/recent, SortFound):
-- a partial index on first_seen over Open listings, so the 5s-poll query is an index
-- scan instead of a full sort of the whole corpus. Partial (closed_at IS NULL) keeps
-- it small and matches the feed's open-only predicate; DESC matches the ORDER BY.
CREATE INDEX job_listing_open_first_seen_idx
    ON job_listing (first_seen DESC) WHERE closed_at IS NULL;

-- +goose Down
DROP INDEX job_listing_open_first_seen_idx;
