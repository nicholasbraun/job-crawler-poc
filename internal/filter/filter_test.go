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

func createErrAllowedSpyCheckFn[T any]() filter.CheckFn[T] {
	return func(t T) error {
		return filter.ErrAllowed
	}
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

	t.Run("explicitly allow", func(t *testing.T) {
		allowCheckFn := createErrAllowedSpyCheckFn[int]()
		count, checkFnPasses := createSpyCheckFn[int](false)
		chainCheckFn := filter.Chain(allowCheckFn, checkFnPasses)

		err := chainCheckFn(1)
		if err != nil {
			t.Errorf("expected no error, got %v", err)
		}

		if *count != 0 {
			t.Errorf("second checkFn must not run")
		}
	})
}

func TestEvery(t *testing.T) {
	checkFn := filter.Every(
		filter.Contains("foo", "bar"),
		filter.Contains("hello", "world"),
	)

	tests := []struct {
		name       string
		testString string
		wantMatch  bool
	}{
		{"no match", "something different", false},
		{"single match", "hello world", false},
		{"match", "hello foo", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkFn(tt.testString)
			if err != nil && !tt.wantMatch {
				t.Errorf("expected nil, got: %v", err)
			}
			if err == nil && tt.wantMatch {
				t.Errorf("expected an error, got: nil")
			}
		})
	}
}

func TestContains(t *testing.T) {
	checkFn := filter.Contains("foo", "bar")

	tests := []struct {
		name       string
		testString string
		wantMatch  bool
	}{
		{"not found", "hello world", false},
		{"found", "hello foo", true},
		{"found (case insensitive)", "hello BAR", true},
		{"found (case insensitive + in longer word)", "hello FOOFoo", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkFn(tt.testString)
			if err != nil && !tt.wantMatch {
				t.Errorf("expected nil, got: %v", err)
			}
			if err == nil && tt.wantMatch {
				t.Errorf("expected an error, got: nil")
			}
		})
	}
}

func TestReject(t *testing.T) {
	rejectFn := filter.Reject[string]()
	count, passingCheckFn := createSpyCheckFn[string](false)
	chain := filter.Chain(rejectFn, passingCheckFn)

	err := chain("foo")
	if err != filter.ErrRejected {
		t.Errorf("expected reject, got: %v", err)
	}
	if *count != 0 {
		t.Errorf("expected passingCheckFn to not run, but it did %d times", *count)
	}
}

func TestChainOneOfReject(t *testing.T) {
	checkFn := filter.Contains("foo", "bar")
	rejectFn := filter.Reject[string]()
	chain := filter.Chain(checkFn, rejectFn)

	err := chain("hello Fooworld")
	if err != nil {
		t.Errorf("expected nil, got: %v", err)
	}

	err = chain("no match")
	if err != filter.ErrRejected {
		t.Errorf("expected ErrReject, got: %v", err)
	}
}
