# A frozen gold-set row may be labelled against a re-fetch, but only under measured Capture Fidelity

ADR-0043 stores the Extract Gold Set as the parsed content the pipeline produced and states
the rule plainly: "It is never re-fetched: a page re-fetched months later is a different page,
and that drift corrupts the label." That reasoning is untouched — the label describes the
captured bytes, and nothing here changes what a label is about.

What it did not anticipate is that the captured form is unreadable by the human the ADR
requires. 379 of 457 rows carry an LLM-proposed label no person has confirmed, and the reason
the confirmation rate is what bounds the gold set (#274) is that a labeller is being asked to
resolve a page from one unpunctuated run of words. The rows that most need a human are the
ones a human can least read.

Rows drawn after ADR-0046 carry their own Structural Rendering and need nothing further. The
rows that already exist carry no markup at all, so the only way to put a legible page in front
of a labeller is to fetch the URL again — and the question is not whether that is risky but
**how to know, per row, whether it is.**

## Fidelity is measured per row, never assumed

A re-fetch is re-parsed to the same main region and compared against the row's captured text
by word 3-gram retention, which is robust to reordering and boilerplate churn in a way length
is not:

- **same** — HTTP 200 and >= 0.90 of the captured text's 3-grams still present. The page on
  screen is the page the label is about, and it is rendered in full.
- **drifted** — 200, but the text has moved. Rendered behind a warning; the captured text is
  restated as the authority.
- **gone** — non-200, or the captured text is essentially absent. **Not rendered at all.**
  This is the case that would corrupt a label rather than merely weaken it: a `detail` posting
  that has since closed serves a withdrawal page, and "a withdrawn posting with no role body"
  is `residue` in the labelling rubric, so the live page argues confidently for the wrong
  answer.

The two thresholds are not symmetric, and deliberately so. `same` is retention >= 0.90; `gone`
is retention < 0.30, with everything between them `drifted`. Calling a live page `gone` costs an
aid — the row is still labellable from its captured text, and the URL is still one click away.
Calling a `gone` page `drifted` costs a *label*. A page that has kept under a third of its
captured 3-grams has lost its body; what survives is furniture, and furniture cannot support a
label. The title is the third input and it only ever *withdraws* a claim: a page whose text is
0.90 retained but whose title has changed is `drifted`, never `same`, because `same` promises
the page on screen is the page the label is about and a changed title is the page itself saying
otherwise. A title is read only when both the captured page and the live one carry one;
otherwise retention decides alone.

Two cases fall outside retention. A row whose captured text is under three words has no 3-grams
and so no retention: it is `drifted` — a hint, never evidence — unless the live page is also
textless and the titles agree, which is the JS-shell case below, where the aid agrees with the
capture. And a host that refuses the re-fetch under robots.txt is *not measured at all*: `gone`
is a statement about the page, and we did not look.

Measured over 40 unconfirmed rows re-fetched on 2026-08-20: **25 same, 11 drifted, 4 gone**.
So roughly 62% of the standing backlog is labellable this way, and the remainder is identified
rather than guessed at.

The fidelity check reads the HTTP status, the title, and the text, and **nothing derived from
structured data**. A JSON-LD `JobPosting` node count would be the sharpest possible signal
here and is forbidden: the row's stratum IS that count, and every gold-set review surface
withholds it precisely so a label cannot be inferred from the signal the set exists to test
the mechanism against (ADR-0043).

## The captured content stays the authority, and says so on screen

The rendering is an **aid**. The label is a statement about the captured content, which is
shown alongside it, and the screen states which is which. This is not a formality: without it
the tool quietly redefines what a gold-set label means, and the whole set's value rests on
that meaning being fixed.

## What #288 built: the rendering is served, never framed

The page a labeller reads is the row's **Structural Rendering**, drawn as HTML by the tool
and served from its own loopback origin. Framing the site directly was the obvious thing and
is wrong twice: a site's `X-Frame-Options` refuses it, and a browser running the site's own
CSS and scripts would show a *richer* page than the crawler ever saw — the crawler executes
no JavaScript, so what the Extract Gate read is the parse, not the painted page. Serving the
parse has neither problem, and it is the same rendering the extractor reads, with the link
targets kept (ADR-0046: `/organization/…` against `/jobs/…` is what settles an employer
directory).

The frame is `sandbox`ed with no `allow-scripts` and no `allow-same-origin`, and the document
is served under `default-src 'none'`, so the third-party text on screen cannot reach the
network and cannot see the session that writes committed ground truth. A `<base>` reference
is injected so a target the page wrote relative resolves to the site it came from; a click
opens a real tab. Link targets are shown as the renderer recorded them — sanitized and capped
at 80 runes — so a long target displays and clicks short. That is stated on screen rather than
papered over: the rendering is what is being judged, and the tool does not invent a target the
rendering does not have.

*same* is rendered as the primary view beside the captured text; *drifted* behind a visible
warning with the captured text restated as the authority; *gone* is refused **at the wire**,
with a 409 and no bytes of the live page in the response, not merely hidden in the page's CSS.

A row that carries its own Structural Rendering is shown it directly, with no fetch at all —
fidelity `same` by construction, and no live view offered because none is needed. That is the
path that outlives this ADR: once a fresh drawing carries its own rendering, the re-fetch half
is dead weight and the display half is all that is left.

## Consequences

- The crawler's downloader does not execute JavaScript, so a re-fetch rendered without scripts
  reproduces what the gate actually saw rather than a richer page. A JS-shell board renders
  empty in both, and "a JS shell" is `residue` — the aid agrees with the capture instead of
  contradicting it.
- Re-fetched HTML is a working artefact: cached outside the repository, never committed, and
  never written back onto the row. A row's stored content is what the tap captured, full stop.
- ~38% of the standing backlog cannot be rendered at all. Those rows stay owed, and the honest
  route to clearing them is a fresh drawing under ADR-0046, not a label taken from a page that
  is no longer the one being judged.
- This applies to the existing drawings only. Once rows carry a Structural Rendering, fidelity
  is `same` by construction and the re-fetch path is dead weight that should be removed rather
  than maintained.
