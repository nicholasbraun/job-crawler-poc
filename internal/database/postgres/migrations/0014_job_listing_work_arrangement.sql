-- +goose Up
-- Work Arrangement (ADR-0030) replaces the remote boolean. Add the column with the
-- honest default, backfill remote:true -> 'remote' (false/NULL -> 'unspecified' via
-- the default), then drop remote. The DEFAULT is a constant, so on Postgres 11+ the
-- ADD COLUMN is metadata-only (no table rewrite). DROP is ordered last, after the new
-- column exists and is backfilled: safe only under ADR-0030's single coordinated
-- deploy, where the DTO/persistence reshape ships atomically and no old reader of
-- remote remains. Lossy and one-way: remote:false history cannot be re-split into
-- onsite/hybrid, so it backfills to 'unspecified' (accepted per ADR).
ALTER TABLE job_listing ADD COLUMN work_arrangement text NOT NULL DEFAULT 'unspecified';
UPDATE job_listing SET work_arrangement = 'remote' WHERE remote = true;
ALTER TABLE job_listing DROP COLUMN remote;

-- +goose Down
ALTER TABLE job_listing ADD COLUMN remote boolean;
UPDATE job_listing SET remote = (work_arrangement = 'remote');
ALTER TABLE job_listing DROP COLUMN work_arrangement;
