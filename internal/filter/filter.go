// Package filter provides a generic filter chain built on CheckFn. Compose
// checks with Chain (short-circuit on first failure or ErrPass) and Every
// (logical AND). Domain-specific filters live in sub-packages.
package filter

import (
	"errors"
	"regexp"
	"strings"
)

// CheckFn evaluates an item and returns nil to pass, a non-nil error to
// reject, or ErrPass to short-circuit a Chain and explicitly pass.
type CheckFn[T any] func(T) error

var (
	// ErrPass is returned by a CheckFn to short-circuit a Chain and explicitly
	// pass the item, skipping any remaining checks. Used for allowlist rules
	// (e.g., a URL subdomain that should always be crawled).
	ErrPass     = errors.New("filter: explicitly pass")
	ErrRejected = errors.New("filter: explicitly rejected")
)

// Chain reduces multiple checks to one check. When the returned check is called,
// all the checks in the chain will get called in the same order as provided.
// Chain returns nil when all checks pass and an error when the first check fails.
//
// IMPORTANT: It is possible to add 'Pass' CheckFns which also fail fast, but pass.
// For example for a URL that we explicitly want, like "jobs.example.com".
// ORDERING MATTERS: PASS must come before BLOCK CheckFns
func Chain[T any](checks ...CheckFn[T]) CheckFn[T] {
	return func(item T) error {
		for _, fn := range checks {
			err := fn(item)
			if errors.Is(err, ErrPass) {
				return nil
			}
			if err != nil {
				return err
			}
		}

		return nil
	}
}

// Contains returns a CheckFn that passes (ErrPass) when the input contains any
// of the provided keywords as a whole word, matched case-insensitively.
//
// Matching is word-boundary based (regexp \b), not substring: this keeps short,
// common keywords such as "go" from matching inside unrelated words like
// "logout", "category", or "Chicago". That precision is what lets the keyword
// relevance filter prune pages before the expensive LLM extraction (ADR-0004).
// The keyword patterns are compiled once, when Contains is called.
//
// For plain substring matching (e.g. URL allowlists) use ContainsSubstring.
func Contains(keywords ...string) CheckFn[string] {
	patterns := []*regexp.Regexp{}
	for _, keyword := range keywords {
		if keyword == "" {
			continue
		}
		patterns = append(patterns, regexp.MustCompile(`(?i)\b`+regexp.QuoteMeta(keyword)+`\b`))
	}

	return func(s string) error {
		for _, pattern := range patterns {
			if pattern.MatchString(s) {
				return ErrPass
			}
		}

		return nil
	}
}

// ContainsSubstring returns a CheckFn that passes (ErrPass) when the input
// contains any of the provided keywords as a substring, matched
// case-insensitively. Unlike Contains it does not require word boundaries, so
// it stays suitable for allowlist-style matches (e.g. a URL host or path).
func ContainsSubstring(keywords ...string) CheckFn[string] {
	return func(s string) error {
		for _, keyword := range keywords {
			if strings.Contains(strings.ToLower(s), strings.ToLower(keyword)) {
				return ErrPass
			}
		}

		return nil
	}
}

// Every returns a CheckFn that passes (returns ErrPass) only when all
// provided checks pass. If any check does not return ErrPass, Every
// returns nil (allowing the Chain to continue). This is the logical AND
// of pass-style checks.
func Every[T any](checks ...CheckFn[T]) CheckFn[T] {
	return func(item T) error {
		for _, fn := range checks {
			if err := fn(item); !errors.Is(err, ErrPass) {
				return nil
			}
		}

		return ErrPass
	}
}

func Reject[T any]() CheckFn[T] {
	return func(a T) error {
		return ErrRejected
	}
}
