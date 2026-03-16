// Package filter is responsible for the filtering logic.
// It implements all the filters for content, urls and relevant jobs.
package filter

import "errors"

type CheckFn[T any] func(T) error

var ErrAllowed = errors.New("filter: explicitly allowed")

// Chain reduces multiple checks to one check. When the returned check is called,
// all the checks in the chain will get called in the same order as provided.
// Chain returns nil when all checks pass and an error when the first check fails.
//
// IMPORTANT: It is possible to add 'Allow' CheckFns which also fail fast, but pass.
// For example for a URL that we explicitly want, like "jobs.example.com".
// ORDERING MATTERS: ALLOW must come before BLOCK CheckFns
func Chain[T any](checks ...CheckFn[T]) CheckFn[T] {
	return func(item T) error {
		for _, fn := range checks {
			err := fn(item)
			if errors.Is(err, ErrAllowed) {
				return nil
			}
			if err != nil {
				return err
			}
		}

		return nil
	}
}
