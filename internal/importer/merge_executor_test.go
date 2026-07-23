package importer_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/importer"
)

// fakeCatalogCompanyRepo records the Company merges NewMergeExecutor issues and
// can be told to fail. It assigns a stable id per CompanyKey so the executor can
// thread it into the page merges.
type fakeCatalogCompanyRepo struct {
	merged []crawler.CompanyMerge
	byKey  map[string]uuid.UUID
	err    error
}

func (f *fakeCatalogCompanyRepo) Upsert(context.Context, *crawler.Company) error   { return nil }
func (f *fakeCatalogCompanyRepo) List(context.Context) ([]*crawler.Company, error) { return nil, nil }
func (f *fakeCatalogCompanyRepo) ListPagelessSeeds(context.Context) ([]crawler.CatalogSeed, error) {
	return nil, nil
}
func (f *fakeCatalogCompanyRepo) MergeImport(ctx context.Context, m *crawler.CompanyMerge) error {
	if f.err != nil {
		return f.err
	}
	if f.byKey == nil {
		f.byKey = map[string]uuid.UUID{}
	}
	id, ok := f.byKey[m.CompanyKey]
	if !ok {
		id = uuid.New()
		f.byKey[m.CompanyKey] = id
	}
	m.ID = id
	f.merged = append(f.merged, *m)
	return nil
}

// fakeCatalogPageRepo records the Career Page merges and can be told to fail.
type fakeCatalogPageRepo struct {
	merged []crawler.CareerPageMerge
	err    error
}

func (f *fakeCatalogPageRepo) Upsert(context.Context, *crawler.CareerPage) error { return nil }
func (f *fakeCatalogPageRepo) ListCollectionSeeds(context.Context, int) ([]crawler.CollectionSeed, error) {
	return nil, nil
}
func (f *fakeCatalogPageRepo) RecordProbe(context.Context, uuid.UUID, crawler.ProbeOutcome, int) (crawler.DormancyResult, error) {
	return crawler.DormancyResult{}, nil
}
func (f *fakeCatalogPageRepo) List(context.Context) ([]*crawler.CareerPage, error) {
	return nil, nil
}
func (f *fakeCatalogPageRepo) FirstSeenByDay(context.Context) ([]crawler.DayCount, error) {
	return nil, nil
}
func (f *fakeCatalogPageRepo) MergeImport(ctx context.Context, m *crawler.CareerPageMerge) error {
	if f.err != nil {
		return f.err
	}
	f.merged = append(f.merged, *m)
	return nil
}

func TestMergeExecutorWritesCompaniesAndPages(t *testing.T) {
	companies := &fakeCatalogCompanyRepo{}
	pages := &fakeCatalogPageRepo{}
	exec := importer.NewMergeExecutor(companies, pages)

	// A trailing-slash page URL proves canonicalisation flows through the merge.
	payload := []byte(`{"companyKey":"acme.com","careerPages":[{"url":"https://acme.com/careers/"}]}` + "\n" +
		`{"companyKey":"globex.com","careerPages":[{"url":"https://globex.com/jobs"}]}`)

	res, err := exec(context.Background(), payload, false)
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if res.CompaniesUpserted != 2 {
		t.Errorf("companiesUpserted: got %d, want 2", res.CompaniesUpserted)
	}
	if res.PagesUpserted != 2 {
		t.Errorf("pagesUpserted: got %d, want 2", res.PagesUpserted)
	}
	if len(companies.merged) != 2 {
		t.Fatalf("company merges: got %d, want 2", len(companies.merged))
	}
	gotKeys := map[string]bool{}
	for _, c := range companies.merged {
		gotKeys[c.CompanyKey] = true
	}
	if !gotKeys["acme.com"] || !gotKeys["globex.com"] {
		t.Errorf("merged company keys: got %v, want acme.com and globex.com", gotKeys)
	}
	if len(pages.merged) != 2 {
		t.Fatalf("page merges: got %d, want 2", len(pages.merged))
	}
	// The trailing slash is stripped by canonicalisation, and politeness domain is
	// derived from the URL host — neither is carried verbatim from the file.
	acmePage := pages.merged[0]
	if acmePage.URL != "https://acme.com/careers" {
		t.Errorf("page URL should be canonical (no trailing slash), got %q", acmePage.URL)
	}
	if acmePage.PolitenessDomain != "acme.com" {
		t.Errorf("politeness domain should be the URL host acme.com, got %q", acmePage.PolitenessDomain)
	}
	// The page merge carries the company id its Company merge wrote back.
	if acmePage.CompanyID != companies.byKey["acme.com"] {
		t.Errorf("page company id: got %v, want %v", acmePage.CompanyID, companies.byKey["acme.com"])
	}
}

func TestMergeExecutorDryRunWritesNothing(t *testing.T) {
	companies := &fakeCatalogCompanyRepo{}
	pages := &fakeCatalogPageRepo{}
	exec := importer.NewMergeExecutor(companies, pages)

	payload := []byte(`{"companyKey":"acme.com","careerPages":[{"url":"https://acme.com/careers"}]}` + "\n" +
		`{"companyKey":"globex.com","careerPages":[{"url":"https://globex.com/jobs"},{"url":"https://globex.com/roles"}]}`)

	res, err := exec(context.Background(), payload, true)
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if len(companies.merged) != 0 || len(pages.merged) != 0 {
		t.Errorf("dry run must not write: %d companies, %d pages merged", len(companies.merged), len(pages.merged))
	}
	// Counters still report the would-upsert totals so the operator can preview.
	if res.CompaniesUpserted != 2 {
		t.Errorf("companiesUpserted: got %d, want 2", res.CompaniesUpserted)
	}
	if res.PagesUpserted != 3 {
		t.Errorf("pagesUpserted: got %d, want 3", res.PagesUpserted)
	}
}

func TestMergeExecutorSubLineBestEffortForKeyedRecord(t *testing.T) {
	companies := &fakeCatalogCompanyRepo{}
	pages := &fakeCatalogPageRepo{}
	exec := importer.NewMergeExecutor(companies, pages)

	// A keyed record: the company + its one valid page still land; the bad page
	// is collected as a sub-line error.
	payload := []byte(`{"companyKey":"acme.com","careerPages":[{"url":"https://acme.com/careers"},{"url":"not-a-url"}]}`)

	res, err := exec(context.Background(), payload, false)
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if res.CompaniesUpserted != 1 {
		t.Errorf("companiesUpserted: got %d, want 1", res.CompaniesUpserted)
	}
	if res.PagesUpserted != 1 {
		t.Errorf("pagesUpserted: got %d, want 1", res.PagesUpserted)
	}
	if res.ErrorCount != 1 {
		t.Errorf("errorCount: got %d, want 1", res.ErrorCount)
	}
	if len(companies.merged) != 1 || len(pages.merged) != 1 {
		t.Errorf("want 1 company + 1 page merged, got %d + %d", len(companies.merged), len(pages.merged))
	}
	if len(res.Errors) != 1 || res.Errors[0].Line != 1 {
		t.Errorf("expected one line-1 sub-line error, got %+v", res.Errors)
	}
}

func TestMergeExecutorCompanyInfraErrorFailsJob(t *testing.T) {
	companies := &fakeCatalogCompanyRepo{err: errors.New("db down")}
	pages := &fakeCatalogPageRepo{}
	exec := importer.NewMergeExecutor(companies, pages)

	res, err := exec(context.Background(), []byte(validKeyedOnePage), false)
	if err == nil {
		t.Fatal("a company merge failure should fail the whole job")
	}
	if !errors.Is(err, companies.err) {
		t.Errorf("error should wrap the repo error, got %v", err)
	}
	if res.CompaniesUpserted != 0 || res.PagesUpserted != 0 || res.ErrorCount != 0 {
		t.Errorf("infra failure should return the zero result, got %+v", res)
	}
}

func TestMergeExecutorPageInfraErrorFailsJob(t *testing.T) {
	companies := &fakeCatalogCompanyRepo{}
	pages := &fakeCatalogPageRepo{err: errors.New("db down")}
	exec := importer.NewMergeExecutor(companies, pages)

	_, err := exec(context.Background(), []byte(validKeyedOnePage), false)
	if err == nil {
		t.Fatal("a page merge failure should fail the whole job")
	}
	if !errors.Is(err, pages.err) {
		t.Errorf("error should wrap the repo error, got %v", err)
	}
}
