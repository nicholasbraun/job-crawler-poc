-- +goose Up
-- max_domains was a Frontier breadth cap that never bounded a run (enforced only
-- on admit, never on drain) and did not guard the real growth vector (the Redis
-- visited set). The cap machinery is removed end-to-end (see issue #11), so the
-- column is dead. Forward-only in production; migrations run up at startup.
ALTER TABLE crawl_definition DROP COLUMN max_domains;

-- +goose Down
-- Re-add with a DEFAULT so the rollback runs against existing rows (the column
-- was originally NOT NULL with no default). 10000 is the historical server
-- default that seeded new definitions.
ALTER TABLE crawl_definition ADD COLUMN max_domains integer NOT NULL DEFAULT 10000;
