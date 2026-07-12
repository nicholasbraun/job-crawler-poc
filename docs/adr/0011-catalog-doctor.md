# Catalog cleanup is a rule-replaying Go pass (the Catalog Doctor), not a SQL migration or re-crawl

Hardening the Gate, identity, and canonicalization logic only affects future
crawls: `Upsert` never deletes, so career pages already catalogued under the old
rules persist forever. The **Catalog Doctor** (`internal/catalogdoctor`, driven by
a thin `cmd/doctor` CLI that defaults to a dry-run report) is an idempotent pass
that re-derives Company identity, re-canonicalizes URLs, and re-applies the
URL-structural reject rules over the stored rows — hard-deleting or re-attributing
the losers and sweeping the Companies left orphaned (e.g. the fake `join.com`
company once its 20 real tenants are split out).

We chose a Go pass over a **SQL/goose migration** because the rules
(`catalog.Identify`, the posting-path veto, the storage-layer URL canonicalizer)
are Go and cannot be expressed in SQL; and over a **re-crawl** because a re-crawl is
slow, non-deterministic, and *still* never deletes rows that no longer qualify.

## Consequences

- The Catalog stores only URLs, not page content, so the Doctor corrects only
  URL-decidable errors. Content-driven false positives (a product page whose copy
  reads as careers, a marketplace's gig-signup page) survive until the next crawl.
  This bounds what "cleanup" can mean and is why the Doctor is not a precision
  silver bullet.
- We hard-delete, gated by the default dry-run report, rather than add a
  `quarantine`/`rejected_at` column — keeping this fix small. The rows are
  re-derivable derived data, so deletion is low-stakes.
- The logic lives in an importable package rather than a `main()` **specifically**
  so a future dashboard "report" feature — a button that appends a domain to a
  *persisted* denylist and doctors the offending entry — can reuse `Plan`/`Apply`.
  That persisted-list + report-button feature is out of scope here; this package is
  its intended engine.
