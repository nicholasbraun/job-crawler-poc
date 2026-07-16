-- +goose Up
-- Precondition: these are plain (non-CONCURRENT) unique index builds with no
-- dedup/backfill, so they assume a database that does not already violate the
-- invariants below. On a fresh or reset database that holds by construction
-- (ADR-0017's reset posture); a build over pre-existing duplicates fails and,
-- since migrations run at startup, blocks boot. Productionizing against live
-- data needs a dedup step first and a CONCURRENTLY build -- tracked as a
-- follow-up, not required for the POC.
--
-- Singleton Discovery Crawl (ADR-0017): at most one crawl_definition of kind
-- 'discovery'. A partial unique index on kind over the discovery predicate lets
-- keyword definitions accumulate freely while rejecting a second discovery
-- definition at the database -- race-proof, unlike an app-level check. Because
-- every indexed row shares kind='discovery', uniqueness on kind admits one row.
CREATE UNIQUE INDEX crawl_definition_single_discovery_idx
    ON crawl_definition (kind)
    WHERE kind = 'discovery';

-- One active Run per Definition (ADR-0017): at most one non-terminal crawl_run
-- per definition_id, discovery or keyword, so the same configuration never
-- double-crawls concurrently. Non-terminal is the EXPLICIT status list, not
-- NOT IN (terminal), so a future status defaults to non-blocking until
-- deliberately added here. Terminal runs (stopped, completed, failed) fall
-- outside the predicate and accumulate as append-only history (ADR-0005).
-- Reconcile and Resume mutate a run's status in place (UPDATE), never INSERT, so
-- neither trips this index.
CREATE UNIQUE INDEX crawl_run_one_active_per_definition_idx
    ON crawl_run (definition_id)
    WHERE status IN ('running', 'stopping', 'pausing', 'paused');

-- +goose Down
DROP INDEX crawl_run_one_active_per_definition_idx;
DROP INDEX crawl_definition_single_discovery_idx;
