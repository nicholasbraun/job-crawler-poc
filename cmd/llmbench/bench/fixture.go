package bench

import (
	"fmt"
	"net/url"
	"path"
	"strconv"
	"strings"
)

// slugMaxLen bounds a generated fixture slug so filenames stay manageable.
const slugMaxLen = 60

// Slug derives a filesystem-safe, lowercase slug from a page URL: the host
// (minus a leading "www.") joined with the path, every run of non-alphanumeric
// characters collapsed to a single "-", trimmed, and capped at slugMaxLen.
// Returns "page" when the URL yields no usable tokens.
func Slug(rawURL string) string {
	raw := rawURL
	if u, err := url.Parse(rawURL); err == nil && u.Host != "" {
		raw = strings.TrimPrefix(u.Hostname(), "www.") + "/" + u.Path
	}
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(raw) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			prevDash = false
		} else if !prevDash {
			b.WriteByte('-')
			prevDash = true
		}
	}
	s := strings.Trim(b.String(), "-")
	if len(s) > slugMaxLen {
		s = strings.Trim(s[:slugMaxLen], "-")
	}
	if s == "" {
		return "page"
	}
	return s
}

// NextIndex returns the next free zero-padded fixture index given the existing
// pages/ basenames. It scans the leading NNNN- prefix capture writes, takes the
// max, and adds one; names without a numeric prefix are ignored, and an empty
// or all-non-numbered directory yields 0.
func NextIndex(existing []string) int {
	max := -1
	for _, name := range existing {
		base := path.Base(name)
		i := strings.IndexByte(base, '-')
		if i <= 0 {
			continue
		}
		n, err := strconv.Atoi(base[:i])
		if err != nil {
			continue
		}
		if n > max {
			max = n
		}
	}
	return max + 1
}

// FixtureName formats the pages/ basename stored in Entry.File: "%04d-<slug>.html".
func FixtureName(index int, slug string) string {
	return fmt.Sprintf("%04d-%s.html", index, slug)
}
