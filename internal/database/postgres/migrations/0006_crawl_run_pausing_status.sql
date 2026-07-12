-- +goose Up
-- 'pausing' is a transient desired-state mirroring 'stopping': a pause was
-- requested and the run's status watcher drains its in-flight work before
-- parking it as 'paused' (ADR-0009). The CHECK was recreated under the
-- auto-generated name crawl_run_status_check in 0005, so re-derive it by that
-- name, widening the value set to admit 'pausing'.
ALTER TABLE crawl_run DROP CONSTRAINT crawl_run_status_check;
ALTER TABLE crawl_run ADD CONSTRAINT crawl_run_status_check
    CHECK (status IN ('running', 'stopping', 'pausing', 'paused', 'stopped', 'completed', 'failed'));

-- +goose Down
-- Fails if any 'pausing' rows remain; resume, pause, or terminate them first.
ALTER TABLE crawl_run DROP CONSTRAINT crawl_run_status_check;
ALTER TABLE crawl_run ADD CONSTRAINT crawl_run_status_check
    CHECK (status IN ('running', 'stopping', 'paused', 'stopped', 'completed', 'failed'));
