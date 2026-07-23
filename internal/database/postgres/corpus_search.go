package postgres

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
)

const (
	// defaultSearchLimit caps a ListingQuery page when the caller sets no positive
	// Limit, so a keywordless browse can never scan the whole Corpus into memory.
	defaultSearchLimit = 100

	// fuzzyMatchThreshold is the minimum pg_trgm word_similarity for a keyword to
	// fuzzily match a title/company. It trades recall against noise: lower admits more
	// near-miss hits (and more false positives), higher tightens toward exact match.
	// 0.3 clears a realistic single-typo miss ("enginer" -> "Engineer") while rejecting
	// unrelated terms.
	fuzzyMatchThreshold = 0.3
)

// SearchListings implements crawler.CorpusSearchRepository over Postgres FTS (ADR-0037):
// it composes a keyword predicate (the weighted title/description/company tsvector via
// websearch_to_tsquery, OR-ed with a pg_trgm word_similarity fuzzy tail on title/company)
// with the structured country / work-arrangement / open-closed filters, then orders by
// q.Sort (ts_rank relevance with a last_seen recency tiebreak by default; strict recency
// for SortRecent or a keywordless browse) and pages by q.Limit/q.Offset. The predicates
// and ordering are assembled dynamically with $n placeholders so an absent filter adds
// neither SQL nor an argument. Never returns nil.
func (r *CorpusRepository) SearchListings(ctx context.Context, q crawler.ListingQuery) ([]*crawler.CorpusListing, error) {
	// Effective keywords: trimmed, blanks dropped. A query of only-blank keywords
	// degrades to a pure recency browse (no keyword predicate, no relevance ordering).
	keywords := []string{}
	for _, k := range q.Keywords {
		if k = strings.TrimSpace(k); k != "" {
			keywords = append(keywords, k)
		}
	}

	// next appends an argument and returns its positional placeholder ($1, $2, ...).
	// Placeholders bind by position, so a fragment's textual order in the query is
	// independent of the order args are appended.
	args := []any{}
	argN := 0
	next := func(v any) string {
		args = append(args, v)
		argN++
		return fmt.Sprintf("$%d", argN)
	}

	predicates := []string{}

	// Open-only unless the caller opts into closed listings.
	if !q.IncludeClosed {
		predicates = append(predicates, "closed_at IS NULL")
	}

	// Keyword predicate: each keyword is an OR-group (exact tsquery OR fuzzy title/company),
	// and the groups are AND-ed so every keyword must match. word_similarity(query, target)
	// with the keyword first finds the best contiguous fuzzy window; company may be NULL and
	// NULL >= threshold is NULL (not true), so a NULL company never spuriously matches.
	if len(keywords) > 0 {
		thresh := next(fuzzyMatchThreshold)
		groups := make([]string, 0, len(keywords))
		for _, k := range keywords {
			kw := next(k)
			groups = append(groups, fmt.Sprintf(
				"(search_tsv @@ websearch_to_tsquery('simple', %s)"+
					" OR word_similarity(%s, title) >= %s"+
					" OR word_similarity(%s, company) >= %s)",
				kw, kw, thresh, kw, thresh,
			))
		}
		predicates = append(predicates, strings.Join(groups, " AND "))
	}

	// Country filter: OR within Countries, matched against the stored uppercase ISO code
	// (uppercased here so a lowercase "de" still matches).
	if len(q.Countries) > 0 {
		upper := make([]string, len(q.Countries))
		for i, c := range q.Countries {
			upper[i] = strings.ToUpper(c)
		}
		predicates = append(predicates, "country = ANY("+next(upper)+")")
	}

	// Work-arrangement filter: OR within WorkArrangements.
	if len(q.WorkArrangements) > 0 {
		vals := make([]string, len(q.WorkArrangements))
		for i, w := range q.WorkArrangements {
			vals[i] = string(w)
		}
		predicates = append(predicates, "work_arrangement = ANY("+next(vals)+")")
	}

	// Ordering. Relevance needs keywords to rank; without them (or with SortRecent) it is
	// pure recency. The id tiebreak gives a stable total order so paging never repeats or
	// skips a row. Fuzzy-only matches get ts_rank 0 and fall to the recency tiebreak, so
	// exact beats fuzzy and newer beats older.
	var orderBy string
	if q.Sort == crawler.SortRecent || len(keywords) == 0 {
		orderBy = "last_seen DESC, id"
	} else {
		rank := next(strings.Join(keywords, " "))
		orderBy = fmt.Sprintf(
			"ts_rank(search_tsv, websearch_to_tsquery('simple', %s)) DESC, last_seen DESC, id",
			rank,
		)
	}

	limit := q.Limit
	if limit <= 0 {
		limit = defaultSearchLimit
	}
	offset := q.Offset
	if offset < 0 {
		offset = 0
	}

	var sb strings.Builder
	sb.WriteString(`
		SELECT id, canonical_url, url, title,
		       coalesce(description, ''), coalesce(company, ''), company_key,
		       coalesce(department, ''), coalesce(location, ''), country,
		       work_arrangement, source,
		       career_page_id, first_seen, last_seen, closed_at
		FROM job_listing`)
	if len(predicates) > 0 {
		sb.WriteString("\n\t\tWHERE ")
		sb.WriteString(strings.Join(predicates, " AND "))
	}
	sb.WriteString("\n\t\tORDER BY ")
	sb.WriteString(orderBy)
	fmt.Fprintf(&sb, "\n\t\tLIMIT %s OFFSET %s", next(limit), next(offset))

	rows, err := r.pool.Query(ctx, sb.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("postgres: error searching listings: %w", err)
	}
	defer rows.Close()

	results := []*crawler.CorpusListing{}
	for rows.Next() {
		cl := &crawler.CorpusListing{}
		var (
			source       string
			arrangement  string
			careerPageID *uuid.UUID
		)
		if err := rows.Scan(
			&cl.ID, &cl.CanonicalURL, &cl.URL, &cl.Title,
			&cl.Description, &cl.Company, &cl.CompanyKey,
			&cl.Department, &cl.Location, &cl.Country, &arrangement, &source,
			&careerPageID, &cl.FirstSeen, &cl.LastSeen, &cl.ClosedAt,
		); err != nil {
			return nil, fmt.Errorf("postgres: error scanning searched listing: %w", err)
		}
		cl.Source = crawler.SourceLane(source)
		cl.WorkArrangement = crawler.WorkArrangement(arrangement)
		if careerPageID != nil {
			cl.CareerPageID = *careerPageID
		}
		results = append(results, cl)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: error searching listings: %w", err)
	}

	return results, nil
}
