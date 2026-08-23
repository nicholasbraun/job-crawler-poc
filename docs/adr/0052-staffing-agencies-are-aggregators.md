# Staffing agencies are Aggregators

The Aggregator denylist (`aggregatorHosts`, `internal/catalog/identity.go`) was built for
hosts that carry **somebody else's** postings and name that somebody on the page: a job
board, a VC-portfolio board, a professional network. Every entry is added under one
standard — confirmed by the *employer named on the page*, never by host name.

A staffing agency fails that standard while producing the same damage. Measured on the live
Corpus (2026-08-23), `gigroup.com` alone holds **701 open Job Listings across 20 country
subdomains and 351 distinct locations, all collapsed onto one Company, "Gi Group"** — the
exact Catalog shape the denylist exists to stop. But the pages name no third-party
employer. They say the opposite:

> *We are currently recruiting for a Butcher, **for our client** in Leighton Buzzard.*

So the two obvious readings both fail. Attributing the posting to Gi Group is not *wrong*
— in temp work (Arbeitnehmerüberlassung) the agency really is the employer of record — so
this is not mis-attribution the way `80000hours.org` was. And attributing it to the
workplace is impossible, because the page deliberately withholds it.

## The decision

Staffing and recruitment agencies go on the Aggregator denylist, and `CONTEXT.md`'s
**Aggregator** definition widens to name them.

The reason is a *second* qualifying shape, recorded here because it is not the one the
list's comment describes and a future reader will otherwise judge these entries against
the wrong standard:

- **The classic shape** — the page names an employer that is not the host. The Company we
  mint is a fake; every listing is mis-attributed by construction.
- **This shape** — the page names *no* employer beyond the agency, and one host absorbs an
  unbounded stream of client roles across every industry, country, and location. The
  Company we mint is real, and useless: it answers "who is hiring?" with the name of a
  middleman, for 701 jobs at 700 different workplaces.

What unites them is the Catalog invariant, not the attribution error: **a Career Page
belongs to one Company, and the openings on it are that Company's own.** An agency board
breaks that invariant as thoroughly as a job board does, so it earns the same reject.

## Consequences

The reject is *certain*, so this retroactively closes what these hosts already minted —
776 open Job Listings, 18 Catalog pages, 7 Companies. As with every eTLD+1 entry, it also
sheds the agencies' own internal openings (Gi Group hiring its own recruiters). That is the
accepted trade, and here it costs less than usual: those roles are a rounding error against
the 701.

The line this draws is **the agency, not the client relationship**. A consultancy that
staffs its own employees onto client projects (Deloitte, Materna, Avenga) is a normal
employer and stays in the Catalog, even though its postings also say "our clients" — a
text search for that phrase is not the test. The test is whether the host's openings are
its own. Talent marketplaces (`toptal.com`, `work.mercor.com`, `preply.com`) sit on the
same fault line and are deliberately **not** decided here; they are left in until someone
looks at what their pages actually say.
