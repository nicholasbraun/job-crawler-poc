package filter_test

import (
	"errors"
	"testing"

	"github.com/nicholasbraun/job-crawler-poc/internal/filter"
)

func createSpyCheckFn[T any](returnsError bool) (*int, filter.CheckFn[T]) {
	count := 0
	fn := func(t T) error {
		count++
		if returnsError {
			return errors.New("failed")
		}

		return nil
	}

	return &count, fn
}

func TestFilterChain(t *testing.T) {
	t.Run("empty chain", func(t *testing.T) {
		checks := []filter.CheckFn[int]{}
		checkFn := filter.Chain(checks...)

		err := checkFn(1)
		if err != nil {
			t.Error("empty chain must not return error")
		}
	})

	t.Run("all checks pass", func(t *testing.T) {
		_, checkFnPasses := createSpyCheckFn[int](false)
		_, checkFnPasses2 := createSpyCheckFn[int](false)
		checks := []filter.CheckFn[int]{checkFnPasses, checkFnPasses2}
		checkFn := filter.Chain(checks...)

		err := checkFn(1)
		if err != nil {
			t.Errorf("all check must pass: %v", err)
		}
	})

	t.Run("one check fails (fails fast)", func(t *testing.T) {
		count, checkFnPasses := createSpyCheckFn[int](false)
		_, checkFnFails := createSpyCheckFn[int](true)
		checks := []filter.CheckFn[int]{checkFnFails, checkFnPasses}
		checkFn := filter.Chain(checks...)

		err := checkFn(1)
		if err == nil {
			t.Error("one check must fail")
		}
		if *count > 0 {
			t.Errorf("checkFnPasses must not run. but it ran %d times", *count)
		}
	})

	t.Run("one check fails (second)", func(t *testing.T) {
		count, checkFnPasses := createSpyCheckFn[int](false)
		_, checkFnFails := createSpyCheckFn[int](true)
		checks := []filter.CheckFn[int]{checkFnPasses, checkFnFails}
		checkFn := filter.Chain(checks...)

		err := checkFn(1)
		if err == nil {
			t.Error("one check must fail")
		}
		if *count != 1 {
			t.Errorf("checkFnPasses must run 1 time. but it ran %d times", *count)
		}
	})
}
