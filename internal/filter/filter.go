// Package filter is responsible for the filtering logic.
// It implements all the filters for content, urls and relevant jobs.
package filter

import (
	"errors"
	"strings"
)

type CheckFn[T any] func(T) error

var (
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

// Contains checks if a string contains one of the provided keywords (case insensitive) and returns ErrPass if it does.
func Contains(keywords ...string) CheckFn[string] {
	return func(s string) error {
		for _, keyword := range keywords {
			if strings.Contains(strings.ToLower(s), strings.ToLower(keyword)) {
				return ErrPass
			}
		}

		return nil
	}
}

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
