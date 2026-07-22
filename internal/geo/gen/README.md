# geo/gen — Country Resolver gazetteer generator

This build tool generates `internal/geo/gazetteer.tsv`, the lookup table backing
the deterministic Country Resolver (ADR-0029, ADR-0031). It replaces hand-typed
tables with a table derived from published reference data, while keeping the
matcher and the keep-on-doubt safety policy hand-owned in `internal/geo`.

## Regenerate

```bash
go generate ./internal/geo/gen
```

Regeneration is **offline and deterministic**: the reference files are embedded
(`//go:embed`), the dominance policy is applied in `build`, and the output is
sorted, so re-running produces a byte-identical artifact (an empty `git diff`).
The `go:generate` directive lives at the top of `main.go` and writes the artifact
to `../gazetteer.tsv` (i.e. `internal/geo/gazetteer.tsv`).

## Pinned snapshot

The reference files under `data/` are a **pinned snapshot** — GeoNames dumps
carry no version, so the pin is the download date:

| File | Source |
| --- | --- |
| `data/cities5000.txt` | https://download.geonames.org/export/dump/cities5000.zip |
| `data/countryInfo.txt` | https://download.geonames.org/export/dump/countryInfo.txt |
| `data/admin1CodesASCII.txt` | https://download.geonames.org/export/dump/admin1CodesASCII.txt |

**Pinned snapshot date: 2026-07-22.**

Updating the gazetteer is a deliberate act: re-download the three files, commit
them alongside a bumped `pinnedSnapshotDate` in `write.go`, regenerate, and
review the sorted diff. The build never downloads at run time.

## License / attribution

The GeoNames data is licensed **CC BY 4.0, © GeoNames (https://www.geonames.org/)**.
Attribution is also stamped into the generated artifact's header. ISO 3166-1
alpha-2 codes are used as identifiers.

## What the generator does

- **Country layer** — ISO 3166 country names (`countryInfo`) plus a small hand
  synonym supplement (`USA`, `UK`, `Deutschland`, `Österreich`, …). Demonyms are
  absent by construction. Codes ISO no longer assigns (`AN`, `CS`, `XK`) are
  dropped so every emitted code is valid.
- **State layer** — US two-letter state codes, admitted only when they collide
  with neither a foreign ISO country code nor a small English-word stoplist
  (`or`/`hi`/`ok`/`oh`), plus full state names. A subdivision name equal to a
  country name (`Georgia`) is kept in the country layer, not the state layer.
- **City layer** — GeoNames populated places, disambiguated by the keep-on-doubt
  dominance policy: a name in one country is assigned to it (no floor); a name in
  several is assigned only when the top country clears `FLOOR = 100k` and beats
  the runner-up by `RATIO = 8×`, else dropped to the empty Country.

Precedence `country > state > city` is baked into the data: each key is emitted
in the highest layer it qualifies for and suppressed from every lower one.
