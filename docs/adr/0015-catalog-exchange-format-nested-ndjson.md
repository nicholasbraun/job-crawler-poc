# Catalog exchange format: nested-record NDJSON, deterministic full exports

The import/export exchange format is NDJSON where each line is one **Company with its
Career Pages embedded**. The nesting mirrors the domain constraint (a page is
meaningless without its company; the natural key is `(company_id, url)`), making the
parent-child link unlosable — no cross-record ID resolution, no ordering rules, no
representable dangling page. NDJSON gives O(1)-memory streaming on both sides (export
is an `Encoder.Encode` loop, import a `bufio.Scanner` loop) and line-granular error
reporting ("line 217: invalid url"). A single JSON document was rejected because both
sides must buffer it and one syntax error rejects the file with no line context; CSV
was rejected because two entities force denormalisation or a zip, and it fumbles
exactly the null-vs-empty distinctions ADR-0001 cares about (`atsProvider`). Every
field except the identity is optional on import (ADR-0013 presence semantics), so the
same format serves full-fidelity backup and minimal hand-written prospect lists.
`politenessDomain` is never in the file; `website` is (it is the Identity Ladder's
second rung).

Exports are always the **full catalog** (zero-page Companies included, filtered views
are presentation-only — a filtered backup is a footgun) and **deterministically
ordered** by natural key (companies by `companyKey`, pages by canonical URL) rather
than the repositories' `last_seen DESC`: two exports diff meaningfully, a catalog can
be versioned in git, and an export → import → export round trip is byte-identical.
The stream carries no trailer/summary line — truncation detectability was judged not
worth complicating every consumer of the format.

## Consequences

- The export reads pages before companies and accepts the unsynchronised-snapshot
  race: a company created between the two reads appears with an empty `careerPages`
  array rather than pages being dropped. A repeatable-read transaction would need new
  transactional repository plumbing for a benign, human-triggered race.
- Once the first byte streams, the HTTP status is committed — a mid-stream DB error
  can only truncate output, never return a 500.
- Format changes after files circulate are breaking changes; there is no embedded
  schema version. If one is ever needed, NDJSON admits a header line without a format
  break.
