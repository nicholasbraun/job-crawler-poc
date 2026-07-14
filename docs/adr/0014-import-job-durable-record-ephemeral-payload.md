# Import Jobs: durable record, ephemeral payload, idempotent submission

An Import Job must outlive its HTTP request (the upload is buffered at submit, `202 +
jobId` returned, processing async), so job state needs a home. We persist the job
*record* (an `import_job` table: status lifecycle, dry-run flag, filename, result
JSON, error) but deliberately **not the uploaded payload**: a boot-time sweep marks
jobs left `pending`/`running` by a dead process as `failed` ("interrupted by server
restart; re-upload the file"). Auto-resume would require storing and garbage-collecting
blobs to save one human click in a rare failure mode — and ADR-0013's monotone merge
makes re-upload a *complete* recovery, since a half-applied file plus a re-upload
equals exactly one full import. The in-memory alternative (no table) was rejected
because a restart would 404 the job the UI is polling and fork a second, weaker
job-state idiom next to `crawl_run`.

Submission itself is idempotent via an optional `Idempotency-Key` header (one key per
user action, minted client-side): the key is a UNIQUE column, insertion races resolve
via `ON CONFLICT DO NOTHING`, and a replay returns the original job (`200` vs `202`
fresh). A SHA-256 fingerprint of payload+flags is stored alongside; reusing a key with
a *different* request is a `422`, because silently returning a job that imported
different bytes is the one dangerous replay mode. Content-hash dedupe (no header) was
rejected: it cannot distinguish a retry from a deliberate re-import of the same file
after the catalog changed. Keys never expire — they live exactly as long as job rows.
Execution is serialized FIFO (size-1 semaphore): concurrent imports are harmless under
ADR-0013 but wasteful, and duplicates converge rather than corrupt.

## Consequences

- A same-key retry that straddles a server restart gets back the swept `failed` job
  and must mint a fresh key to actually import — correct (the key names *that
  submission attempt's* outcome) but subtle for headless clients.
- The dashboard always sends a key; bare `curl -F file=@…` without one simply always
  creates a new job.
- Dry-run and real runs of the same file are different requests by fingerprint, so a
  UI "import for real" step after a dry run must mint a new key — the design forces
  the correct behaviour.
