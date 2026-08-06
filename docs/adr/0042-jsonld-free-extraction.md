# A lone structured-data JobPosting is extracted without the model

A page carrying exactly one JSON-LD `JobPosting` node and no `ItemList` — the exact
complement of the Extract Gate's openings-index reject — has its judgment fields (it is a
posting; its title, location, and work arrangement) read straight from that structured data,
with no LLM call. This **Free Extraction** is on by default; `EXTRACT_FROM_JSONLD=false` is
the kill switch, kept because this is the one path that can save a Job Listing with no model
in the loop, and if it ever goes wrong it goes wrong silently and at scale.

Measured by replaying the decorator over 2222 live captured pages: it fires on 42.2% of the
pages the extractor accepts, and on **2 of 1168 abstains** (0.17%) — and both of those are
real postings the model wrongly abstained on. It recovers misses rather than admitting junk.

## Why this is not the #45/#46 regression

That precedent — a JSON-LD bypass that cost the sibling career-page pipeline ~45% precision
— keyed on structured data being *present* and used it to skip classification entirely. This
one requires exactly one `JobPosting`, no `ItemList`, and a non-empty title, and it replaces
only the extraction of a page the Extract Gate has already admitted. Presence is not the
signal; unambiguity is.

## Consequences

- A Free Extraction records as a gate resolution, not an LLM call, so the call counters stay
  real calls. Two readings shift as a result: the **Empty-Extraction Rate** now covers only
  model-path extractions and is no longer a Corpus-wide precision measure, and the gated
  counter must be read split by reason — structured-data resolution means "saved for free"
  while every other reason means "shed nothing saved."
- Weighted to the live stream the free path removes only ~2.5 points of extract calls,
  because it fires almost exclusively on the ~5.5% of pages that are real postings. Its value
  is precision and recovered misses; the cost lever is the Extract Gate (ADR-0044).
- The same lone-posting signal appears in the Extract Gate, where it contributes **no unique
  admissions** — every page carrying it already carries another signal. It is kept there for
  its precision (it admits 0.2% of non-postings), not for coverage.
