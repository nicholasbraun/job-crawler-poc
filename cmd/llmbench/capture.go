package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/nicholasbraun/job-crawler-poc/cmd/llmbench/bench"
	"github.com/nicholasbraun/job-crawler-poc/internal/downloader"
)

// userAgent is the crawler's UA, copied from cmd/server/main.go so capture
// freezes bytes with the same identity the live pipeline downloads under. The
// literal is unexported there, so there is no shared home to import.
const userAgent = "JobCrawlerBot/0.1 (+https://github.com/nicholasbraun/job-crawler-poc)"

// runCapture fetches <url> through the crawler downloader (faithful bytes),
// resolves the final URL, writes pages/NNNN-slug.html and appends an unlabeled
// manifest stub. Returns the process exit code (2 usage error, 1 fetch/IO error).
func runCapture(args []string) int {
	fs := flag.NewFlagSet("capture", flag.ExitOnError)
	gold := fs.String("gold", "cmd/llmbench/testdata", "Gold-Set directory holding manifest.json and pages/*.html")
	timeout := fs.Duration("timeout", 30*time.Second, "download timeout")
	_ = fs.Parse(args)

	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: llmbench capture [-gold dir] [-timeout d] <url>")
		return 2
	}
	reqURL := fs.Arg(0)

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	resp, err := downloader.NewClient(userAgent).Get(ctx, reqURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "llmbench capture: download %q: %v\n", reqURL, err)
		return 1
	}

	finalURL, note := resolveFinalURL(ctx, reqURL)

	if err := writeFixture(*gold, finalURL, note, resp.Content); err != nil {
		fmt.Fprintf(os.Stderr, "llmbench capture: %v\n", err)
		return 1
	}
	return 0
}

// resolveFinalURL follows reqURL with a stdlib http client using the crawler's
// UA and reports the final resolved URL plus a redirect note. On any error it
// falls back to reqURL with an empty note.
func resolveFinalURL(ctx context.Context, reqURL string) (finalURL, note string) {
	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return reqURL, ""
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")

	orig := req.URL.String()
	res, err := client.Do(req)
	if err != nil {
		return reqURL, ""
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<20))

	final := res.Request.URL.String()
	if final != orig {
		return final, "redirected " + orig + " -> " + final
	}
	return final, ""
}

// writeFixture writes body to <gold>/pages/NNNN-slug.html (NNNN next free index,
// slug from finalURL) and appends an unlabeled stub Entry to <gold>/manifest.json,
// creating pages/ and an empty manifest array when absent.
func writeFixture(goldDir, finalURL, note string, body []byte) error {
	pagesDir := filepath.Join(goldDir, "pages")
	if err := os.MkdirAll(pagesDir, 0o755); err != nil {
		return fmt.Errorf("create pages dir: %w", err)
	}

	dirEntries, err := os.ReadDir(pagesDir)
	if err != nil {
		return fmt.Errorf("read pages dir: %w", err)
	}
	existing := []string{}
	for _, e := range dirEntries {
		existing = append(existing, e.Name())
	}

	idx := bench.NextIndex(existing)
	file := bench.FixtureName(idx, bench.Slug(finalURL))
	if err := os.WriteFile(filepath.Join(pagesDir, file), body, 0o644); err != nil {
		return fmt.Errorf("write fixture: %w", err)
	}

	manifestPath := filepath.Join(goldDir, "manifest.json")
	entries := []bench.Entry{}
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("read manifest: %w", err)
		}
	} else if err := json.Unmarshal(data, &entries); err != nil {
		return fmt.Errorf("read manifest: %w", err)
	}

	entries = append(entries, bench.Entry{
		File:      file,
		URL:       finalURL,
		Label:     "",
		Category:  "",
		Verified:  false,
		FetchedAt: time.Now().UTC().Format(time.RFC3339),
		Note:      note,
	})

	out, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return fmt.Errorf("encode manifest: %w", err)
	}
	out = append(out, '\n')
	if err := os.WriteFile(manifestPath, out, 0o644); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}
	return nil
}
