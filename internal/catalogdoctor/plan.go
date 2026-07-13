package catalogdoctor

import (
	"sort"
	"strings"

	"github.com/google/uuid"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/catalog"
	"github.com/nicholasbraun/job-crawler-poc/internal/pagegate"
)

// candidate is a keep-candidate Career Page carried from the reject-classification
// pass into the merge/re-attribute pass: it stashes the identity, the final owner
// key, and the canonical stored URL so Pass 2 need not recompute them.
type candidate struct {
	page      *crawler.CareerPage
	targetKey string // catalog.Identity.CompanyKey: the final owner key
	finalURL  string // canonical stored URL, mirroring the write path
	identity  catalog.Identity
}

// Plan replays the current URL-structural rules over the stored Catalog and
// returns a Result of per-page dispositions plus the Companies left orphaned. It
// is a pure function over the two lists -- no context, no I/O -- so it is fully
// table-testable and deterministic: an already-clean Catalog yields an all-Keep
// Pages slice with no Orphans. It reuses the posting-path predicate (#63), the
// identity and Aggregator checks (#64), and the canonicaliser (#65); it invents
// no rule logic of its own.
func Plan(pages []*crawler.CareerPage, companies []*crawler.Company) Result {
	byID := map[uuid.UUID]*crawler.Company{}
	byKey := map[string]*crawler.Company{}
	for _, c := range companies {
		byID[c.ID] = c
		byKey[c.CompanyKey] = c
	}

	// Pass 1 -- per-page reject classification, mirroring the Discovery Gate's
	// reject order. Rejected pages get an immediate disposition; survivors become
	// keep-candidates for Pass 2.
	decided := []PageDisposition{}
	candidates := []candidate{}
	for _, page := range pages {
		u, err := crawler.NewURL(page.URL)
		if err != nil {
			// An unparseable URL is not URL-decidable, so the Doctor leaves it
			// (ADR-0011 bounds cleanup to URL-decidable errors).
			decided = append(decided, PageDisposition{Page: page, Action: Keep, Reason: "unparseable url"})
			continue
		}
		if catalog.IsAggregatorHost(u) {
			decided = append(decided, PageDisposition{Page: page, Action: Delete, Reason: "aggregator host"})
			continue
		}
		switch catalog.Classify(u) {
		case catalog.RoleJobListing:
			decided = append(decided, PageDisposition{Page: page, Action: Delete, Reason: "ats job listing"})
			continue
		case catalog.RoleCareerPage:
			// Structurally an ATS board root -- a keep-candidate.
		default: // catalog.RoleUnknown
			if pagegate.IsPostingPath(u) {
				decided = append(decided, PageDisposition{Page: page, Action: Delete, Reason: "single posting or deep career sub-page"})
				continue
			}
		}
		id := catalog.Identify(u)
		candidates = append(candidates, candidate{
			page:      page,
			targetKey: id.CompanyKey,
			finalURL:  catalog.CanonicalURL(careerURLFor(u)),
			identity:  id,
		})
	}

	// Pass 2 -- merge canonical duplicates and re-attribute mis-owned survivors.
	// Group candidates by (final owner key, canonical URL) so trivially-equivalent
	// rows collapse onto one survivor; process groups in a stable order.
	groups := map[string][]candidate{}
	for _, c := range candidates {
		key := c.targetKey + "\x00" + c.finalURL
		groups[key] = append(groups[key], c)
	}
	groupKeys := make([]string, 0, len(groups))
	for k := range groups {
		groupKeys = append(groupKeys, k)
	}
	sort.Strings(groupKeys)

	pages2 := []PageDisposition{}
	for _, gk := range groupKeys {
		group := groups[gk]
		survivor := pickSurvivor(group, byID)

		owner := byID[survivor.page.CompanyID]
		if owner != nil && owner.CompanyKey == survivor.targetKey {
			pages2 = append(pages2, PageDisposition{Page: survivor.page, Action: Keep, Reason: "valid career page"})
		} else {
			ownerKey := ""
			if owner != nil {
				ownerKey = owner.CompanyKey
			}
			target := byKey[survivor.targetKey]
			if target == nil {
				target = deriveCompany(survivor.identity)
			}
			pages2 = append(pages2, PageDisposition{
				Page:   survivor.page,
				Action: Reattribute,
				Reason: "identity " + ownerKey + " -> " + survivor.targetKey,
				Target: target,
			})
		}

		// Every other row in the group canonicalises onto the survivor: merge it.
		losers := make([]candidate, 0, len(group)-1)
		for _, c := range group {
			if c.page.ID != survivor.page.ID {
				losers = append(losers, c)
			}
		}
		sort.Slice(losers, func(i, j int) bool {
			return losers[i].page.ID.String() < losers[j].page.ID.String()
		})
		for _, c := range losers {
			pages2 = append(pages2, PageDisposition{
				Page:      c.page,
				Action:    Merge,
				Reason:    "duplicate of " + survivor.finalURL,
				MergeInto: survivor.page.ID,
			})
		}
	}

	result := Result{Pages: append(decided, pages2...)}

	// Pass 3 -- orphan sweep. A Company survives iff at least one input page ends
	// up attributed to it: a Keep leaves the page on its current owner, and a
	// Reattribute to an existing Catalog Company (Target.ID set) gives that
	// Company a page. Delete, Merge, and re-attributions to newly-derived
	// Companies contribute nothing.
	surviving := map[uuid.UUID]bool{}
	for _, d := range result.Pages {
		switch d.Action {
		case Keep:
			surviving[d.Page.CompanyID] = true
		case Reattribute:
			if d.Target != nil && d.Target.ID != uuid.Nil {
				surviving[d.Target.ID] = true
			}
		}
	}
	for _, c := range companies {
		if !surviving[c.ID] {
			result.Orphans = append(result.Orphans, c)
		}
	}

	return result
}

// pickSurvivor chooses the one row a duplicate group collapses onto, preferring
// (in order) a row already clean and correctly owned, then one already owned by
// the target key (avoiding a needless re-attribute), then one already canonical,
// and finally the lexicographically smallest id (stable and deterministic).
func pickSurvivor(group []candidate, byID map[uuid.UUID]*crawler.Company) candidate {
	best := group[0]
	for _, c := range group[1:] {
		if survivorRank(c, byID) < survivorRank(best, byID) {
			best = c
			continue
		}
		if survivorRank(c, byID) == survivorRank(best, byID) &&
			c.page.ID.String() < best.page.ID.String() {
			best = c
		}
	}
	return best
}

// survivorRank scores a candidate for pickSurvivor; lower is preferred.
func survivorRank(c candidate, byID map[uuid.UUID]*crawler.Company) int {
	owner := byID[c.page.CompanyID]
	owned := owner != nil && owner.CompanyKey == c.targetKey
	canonical := c.page.URL == c.finalURL
	switch {
	case owned && canonical:
		return 0
	case owned:
		return 1
	case canonical:
		return 2
	default:
		return 3
	}
}

// careerURLFor returns the pre-canonical stored URL for u, mirroring the
// career-page processor's write path exactly: an ATS tenant URL collapses to its
// board root, and a self-hosted page keeps its own (already-normalized) URL. The
// caller then applies catalog.CanonicalURL, so this reproduces the identical
// two-step the crawler used when the row was first written.
func careerURLFor(u crawler.URL) string {
	if canonical, ok := catalog.CareerPageURL(u); ok {
		return canonical
	}
	return u.RawURL
}

// deriveCompany builds the new per-tenant Company a re-attribution moves to when
// the destination is not yet in the Catalog. It is URL-only -- the honest
// content-less analogue of the processor's name/domain fallbacks, since the
// Doctor has no page content: an ATS tenant takes the slug as its name, a
// self-hosted host takes its registrable domain. ID is left zero so Apply
// materialises the row and learns its generated id.
func deriveCompany(id catalog.Identity) *crawler.Company {
	if id.ATSProvider != "" {
		_, slug, _ := strings.Cut(id.CompanyKey, ":")
		return &crawler.Company{CompanyKey: id.CompanyKey, ATSProvider: id.ATSProvider, Name: slug}
	}
	return &crawler.Company{CompanyKey: id.CompanyKey, DisplayDomain: id.CompanyKey, Name: id.CompanyKey}
}
