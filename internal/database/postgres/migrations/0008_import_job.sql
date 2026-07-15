-- +goose Up
-- An Import Job is one asynchronous execution of a Catalog Import (ADR-0014):
-- the record is durable, the uploaded payload is not. idempotency_key and
-- request_fingerprint are added now but stay NULL until idempotent submission
-- (#87) populates them; idempotency_key is UNIQUE (NULLs are distinct in
-- Postgres, so keyless jobs never collide).
CREATE TABLE import_job (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    status              text NOT NULL CHECK (status IN ('pending', 'running', 'completed', 'failed')),
    dry_run             boolean NOT NULL,
    filename            text NOT NULL DEFAULT '',
    file_size           bigint NOT NULL DEFAULT 0,
    result              jsonb,
    error               text NOT NULL DEFAULT '',
    idempotency_key     text UNIQUE,
    request_fingerprint text,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now()
);

-- The boot-time sweep filters on status; the read endpoint lists newest first.
CREATE INDEX import_job_status_idx ON import_job (status);

-- +goose Down
DROP TABLE import_job;
