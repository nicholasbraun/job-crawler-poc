package postgres

import (
	"context"

	"github.com/google/uuid"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/catalogdoctor"
)

// CatalogDoctorStore adapts the separate Company and Career Page repositories to
// the catalogdoctor.Store port (ADR-0011), so the Catalog Doctor's Apply pass can
// mutate the Postgres Catalog through one narrow, importable seam. It is the
// production Store both the cmd/doctor CLI and the end-to-end test drive, so the
// re-attribution/deletion/orphan-sweep ordering is exercised against real
// repositories rather than a hand-rolled copy.
type CatalogDoctorStore struct {
	companies *CompanyRepository
	pages     *CareerPageRepository
}

var _ catalogdoctor.Store = (*CatalogDoctorStore)(nil)

// NewCatalogDoctorStore wires the Company and Career Page repositories into a
// Store the Catalog Doctor can execute a plan against.
func NewCatalogDoctorStore(companies *CompanyRepository, pages *CareerPageRepository) *CatalogDoctorStore {
	return &CatalogDoctorStore{companies: companies, pages: pages}
}

// UpsertCompany materialises a re-attribution target, writing the row id back
// into c.ID so a freshly-created per-tenant Company is available to
// ReattributeCareerPage.
func (s *CatalogDoctorStore) UpsertCompany(ctx context.Context, c *crawler.Company) error {
	return s.companies.Upsert(ctx, c)
}

// DeleteCompany sweeps an orphaned Company. The Doctor always removes or moves a
// Company's Career Pages first, so the career_page.company_id FK (no cascade)
// does not reject the delete.
func (s *CatalogDoctorStore) DeleteCompany(ctx context.Context, id uuid.UUID) error {
	return s.companies.Delete(ctx, id)
}

// DeleteCareerPage removes a rejected or merged-away Career Page; a missing id is
// a no-op, keeping a repeated pass idempotent.
func (s *CatalogDoctorStore) DeleteCareerPage(ctx context.Context, id uuid.UUID) error {
	return s.pages.Delete(ctx, id)
}

// ReattributeCareerPage re-points a mis-attributed Career Page (the join.com
// split); a missing id is a no-op.
func (s *CatalogDoctorStore) ReattributeCareerPage(ctx context.Context, id, companyID uuid.UUID) error {
	return s.pages.Reattribute(ctx, id, companyID)
}
