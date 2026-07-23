package collection

import (
	"net/url"
	"strings"

	"github.com/google/uuid"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
)

// NewAttributor builds a per-run Owner → Career Page best-match closure the
// job-listing processor uses to stamp a crawled posting's career_page_id
// (ADR-0035). It groups pages by their owning Company's CompanyKey (via
// companyKeyByID), preserving the input order — pass careerPageRepository.List's
// most-recently-seen-first order so the fallback is the freshest page.
//
// The returned closure attributes a posting (companyKey + posting URL) to the
// Company's Career Page whose scheme+host+path is the longest prefix of the posting
// URL; when none is a prefix it falls back to the Company's most-recently-seen page;
// a Company with no page yields uuid.Nil. For the common single-page Company it is
// always that one page. The rule is deterministic so a posting attributes to the
// same page across Cycles.
func NewAttributor(pages []*crawler.CareerPage, companyKeyByID map[uuid.UUID]string) func(companyKey, postingURL string) uuid.UUID {
	byCompany := map[string][]*crawler.CareerPage{}
	for _, p := range pages {
		key := companyKeyByID[p.CompanyID]
		if key == "" {
			continue
		}
		byCompany[key] = append(byCompany[key], p)
	}

	return func(companyKey, postingURL string) uuid.UUID {
		candidates := byCompany[companyKey]
		if len(candidates) == 0 {
			return uuid.Nil
		}
		posting := prefixKey(postingURL)
		// Fallback: the most-recently-seen page (candidates[0], since List is desc).
		best := candidates[0]
		bestLen := -1
		for _, p := range candidates {
			pk := prefixKey(p.URL)
			if strings.HasPrefix(posting, pk) && len(pk) > bestLen {
				best = p
				bestLen = len(pk)
			}
		}
		return best.ID
	}
}

// prefixKey reduces a URL to the scheme+host+path used for longest-prefix matching,
// dropping the query and fragment so a posting's tracking params never defeat the
// match. An unparseable URL is returned as-is (it simply won't prefix-match).
func prefixKey(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	u.RawQuery = ""
	u.ForceQuery = false
	u.Fragment = ""
	u.RawFragment = ""
	return u.String()
}
