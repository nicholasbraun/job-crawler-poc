-- +goose Up
-- 'paused' is a graceful-shutdown parking state: a run interrupted by a clean
-- restart/redeploy, left resumable so boot-time reconcile re-adopts it. Unlike
-- 'stopped' it is non-terminal (finished_at stays null). The original CHECK was
-- created inline in 0001, so Postgres auto-named it crawl_run_status_check.
ALTER TABLE crawl_run DROP CONSTRAINT crawl_run_status_check;
ALTER TABLE crawl_run ADD CONSTRAINT crawl_run_status_check
    CHECK (status IN ('running', 'stopping', 'paused', 'stopped', 'completed', 'failed'));

-- +goose Down
-- Fails if any 'paused' rows remain; resume or terminate them first.
ALTER TABLE crawl_run DROP CONSTRAINT crawl_run_status_check;
ALTER TABLE crawl_run ADD CONSTRAINT crawl_run_status_check
    CHECK (status IN ('running', 'stopping', 'stopped', 'completed', 'failed'));
