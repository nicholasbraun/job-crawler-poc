# A confidence-ordered Name Ladder derives Company names, reusing the classify call and recording provenance

## Context

A Company's display name was read off the page `<title>` by a boilerplate-stripping heuristic (last extended in 01ae215). The heuristic is a treadmill — every new junk pattern needs another word — and, worse, it emits a bare-title guess (`Zulieferern`, `Karriere an der Ohm`) with the same authority as a name declared in structured data, giving the Catalog no signal of which names to trust. The deeper flaw is that nothing deterministic can tell a real bare-title name (`Remote`) from a junk fragment (`Zulieferern`).

## Decision

Derive the Company name through an ordered **Name Ladder**, higher-trust sources first, and stamp each name with the rung that produced it (its **Name Source**):

1. a JSON-LD organization on the page,
2. the page's own site metadata (`og:site_name`), gated to self-hosted Companies,
3. the name returned by the LLM classify step,
4. a name parsed from the `<title>` **only** when it carries a structural cue (a connector / separator / suffix); a bare title abstains,
5. the Company's bare identity — tenant slug or registrable domain.

The LLM name is not a new call: the classify step that already confirms every uncertain candidate (ADR-0007) is widened to return `{is_career_page, company_name}` in one round-trip, `company_name` null unless the page unambiguously names its employer.

## Considered options

- **More heuristic rules (status quo).** Rejected — a treadmill, and it cannot distinguish a real bare-title name from a junk fragment; no confidence signal.
- **A dedicated LLM name-extractor call.** Rejected — a second LLM call per page for a signal the classify call yields for a few output tokens.
- **Delete the title heuristic entirely (structured → LLM → identity).** Rejected — a *certain* accept (an ATS board root, ADR-0016) skips the LLM, so it would fall from a clean title name to a bare slug. Demoting the heuristic to a structural-cue-only net *below* the LLM keeps those names while removing the treadmill pressure, since a bare fragment now abstains to the domain.
- **`og:site_name` for every page.** Rejected — on an ATS board the site metadata is usually the ATS's brand, not the employer; the metadata rung is gated to self-hosted Companies (where it is the employer's own brand).
- **Persist provenance in the exchange format.** Deferred — Name Source is nullable, backfilled only by re-crawl, and left out of Catalog Import/Export in v1 (ADR-0015); an imported name's provenance is a future value.

## Consequences

- The classify prompt now does two jobs, so its Career-Page accuracy is guarded by the classifier benchmark (ADR-0008) as a hard merge gate. If the added field regresses classification, the name extraction splits back out into its own call — the ladder is unchanged, only rung 3's source moves.
- A name from the LLM or the title is page-controlled and unverified, so it is a **display-only** label: it never influences Company identity, which stays ATS-aware (ADR-0001), and a hostile page can at worst plant a flagged string. The dashboard reports the verified share and lets unverified or nameless entries be audited.
- When every rung abstains, the stored name equals the Company's domain — honest and marked as a fallback, rather than a plausible-looking fragment.
