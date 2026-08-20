# Parser golden artefacts

## `flattened_text.golden.jsonl`

One JSON object per line — `{"fixture": …, "flattened_text": …}` — sorted by fixture
name. It records today's `parser.Parse(...).MainContent` for every page under
`cmd/llmbench/testdata/pages/`: 90 rows, 526,159 bytes of **Flattened Text**.

## Why it exists

ADR-0046. All of spec #275 rests on one asserted invariant — stripping a **Structural
Rendering** reproduces today's Flattened Text byte for byte. `SourceHash` is persisted
on ~46k Job Listings and a different value there silently re-extracts the whole Corpus,
so the derivation is proven exact rather than argued (#277).

This file is what "exact" is measured against. It was frozen *before* the parser moved,
because reconstructing "what it used to say" from memory afterwards is precisely the
mistake that lets a silent break through.

## Regenerating

```bash
go test ./internal/parser/ -run TestFlattenedTextGolden -update
```

**Legitimate only when the committed fixture set changed** — a page added to or removed
from `cmd/llmbench/testdata/pages/`. Regenerate in the same commit that changes the
fixtures, and read the per-fixture deltas the run logs (`-v`) before staging the result.

**Never to make a failing round-trip go green.** A red round-trip means the renderer or
the derivation is wrong; ADR-0046's stated fallback is #275's two-field design, decided
on the failing fixtures. Regenerating destroys the only evidence that says which. Same
house rule as the Gold Set labels: ground truth is human-owned and is never edited to
make a run pass.

## Two things that look like bugs and are not

- **Five rows are empty.** `0005-jobs-ashbyhq-com-alephalpha`, `0008-jobs-ashbyhq-com-alan`,
  `0011-jobs-ashbyhq-com-finary`, `0014-jobs-ashbyhq-com-fyxer` (Ashby single-page apps) and
  `0075-redis-io` render their content with JavaScript, and the crawler executes none. `""`
  is their real output and they stay in the artefact.
- **Words run together across block boundaries** — `"…at TideCreate a Job Alert"` — because
  `normalizeWS` splits `goquery`'s `.Text()` on whitespace and block elements contribute
  none. That is today's real output. The artefact is byte-exact and is never tidied.
