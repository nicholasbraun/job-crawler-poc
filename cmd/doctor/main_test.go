package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/google/uuid"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/catalogdoctor"
)

// TestPrintReport pins the dry-run report format: the per-action summary, one
// line per non-Keep disposition (Keep rows are omitted), the re-attribution
// target arrow, and the orphaned Companies.
func TestPrintReport(t *testing.T) {
	keep := &crawler.CareerPage{ID: uuid.New(), URL: "https://acme.com/careers"}
	del := &crawler.CareerPage{ID: uuid.New(), URL: "https://indeed.com/jobs/123"}
	reattr := &crawler.CareerPage{ID: uuid.New(), URL: "https://join.com/companies/fugro"}
	merged := &crawler.CareerPage{ID: uuid.New(), URL: "http://acme.com/careers/"}

	result := catalogdoctor.Result{
		Pages: []catalogdoctor.PageDisposition{
			{Page: keep, Action: catalogdoctor.Keep, Reason: "valid career page"},
			{Page: del, Action: catalogdoctor.Delete, Reason: "aggregator host"},
			{
				Page:   reattr,
				Action: catalogdoctor.Reattribute,
				Reason: "identity join.com -> join:fugro",
				Target: &crawler.Company{CompanyKey: "join:fugro"},
			},
			{Page: merged, Action: catalogdoctor.Merge, Reason: "duplicate of https://acme.com/careers", MergeInto: keep.ID},
		},
		Orphans: []*crawler.Company{{CompanyKey: "join.com"}},
	}

	var buf bytes.Buffer
	printReport(&buf, len(result.Pages), result)
	out := buf.String()

	wantContains := []string{
		"career pages      4",
		"keep              1",
		"delete            1",
		"reattribute       1",
		"merge             1",
		"orphan companies  1",
		"https://indeed.com/jobs/123  (aggregator host)",
		"-> join:fugro",
		"duplicate of https://acme.com/careers",
		"orphan company   join.com",
	}
	for _, want := range wantContains {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q\nfull report:\n%s", want, out)
		}
	}

	// Keep rows are summarised in the counts but never listed line-by-line.
	if strings.Contains(out, keep.URL+"  (valid career page)") {
		t.Errorf("report listed a Keep disposition line, want summary only:\n%s", out)
	}
}
