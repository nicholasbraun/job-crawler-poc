# The stored page content is a Structural Rendering; plain text is derived from it

`parser.getMainContent` ends in `normalizeWS`, which collapses every run of whitespace
because "page layout is irrelevant to the downstream LLM, so this trades it for a denser,
smaller input." It is not irrelevant. It is frequently the whole answer to the one question
the extractor is asked — is this ONE posting? — and to the same question a human labeller
answers on the Extract Gold Set.

`advertorial.de/stellenangebot-sales-manager` is one Sales Manager posting, human-confirmed
`detail`. Its apply form offers a role picker, which flattens to `… Dann bewirb Dich jetzt!
Position Position Sales Manager (m/w/d) Teamleiter Sales (m/w/d) Junior Online-Redakteur
(m/w/d) Vorname Nachname …` — three role titles in a row, which reads as an index of three
roles. Rendered structurally the same bytes read `Position Position [input checkbox: ?] Sales
Manager (m/w/d) [input checkbox: ?] Teamleiter Sales (m/w/d) …`, unambiguously one form
offering one application. Scanning the gold set for the shape (#275) finds it on ~4% of
`detail` rows. `neckarfilsjobs.de/arbeitgeber` is the mirror case: 1433 characters of
run-together company names and street addresses, which renders as `# Unternehmen` over a list
of `[company](/organization/…)` links — an employer directory, not a job index, decided at a
glance.

So the parser keeps the structure: headings, list items, table rows, link text with its href,
and the form controls (`[select …]`, `[input …]`, `[button: …]`) that the advertorial case
turns on.

## One stored form, not two, because the dependency runs one way

#275 proposed a second field, reasoning that `MainContent` has thirteen non-test consumers —
it IS the Posting Body (ADR-0041), the FTS index source, the Extract Gate's Positive Evidence
input, the duplication probe, the keyword filters — and that markup would poison all of them.
That is the right worry and the wrong conclusion. A Structural Rendering reduces to today's
plain text deterministically; today's plain text cannot be raised back into structure. The
richer form is therefore what belongs in storage, and every plain-text consumer derives its
own input through one pure function (`FlattenedText`) rather than reading a second field
that can drift from the first.

## The round-trip test is the entire safety argument

`SourceHash` is persisted on `job_listing.source_hash` — ~46k rows — and says so in its own
comment: "must never change: a different value here silently re-extracts the whole Corpus."
The refetch lane compares it to confirm a listing alive with no model call. A rendering change
that reaches that hash re-extracts every listing in one wave, and re-baselines every title and
location the model reads back.

That is avoided if and only if the derivation is exact, so it is asserted rather than argued:

    FlattenedText(StructuralRendering(html)) == getMainContent(html)

byte for byte, over the 90 real committed classifier fixtures. The invariant constrains the
renderer — `[input text: Vorname]` must strip to nothing because the `<label>` already
contributed the word, `[button: Senden]` must strip to `Senden`, an image's `[img: alt]` must
vanish because today's text has no alt text — and the test is what proves it holds. **If it cannot be
held, the fallback is #275's two-field design**, taken on evidence rather than on caution.

## The rendering grammar

The invariant decides the syntax, so the syntax is recorded here rather than left to the
renderer (#279). The illustrations above are prose; this is the grammar.

Every run of whitespace inside a text node becomes a *separator* rather than characters, so
**source-derived text in a rendering never contains a tab or a newline** — every one of them
is synthesized, which is what makes the strip unambiguous. Which separator is written depends
on what the page itself had at the boundary:

| boundary | rendered | flattens to |
| --- | --- | --- |
| block, page had whitespace | `"\n"` | one space |
| block, page had none | `"\t\n"` (the JOIN) | nothing |
| table cell, page had whitespace | `"\t"` | one space |
| table cell, page had none | `""` | nothing |
| no boundary, page had whitespace | `" "` | one space |
| line-prefix terminator | `"\v"` | nothing, with its prefix |

The JOIN is not a nicety. `goquery`'s `.Text()` concatenates text nodes with no separator at
all, so a minified `<p>A</p><p>B</p>` is `AB` in today's output — and **58 of the 90 committed
fixtures** run words together across at least one block boundary (`Über RedcareNewsroom`,
`SolutionsSOLUTIONSCedar Pay`). A renderer that emitted a newline there and let it collapse to
a space would re-key 64% of the Corpus. The rendering therefore records the absence, and the
derivation deletes the mark instead of folding it.

Line prefixes — `#`…`######` for a heading level, `-` for a list item or an option, `|` for a
table row — are written after the newline and end in a **vertical tab**, and are emitted
lazily so an empty block contributes nothing. `"\v"` terminates a prefix and does nothing
else in this grammar, so the strip's `^(#{1,6}|-|\|)\v` rule has exactly one reading.
The one shape never written is a tab immediately before a newline other than the JOIN itself.

**This was originally a tab, and that was a round-trip break.** The reasoning was that page
text can hold `"- "` but never `"-\t"`, so an anchored rule cannot eat a paragraph beginning
`"- "` — true, but it guards the wrong side. The JOIN *is* `"\t\n"` and a cell separator *is*
`"\t"`, so a page word that is exactly `-`, `|` or `#` and lands at the start of a rendered
line is followed by a synthesized tab: `^-\t` matched, and the strip deleted both the page's
own character and the boundary behind it, folding `Salary-Berlin` into `Salary Berlin` with a
new `SourceHash`. None of the 90 committed fixtures contains the shape — 30 of them carry a
standalone `-`/`|`/`#` token but never in that position — so the round-trip test was green
while the invariant was false. Three independent reviews of #279 found it (#289). A terminator
the JOIN cannot forge is what makes the grammar injective here rather than merely unfalsified;
the case is pinned by hand-written pages, since no fixture can pin it.

**The two wrapping markers decline themselves rather than corrupt their text.** Page text that
would make a marker unreadable — an unbalanced `]`, which closes the marker early, or text that
turns `[…]` into something the strip reads as a control marker, as in `<a>input your details</a>`
— is written bare, with no marking at all. `crawler.WrappableMarkerText` is the single predicate,
and it lives beside the strip's patterns because the question is entirely about how the strip
reads a marker. Before it, `<a href="/x">See ] more</a>` left `[See ] more](/x)` — the raw
marker *and the href* — in the Posting Body, the Corpus search index and `SourceHash`. Losing a
marking costs the model a signal; corrupting the text costs the Corpus a re-extraction, so the
trade is not close.

Markers are built from **attributes only** — `href`, `alt`, `value`, `type`, `placeholder`,
`aria-label`, `name` — none of which is in today's flattened output, so each may strip to
nothing: `[img: alt]`, `[input text: Vorname]`, `[select: Position]`, `[textarea: message]`,
`[input submit: Senden]`. Exactly two markers wrap page text and both give it back:
`[button: Senden]` and `[text](href)`. An `<option>`'s and a `<textarea>`'s own text is page
text and stands beside its marker rather than inside it. Attribute text is sanitized by the
renderer (whitespace collapsed, brackets turned to parentheses, `)` percent-encoded in an
href, capped) — it is synthetic, so this is free and exact.

The `![alt]` spelling was tried for images and dropped: page text ending in `!` directly before
a link or a button forged an image marker on three fixtures, and the strip then ate text the
page really had. Wrapping markers tolerate one level of brackets in the text they wrap,
because four fixtures link text like `[email protected]`, `Fixed Term [12 Months]` and
`(1965) [pdf]`.

What remains ambiguous is page text that literally contains `](` or a marker's own shape.
Zero occurrences across the 526 KB of page text in the 90 fixtures; the round-trip test is the
standing check; the rendering is off by default until a harvest has scored it.

## The prompt and the human read different variants

Rendering cost was measured over 36 live pages: median **1.43x** the flattened text with
hrefs, against #275's measured 1.10x without them. `LLM_EXTRACT_MAX_CHARS` is 8000, so at an
unchanged cap the href-carrying form would show the model ~30% less page than today — "keep
the tags" and "raise the cap" compound rather than compose. The extractor prompt therefore
reads the href-free variant; the labelling UI reads the full one, because `/organization/…`
versus `/jobs/…` is exactly what settles a page like `neckarfilsjobs.de`.

What #280 built: the narrowing is `crawler.WithoutLinkTargets`, a pure deletion of the
`(href)` half of a link marker, so the labeller's variant is literally a superset of the
prompt's rather than a second rendering that has to be kept in step. The brackets stay — a
rail of "similar jobs" links has to remain distinguishable from the posting's own prose, and
dropping them buys nothing. The line prefixes and the JOIN reach the model
unchanged: they are what make the rendering strippable, and they read as structure as they
are. The cap applies to the narrowed form, measured over the 90 committed fixtures at
**1.253x** the Flattened Text with targets and **1.081x** without, beside the 36-live-page
figures above. The extractor's "judge by DETAIL, not by how many role names appear" bullet is
**kept**, not retired: the kill switch is off by default, so production still reads Flattened
Text where the bullet stands in for the missing form controls, and #282 is the ticket that
scores its removal.

What #282 measured: `cmd/llmbench score-rendering` replays the extract path over the committed
pages twice — once through a flattening parser, once through a rendering one — at one prompt
budget applied identically to both arms, with no network and no model. It confirms the
narrowed form's cost at the cap rather than correcting it: **1.0937x** the Flattened Text in
runes over the 90 real fixtures and **1.0689x** over the 26 labelled ones, beside the 1.081x
uncapped and 1.10x live figures above. Where that lands is the number the decision turns on:
at an 8000-rune budget the rendering delivers **1.80% fewer page words** to the extractor over
the 90 real pages (40444 against 41184 of 60432 available), and takes the pages the cap
truncates from 13 to 15 of 90 — `governikus.de/karriere/arbeiten-bei-uns` and `xing.com`. The
standing context is that the cap, not the rendering, is what bounds what the extractor sees:
62 of the 457 real Extract Gold Set rows (13.6%) already exceed the budget today, and only
78.3% of their page words reach the model at all.

So `PARSE_STRUCTURAL_RENDERING` **stays `false`**, and the "judge by DETAIL" bullet stays with
it. The A/B establishes safety and bounds the cost; the benefit — a model reading an apply
form's picker as a picker — is only observable WITH a model, and an offline benchmark cannot
settle a prompt question any more than it can settle a model one.

### The online measurement (#289): the default is conditional on the model

That online scoring has now been run, against a local server with two models loaded, using
`extractbench -fixtures ... [-structural]`. It could not use the committed Extract Gold Set —
those rows are parser-blind (ADR-0043) and no renderer can be replayed over them — so the 25
`detail` rows that `subsample()` selects were **re-fetched live** to make them re-renderable.
Both arms then read identical HTML, temperature 0 and seed 42 as in production, and the
flattened arm was run twice to confirm the numbers reproduce exactly.

| model | arm | recall | false-drops | wall |
| --- | --- | --- | --- | --- |
| Qwen2.5-3B-Instruct | flattened | 44.0% (11/25) | 14 | 1m58s |
| Qwen2.5-3B-Instruct | **structural** | **56.0% (14/25)** | **11** | 2m18s |
| Qwen3.5-9B | flattened | 92.0% (23/25) | 2 | 5m57s |
| Qwen3.5-9B | **structural** | 92.0% (23/25) | 2 | 8m25s |

**Structure buys the small model +12 points of recall and buys the large model nothing.** The
9B has no headroom to gain: it scored 100% recall on the STORED versions of these same rows,
and the 2 false-drops here are pages that changed or expired between labelling and the
re-fetch — page drift, not the renderer, and it applies to both arms equally. That also puts
the achievable ceiling on this set at 23/25, so the 3B went from 11 to 14 of a possible 23:
the rendering closes about a quarter of the gap between the two models. It does not rescue a
small model; it meaningfully narrows the distance.

The cost is the mirror image. Structural is **+41% wall time on the 9B** and +17% on the 3B —
the ~1.09x prompt tokens above, amplified by generation. On a model with no headroom that is
paid for nothing.

So the default is **conditional, not absolute**:

- **Serving a large model** — keep the switch `false`. Measured: no recall gain, +41% latency.
- **Serving a small local model to cut the extract bill** — turn it `true`. It recovers a
  meaningful slice of exactly what a small model loses, and the extract bill is the reason to
  be running one.

This is one model pair, one page sample, **n=25**, and the 3B effect is 3 rows (a confidence
interval of roughly ±20 points). It is a signal strong enough to condition a flag on, not an
established effect size. A harvest under the flag (which stamps rows `structural-v2` and gives
`score-rendering`'s gold-set leg a real second arm) is still what would settle it at scale.

## Consequences

- Rendering doubles the parser's DOM walk on **every** crawled page, discovery included. It
  sits behind a kill switch defaulted to today's plain text, so production pays nothing until
  a harvest or an A/B deliberately turns it on.
- The right setting of that switch **depends on the model being served** (#289): worth turning
  on for a small local model, worth leaving off for a large one. Whoever changes `LLM_MODEL`
  owns re-deciding it, and `extractbench -fixtures ... -structural` is how.
- The renderer's version is stamped on each extract-capture record. A renderer change then
  shows up as rows produced by two renderers rather than silently mixing them, which matters
  because a Structural Rendering is a derived artefact the Extract Gold Set will store.

  What #281 built: `parser.RendererFlattened` / `parser.RendererStructural`, read off the
  parser INSTANCE through `RendererID()` so the stamp cannot drift from the bytes it
  describes, written as the record's `renderer` key beside `url`, `verdict` and `ts`. A sink
  that cannot name its renderer is refused at construction rather than writing rows nobody can
  attribute later. The bump is mechanical, not remembered:
  `TestStructuralRendererFingerprint` hashes the rendering of all 90 committed fixtures and
  fails until both the hash and the version recorded beside it have moved. On the gold set's
  own `goldRow` the field is `omitempty`, so the 457 rows drawn before the stamp keep reading
  as an *unknown* renderer — the population below, and ADR-0047's subject — rather than
  acquiring an empty one and rewriting 6.3 MB of substrate.
- The Extract Gold Set inherits it for free: the capture tap already serializes the parsed
  content, so rows drawn after this carry their own Structural Rendering with no side store,
  no digest join, and no new file format. Raw HTML was considered for that job and rejected —
  at 233 KB per page against 15 KB today it multiplies the durable llmstream payload ~15x
  (5000 backlog entries: 75 MB to 1.24 GB) and forces gzipped sidecar files on the committed
  gold set, for an archival property the round-trip test makes unnecessary.
- Rows captured BEFORE this change keep only flattened text. They are unreachable by any
  renderer and are the subject of ADR-0047.
- The rendering is scored against the flattening rather than argued about, by
  `llmbench score-rendering` (#282). The Extract Gate is invariant to the rendering BY
  CONSTRUCTION — every page-text input it reads goes through `crawler.FlattenedText`, whose
  round-trip this ADR already pins — so the verb's gate half is a neutrality demonstration at
  a longer seam than the parser's golden file covers, and it holds: **0 flips over 116
  committed pages**, with each arm's extract-call rate and false-drop count identical
  (0.3846 / 0 drops on the labelled fixtures, 0.1111 on the real pages, 0.3939 / 13 drops on
  the gold set). The verb therefore exits non-zero on a DISAGREEMENT between the arms and
  never on their level: the absolute false-drop count is `extract`'s and `score-capture`'s to
  guard, and a verb that inherited that rule would be red on day one over the 13 drops
  `extractGoldSetFalseDrops` already tolerates, hiding the A/B's own finding underneath it.
