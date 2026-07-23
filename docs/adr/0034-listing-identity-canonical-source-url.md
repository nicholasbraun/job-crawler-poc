# Listing identity is the canonical source URL

A collected Job Listing is identified by its **canonical source URL** — one row per
real-world posting, globally, in the Corpus — not by the `(definition_id, url)` pair the
pre-inversion Keyword Crawls used. The Owner Company (`company_key`) is an *attribute*, not
part of the identity, and `definition_id` leaves the key entirely. This is what makes
collect-once/query-many honest: liveness ("is this posting open?") and new-detection are
statements about a posting in the world, not about the run that happened to see it.

## Per-lane canonicalization

Everything rides on identity staying stable across Cycles, so canonicalization is
lane-specific:

- **ATS Fetch lane:** identity is `provider:tenant:SourceID` from the provider's stable
  posting id, **not** the URL — several providers append a title slug to the URL
  (SmartRecruiters `…/{id}--title`), which re-slugs when a title is edited and would forge a
  "new" posting. This adds a `SourceID` field the BoardFetchers populate.
- **Crawl lane:** identity is the canonicalized posting URL (force `https`, lowercase host,
  strip `www`/fragment/trailing slash, sort params) with a tracking-param **denylist that
  keeps unknown params** — some boards carry the posting id in a query param (`?jobId=`), so a
  blanket query-strip (like `catalog.CanonicalURL`) would false-merge distinct postings.

Every ambiguous call biases toward **keep-distinct**: a false-merge silently loses a real
posting (unrecoverable), whereas a false-new is only a visible, recoverable blip.

## Considered and rejected

A singleton "collection definition" keeping the `(definition_id, url)` key (cheapest, but
identity stays run-scoped — a lie the moment two Cycles or lanes touch one posting); a
`(company_key, url)` key (redundant — a posting URL already belongs to exactly one Company).
Cross-source **semantic** dedup (the same job on two different boards) is explicitly deferred:
identity is the *source*, not the *job*.
