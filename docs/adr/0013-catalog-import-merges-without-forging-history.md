# Catalog Import merges without forging history

A Catalog Import cannot reuse the crawler's `Upsert`: that primitive encodes "a live
Sighting happened now" — it stamps `last_seen = now()` and unconditionally overwrites
mutable fields — so restoring a backup would rewrite every `first_seen` to import day
(collapsing the ADR-0012 growth curve into a one-day cliff) and a minimal hand-written
record would blank fields it never mentioned. Import instead gets its own merge
primitive with three rules: timestamps merge monotonically (`first_seen =
LEAST(existing, file)`, `last_seen = GREATEST(existing, file)`, defaulting to `now()`
on first insert only), mutable fields win only when the field is **present in the
JSON** (pointer-typed decode; `"atsProvider": ""` deliberately sets self-hosted), and
an Import is **not a Sighting** — it never advances `last_seen` to now, because
`last_seen` means "the crawler observed this live", and an imported-but-dead page
should look exactly as stale as it is.

Company attribution is **file-authoritative**, resolved down an Identity Ladder:
explicit `companyKey` > derivation from the record's Website
(`catalog.RegistrableDomain`) > derivation from its Career Page URLs
(`catalog.Identify`, per ADR-0001). The file outranks derivation because the Catalog
legitimately contains attributions URL-derivation cannot reproduce (LLM/content-based
identification, Catalog Doctor corrections) and page content is not available at
import time — a derivation-authoritative import would silently undo those on every
round trip. It also protects against unknown-ATS tenant collapse: a careers URL on an
unrecognized ATS host would eTLD+1-fallback to the ATS's own domain as the company
key, merging every tenant of that ATS into one fake Company; attribution from the
file's key or Website sidesteps the fallback entirely. A keyless record whose pages
derive multiple distinct companies is rejected per-line rather than silently fanned
out.

## Consequences

- Because all writes are monotone merges, a partially-applied file is a *converging*
  state, not a corrupt one — re-uploading after fixing errors (or after a crash) is a
  complete recovery mechanism. ADR-0014's job model leans on this.
- Import errors are best-effort and per-line (collected and reported, first ~100),
  with a dry-run variant running the identical loop without writes; infrastructure
  errors fail the whole job instead of being collected per line.
- Page URLs are canonicalised (`catalog.CanonicalURL`) and `politeness_domain` is
  always derived from the URL host — the exchange format never carries it — so
  imported rows are indistinguishable from discovered ones.
- A hand-written file can mis-attribute a page; that is the Catalog Doctor's existing
  jurisdiction (ADR-0011), not a validation error.
