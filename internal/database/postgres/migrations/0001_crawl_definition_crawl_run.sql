-- +goose Up
CREATE TABLE crawl_definition (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name        text NOT NULL,
    kind        text NOT NULL,
    seed_urls   text[] NOT NULL DEFAULT '{}',
    keywords    text[] NOT NULL DEFAULT '{}',
    max_depth   integer NOT NULL,
    max_domains integer NOT NULL,
    url_filter  jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE crawl_run (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    definition_id  uuid NOT NULL REFERENCES crawl_definition (id),
    status         text NOT NULL CHECK (status IN ('running', 'stopping', 'stopped', 'completed', 'failed')),
    pages_crawled  bigint NOT NULL DEFAULT 0,
    listings_found bigint NOT NULL DEFAULT 0,
    started_at     timestamptz NOT NULL DEFAULT now(),
    finished_at    timestamptz,
    error          text NOT NULL DEFAULT ''
);

CREATE INDEX crawl_run_status_idx ON crawl_run (status);

-- +goose Down
DROP TABLE crawl_run;
DROP TABLE crawl_definition;
