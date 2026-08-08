# A lone structured-data JobPosting is extracted without the model

A page carrying exactly one JSON-LD `JobPosting` node and no `ItemList` — the exact
complement of the Extract Gate's openings-index reject — **that still renders the posting it
declares** has its judgment fields (it is a posting; its title, location, and work
arrangement) read straight from that structured data, with no LLM call. This **Free
Extraction** is on by default; `EXTRACT_FROM_JSONLD=false` is the kill switch, kept because
this is the one path that can save a Job Listing with no model in the loop, and if it ever
goes wrong it goes wrong silently and at scale.

## The abstain-override claim was wrong, and the predicate was narrowed because of it

This decision originally rested on replaying the decorator over 2222 live captured pages: it
fired on 42.2% of accepted pages and on **2 of 1168 abstains** (0.17%), and both of those two
were real postings the model wrongly abstained on — "it recovers misses rather than admitting
junk."

That claim did not survive a larger sample. On the 149-row Extract Gold Set (ADR-0043), which
samples the abstain-override cell **exhaustively** rather than by chance, the cell is 24 pages
and **17 of them are not postings**. Sixteen are postings withdrawn or filled
while their `JobPosting` node kept being served in full, above a body reading only "This
position is no longer active" — in five languages across the sample. The original replay saw
two of these and read both as model errors.

This is the worst possible failure for this mechanism. A withdrawn posting saved by the free path
enters the Corpus as a live job with no model in the loop, and the refetch lane can never
remove it: the page keeps serving that same body, so its `SourceHash` reads unchanged and the
listing is confirmed Alive every Collection Cycle, forever.

So the predicate gained a third condition: the page must not be a **Withdrawal Notice**
(`crawler.WithdrawalNotice`). `validThrough` cannot serve — it is absent on 18 of the 19 and
expired on none — and neither can the title, which appears in these bodies exactly as a live
posting's does.

Sites produce the shape two ways, and both had to be recognized:

- **The body is replaced by a message.** The page then declares far more than it renders.
  Across the labelled lone-posting rows the populations do not overlap: real postings top out
  at a declared/rendered ratio of 1.49, every one of these starts at 2.15. The bound is **1.8**,
  low in that empty band on purpose — a real posting wrongly delegated costs one model call and
  is extracted anyway, while a withdrawal notice wrongly extracted is permanent.
- **The posting is left whole under a banner.** Here no length comparison can see anything: the
  page really does render its posting, and only the words say it is gone. Recognized by a small
  set of whole banner phrases read off the live capture. They are phrases, never tokens,
  because the same capture holds a German "nicht mehr anzeigen" dismiss control and French prose
  about roles "pourvus en interne" that a token would refuse. On the capture this second shape
  is **95 pages the length comparison alone missed**.

The second shape was found only during the gold set's human confirmation pass — three pages
labelled `detail` whose bodies opened "This job is no longer accepting applications" and "Sorry,
this job was removed at 04:14 p.m." Three code reviews, the benchmark and a live crawl had all
passed over them. They are now labelled `residue` and refused.

The narrowing **loses no real posting**: every row it removes is one a reader confirmed is not a
live posting.

**Three known false fires remain**, accepted rather than hidden: evergreen talent-pool pages —
"General Application", "Spontaneous Application", an internship-enquiry page — which publish
`datePosted`, `employmentType`, `identifier`, `hiringOrganization`, and in one case `baseSalary`,
exactly as a real posting does. No structural signal distinguishes them; only reading them does,
which is what the model is for. Each carries a written reason and a human confirmer in the gold
set, so the count can only rise by a person's deliberate act.

## Why this is not the #45/#46 regression

That precedent — a JSON-LD bypass that cost the sibling career-page pipeline ~45% precision
— keyed on structured data being *present* and used it to skip classification entirely. This
one requires exactly one `JobPosting`, no `ItemList`, a non-empty title, and a page that still
renders the posting, and it replaces only the extraction of a page the Extract Gate has already
admitted. Presence is not the signal; unambiguity is.

The withdrawn-posting finding above is a reminder of how thin that distinction is. Structured data
describes what a page *says about itself*, and a page can keep saying it long after it stops
being true. Every future use of it should ask not only "is this unambiguous?" but "is the page
still backing this claim?"

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
