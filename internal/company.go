package crawler

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Company is a durable Catalog entry identifying an employer whose Career Pages
// the crawler has discovered. Identity is ATS-aware (ADR-0001): CompanyKey is
// globally unique and provider-qualified ("greenhouse:acme", or the eTLD+1
// "acme.com" for a self-hosted page) so distinct tenants sharing an ATS host
// are never collapsed into one company. Used as a pointer type.
type Company struct {
	ID uuid.UUID
	// CompanyKey is the globally-unique identity key. It is the natural key the
	// repository upserts on.
	CompanyKey string
	// ATSProvider is the ATS host family ("greenhouse", "lever", …) or "" for a
	// self-hosted career page. Stored separately so it is queryable.
	ATSProvider string
	// DisplayDomain is the human-facing host for the company.
	DisplayDomain string
	Name          string
	FirstSeen     time.Time
	LastSeen      time.Time
}

// CompanyRepository persists Companies in the Catalog.
type CompanyRepository interface {
	// Upsert inserts c or, when a row with the same CompanyKey already exists,
	// refreshes its mutable fields and advances last_seen while preserving
	// first_seen. It writes the row's id back into c.ID.
	Upsert(ctx context.Context, c *Company) error

	// List returns every catalogued Company, most-recently-seen first. It never
	// returns nil; an empty Catalog yields an empty slice.
	List(ctx context.Context) ([]*Company, error)
}
