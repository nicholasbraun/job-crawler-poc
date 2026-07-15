package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
)

// companyExportRecord is one Catalog Export line: a Company with its Career
// Pages nested (ADR-0015). Field declaration order is the JSON key order the
// exchange format promises, so exports diff meaningfully; `website` slots
// between DisplayDomain and FirstSeen. Row ids and politeness domains never
// appear in the file — ids are instance-local, and the politeness domain is
// always derivable from the page URL's host. ATSProvider is emitted even when
// "" because "" is a definite value (self-hosted, ADR-0001), not an unknown;
// Name, DisplayDomain, and Website are omitted when empty because an empty
// string there means "not known", and ADR-0013's presence semantics would
// otherwise turn a re-import of this file into an explicit blanking write.
type companyExportRecord struct {
	CompanyKey    string                   `json:"companyKey"`
	ATSProvider   string                   `json:"atsProvider"`
	Name          string                   `json:"name,omitempty"`
	DisplayDomain string                   `json:"displayDomain,omitempty"`
	Website       string                   `json:"website,omitempty"`
	FirstSeen     time.Time                `json:"firstSeen"`
	LastSeen      time.Time                `json:"lastSeen"`
	CareerPages   []careerPageExportRecord `json:"careerPages"`
}

type careerPageExportRecord struct {
	URL       string    `json:"url"`
	FirstSeen time.Time `json:"firstSeen"`
	LastSeen  time.Time `json:"lastSeen"`
}

// exportRecords assembles the Catalog Export lines from a Catalog snapshot:
// Companies ascending by companyKey, each with its Career Pages ascending by
// canonical URL, so the same Catalog always exports byte-identically
// (ADR-0015). Timestamps are normalized to UTC at full sub-second fidelity so
// a round trip through import's LEAST/GREATEST merge is a no-op. Zero-page
// Companies (including Pageless Companies) get an empty, non-nil careerPages
// array. A page whose company is absent from the snapshot is dropped — the
// format cannot represent a dangling page.
func exportRecords(companies []*crawler.Company, pages []*crawler.CareerPage) []companyExportRecord {
	byCompany := make(map[uuid.UUID][]*crawler.CareerPage, len(companies))
	for _, p := range pages {
		byCompany[p.CompanyID] = append(byCompany[p.CompanyID], p)
	}

	// Sort clones: the input slices are repository-owned and arrive in
	// last_seen DESC order, which the handler must not mutate.
	sorted := slices.Clone(companies)
	slices.SortFunc(sorted, func(a, b *crawler.Company) int {
		return strings.Compare(a.CompanyKey, b.CompanyKey)
	})

	records := []companyExportRecord{}
	for _, c := range sorted {
		// Stored URLs already went through catalog.CanonicalURL, so byte-order
		// on the stored string is "canonical URL ascending".
		companyPages := slices.Clone(byCompany[c.ID])
		slices.SortFunc(companyPages, func(a, b *crawler.CareerPage) int {
			return strings.Compare(a.URL, b.URL)
		})

		pageRecords := []careerPageExportRecord{}
		for _, p := range companyPages {
			pageRecords = append(pageRecords, careerPageExportRecord{
				URL:       p.URL,
				FirstSeen: p.FirstSeen.UTC(),
				LastSeen:  p.LastSeen.UTC(),
			})
		}

		records = append(records, companyExportRecord{
			CompanyKey:    c.CompanyKey,
			ATSProvider:   c.ATSProvider,
			Name:          c.Name,
			DisplayDomain: c.DisplayDomain,
			Website:       c.Website,
			FirstSeen:     c.FirstSeen.UTC(),
			LastSeen:      c.LastSeen.UTC(),
			CareerPages:   pageRecords,
		})
	}

	return records
}

// exportCatalog streams the full Catalog as deterministic NDJSON — the Catalog
// Export download. Career Pages are read before Companies so the
// unsynchronised-snapshot race yields a Company with an empty careerPages
// array rather than dropped pages (ADR-0015). Once the first line has been
// written the 200 is committed, so a mid-stream failure can only truncate the
// output, never turn into a 500.
func (h *Handler) exportCatalog(w http.ResponseWriter, r *http.Request) {
	pages, err := h.cfg.CareerPages.List(r.Context())
	if err != nil {
		slog.Error("api: error reading career pages for export", "err", err)
		writeError(w, http.StatusInternalServerError, "could not export catalog")
		return
	}

	companies, err := h.cfg.Companies.List(r.Context())
	if err != nil {
		slog.Error("api: error reading companies for export", "err", err)
		writeError(w, http.StatusInternalServerError, "could not export catalog")
		return
	}

	records := exportRecords(companies, pages)

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Content-Disposition", `attachment; filename="catalog-`+time.Now().UTC().Format("2006-01-02")+`.ndjson"`)
	w.Header().Set("Cache-Control", "no-store")

	// Encode appends exactly one \n per record — the NDJSON framing; the
	// stream carries no trailer line (ADR-0015). HTML escaping is off so `&`
	// in career-page URLs stays literal and the file stays hand-editable.
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	for _, rec := range records {
		if err := enc.Encode(rec); err != nil {
			slog.Error("api: error streaming catalog export", "err", err)
			return
		}
	}
}
