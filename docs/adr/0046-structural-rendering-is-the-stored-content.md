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
contributed the word, `[button: Senden]` must strip to `Senden`, an `![alt]` must vanish
because today's text has no alt text — and the test is what proves it holds. **If it cannot be
held, the fallback is #275's two-field design**, taken on evidence rather than on caution.

## The prompt and the human read different variants

Rendering cost was measured over 36 live pages: median **1.43x** the flattened text with
hrefs, against #275's measured 1.10x without them. `LLM_EXTRACT_MAX_CHARS` is 8000, so at an
unchanged cap the href-carrying form would show the model ~30% less page than today — "keep
the tags" and "raise the cap" compound rather than compose. The extractor prompt therefore
reads the href-free variant; the labelling UI reads the full one, because `/organization/…`
versus `/jobs/…` is exactly what settles a page like `neckarfilsjobs.de`.

## Consequences

- Rendering doubles the parser's DOM walk on **every** crawled page, discovery included. It
  sits behind a kill switch defaulted to today's plain text, so production pays nothing until
  a harvest or an A/B deliberately turns it on.
- The renderer's version is stamped on each extract-capture record. A renderer change then
  shows up as rows produced by two renderers rather than silently mixing them, which matters
  because a Structural Rendering is a derived artefact the Extract Gold Set will store.
- The Extract Gold Set inherits it for free: the capture tap already serializes the parsed
  content, so rows drawn after this carry their own Structural Rendering with no side store,
  no digest join, and no new file format. Raw HTML was considered for that job and rejected —
  at 233 KB per page against 15 KB today it multiplies the durable llmstream payload ~15x
  (5000 backlog entries: 75 MB to 1.24 GB) and forces gzipped sidecar files on the committed
  gold set, for an archival property the round-trip test makes unnecessary.
- Rows captured BEFORE this change keep only flattened text. They are unreachable by any
  renderer and are the subject of ADR-0047.
