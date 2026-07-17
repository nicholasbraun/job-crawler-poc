-- +goose Up
-- tech_stack is removed end-to-end (ADR-0023): the ATS Fetch lane cannot supply a
-- normalized technology list from a board API, and keeping it would force a
-- per-posting LLM call. Job Listings keep a free-text description instead.
ALTER TABLE job_listing DROP COLUMN tech_stack;

-- +goose Down
ALTER TABLE job_listing ADD COLUMN tech_stack text[] NOT NULL DEFAULT '{}';
