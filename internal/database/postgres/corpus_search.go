package postgres

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
)

const (
	// defaultSearchLimit caps a ListingQuery page when the caller sets no positive
	// Limit, so a keywordless browse can never scan the whole Corpus into memory.
	defaultSearchLimit = 100

	// fuzzyMatchThreshold is the minimum pg_trgm word similarity for a keyword to
	// fuzzily match a title/company. A keyword search pins it as
	// pg_trgm.word_similarity_threshold (SET LOCAL) so the index-backed %> operator
	// matches at exactly this cutoff. It trades recall against noise: lower admits more
	// near-miss hits (and more false positives), higher tightens toward exact match.
	// 0.3 clears a realistic single-typo miss ("enginer" -> "Engineer") while rejecting
	// unrelated terms.
	fuzzyMatchThreshold = 0.3
)

// rowQuerier is the read surface SearchListings needs from either the pool (a
// keywordless browse) or a transaction (a keyword search, which first pins the
// pg_trgm threshold via SET LOCAL). Both *pgxpool.Pool and pgx.Tx satisfy it.
type rowQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// SearchListings implements crawler.CorpusSearchRepository over Postgres FTS (ADR-0037):
// it composes a keyword predicate (the weighted title/description/company tsvector via
// websearch_to_tsquery, OR-ed with an index-backed pg_trgm word-similarity fuzzy tail on
// title/company — the %> operator, matched at fuzzyMatchThreshold via a SET LOCAL GUC so
// gin_trgm_ops serves it) with the structured country / work-arrangement / open-closed
// filters, then orders by q.Sort (ts_rank relevance with a last_seen recency tiebreak by
// default; strict recency for SortRecent or a keywordless browse) and pages by
// q.Limit/q.Offset. The predicates and ordering are assembled dynamically with $n
// placeholders so an absent filter adds neither SQL nor an argument. Never returns nil.
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

	// Keyword predicate: each keyword is an OR-group (exact tsquery OR fuzzy title OR fuzzy
	// company), and the groups are AND-ed so every keyword must match. The fuzzy branch uses
	// the pg_trgm word-similarity operator (title %> kw is word_similarity(kw, title) >=
	// pg_trgm.word_similarity_threshold), which the title/company gin_trgm_ops indexes serve
	// via a BitmapOr with the tsvector index — unlike the old word_similarity(...) >= const
	// function form, which was opaque to the planner and forced a per-row seqscan.
	// searchWithFuzzyThreshold pins the GUC to fuzzyMatchThreshold so the >= boundary matches
	// the former inline 0.3 exactly. company may be NULL and NULL %> kw is NULL (not true),
	// so a NULL company never spuriously matches.
	if len(keywords) > 0 {
		groups := make([]string, 0, len(keywords))
		for _, k := range keywords {
			kw := next(k)
			groups = append(groups, fmt.Sprintf(
				"(search_tsv @@ websearch_to_tsquery('simple', %s)"+
					" OR title %%> %s"+
					" OR company %%> %s)",
				kw, kw, kw,
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
	switch {
	case q.Sort == crawler.SortFound:
		// Newly-discovered postings first, regardless of keywords (the live
		// collection feed). first_seen never bumps on re-verification.
		orderBy = "first_seen DESC, id"
	case q.Sort == crawler.SortRecent || len(keywords) == 0:
		orderBy = "last_seen DESC, id"
	default:
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

	// A keywordless browse has no %> operator, so it needs no threshold GUC and runs
	// directly on the pool. A keyword search runs inside a transaction that pins the
	// pg_trgm threshold first (see searchWithFuzzyThreshold).
	if len(keywords) == 0 {
		return scanListings(ctx, r.pool, sb.String(), args)
	}
	return r.searchWithFuzzyThreshold(ctx, sb.String(), args)
}

// ListingCounts returns the distinct open and total Corpus listing counts — the true
// corpus size, not a run's save-event counter (crawler.CorpusSearchRepository).
func (r *CorpusRepository) ListingCounts(ctx context.Context) (open int, total int, err error) {
	if err = r.pool.QueryRow(ctx,
		`SELECT count(*) FILTER (WHERE closed_at IS NULL), count(*) FROM job_listing`,
	).Scan(&open, &total); err != nil {
		return 0, 0, fmt.Errorf("postgres: error counting corpus listings: %w", err)
	}
	return open, total, nil
}

// searchWithFuzzyThreshold runs a keyword search SELECT inside a transaction that pins
// pg_trgm.word_similarity_threshold via SET LOCAL, so the title %> kw / company %> kw
// operators match at exactly fuzzyMatchThreshold — a GUC-scoped setting the operator
// honors, not an inline arg. SET takes no bind parameters, so the value is inlined from
// the package constant (never user input). SET LOCAL is transaction-scoped, so the pooled
// connection is never left with a mutated threshold.
func (r *CorpusRepository) searchWithFuzzyThreshold(ctx context.Context, query string, args []any) ([]*crawler.CorpusListing, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("postgres: error beginning search tx: %w", err)
	}
	// Rollback is a no-op once Commit has succeeded; the error is not actionable.
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, fmt.Sprintf(
		"SET LOCAL pg_trgm.word_similarity_threshold = %v", fuzzyMatchThreshold,
	)); err != nil {
		return nil, fmt.Errorf("postgres: error setting word-similarity threshold: %w", err)
	}

	results, err := scanListings(ctx, tx, query, args)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("postgres: error committing search tx: %w", err)
	}
	return results, nil
}

// scanListings runs the assembled search query against q (a pool for a browse, a
// transaction for a keyword search) and projects the rows into CorpusListings. Never
// returns nil — no match yields an empty slice.
func scanListings(ctx context.Context, q rowQuerier, query string, args []any) ([]*crawler.CorpusListing, error) {
	rows, err := q.Query(ctx, query, args...)
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
