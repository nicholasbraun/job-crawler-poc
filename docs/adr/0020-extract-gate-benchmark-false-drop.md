# Extract-gate benchmark: a second Gold Set guarded on false-drops

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
