package filter_test

import (
	"errors"
	"testing"

	"github.com/nicholasbraun/job-crawler-poc/internal/filter"
	urlfilter "github.com/nicholasbraun/job-crawler-poc/internal/filter/url"
)

type test struct {
	name     string
	url      string
	wantPass bool
}

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

func TestURLFilter(t *testing.T) {
	t.Run("allow subdomains", func(t *testing.T) {
		allowSubdomainCheck := urlfilter.AllowSubdomains("jobs", "career")
		tests := []struct {
			name             string
			url              string
			expectedErr      error
			expectedChainErr error
		}{
			{"allowed 1", "https://jobs.google.com/something", filter.ErrAllowed, nil},
			{"allowed 2", "https://jobs.google.co.uk/something", filter.ErrAllowed, nil},
			{"relative", "/something", nil, nil},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				err := allowSubdomainCheck(tt.url)
				if !errors.Is(err, tt.expectedErr) {
					t.Errorf("expected %v, got %v", tt.expectedErr, err)
				}

				checkFn := filter.Chain(allowSubdomainCheck)

				err = checkFn(tt.url)
				if !errors.Is(err, tt.expectedChainErr) {
					t.Errorf("expected no error. got: %v", err)
				}
			})
		}
	})

	t.Run("block subdomains", func(t *testing.T) {
		blockSubdomainCheck := urlfilter.BlockSubdomains("blog", "login")
		checkFn := filter.Chain(blockSubdomainCheck)

		tests := []test{
			{"block blog", "https://blog.google.com/something", false},
			{"allow jobs", "https://jobs.google.com/something", true},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				err := checkFn(tt.url)
				if err == nil && !tt.wantPass {
					t.Errorf("expected an error. got: nil")
				}

				if err != nil && tt.wantPass {
					t.Errorf("expected no error. got: %v", err)
				}
			})
		}
	})

	t.Run("block invalid urls", func(t *testing.T) {
		blockInvalidURLs := urlfilter.BlockInvalidURLs()
		checkFn := filter.Chain(blockInvalidURLs)

		tests := []test{
			{"fragment", "#fragment", false},
			{"mailto", "mailto@something.com", false},
			{"javascript", "javascript:void(0)", false},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				err := checkFn(tt.url)

				if err != nil && tt.wantPass {
					t.Errorf("expected url to pass %s", tt.url)
				}

				if err == nil && !tt.wantPass {
					t.Errorf("expected to block url %s", tt.url)
				}
			})
		}
	})

	t.Run("allow path segments", func(t *testing.T) {
		allowPathSegmentsCheck := urlfilter.AllowPathSegments("jobs", "career")

		tests := []struct {
			name             string
			url              string
			expectedErr      error
			expectedChainErr error
		}{
			{"absolute", "https://google.com/jobs/something", filter.ErrAllowed, nil},
			{"relative", "/career/something", filter.ErrAllowed, nil},
			{"relative (fail)", "/careers/something", nil, nil},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				err := allowPathSegmentsCheck(tt.url)
				if !errors.Is(err, tt.expectedErr) {
					t.Errorf("expected %v, got %v", tt.expectedErr, err)
				}

				checkFn := filter.Chain(allowPathSegmentsCheck)

				err = checkFn(tt.url)
				if !errors.Is(err, tt.expectedChainErr) {
					t.Errorf("expected no error. got: %v", err)
				}
			})
		}
	})

	t.Run("block hostnames", func(t *testing.T) {
		blockHostnames := urlfilter.BlockHostnames("x.com", "github.com")

		tests := []test{
			{"x.com", "https://x.com/user", false},
			{"github.com", "https://github.com/user", false},
			{"google.com", "https://google.com/user", true},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				checkFn := filter.Chain(blockHostnames)

				err := checkFn(tt.url)
				if err != nil && tt.wantPass {
					t.Errorf("expected url to pass %s", tt.url)
				}

				if err == nil && !tt.wantPass {
					t.Errorf("expected to block url %s", tt.url)
				}
			})
		}
	})

	t.Run("block path segments", func(t *testing.T) {
		blockPathSegmentsCheck := urlfilter.BlockPathSegments("blog", "contact")
		tests := []test{
			{"block absolute", "https://google.com/blog", false},
			{"allow absolute", "https://google.com/jobs", true},
			{"block relative", "/blog", false},
			{"allow relative", "/jobs", true},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				checkFn := filter.Chain(blockPathSegmentsCheck)

				err := checkFn(tt.url)
				if err == nil && !tt.wantPass {
					t.Errorf("expected an error. got nil")
				}
				if err != nil && tt.wantPass {
					t.Errorf("expected no error. got %v", err)
				}
			})
		}
	})

	t.Run("allow check fails fast", func(t *testing.T) {
		allowPathSegmentsCheck := urlfilter.AllowPathSegments("jobs", "career")
		blockPathSegmentsCheck := urlfilter.BlockPathSegments("blog", "contact")
		allowedURL1 := "https://google.com/jobs/something/blog"

		checkFn := filter.Chain(allowPathSegmentsCheck, blockPathSegmentsCheck)

		err := checkFn(allowedURL1)
		if err != nil {
			t.Errorf("expected no error. got: %v", err)
		}
	})

	t.Run("block check fails fast", func(t *testing.T) {
		allowPathSegmentsCheck := urlfilter.AllowPathSegments("jobs", "career")
		blockPathSegmentsCheck := urlfilter.BlockPathSegments("blog", "contact")
		blockedURL1 := "https://google.com/jobs/something/blog"

		checkFn := filter.Chain(blockPathSegmentsCheck, allowPathSegmentsCheck)

		err := checkFn(blockedURL1)
		if err == nil {
			t.Errorf("expected an error. got: nil")
		}
	})
}
