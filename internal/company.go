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

// CompanyMerge is one import line's Company rendered as a merge instruction
// (ADR-0013). Unlike a Company, each mutable field pairs its value with a
// Present flag carrying JSON-presence semantics: a presence-wins merge writes a
// field only when its Present flag is set, so a sparse hand-written record never
// blanks catalogued data it did not mention. FirstSeen/LastSeen are nil when the
// file omitted them (defaulting to now() on first insert only). ID is an output:
// MergeImport writes the merged row's id into it.
type CompanyMerge struct {
	ID                   uuid.UUID
	CompanyKey           string
	ATSProvider          string // "" is a definite value (self-hosted); stored as NULL
	ATSProviderPresent   bool
	DisplayDomain        string
	DisplayDomainPresent bool
	Name                 string
	NamePresent          bool
	FirstSeen            *time.Time // nil = absent; first_seen = LEAST(existing, this)
	LastSeen             *time.Time // nil = absent; last_seen = GREATEST(existing, this)
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

	// MergeImport lands an imported Company keyed on CompanyKey (ADR-0013). It is
	// deliberately not a Sighting: it never stamps last_seen = now() the way
	// Upsert does. Timestamps merge monotonically (first_seen = LEAST(existing,
	// file), last_seen = GREATEST(existing, file), each honoring an absent file
	// value as "leave unchanged"); on first insert an absent timestamp defaults to
	// now(). Each mutable field is written only when its Present flag is set (an
	// explicit empty ATSProvider sets self-hosted). It writes the merged row's id
	// into m.ID. Re-merging the same instruction changes no data.
	MergeImport(ctx context.Context, m *CompanyMerge) error
}
