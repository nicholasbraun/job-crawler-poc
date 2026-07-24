// Package urlfilter provides CheckFn factories for filtering URLs by TLD,
// subdomain, path segment, and hostname. Filters return either a blocking
// error or filter.ErrPass for allowlist rules.
package urlfilter

import (
	"errors"
	"fmt"
	"net/url"
	"path"
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

// BlockFileExtensions rejects a URL whose final path segment ends in one of the
// given extensions (given without a leading dot; compared case-insensitively).
// It sheds non-HTML assets -- documents, images, archives, audio/video -- that a
// discovery crawl should never fetch: they yield no outbound company links, so
// downloading them is pure wasted budget. Extensionless paths and page
// extensions the list omits (e.g. .html, .php) pass through untouched.
func BlockFileExtensions(extensions ...string) filter.CheckFn[string] {
	return func(u string) error {
		parsed, err := url.Parse(u)
		if err != nil {
			return fmt.Errorf("filter: error parsing url: %s. err: %w", u, err)
		}

		ext := strings.ToLower(strings.TrimPrefix(path.Ext(parsed.Path), "."))
		if ext != "" && slices.Contains(extensions, ext) {
			return fmt.Errorf("filter: blocked because of file extension: %s", ext)
		}

		return nil
	}
}

// BlockQueryParams rejects a URL carrying any of the given query-parameter keys
// (compared case-insensitively). These are crawler-trap and cache-busting params
// that mint unbounded distinct URLs for the same underlying page -- TYPO3 plugin
// routing (tx_*, observed live generating a fresh URL per nav/RSS include), cache
// busters (no_cache), WordPress comment-reply chains (replytocom), and session ids
// (PHPSESSID) -- so blocking them keeps the frontier from spiralling on one host.
// A key ending in "*" is a prefix wildcard (e.g. "tx_*" blocks every tx_… param);
// all others match a present parameter name exactly. Purely tracking params
// (utm_*, gclid) are stripped by url.normalize instead, since those pages are worth
// fetching once -- only their duplicate variants are waste.
func BlockQueryParams(params ...string) filter.CheckFn[string] {
	return func(u string) error {
		parsed, err := url.Parse(u)
		if err != nil {
			return fmt.Errorf("filter: error parsing url: %s. err: %w", u, err)
		}

		for key := range parsed.Query() {
			lower := strings.ToLower(key)
			for _, p := range params {
				if prefix, ok := strings.CutSuffix(p, "*"); ok {
					if strings.HasPrefix(lower, prefix) {
						return fmt.Errorf("filter: blocked because of query param: %s", key)
					}
				} else if lower == p {
					return fmt.Errorf("filter: blocked because of query param: %s", key)
				}
			}
		}

		return nil
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
