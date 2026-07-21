# The Country Resolver's gazetteer is generated from published reference data

The gazetteer backing the deterministic Country Resolver (ADR-0029) is produced at
build time by a generator over GeoNames (`cities5000`) and ISO 3166 (`countryInfo`,
`admin1CodesASCII`), rather than hand-typed, so the resolver reads US and global
locations without a runtime geo dependency. The generated table is vendored as an
embedded, sorted `gazetteer.tsv` parsed once at init; the matcher and the safety
policy stay hand-owned in `internal/geo`.

## Considered options

- **A runtime geocoder** (`geobed`, `libpostal`, or a geocoding API). Rejected:
  `geobed` *guesses* by population — the opposite of our keep-on-doubt bias — and
  needs 512 MB in memory plus a runtime data download; `libpostal` is a cgo + multi-GB
  dependency that only *parses* components and hands the ambiguity back; geocoding APIs
  add per-listing network calls and non-determinism, and the ATS Fetch lane makes no
  third-party call by design (ADR-0022). None can own the disambiguation anyway: our
  keep-on-doubt, DACH-biased policy is non-standard. A dataset gives *data*, not
  *judgment* — so we take the data and keep the judgment.
- **Continued hand-curation** (the prior gazetteer). Rejected: partial by construction,
  toilsome, and the direct cause of the US blind spot that let US jobs leak into a DE
  crawl.

## Consequences

The generator bakes our safety policy into the data, so the table inherits
keep-on-doubt:

- A place name present in **one** country is assigned to it. A name present in
  **several** is assigned only when one country **dominates by population** (population
  floor 100k, ratio 8×); otherwise it is dropped to the empty Country, which the
  Country Constraint keeps (ADR-0028). Dropping a contested name under-filters (safe);
  guessing a dominant one risks false-dropping a minority-namesake listing (the cardinal
  sin), so the threshold is deliberately conservative.
- A two-letter US state code is admitted only when it collides with neither a foreign
  ISO country code nor an English word — computed from the ISO set plus a stoplist, so
  `DE`/`CA`/`IN` are excluded (they resolve via city or full state name) while
  `TX`/`NY`/`FL` are safe. Bare `US`, which folding would corrupt into the pronoun, is
  matched separately by a case- and delimiter-aware pass.
- A subdivision name equal to a country name (`Georgia`) is excluded from the state
  layer, preserving the country reading (`GE`).

Regeneration is a deliberate act against a **pinned** GeoNames snapshot committed
beside the generator, emitting a deterministic, sorted diff for review; the build
compiles the committed artifact and never downloads at run time. Coverage remains
deliberately partial, always failing in the safe under-filtering direction. The data
is CC BY 4.0 (GeoNames); attribution is stamped into the generated artifact.
