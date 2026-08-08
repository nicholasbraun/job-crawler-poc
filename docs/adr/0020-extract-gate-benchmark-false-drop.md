# Extract-gate benchmark: a second Gold Set guarded on false-drops

> **Amended by ADR-0043.** The three-way labels, the binary scoring, and the false-drop hard
> guard are unchanged. The substrate is not: fixtures are now pages captured from the live
> extract stream and stored as parsed Content, split into a random and a Boundary Stratum
> with sampling weights on each row.

> **Amended by #264.** The hard guard is unchanged in kind and now runs in `go test`
> (`TestExtractGoldSetFalseDropGuard`), so CI enforces it rather than a benchmark verb somebody
> remembers to run. It is an **exact-set ratchet over a named ledger** rather than an absolute
> zero: thirteen specific pages are individually argued in `extractGoldSetFalseDrops` and in
> ADR-0044, and every other page on the web is a hard zero. Two of the thirteen are dropped by a
> *reject* rung and predate the Positive Evidence rung entirely, so an absolute zero would fail
> for a cause #257 forbids touching; eleven rest on labels ADR-0043 forbids acting on until a
> human confirms them. The ledger may only shrink, a recovery must be recorded rather than
> pocketed, and an entry goes red the moment its label's confirmation state changes — so the
> guard arms itself as the confirmation pass lands. Everything else stays descriptive with no
> threshold, including the projected-spend line, which is the extract-call rate relabelled: it
> is the multiplier on today's bill because the capture frame sits downstream of the gate.

The Extract Gate is measured by its own Extract Gold Set, separate from the classifier
Gold Set (ADR-0008), because it decides a different question — *is this page a single
Job Listing?* — over a different sample: keyword-relevant pages of every shape, not a
Career-Page-centric one. Each fixture is labelled one of three classes — single-posting
**detail**, **hub/index**, or structurally-silent **residue** — but the *scored*
decision is binary (extract or skip), collapsing hub+residue to "skip". The three-way
label is kept so precision/recall can be sliced per non-posting type (which reject signal
catches which) and so the residue's size and junk-ratio answer whether the deferred L2
confirm is ever worth building. As with ADR-0008 only the irrecoverable failure is a hard
guard: a **false-drop** — a real detail page the gate rejects — prints red and exits
non-zero, while the extract-call rate stays a soft, composition-dependent measurement.
The benchmark reuses `llmbench`'s fixture store, `manifest.json`, live-pipeline replay,
and layered report; it adds an extract scorer and replays `parser → ShouldExtract →
extractor`.
