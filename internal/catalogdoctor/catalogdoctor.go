// Package catalogdoctor is the Catalog Doctor (ADR-0011): an idempotent
// maintenance pass that replays the current URL-structural rules over the
// already-stored Catalog and repairs the rows the rules now reject. Plan is a
// pure function over the listed Career Pages and Companies that returns a
// Result of per-page dispositions (keep / delete / re-attribute / merge) plus
// the Companies left orphaned; Apply executes that Result against a narrow
// Store port the package defines, so the rules run identically whether driven
// by the cmd/doctor CLI or a future dashboard report button. The Catalog stores
// no page content, so the Doctor corrects only URL-decidable errors -- it reuses
// pagegate.IsPostingPath (#63), catalog.Identify/Classify/IsAggregatorHost
// (#64), and catalog.CanonicalURL/CareerPageURL (#65) and adds no rule logic of
// its own.
package catalogdoctor

import (
	"context"

	"github.com/google/uuid"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
)

// Action is the disposition the Catalog Doctor assigns to a Career Page.
type Action int

const (
	Keep        Action = iota // row is valid under the current rules; leave it
	Delete                    // hard-delete: single posting, Aggregator, or ATS Job Listing
	Reattribute               // identity changed (e.g. the join.com split): move to Target
	Merge                     // canonicalises onto another kept row; delete it, keep MergeInto
)

// String returns the lowercase action name used in the dry-run report.
func (a Action) String() string {
	switch a {
	case Keep:
		return "keep"
	case Delete:
		return "delete"
	case Reattribute:
		return "reattribute"
	case Merge:
		return "merge"
	default:
		return "unknown"
	}
}

// PageDisposition is the Catalog Doctor's decision for one stored Career Page.
type PageDisposition struct {
	Page   *crawler.CareerPage
	Action Action
	// Reason is a short human-readable rationale for the dry-run report.
	Reason string
	// Target is the Company to move Page to; set only when Action is Reattribute.
	// Its ID is non-zero when the destination Company already exists in the
	// Catalog, and zero when it is a new per-tenant Company that Apply must
	// create (the join.com split).
	Target *crawler.Company
	// MergeInto is the surviving Career Page's ID; set only when Action is Merge.
	MergeInto uuid.UUID
}

// Result is a full Catalog Doctor plan over the Catalog.
type Result struct {
	// Pages holds one disposition per input Career Page, so an all-Keep Pages
	// slice with empty Orphans is an already-clean, idempotent Catalog.
	Pages []PageDisposition
	// Orphans are the Companies left owning no Career Pages once Pages is
	// applied; Apply deletes them after their pages are moved or removed.
	Orphans []*crawler.Company
}

// Store is the narrow, Postgres-independent persistence port the Catalog Doctor
// executes a Result against. Method names are distinct (not the repositories'
// bare Delete/List) so a single adapter can satisfy it over the separate
// Company and CareerPage repositories.
type Store interface {
	// UpsertCompany inserts or updates c keyed on CompanyKey and writes the row
	// id back into c.ID, materialising a re-attribution Target so its id is
	// available for ReattributeCareerPage.
	UpsertCompany(ctx context.Context, c *crawler.Company) error
	// DeleteCompany removes a Company. Deleting one that still owns Career Pages
	// is rejected by the FK; the Doctor always removes/moves its pages first.
	DeleteCompany(ctx context.Context, id uuid.UUID) error
	// DeleteCareerPage removes a Career Page; a missing id is a no-op.
	DeleteCareerPage(ctx context.Context, id uuid.UUID) error
	// ReattributeCareerPage re-points a Career Page's owning Company; a missing
	// id is a no-op.
	ReattributeCareerPage(ctx context.Context, id, companyID uuid.UUID) error
}
