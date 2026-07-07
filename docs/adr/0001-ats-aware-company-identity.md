# ATS-aware Company identity

Discovery finds Career Pages both self-hosted (`acme.com/careers`) and on shared ATS
platforms (`boards.greenhouse.io/acmecorp`, `{tenant}.recruitee.com`,
`jobs.lever.co/{tenant}`). Keying a **Company** by registrable domain (eTLD+1) would
collapse every ATS tenant into one fake company (all Greenhouse tenants → `greenhouse.io`),
and ATS boards are our highest-yield discovery source. So a Company is identified by an
ATS-aware rule: a known ATS host → the tenant slug from its subdomain/path; otherwise
eTLD+1. The Frontier separately rate-limits by **Politeness Domain** (the host), which is
deliberately distinct — many Companies share one Politeness Domain.

## Consequences

We maintain a small registry of ATS host patterns (greenhouse, lever, ashby, recruitee,
personio, workable, …). The alternative — pure eTLD+1 — is simpler but produces a broken
Catalog for exactly the sources we rely on most.
