# A Work Arrangement enum replaces the Remote boolean

## Context

The Job Listing carried a `Remote bool`. Users want richer working-mode tags —
remote, in-office, hybrid, or nothing when the source is silent — and the location
constraint's remote-override (ADR-0028) needs a trustworthy remote signal. A boolean
cannot express onsite-vs-hybrid-vs-unknown: it collapses all three of them into
`false`.

## Decision

Replace `Remote bool` with a **Work Arrangement** enum on Job Listing:
`remote` / `onsite` / `hybrid` / `unspecified`. A source that does not *positively*
state the mode maps to `unspecified`, never `onsite` — a bare "not remote"
(SmartRecruiters' `remote:false`) or a silent provider is not an on-site signal.
The crawl-lane LLM emits the enum (validated, off-enum → `unspecified`); the ATS
lane maps each provider's structured field, degrading to `unspecified` where the
provider says less. The migration backfills `remote:true → remote`,
`remote:false → unspecified`, then drops the `remote` column.

## Considered options / why replace rather than add

Keeping the boolean alongside a new enum would be a non-destructive, trivially
reversible migration — but it reintroduces two sources of truth that can drift
(`Remote=false, Arrangement=hybrid` — remote-eligible or not?). The enum strictly
subsumes the boolean (`remote == Arrangement=="remote"`), and providers already
parse the richer signal (Ashby `Remote/Hybrid/Onsite`, Workable `workplace_type`),
so a boolean actively discards data. Replacing is the consistent finish.

## Consequences

Dropping the column is a lossy, largely one-way migration: `remote:false` history
cannot be re-split into onsite/hybrid/unknown, so it backfills to `unspecified`.
Accepted for a single-deploy POC where re-crawls repopulate via upsert. The
conservative `unspecified` default keeps the displayed tag honest at the cost of
many ATS listings reading `unspecified` (several providers expose no arrangement).
`contentHash` swaps `Remote` for the enum, so every listing's hash changes once —
a benign one-time churn.
