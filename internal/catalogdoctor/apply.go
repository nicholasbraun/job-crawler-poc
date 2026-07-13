package catalogdoctor

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
)

// Apply executes a Result against store. Ordering is dictated by the FK
// (career_page.company_id has no cascade): re-attribution targets are
// materialised and pages moved before any Company is deleted, and pages are
// removed before their now-orphaned Company is swept. An all-Keep Result with no
// Orphans issues zero mutations, so a second pass over an already-clean Catalog
// is a no-op -- idempotency by construction.
func Apply(ctx context.Context, store Store, result Result) error {
	// created caches the id of each newly-materialised re-attribution target so a
	// target shared by several pages is upserted exactly once.
	created := map[string]uuid.UUID{}

	for _, d := range result.Pages {
		if d.Action != Reattribute {
			continue
		}
		targetID, err := ensureTarget(ctx, store, d.Target, created)
		if err != nil {
			return err
		}
		if err := store.ReattributeCareerPage(ctx, d.Page.ID, targetID); err != nil {
			return fmt.Errorf("catalogdoctor: re-attribute career page %s: %w", d.Page.URL, err)
		}
	}

	for _, d := range result.Pages {
		if d.Action != Delete && d.Action != Merge {
			continue
		}
		if err := store.DeleteCareerPage(ctx, d.Page.ID); err != nil {
			return fmt.Errorf("catalogdoctor: delete career page %s: %w", d.Page.URL, err)
		}
	}

	for _, c := range result.Orphans {
		if err := store.DeleteCompany(ctx, c.ID); err != nil {
			return fmt.Errorf("catalogdoctor: delete orphan company %s: %w", c.CompanyKey, err)
		}
	}

	return nil
}

// ensureTarget resolves the owning-Company id a re-attribution points at: an
// existing Catalog Company by its known id, a target already materialised in
// this pass from the cache, or a freshly upserted Company (whose generated id is
// then cached).
func ensureTarget(ctx context.Context, store Store, target *crawler.Company, created map[string]uuid.UUID) (uuid.UUID, error) {
	if target.ID != uuid.Nil {
		return target.ID, nil
	}
	if id, ok := created[target.CompanyKey]; ok {
		return id, nil
	}
	if err := store.UpsertCompany(ctx, target); err != nil {
		return uuid.Nil, fmt.Errorf("catalogdoctor: upsert re-attribution target %s: %w", target.CompanyKey, err)
	}
	created[target.CompanyKey] = target.ID
	return target.ID, nil
}
