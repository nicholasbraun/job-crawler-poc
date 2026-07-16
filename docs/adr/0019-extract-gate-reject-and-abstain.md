# Extract gate: reject non-postings and let the extractor abstain, rather than positively accept

The Keyword Crawl's Extract Gate (`pagegate.ShouldExtract`) sheds non–single-posting
pages by *rejecting* them and by letting the extractor *abstain* — not by positively
accepting confirmed postings. The originating design (#111) proposed a deterministic
ACCEPT rung (a lone JSON-LD `JobPosting` node → extract), but the keyword relevance
filter already sits in front of the extractor and is deliberately high-recall (a page
passes on a keyword in its title *or* body), so every page reaching the extractor has
already been accepted — a structural ACCEPT is a no-op, and by design must not bypass
the keyword gate to rescue a posting that lacks the keyword. The gate therefore only
*removes* calls: it rejects the page shapes the Discovery Gate accepts as hubs — an ATS
Embed, a structured-data openings index, a link-saturated page — reusing those
Structural Signal detectors with **opposite (reject) polarity**. As a safety net the
extractor emits `is_job_posting=false` (an Extractor Abstain) for a non-posting that
slips through: the record is discarded, the call is counted toward the Empty-Extraction
Rate, and the durable task is acked (not retried — an abstain is a completed decision,
not a failure).

## Considered options

- **The #111 layered accept/confirm gate: L1 deterministic ACCEPT + REJECT, L2 cheap
  binary LLM confirm, L3 abstain.** Rejected: L1-ACCEPT is inert because the keyword
  filter precedes the extractor, and L2 (a cheaper "is this one posting?" call) only
  pays off when the structurally-silent residue is *large*, *junk-heavy*, and the
  confirm call is *materially cheaper* than full extraction in the target deployment
  (on a local model, prompt-processing latency can erase the gap). Those are unknown
  until measured, so L2 is deferred behind that three-part data-gate and L1-ACCEPT is
  dropped entirely.
- **Refactor the shared detectors into one signal-extraction core with two verdict
  mappings.** Rejected for a minimal fork: `atsEmbed`, `jsonLDHub`, and
  `countJobPostingLinks`/`jobLinkSaturation` are already pure functions, so there is
  nothing to de-duplicate; a shared core would mean rewriting `confidenceScore` inside
  the bench-guarded Discovery Gate (ADR-0016) — the exact regression ADR-0016 warned
  about. `ShouldExtract` instead calls the detectors directly with reject polarity, and
  the Discovery Gate is left untouched.
- **Reuse the Discovery Gate's tuning knobs for the reject line.** Rejected: the one
  shared tunable (the job-link saturation count) gets a dedicated `Extract*` config
  field, so calibrating the extract path against its own gold set cannot silently shift
  the Discovery Gate's certain-accept behaviour. The real polarity hazard is config
  coupling, not code.

## Consequences

- ATS postings are exempt from the content rejects deterministically
  (`catalog.Classify == RoleJobListing → extract`), so an ATS posting's "more openings"
  sidebar cannot drop it. Self-hosted postings are *not* exempt; the saturation reject's
  recall risk on them is bounded by the Extract Gold Set (ADR-0020), and saturation is
  the first signal cut if it over-drops. A blanket `IsPostingPath` exemption was
  rejected — it would let `/jobs/all`-style hubs through.
- `ShouldExtract` now needs the parsed `*Content` (for the JSON-LD / embed / link
  signals); it is already in scope at the `url_processor` call site.
- Ingesting ATS postings via provider board APIs instead of scrape+LLM is a separate,
  complementary effort (issue #112): it owns the ATS slice and the embedded-board recall
  gap, while this gate owns the self-hosted long tail.
