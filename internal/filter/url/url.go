// Package urlfilter implements the url filtering logic
package urlfilter

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/nicholasbraun/job-crawler-poc/internal/filter"
)

func BlockInvalidURLs() filter.CheckFn[string] {
	return func(u string) error {
		if !strings.HasPrefix(u, "http") && !strings.HasPrefix(u, "/") && u != "" {
			return fmt.Errorf("filter: cannot parse url without schema or relative path. url=%s", u)
		}

		_, err := url.Parse(u)
		if err != nil {
			return fmt.Errorf("filter: error parsing url: %s. err: %w", u, err)
		}

		return nil
	}
}

func BlockPathSegments(pathSegments ...string) filter.CheckFn[string] {
	return func(u string) error {
		err := fmt.Errorf("filter: blocked because of path segment")
		return checkPathSegments(u, err, pathSegments...)
	}
}

func BlockHostnames(hostnames ...string) filter.CheckFn[string] {
	return func(u string) error {
		parsed, err := url.Parse(u)
		if err != nil {
			return fmt.Errorf("filter: error parsing url: %s. err: %w", u, err)
		}

		hostname := parsed.Hostname()
		if slices.Contains(hostnames, hostname) {
			return fmt.Errorf("filter: blocked because of hostname: %s", hostname)
		}

		return nil
	}
}

func AllowedTLDs(tlds ...string) filter.CheckFn[string] {
	return func(u string) error {
		parsed, err := url.Parse(u)
		if err != nil {
			return fmt.Errorf("filter: error parsing url: %s. err: %w", u, err)
		}

		hostname := parsed.Hostname()
		domains := strings.Split(hostname, ".")
		tld := domains[len(domains)-1]

		if !slices.Contains(tlds, tld) {
			return fmt.Errorf("filter: blocked because of TLD: %s", tld)
		}

		return nil
	}
}

func PassPathSegments(pathSegments ...string) filter.CheckFn[string] {
	return func(u string) error {
		return checkPathSegments(u, filter.ErrPass, pathSegments...)
	}
}

func PassSubdomains(subdomains ...string) filter.CheckFn[string] {
	return func(u string) error {
		err := filter.ErrPass
		return checkSubdomains(u, err, subdomains...)
	}
}

func BlockSubdomains(subdomains ...string) filter.CheckFn[string] {
	return func(u string) error {
		err := errors.New("filter: blocked because of subdomain")
		return checkSubdomains(u, err, subdomains...)
	}
}

func checkSubdomains(u string, e error, subdomains ...string) error {
	parsed, err := url.Parse(u)
	if err != nil {
		return fmt.Errorf("filter: error parsing url: %s. err: %w", u, err)
	}

	domains := strings.Split(parsed.Hostname(), ".")

	domainsLength := len(domains)
	if domainsLength < 2 {
		return nil
	}

	// strip domain + tld. we might miss a few like .co.uk . that's ok for the poc
	domains = domains[:domainsLength-2]
	// strip .co.uk and other commonwealth TLDs

	for _, s := range domains {
		if slices.Contains(subdomains, s) {
			return e
		}
	}

	return nil
}

func checkPathSegments(u string, e error, pathSegments ...string) error {
	parsed, err := url.Parse(u)
	if err != nil {
		return fmt.Errorf("filter: error parsing url: %s. err: %w", u, err)
	}
	segments := strings.SplitSeq(parsed.Path, "/")

	for segment := range segments {
		if slices.Contains(pathSegments, segment) {
			return e
		}
	}

	return nil
}
