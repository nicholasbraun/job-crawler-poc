package urlfilter_test

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

func TestURLFilter(t *testing.T) {
	t.Run("pass subdomains", func(t *testing.T) {
		passSubdomainCheck := urlfilter.PassSubdomains("jobs", "career")
		tests := []struct {
			name             string
			url              string
			expectedErr      error
			expectedChainErr error
		}{
			{"passed 1", "https://jobs.google.com/something", filter.ErrPass, nil},
			{"passed 2", "https://jobs.google.co.uk/something", filter.ErrPass, nil},
			{"relative", "/something", nil, nil},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				err := passSubdomainCheck(tt.url)
				if !errors.Is(err, tt.expectedErr) {
					t.Errorf("expected %v, got %v", tt.expectedErr, err)
				}

				checkFn := filter.Chain(passSubdomainCheck)

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
			{"pass jobs", "https://jobs.google.com/something", true},
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

	t.Run("pass path segments", func(t *testing.T) {
		passPathSegmentsCheck := urlfilter.PassPathSegments("jobs", "career")

		tests := []struct {
			name             string
			url              string
			expectedErr      error
			expectedChainErr error
		}{
			{"absolute", "https://google.com/jobs/something", filter.ErrPass, nil},
			{"relative", "/career/something", filter.ErrPass, nil},
			{"relative (fail)", "/careers/something", nil, nil},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				err := passPathSegmentsCheck(tt.url)
				if !errors.Is(err, tt.expectedErr) {
					t.Errorf("expected %v, got %v", tt.expectedErr, err)
				}

				checkFn := filter.Chain(passPathSegmentsCheck)

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
			{"pass absolute", "https://google.com/jobs", true},
			{"block relative", "/blog", false},
			{"pass relative", "/jobs", true},
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

	t.Run("pass check fails fast", func(t *testing.T) {
		passPathSegmentsCheck := urlfilter.PassPathSegments("jobs", "career")
		blockPathSegmentsCheck := urlfilter.BlockPathSegments("blog", "contact")
		passedURL1 := "https://google.com/jobs/something/blog"

		checkFn := filter.Chain(passPathSegmentsCheck, blockPathSegmentsCheck)

		err := checkFn(passedURL1)
		if err != nil {
			t.Errorf("expected no error. got: %v", err)
		}
	})

	t.Run("block check fails fast", func(t *testing.T) {
		passPathSegmentsCheck := urlfilter.PassPathSegments("jobs", "career")
		blockPathSegmentsCheck := urlfilter.BlockPathSegments("blog", "contact")
		blockedURL1 := "https://google.com/jobs/something/blog"

		checkFn := filter.Chain(blockPathSegmentsCheck, passPathSegmentsCheck)

		err := checkFn(blockedURL1)
		if err == nil {
			t.Errorf("expected an error. got: nil")
		}
	})
}

func TestAllowedTLDs(t *testing.T) {
	allowedTLDsCheck := urlfilter.AllowedTLDs("de", "com", "org", "io")
	chainFn := filter.Chain(allowedTLDsCheck)

	tests := []test{
		{"allowed tld", "https://google.com/blog", true},
		{"blocked tld (co.uk)", "https://google.co.uk", false},
		{"blocked tld (fr)", "https://google.fr", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := chainFn(tt.url)
			if err == nil && !tt.wantPass {
				t.Errorf("expected an error. got nil")
			}
			if err != nil && tt.wantPass {
				t.Errorf("expected no error. got %v", err)
			}
		})
	}
}
