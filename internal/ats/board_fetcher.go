// Package ats implements ATS provider board-API clients: per-provider fetchers
// that read a tenant's board API and map its JSON to Job Listings (ADR-0022),
// plus a provider→fetcher Registry the ATS Fetch lane resolves against. The
// per-provider fetchers are a family sharing only the *crawler.JobListing output
// shape — each owns its own JSON structs and mapping — not one interface over a
// shared input (ADR-0022).
package ats

import (
	"context"
	"errors"
	"fmt"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
)

// BoardFetcher fetches one ATS tenant's board and maps its postings to Job
// Listings. Implementations set every field the board API supplies; they leave
// Company and CompanyKey empty for the ingest lane to stamp from the page's
// Owner (ADR-0022). An empty board yields an empty (non-nil) slice, not an error.
//
// Completeness contract (ADR-0035) — the absence-sweep may run only on a provably
// whole snapshot, so the returned error carries the completeness verdict:
//   - err == nil ⟹ the slice is the tenant's COMPLETE open set (safe to sweep).
//     Complete by construction: a fetcher fully enumerates or errors — a truncated
//     body surfaces as a decode error via io.LimitReader, never a silent partial.
//   - errors.Is(err, ErrBoardIncomplete) ⟹ the (non-nil, possibly partial) slice is
//     a PRESENCE SAMPLE — save it, skip the sweep; never mass-close a board.
//   - any other non-nil err (ErrBoardStatus, a decode error, a context error) ⟹ a
//     hard failure with a nil slice.
//   - An empty board is ([]*crawler.JobListing{}, nil): complete and empty.
type BoardFetcher interface {
	Fetch(ctx context.Context, tenant string) ([]*crawler.JobListing, error)
}

// ErrBoardStatus wraps a non-200 response from a provider board API so the lane
// can errors.Is it, e.g. to distinguish a missing tenant from a transient failure.
// A fetcher returns a BoardStatusError (which unwraps to this) so the code is
// available; this bare sentinel remains the coarse "was it a board-status error?"
// match target.
var ErrBoardStatus = errors.New("ats: board api returned non-200 status")

// BoardStatusError is a non-200 board-API response that carries the HTTP status
// code, so the lane can tell a hard-dead board (404/410 — the tenant is gone) apart
// from a transient one (429/5xx — rate-limited or degraded) instead of closing a
// live board on our own politeness signal. It wraps ErrBoardStatus, so an existing
// errors.Is(err, ErrBoardStatus) check still matches; errors.As(err,
// &BoardStatusError{}) recovers the code (mirrors downloader.StatusError, which the
// crawl lane's classifyStatus reads the same way).
type BoardStatusError struct {
	StatusCode int
}

func (e *BoardStatusError) Error() string {
	return fmt.Sprintf("ats: board api returned status %d", e.StatusCode)
}

// Unwrap keeps errors.Is(err, ErrBoardStatus) matching a BoardStatusError, so the
// coarse sentinel check survives while the code stays reachable via errors.As.
func (e *BoardStatusError) Unwrap() error {
	return ErrBoardStatus
}

// ErrBoardIncomplete signals that a fetch could NOT enumerate the tenant's whole
// open set (a capped, short, mismatched, or partially-resolvable board). A fetcher
// returns it ALONGSIDE the partial slice it did collect: errors.Is(err,
// ErrBoardIncomplete) means "save what we saw, skip the absence-sweep" (ADR-0035),
// never "save nothing". It is distinct from ErrBoardStatus, which is a hard board
// failure returned with a nil slice.
var ErrBoardIncomplete = errors.New("ats: board api returned an incomplete result")

// Registry resolves a recognized ATS provider family (e.g. "greenhouse") to the
// BoardFetcher that reads its board API. A provider with no registered fetcher
// falls back to the ordinary crawl-and-fence path (ADR-0022), signalled by
// ok=false from Fetcher.
type Registry struct {
	fetchers map[string]BoardFetcher
}

// RegistryOption configures a Registry at construction.
type RegistryOption func(*Registry)

// WithFetcher registers f as the BoardFetcher for provider. A later registration
// of the same provider replaces an earlier one.
func WithFetcher(provider string, f BoardFetcher) RegistryOption {
	return func(r *Registry) {
		r.fetchers[provider] = f
	}
}

// NewRegistry builds a Registry with the given provider→fetcher registrations.
func NewRegistry(opts ...RegistryOption) *Registry {
	r := &Registry{fetchers: map[string]BoardFetcher{}}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// Fetcher returns the BoardFetcher registered for provider and ok=true, or
// (nil, false) when the provider has no API client — the crawl-fallback signal.
func (r *Registry) Fetcher(provider string) (BoardFetcher, bool) {
	f, ok := r.fetchers[provider]
	return f, ok
}

// NewDefaultRegistry wires every provider the crawler ships a board-API client
// for, each built with default options: Greenhouse, Lever, Personio, Workable,
// Ashby, SmartRecruiters, Recruitee, softgarden, Teamtailor, and Manatal.
func NewDefaultRegistry() *Registry {
	return NewRegistry(
		WithFetcher(ProviderGreenhouse, NewGreenhouseFetcher()),
		WithFetcher(ProviderLever, NewLeverFetcher()),
		WithFetcher(ProviderPersonio, NewPersonioFetcher()),
		WithFetcher(ProviderWorkable, NewWorkableFetcher()),
		WithFetcher(ProviderAshby, NewAshbyFetcher()),
		WithFetcher(ProviderSmartRecruiters, NewSmartRecruitersFetcher()),
		WithFetcher(ProviderRecruitee, NewRecruiteeFetcher()),
		WithFetcher(ProviderSoftgarden, NewSoftgardenFetcher()),
		WithFetcher(ProviderTeamtailor, NewTeamtailorFetcher()),
		WithFetcher(ProviderManatal, NewManatalFetcher()),
	)
}
